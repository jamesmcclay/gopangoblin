// Package reset implements the "reset" gopangoblin tool: it wipes every
// device-owned (not folder/snippet-shared) SCM configuration object for
// the devices listed in a reset.yml playbook -- interfaces, zones,
// routing, security/NAT rules, objects, and HA config -- leaving the
// device's SCM registration intact. It does not touch the management
// interface or DNS settings; SCM doesn't support managing those centrally
// for on-prem/self-registered devices like these (see reconcile.go).
package reset

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jamesmcclay/gopangoblin/internal/scm"
	"github.com/jamesmcclay/gopangoblin/internal/tool"
)

const (
	pushJobTimeout  = 3 * time.Minute
	pushJobPollFreq = 3 * time.Second
)

func init() {
	tool.Register(&Tool{})
}

// Tool is the "reset" gopangoblin tool.
type Tool struct{}

func (t *Tool) Name() string { return "reset" }

func (t *Tool) Summary() string {
	return "Wipe SCM-managed firewall config back to just its HA/network/security/objects baseline"
}

func (t *Tool) Run(args []string) error {
	fs := flag.NewFlagSet("reset", flag.ExitOnError)
	playbookPath := fs.String("playbook", "playbooks/reset.yml", "path to the reset.yml playbook")
	clientID := fs.String("client-id", os.Getenv("SCM_CLIENT_ID"), "SCM service account client ID (env SCM_CLIENT_ID)")
	clientSecret := fs.String("client-secret", os.Getenv("SCM_CLIENT_SECRET"), "SCM service account client secret (env SCM_CLIENT_SECRET)")
	tsgID := fs.String("tsg-id", os.Getenv("SCM_TSG_ID"), "SCM Tenant Service Group ID (env SCM_TSG_ID)")
	dryRun := fs.Bool("dry-run", false, "print planned actions without calling the SCM API")
	noPush := fs.Bool("no-push", false, "skip the automatic config push even if the playbook sets push: true")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *clientID == "" || *clientSecret == "" || *tsgID == "" {
		return fmt.Errorf("client-id, client-secret, and tsg-id are all required (flags or SCM_CLIENT_ID/SCM_CLIENT_SECRET/SCM_TSG_ID env vars)")
	}

	pb, err := LoadPlaybook(*playbookPath)
	if err != nil {
		return err
	}

	fws, err := pb.Resolved()
	if err != nil {
		return err
	}

	client := scm.NewClient(scm.Credentials{
		ClientID:     *clientID,
		ClientSecret: *clientSecret,
		TSGID:        *tsgID,
	})

	devices, err := client.ListDevices()
	if err != nil {
		return fmt.Errorf("listing SCM devices: %w", err)
	}

	// Folders are needed not just to resolve folder_list, but also (via
	// their Parent/Snippets fields) to figure out which devices actually
	// inherit from a wiped folder or snippet, so push can target them --
	// so fetch them whenever either list is used, not just folder_list.
	var folders []scm.Folder
	if len(pb.FolderList) > 0 || len(pb.SnippetList) > 0 {
		folders, err = client.ListFolders()
		if err != nil {
			return fmt.Errorf("listing SCM folders: %w", err)
		}
	}
	resolvedFolders, err := resolveFolders(pb.FolderList, folders)
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
		dryRun:  *dryRun,
		devices: devices,
		folders: folders,
	}

	fmt.Printf("reset: playbook %q, %d device(s), %d folder(s), %d snippet(s)\n",
		pb.Name, len(fws), len(resolvedFolders), len(resolvedSnippets))

	var failures int
	for _, fw := range fws {
		device, err := scm.ResolveDeviceBySerial(devices, fw.Serial)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reset: %s: %v\n", fw.Name, err)
			failures++
			continue
		}
		if err := r.reconcileDevice(device, fw); err != nil {
			fmt.Fprintf(os.Stderr, "reset: %s: %v\n", fw.Name, err)
			failures++
		}
	}

	for _, f := range resolvedFolders {
		if err := r.reconcileFolder(f); err != nil {
			fmt.Fprintf(os.Stderr, "reset: folder %s: %v\n", f.Name, err)
			failures++
		}
	}

	for _, s := range resolvedSnippets {
		if err := r.reconcileSnippet(s); err != nil {
			fmt.Fprintf(os.Stderr, "reset: snippet %s: %v\n", s.Name, err)
			failures++
		}
	}

	touched := r.touchedSerials()
	if pb.Push && !*noPush && !*dryRun && len(touched) > 0 {
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
	job, err := client.WaitForJob(result.JobID, pushJobTimeout, pushJobPollFreq)
	if err != nil {
		return err
	}
	if job.ResultStr != "OK" {
		return fmt.Errorf("push job %s finished with result %s: %s", job.ID, job.ResultStr, job.Details)
	}

	fmt.Printf("reset: push job %s completed successfully\n", job.ID)
	return nil
}
