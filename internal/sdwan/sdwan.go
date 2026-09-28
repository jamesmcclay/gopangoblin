// Package sdwan implements the "sdwan" gopangoblin tool: it layers a PAN-OS
// SD-WAN deployment -- SD-WAN interface profiles, traffic distribution
// profiles, BGP, SD-WAN steering rules, and an Auto VPN hub-and-spoke
// cluster -- on top of the base internet-access config the "internet" tool
// already builds (interfaces, zones, logical router) for the folder and
// firewalls described in an sdwan.yml playbook.
package sdwan

import (
	"fmt"
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
		Use:   "sdwan",
		Short: "Configure PAN-OS SD-WAN (interface/distribution profiles, BGP, steering rules, Auto VPN cluster) on SCM targets",
	}
	flags := tool.AddSCMFlags(cmd, "playbooks/sdwan.yml")
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
	resolved, err := pb.Resolved()
	if err != nil {
		return err
	}

	client := scm.NewClient(scm.Credentials{
		ClientID:     flags.ClientID,
		ClientSecret: flags.ClientSecret,
		TSGID:        flags.TSGID,
	})

	folders, err := client.ListFolders()
	if err != nil {
		return fmt.Errorf("listing SCM folders: %w", err)
	}
	if _, err := scm.ResolveFolderByName(folders, resolved.Folder); err != nil {
		return err
	}

	devices, err := client.ListDevices()
	if err != nil {
		return fmt.Errorf("listing SCM devices: %w", err)
	}
	deviceFolders := map[string]string{}
	for _, s := range allSites(resolved) {
		d, err := scm.ResolveDeviceBySerial(devices, s.Serial)
		if err != nil {
			return fmt.Errorf("%s: %w", s.Name, err)
		}
		deviceFolders[s.Serial] = d.Folder
	}

	r := &reconciler{client: client, dryRun: flags.DryRun, mode: pb.Mode, folder: resolved.Folder, deviceFolders: deviceFolders}

	fmt.Printf("sdwan: playbook %q, mode %s, folder %q, %d hub(s), %d branch(es)\n",
		pb.Name, pb.Mode, resolved.Folder, len(resolved.Hubs), len(resolved.Branches))

	if err := r.Reconcile(resolved); err != nil {
		return fmt.Errorf("reconciling: %w", err)
	}

	touched := r.touchedSerials()
	if pb.Push && !flags.NoPush && !flags.DryRun && len(touched) > 0 {
		if err := pushChanges(client, pb, touched); err != nil {
			return fmt.Errorf("push: %w", err)
		}
	}

	return nil
}

func allSites(r *Resolved) []ResolvedSite {
	out := make([]ResolvedSite, 0, len(r.Hubs)+len(r.Branches))
	out = append(out, r.Hubs...)
	out = append(out, r.Branches...)
	return out
}

// pushChanges triggers an SCM candidate-config push for the given device
// serials and waits for the resulting job to finish.
func pushChanges(client *scm.Client, pb *Playbook, serials []string) error {
	description := fmt.Sprintf("gopangoblin sdwan: %s", pb.Name)

	fmt.Printf("sdwan: pushing config to %d device(s): %v\n", len(serials), serials)
	result, err := client.PushCandidateConfig(serials, description)
	if err != nil {
		return fmt.Errorf("triggering push: %w", err)
	}
	if !result.Success {
		return fmt.Errorf("push not accepted: %s", result.Message)
	}

	fmt.Printf("sdwan: push job %s enqueued, waiting for completion...\n", result.JobID)
	outcome, err := client.WaitForPush(result.JobID, len(serials), pushJobTimeout, pushJobPollFreq)
	if err != nil {
		return err
	}
	if outcome.Failed() {
		return fmt.Errorf("push job %s failed: %s", result.JobID, outcome.Summary())
	}

	fmt.Printf("sdwan: push job %s completed successfully on %d device(s)\n", result.JobID, len(outcome.DeviceJobs))
	return nil
}
