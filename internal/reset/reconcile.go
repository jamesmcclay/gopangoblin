package reset

import (
	"fmt"

	"github.com/jamesmcclay/gopangoblin/internal/scm"
)

type reconciler struct {
	client *scm.Client
	dryRun bool

	// touched collects the serials of devices that actually had something
	// removed this run (not ones already vanilla), so a subsequent push
	// only targets what changed. Folder/snippet wipes never add to this:
	// push only knows how to target devices, so wiping a shared
	// folder/snippet only affects the candidate config for whichever
	// devices inherit from it -- deploying that also requires those
	// devices to be pushed, e.g. by listing them in fw_list too.
	touched []string
}

func (r *reconciler) markTouched(serial string) {
	r.touched = append(r.touched, serial)
}

func (r *reconciler) reconcileDevice(device scm.Device, fw ResolvedFirewall) error {
	haRemoved, err := r.deleteHAConfig(device.ID, fw.Name)
	if err != nil {
		return fmt.Errorf("wiping config: %w", err)
	}
	changed, err := r.wipeScopedResources("device", device.ID, fw.Name)
	if err != nil {
		return fmt.Errorf("wiping config: %w", err)
	}
	if !changed && !haRemoved {
		fmt.Printf("  [skip]   %s already has no device-owned config to remove\n", fw.Name)
		return nil
	}
	r.markTouched(fw.Serial)
	return nil
}

func (r *reconciler) reconcileFolder(folder scm.Folder) error {
	label := fmt.Sprintf("folder %q", folder.Name)
	changed, err := r.wipeScopedResources("folder", folder.Name, label)
	if err != nil {
		return fmt.Errorf("wiping %s: %w", label, err)
	}
	if !changed {
		fmt.Printf("  [skip]   %s already has no directly-owned config to remove\n", label)
	}
	return nil
}

func (r *reconciler) reconcileSnippet(snippet scm.Snippet) error {
	label := fmt.Sprintf("snippet %q", snippet.Name)
	changed, err := r.wipeScopedResources("snippet", snippet.Name, label)
	if err != nil {
		return fmt.Errorf("wiping %s: %w", label, err)
	}
	if !changed {
		fmt.Printf("  [skip]   %s already has no directly-owned config to remove\n", label)
	}
	return nil
}

// wipeScopedResources removes every scm.WipeResources object scoped
// directly to scopeParam=scopeValue ("device", "folder", or "snippet"),
// retrying in rounds so undocumented deletion-order dependencies (e.g. a
// rule referencing a zone) resolve themselves: each round deletes
// everything it can, and whatever still 409s moves to the next round.
// Returns an error only if a full round makes no progress at all.
// changed reports whether anything was actually found to remove. label is
// used only for log output.
func (r *reconciler) wipeScopedResources(scopeParam, scopeValue, label string) (changed bool, err error) {
	type pending struct {
		resource scm.WipeResource
		id       string
		name     string
	}

	var candidates []pending
	for _, res := range scm.WipeResources {
		positions := res.Positions
		if len(positions) == 0 {
			positions = []string{""}
		}
		for _, pos := range positions {
			objs, err := r.client.ListByScope(res.Path, scopeParam, scopeValue, pos)
			if err != nil {
				return false, fmt.Errorf("listing %s: %w", res.Name, err)
			}
			for _, obj := range objs {
				candidates = append(candidates, pending{resource: res, id: obj.ID, name: obj.Name})
			}
		}
	}

	// SCM's scoped list resolves config inherited from an ancestor
	// folder/snippet into the queried scope's view and echoes that scope
	// back in the response, even though the object isn't actually owned
	// there (see scm.GetScopedObject -- confirmed live both for a device
	// inheriting a shared folder template, and for a folder inheriting
	// from its parent folder). Deleting by id would delete the shared
	// object for everything that inherits it, not just this one scope, so
	// every candidate is re-verified by a bare-id fetch before it's
	// trusted for deletion.
	var items []pending
	for _, c := range candidates {
		owned, err := r.client.IsScopedTo(c.resource.Path, c.id, scopeParam, scopeValue)
		if err != nil {
			return false, fmt.Errorf("confirming scope of %s %q: %w", c.resource.Name, c.name, err)
		}
		if !owned {
			fmt.Printf("  [shared] %s: not removing %s %q (%s) -- inherited from an ancestor folder/snippet, not owned directly here\n", label, c.resource.Name, c.name, c.id)
			continue
		}
		items = append(items, c)
	}

	if len(items) == 0 {
		return false, nil
	}

	fmt.Printf("  [wipe]   %s: %d existing object(s) to remove\n", label, len(items))
	for _, it := range items {
		fmt.Printf("             - %s %q (%s)\n", it.resource.Name, it.name, it.id)
	}
	if r.dryRun {
		return true, nil
	}

	var deleted int
	for len(items) > 0 {
		var remaining []pending
		var lastErr error
		for _, it := range items {
			if err := r.client.DeleteByID(it.resource.Path, it.id); err != nil {
				if scm.IsNotFound(err) {
					continue
				}
				if scm.IsDeleteNotAllowed(err) {
					// Permanent, not worth retrying: this object is
					// actually materialized from an attached snippet
					// (see scm.IsDeleteNotAllowed) even though its scope
					// field alone made it look directly owned here.
					fmt.Printf("  [shared] %s: not removing %s %q (%s) -- defined by an attached snippet, not deletable this way\n", label, it.resource.Name, it.name, it.id)
					continue
				}
				if scm.IsConflict(err) {
					remaining = append(remaining, it)
					lastErr = err
					continue
				}
				return false, fmt.Errorf("deleting %s %s: %w", it.resource.Name, it.id, err)
			}
			deleted++
		}
		if len(remaining) == len(items) {
			return false, fmt.Errorf("could not delete %d object(s), still referenced by other config: %w", len(remaining), lastErr)
		}
		items = remaining
	}
	return deleted > 0, nil
}

// deleteHAConfig removes the device's HA configuration, if any, reporting
// whether one was actually found and removed. HA config is always
// device-scoped, never folder/snippet-scoped, so this only applies to
// reconcileDevice.
func (r *reconciler) deleteHAConfig(deviceID, name string) (bool, error) {
	_, err := r.client.GetHAConfiguration(deviceID)
	if err != nil {
		if scm.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("checking existing HA configuration: %w", err)
	}

	fmt.Printf("  [wipe]   %s: existing HA configuration\n", name)
	if r.dryRun {
		return true, nil
	}
	if err := r.client.DeleteHAConfiguration(deviceID); err != nil && !scm.IsNotFound(err) {
		return false, fmt.Errorf("deleting HA configuration: %w", err)
	}
	return true, nil
}
