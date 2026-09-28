// Package internet implements the "internet" gopangoblin tool: it
// configures basic internet access -- trust/untrust interfaces, a LAN
// DHCP server, a default SNAT rule, and an allow-all security policy --
// on the folders, snippets, and firewalls listed in an internet.yml
// playbook, and writes SCM template-variable overrides from an optional
// variable_overrides list, scoped to a device, folder, or snippet.
package internet

import (
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
		Use:   "internet",
		Short: "Configure basic internet access (trust/untrust, DHCP, NAT, security policy) on SCM targets",
	}
	flags := tool.AddSCMFlags(cmd, "playbooks/internet.yml")
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

	items, err := pb.Resolved()
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

	var needFolders, needSnippets bool
	for _, it := range items {
		switch it.Type {
		case ItemFolder:
			needFolders = true
		case ItemSnippet:
			needFolders = true // needed for ancestry-based push targeting
			needSnippets = true
		}
	}
	for _, vo := range pb.VariableOverrides {
		switch {
		case vo.Folder != "":
			needFolders = true
		case vo.Snippet != "":
			needFolders = true // needed for ancestry-based push targeting
			needSnippets = true
		}
	}

	var folders []scm.Folder
	if needFolders {
		folders, err = client.ListFolders()
		if err != nil {
			return fmt.Errorf("listing SCM folders: %w", err)
		}
	}
	var snippets []scm.Snippet
	if needSnippets {
		snippets, err = client.ListSnippets()
		if err != nil {
			return fmt.Errorf("listing SCM snippets: %w", err)
		}
	}

	r := &reconciler{
		client:  client,
		dryRun:  flags.DryRun,
		mode:    pb.Mode,
		devices: devices,
		folders: folders,
	}

	fmt.Printf("internet: playbook %q, mode %s, %d item(s), %d variable override(s)\n",
		pb.Name, pb.Mode, len(items), len(pb.VariableOverrides))

	var failures int
	for _, it := range items {
		scopeParam, scopeValue, label, err := resolveItemScope(it, devices, folders, snippets)
		if err != nil {
			fmt.Fprintf(os.Stderr, "internet: %s: %v\n", it.Name, err)
			failures++
			continue
		}
		if err := r.reconcileItem(scopeParam, scopeValue, label, it); err != nil {
			fmt.Fprintf(os.Stderr, "internet: %s: %v\n", label, err)
			failures++
		}
	}

	for _, vo := range pb.VariableOverrides {
		if err := validateVariableOverrideScope(vo, devices, folders, snippets); err != nil {
			fmt.Fprintf(os.Stderr, "internet: %s: %v\n", vo.Name, err)
			failures++
			continue
		}
		if err := r.reconcileVariableOverride(vo); err != nil {
			fmt.Fprintf(os.Stderr, "internet: %s: %v\n", vo.Name, err)
			failures++
		}
	}

	touched := r.touchedSerials()
	if pb.Push && !flags.NoPush && !flags.DryRun && len(touched) > 0 {
		if err := pushChanges(client, pb, touched); err != nil {
			fmt.Fprintf(os.Stderr, "internet: push: %v\n", err)
			failures++
		}
	}

	if failures > 0 {
		return fmt.Errorf("%d item(s) failed, see above", failures)
	}
	return nil
}

// resolveItemScope maps a ResolvedItem to the (scopeParam, scopeValue,
// label) triple the reconciler operates on, failing fast with a clear
// error if a folder/snippet/firewall it names doesn't actually exist in
// SCM.
func resolveItemScope(it ResolvedItem, devices []scm.Device, folders []scm.Folder, snippets []scm.Snippet) (scopeParam, scopeValue, label string, err error) {
	switch it.Type {
	case ItemFolder:
		if _, err := scm.ResolveFolderByName(folders, it.Name); err != nil {
			return "", "", "", err
		}
		return "folder", it.Name, fmt.Sprintf("folder %q", it.Name), nil
	case ItemSnippet:
		if _, err := scm.ResolveSnippetByName(snippets, it.Name); err != nil {
			return "", "", "", err
		}
		return "snippet", it.Name, fmt.Sprintf("snippet %q", it.Name), nil
	case ItemFirewall:
		if _, err := scm.ResolveDeviceBySerial(devices, it.Serial); err != nil {
			return "", "", "", err
		}
		return "device", it.Serial, it.Name, nil
	default:
		return "", "", "", fmt.Errorf("unknown item type %q", it.Type)
	}
}

// validateVariableOverrideScope fails fast if a variable_overrides entry's
// serial/folder/snippet doesn't actually exist in SCM, mirroring
// resolveItemScope's checks for item_list entries.
func validateVariableOverrideScope(vo VariableOverride, devices []scm.Device, folders []scm.Folder, snippets []scm.Snippet) error {
	switch {
	case vo.Folder != "":
		_, err := scm.ResolveFolderByName(folders, vo.Folder)
		return err
	case vo.Snippet != "":
		_, err := scm.ResolveSnippetByName(snippets, vo.Snippet)
		return err
	default:
		_, err := scm.ResolveDeviceBySerial(devices, vo.Serial)
		return err
	}
}

// pushChanges triggers an SCM candidate-config push for the given device
// serials and waits for the resulting job to finish.
func pushChanges(client *scm.Client, pb *Playbook, serials []string) error {
	description := fmt.Sprintf("gopangoblin internet: %s", pb.Name)

	fmt.Printf("internet: pushing config to %d device(s): %v\n", len(serials), serials)
	result, err := client.PushCandidateConfig(serials, description)
	if err != nil {
		return fmt.Errorf("triggering push: %w", err)
	}
	if !result.Success {
		return fmt.Errorf("push not accepted: %s", result.Message)
	}

	fmt.Printf("internet: push job %s enqueued, waiting for completion...\n", result.JobID)
	outcome, err := client.WaitForPush(result.JobID, len(serials), pushJobTimeout, pushJobPollFreq)
	if err != nil {
		return err
	}
	if outcome.Failed() {
		return fmt.Errorf("push job %s failed: %s", result.JobID, outcome.Summary())
	}

	fmt.Printf("internet: push job %s completed successfully on %d device(s)\n", result.JobID, len(outcome.DeviceJobs))
	return nil
}
