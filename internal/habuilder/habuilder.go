// Package habuilder implements the "habuilder" gopangoblin tool: it builds
// (or tears down) Strata Cloud Manager HA configurations for the firewall
// pairs listed in a ha_pairs.yml playbook.
package habuilder

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

// Tool is the "habuilder" gopangoblin tool.
type Tool struct{}

func (t *Tool) Name() string { return "habuilder" }

func (t *Tool) Summary() string {
	return "Build or remove Strata Cloud Manager HA configs from a playbook"
}

func (t *Tool) Run(args []string) error {
	fs := flag.NewFlagSet("habuilder", flag.ExitOnError)
	playbookPath := fs.String("playbook", "playbooks/ha_pairs.yml", "path to the ha_pairs.yml playbook")
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

	pairs, err := pb.Resolved()
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

	r := &reconciler{
		client: client,
		mode:   pb.Mode,
		dryRun: *dryRun,
	}

	fmt.Printf("habuilder: playbook %q, mode %s, %d HA pair(s)\n", pb.Name, pb.Mode, len(pairs))

	var failures int
	for _, pair := range pairs {
		if err := r.reconcilePair(pair, devices); err != nil {
			fmt.Fprintf(os.Stderr, "habuilder: %s: %v\n", pair.Name, err)
			failures++
		}
	}

	if pb.Push && !*noPush && !*dryRun && len(r.touched) > 0 {
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
