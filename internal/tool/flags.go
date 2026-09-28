package tool

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// SCMFlags holds the flags shared by every playbook-driven tool
// (habuilder, internet, reset, sdwan): where the playbook lives, the SCM
// service account credentials, and the dry-run/no-push behavior toggles.
type SCMFlags struct {
	Playbook     string
	ClientID     string
	ClientSecret string
	TSGID        string
	DryRun       bool
	NoPush       bool
}

// AddSCMFlags registers the common playbook/credential/dry-run/no-push
// flags on cmd, defaulting playbook to defaultPlaybook and the credential
// flags to the SCM_CLIENT_ID/SCM_CLIENT_SECRET/SCM_TSG_ID env vars. Call
// Validate once the command's RunE runs to confirm credentials were
// actually supplied.
func AddSCMFlags(cmd *cobra.Command, defaultPlaybook string) *SCMFlags {
	f := &SCMFlags{}
	cmd.Flags().StringVar(&f.Playbook, "playbook", defaultPlaybook, "path to the playbook file")
	cmd.Flags().StringVar(&f.ClientID, "client-id", os.Getenv("SCM_CLIENT_ID"), "SCM service account client ID (env SCM_CLIENT_ID)")
	cmd.Flags().StringVar(&f.ClientSecret, "client-secret", os.Getenv("SCM_CLIENT_SECRET"), "SCM service account client secret (env SCM_CLIENT_SECRET)")
	cmd.Flags().StringVar(&f.TSGID, "tsg-id", os.Getenv("SCM_TSG_ID"), "SCM Tenant Service Group ID (env SCM_TSG_ID)")
	cmd.Flags().BoolVar(&f.DryRun, "dry-run", false, "print planned actions without calling the SCM API")
	cmd.Flags().BoolVar(&f.NoPush, "no-push", false, "skip the automatic config push even if the playbook sets push: true")
	return f
}

// Validate reports an error if any SCM credential is missing (by flag or
// by the env vars AddSCMFlags falls back to).
func (f *SCMFlags) Validate() error {
	if f.ClientID == "" || f.ClientSecret == "" || f.TSGID == "" {
		return fmt.Errorf("client-id, client-secret, and tsg-id are all required (flags or SCM_CLIENT_ID/SCM_CLIENT_SECRET/SCM_TSG_ID env vars)")
	}
	return nil
}
