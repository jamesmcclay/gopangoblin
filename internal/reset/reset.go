// Package reset implements the "reset" gopangoblin tool: it wipes every
// device-owned (not folder/snippet-shared) SCM configuration object for
// the devices listed in a reset.yml playbook -- interfaces, zones,
// routing, security/NAT rules, objects, and HA config -- leaving the
// device's SCM registration intact. It does not touch the management
// interface or DNS settings; SCM doesn't support managing those centrally
// for on-prem/self-registered devices like these (see reconcile.go).
package reset

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/jamesmcclay/gopangoblin/internal/scm"
	"github.com/jamesmcclay/gopangoblin/internal/tool"
)

const (
	pushJobTimeout  = 3 * time.Minute
	pushJobPollFreq = 3 * time.Second
)

func init() {
	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Wipe SCM-managed firewall config back to just its HA/network/security/objects baseline",
	}
	flags := tool.AddSCMFlags(cmd, "playbooks/reset.yml")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return run(flags)
	}
	tool.Register(cmd)
}

func run(flags *tool.SCMFlags) error {
	if err := flags.Validate(); err != nil {
		return err
	}

	pb, err := LoadPlaybook(flags.Playbook)
	if err != nil {
		return err
	}
	if flags.Changed("push") {
		pb.Push = flags.Push
	}

	fws, err := pb.Resolved()
	if err != nil {
		return err
	}

	client := scm.NewClient(scm.Credentials{
		ClientID:     flags.ClientID,
		ClientSecret: flags.ClientSecret,
		TSGID:        flags.TSGID,
	})

	devices, err := client.ListDevices()
	if err != nil {
		return fmt.Errorf("listing SCM devices: %w", err)
	}

	// "Global" is a sentinel, not a real SCM folder -- confirmed live, it
	// doesn't appear in /config/setup/v1/folders and most resources 400
	// with "Folder Global doesn't exist" when queried with it. It targets
	// tenant-global config instead (currently just Auto VPN clusters -- see
	// reconciler.reconcileGlobal), so it's pulled out of folder_list before
	// the real folders are resolved by name.
	const globalScopeName = "Global"
	var folderEntries []FolderEntry
	wantGlobal := false
	for _, f := range pb.FolderList {
		if f.Name == globalScopeName {
			wantGlobal = true
			continue
		}
		folderEntries = append(folderEntries, f)
	}

	// Folders are needed not just to resolve folder_list, but also (via
	// their Parent/Snippets fields) to figure out which devices actually
	// inherit from a wiped folder or snippet, so push can target them --
	// so fetch them whenever either list is used, not just folder_list.
	var folders []scm.Folder
	if len(folderEntries) > 0 || len(pb.SnippetList) > 0 {
		folders, err = client.ListFolders()
		if err != nil {
			return fmt.Errorf("listing SCM folders: %w", err)
		}
	}
	resolvedFolders, err := resolveFolders(folderEntries, folders)
	if err != nil {
		return err
	}

	var snippets []scm.Snippet
	if len(pb.SnippetList) > 0 {
		snippets, err = client.ListSnippets()
		if err != nil {
			return fmt.Errorf("listing SCM snippets: %w", err)
		}
	}
	resolvedSnippets, err := resolveSnippets(pb.SnippetList, snippets)
	if err != nil {
		return err
	}

	r := &reconciler{
		client:  client,
		dryRun:  flags.DryRun,
		devices: devices,
		folders: folders,
	}

	fmt.Printf("reset: playbook %q, %d device(s), %d folder(s), %d snippet(s), global=%v\n",
		pb.Name, len(fws), len(resolvedFolders), len(resolvedSnippets), wantGlobal)

	// Global is attempted first, before anything folder/snippet/device
	// scoped: an Auto VPN cluster names ethernet-interfaces/logical-routers/
	// sdwan-interface-profiles by reference, and SCM enforces that
	// referential integrity on delete -- confirmed live, running this
	// after the folder wipe left scm_router, both WAN interfaces, and
	// both sdwan-interface-profiles permanently stuck 409ing against
	// "still referenced" while the cluster naming them was still alive.
	// Removing the cluster first can only ever reduce what's referenced
	// elsewhere, never add to it, so trying it first is always safe
	// regardless of what else this playbook targets.
	//
	// But the dependency isn't guaranteed to only run that direction: a
	// manually-created link-tags object living inside a folder's
	// sdwan-traffic-distribution profile can itself be named by the
	// cluster, which blocks the cluster's own delete with a 409 until
	// that folder is wiped -- confirmed live, "james-cluster" stuck
	// referencing "Branches -> ... -> link-tags -> DIA", a link tag
	// reset's own folder wipe goes on to remove. So a 409 here isn't
	// treated as fatal: it's retried below, in the same round-based
	// fashion deleteCandidates already uses for objects within one scope,
	// just one level up across Global/device/folder/snippet targets --
	// each round runs every target still pending, anything that comes
	// back with a conflict is retried next round, and the loop stops
	// once a round makes no further progress. Any other error (e.g. a
	// transient 403, or a genuine listing/API failure) is not retried
	// and is counted as a normal failure instead, matching how
	// device/folder/snippet reconcile failures were always handled.
	type reconcileTask struct {
		label string
		run   func() error
	}

	var tasks []reconcileTask
	if wantGlobal {
		tasks = append(tasks, reconcileTask{label: "Global", run: r.reconcileGlobal})
	}
	for _, fw := range fws {
		tasks = append(tasks, reconcileTask{
			label: fw.Name,
			run: func() error {
				device, err := scm.ResolveDeviceBySerial(devices, fw.Serial)
				if err != nil {
					return err
				}
				return r.reconcileDevice(device, fw)
			},
		})
	}
	for _, f := range resolvedFolders {
		tasks = append(tasks, reconcileTask{label: fmt.Sprintf("folder %s", f.Name), run: func() error { return r.reconcileFolder(f) }})
	}
	for _, s := range resolvedSnippets {
		tasks = append(tasks, reconcileTask{label: fmt.Sprintf("snippet %s", s.Name), run: func() error { return r.reconcileSnippet(s) }})
	}

	var failures int
	pending := tasks
	for len(pending) > 0 {
		var retry []reconcileTask
		conflicts := map[string]error{}
		for _, t := range pending {
			err := t.run()
			if err == nil {
				continue
			}
			if isUnresolvedConflict(err) {
				retry = append(retry, t)
				conflicts[t.label] = err
				continue
			}
			fmt.Fprintf(os.Stderr, "reset: %s: %v\n", t.label, err)
			failures++
		}
		if len(retry) == 0 {
			break
		}
		if len(retry) == len(pending) {
			// A whole round made zero progress: a genuine cross-scope
			// cycle, not just an ordering issue retries can resolve.
			for _, t := range retry {
				fmt.Fprintf(os.Stderr, "reset: %s: still referenced by other config even after every other target was wiped: %v\n", t.label, conflicts[t.label])
				failures++
			}
			break
		}
		pending = retry
	}

	touched := r.touchedSerials()
	if failures > 0 {
		// Skip the push rather than attempt one anyway: confirmed live, a
		// candidate config left half-wiped by a failed folder/device/
		// snippet reconcile (e.g. some objects deleted, others still
		// stuck on a dependency conflict) can leave push itself hanging
		// and timing out instead of failing cleanly, on top of the
		// wipe failure already reported above.
		fmt.Fprintf(os.Stderr, "reset: skipping push: %d wipe failure(s) above left candidate config in an unknown state\n", failures)
	} else if pb.Push && !flags.NoPush && !flags.DryRun && len(touched) > 0 {
		if err := pushChanges(client, pb, touched); err != nil {
			fmt.Fprintf(os.Stderr, "reset: push: %v\n", err)
			failures++
		}
	}

	if failures > 0 {
		return fmt.Errorf("%d device(s) failed, see above", failures)
	}
	return nil
}

// isUnresolvedConflict reports whether err is the "could not delete ...
// still referenced by other config" error deleteCandidates returns once a
// whole retry round makes zero progress on a 409 -- i.e. a dependency on
// something outside the scope just reconciled, which another target in
// this run's task list may well remove. DeleteByID's own conflicts are
// always retried internally first (see deleteCandidates), so any
// *scm.APIError reaching here is either this still-stuck 409 or some
// unrelated status code that isn't worth retrying across scopes.
func isUnresolvedConflict(err error) bool {
	var apiErr *scm.APIError
	return errors.As(err, &apiErr) && scm.IsConflict(apiErr)
}

// resolveFolders matches each folder_list entry to a real SCM folder by
// name, and, if the entry specifies an id, confirms it matches the
// folder's actual server-assigned id -- catching a stale or mistyped name
// pointing at a folder other than the one the user intended.
func resolveFolders(entries []FolderEntry, live []scm.Folder) ([]scm.Folder, error) {
	out := make([]scm.Folder, 0, len(entries))
	for _, e := range entries {
		f, err := scm.ResolveFolderByName(live, e.Name)
		if err != nil {
			return nil, fmt.Errorf("folder_list entry %q: %w", e.Name, err)
		}
		if e.ID != "" && e.ID != f.ID {
			return nil, fmt.Errorf("folder_list entry %q: id %q does not match this folder's actual id %q -- update the playbook or remove the id field", e.Name, e.ID, f.ID)
		}
		out = append(out, f)
	}
	return out, nil
}

// resolveSnippets is the snippet_list analogue of resolveFolders.
func resolveSnippets(entries []SnippetEntry, live []scm.Snippet) ([]scm.Snippet, error) {
	out := make([]scm.Snippet, 0, len(entries))
	for _, e := range entries {
		s, err := scm.ResolveSnippetByName(live, e.Name)
		if err != nil {
			return nil, fmt.Errorf("snippet_list entry %q: %w", e.Name, err)
		}
		if e.ID != "" && e.ID != s.ID {
			return nil, fmt.Errorf("snippet_list entry %q: id %q does not match this snippet's actual id %q -- update the playbook or remove the id field", e.Name, e.ID, s.ID)
		}
		out = append(out, s)
	}
	return out, nil
}

// pushChanges triggers an SCM candidate-config push for the given device
// serials and waits for the resulting job to finish.
func pushChanges(client *scm.Client, pb *Playbook, serials []string) error {
	description := fmt.Sprintf("gopangoblin reset: %s", pb.Name)

	fmt.Printf("reset: pushing config to %d device(s): %v\n", len(serials), serials)
	result, err := client.PushCandidateConfig(serials, description)
	if err != nil {
		return fmt.Errorf("triggering push: %w", err)
	}
	if !result.Success {
		return fmt.Errorf("push not accepted: %s", result.Message)
	}

	fmt.Printf("reset: push job %s enqueued, waiting for completion...\n", result.JobID)
	outcome, err := client.WaitForPush(result.JobID, len(serials), pushJobTimeout, pushJobPollFreq)
	if err != nil {
		return err
	}
	if outcome.Failed() {
		return fmt.Errorf("push job %s failed: %s", result.JobID, outcome.Summary())
	}

	fmt.Printf("reset: push job %s completed successfully on %d device(s)\n", result.JobID, len(outcome.DeviceJobs))
	return nil
}
