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

	r := &reconciler{
		client: client,
		dryRun: *dryRun,
	}

	fmt.Printf("reset: playbook %q, %d device(s)\n", pb.Name, len(fws))

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

	if pb.Push && !*noPush && !*dryRun && len(r.touched) > 0 {
		if err := pushChanges(client, pb, r.touched); err != nil {
			fmt.Fprintf(os.Stderr, "reset: push: %v\n", err)
			failures++
		}
	}

	if failures > 0 {
		return fmt.Errorf("%d device(s) failed, see above", failures)
	}
	return nil
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
