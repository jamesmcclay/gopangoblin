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
	// only targets what changed.
	touched []string
}

func (r *reconciler) markTouched(serial string) {
	r.touched = append(r.touched, serial)
}

func (r *reconciler) reconcileDevice(device scm.Device, fw ResolvedFirewall) error {
	changed, err := r.wipeConfig(device.ID, fw.Name)
	if err != nil {
		return fmt.Errorf("wiping config: %w", err)
	}
	if !changed {
		fmt.Printf("  [skip]   %s already has no device-owned config to remove\n", fw.Name)
		return nil
	}
	r.markTouched(fw.Serial)
	return nil
}

// wipeConfig removes every device-owned resource (HA config plus every
// scm.WipeResources type) from the given device, retrying in rounds so
// undocumented deletion-order dependencies (e.g. a rule referencing a zone)
// resolve themselves: each round deletes everything it can, and whatever
// still 409s moves to the next round. Returns an error only if a full
// round makes no progress at all. changed reports whether anything was
// actually found to remove.
func (r *reconciler) wipeConfig(deviceID, name string) (changed bool, err error) {
	haRemoved, err := r.deleteHAConfig(deviceID, name)
	if err != nil {
		return false, err
	}

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
			objs, err := r.client.ListByDevice(res.Path, deviceID, pos)
			if err != nil {
				return false, fmt.Errorf("listing %s: %w", res.Name, err)
			}
			for _, obj := range objs {
				candidates = append(candidates, pending{resource: res, id: obj.ID, name: obj.Name})
			}
		}
	}

	// SCM's device-filtered list resolves folder/snippet-scoped (shared)
	// objects into this device's view and echoes the queried device back
	// in the response, even though the object isn't actually owned by
	// this device (see scm.GetScopedObject -- confirmed live: a shared
	// "$eth-internet" template stored under folder "ngfw-shared" comes
	// back with "device": "<serial>" and the same id for every device
	// that inherits it). Deleting by id would delete the shared object
	// for every device using it, not just this one, so every candidate is
	// re-verified by a bare-id fetch before it's trusted for deletion.
	var items []pending
	for _, c := range candidates {
		owned, err := r.client.IsDeviceOwned(c.resource.Path, c.id, deviceID)
		if err != nil {
			return false, fmt.Errorf("confirming scope of %s %q: %w", c.resource.Name, c.name, err)
		}
		if !owned {
			fmt.Printf("  [shared] %s: not removing %s %q (%s) -- inherited from a shared folder/snippet, not owned by this device\n", name, c.resource.Name, c.name, c.id)
			continue
		}
		items = append(items, c)
	}

	if len(items) == 0 {
		return haRemoved, nil
	}

	fmt.Printf("  [wipe]   %s: %d existing object(s) to remove\n", name, len(items))
	for _, it := range items {
		fmt.Printf("             - %s %q (%s)\n", it.resource.Name, it.name, it.id)
	}
	if r.dryRun {
		return true, nil
	}

	for len(items) > 0 {
		var remaining []pending
		var lastErr error
		for _, it := range items {
			if err := r.client.DeleteByID(it.resource.Path, it.id); err != nil {
				if scm.IsNotFound(err) {
					continue
				}
				if scm.IsConflict(err) {
					remaining = append(remaining, it)
					lastErr = err
					continue
				}
				return false, fmt.Errorf("deleting %s %s: %w", it.resource.Name, it.id, err)
			}
		}
		if len(remaining) == len(items) {
			return false, fmt.Errorf("could not delete %d object(s), still referenced by other config: %w", len(remaining), lastErr)
		}
		items = remaining
	}
	return true, nil
}

// deleteHAConfig removes the device's HA configuration, if any, reporting
// whether one was actually found and removed.
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
