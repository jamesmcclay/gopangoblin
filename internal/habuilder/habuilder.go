// Package habuilder implements the "habuilder" gopangoblin tool: it builds
// (or tears down) Strata Cloud Manager HA configurations for the firewall
// pairs listed in a ha_pairs.yml playbook.
package habuilder

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
		Use:   "habuilder",
		Short: "Build or remove Strata Cloud Manager HA configs from a playbook",
	}
	flags := tool.AddSCMFlags(cmd, "playbooks/ha_pairs.yml")
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

	pairs, err := pb.Resolved()
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

	r := &reconciler{
		client: client,
		mode:   pb.Mode,
		dryRun: flags.DryRun,
	}

	fmt.Printf("habuilder: playbook %q, mode %s, %d HA pair(s)\n", pb.Name, pb.Mode, len(pairs))

	var failures int
	for _, pair := range pairs {
		if err := r.reconcilePair(pair, devices); err != nil {
			fmt.Fprintf(os.Stderr, "habuilder: %s: %v\n", pair.Name, err)
			failures++
		}
	}

	if pb.Push && !flags.NoPush && !flags.DryRun && len(r.touched) > 0 {
		if err := pushChanges(client, pb, r.touched); err != nil {
			fmt.Fprintf(os.Stderr, "habuilder: push: %v\n", err)
			failures++
		}
	}

	if failures > 0 {
		return fmt.Errorf("%d HA pair(s) failed, see above", failures)
	}
	return nil
}

// pushChanges triggers an SCM candidate-config push for the given device
// serials and waits for the resulting job to finish.
func pushChanges(client *scm.Client, pb *Playbook, serials []string) error {
	description := fmt.Sprintf("gopangoblin habuilder: %s (%s)", pb.Name, pb.Mode)

	fmt.Printf("habuilder: pushing config to %d device(s): %v\n", len(serials), serials)
	result, err := client.PushCandidateConfig(serials, description)
	if err != nil {
		return fmt.Errorf("triggering push: %w", err)
	}
	if !result.Success {
		return fmt.Errorf("push not accepted: %s", result.Message)
	}

	fmt.Printf("habuilder: push job %s enqueued, waiting for completion...\n", result.JobID)
	outcome, err := client.WaitForPush(result.JobID, len(serials), pushJobTimeout, pushJobPollFreq)
	if err != nil {
		return err
	}
	if outcome.Failed() {
		return fmt.Errorf("push job %s failed: %s", result.JobID, outcome.Summary())
	}

	fmt.Printf("habuilder: push job %s completed successfully on %d device(s)\n", result.JobID, len(outcome.DeviceJobs))
	return nil
}
