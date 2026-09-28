package tool

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// DefaultSharedConfigPath is where SCMFlags.Validate looks for a shared
// credentials file when a client-id/client-secret/tsg-id flag is left
// unset and its matching env var isn't set either -- see
// AddSCMFlags/Validate's doc comments.
const DefaultSharedConfigPath = "playbooks/shared.yml"

// SCMFlags holds the flags shared by every playbook-driven tool
// (habuilder, internet, reset, sdwan): where the playbook lives, the SCM
// service account credentials, and the dry-run/no-push behavior toggles.
type SCMFlags struct {
	Playbook     string
	ClientID     string
	ClientSecret string
	TSGID        string
	SharedConfig string
	DryRun       bool
	NoPush       bool
}

// AddSCMFlags registers the common playbook/credential/dry-run/no-push
// flags on cmd, defaulting playbook to defaultPlaybook and the credential
// flags to the SCM_CLIENT_ID/SCM_CLIENT_SECRET/SCM_TSG_ID env vars. Call
// Validate once the command's RunE runs to confirm credentials were
// actually supplied -- it also applies SharedConfig, the third and last
// fallback (see its doc comment).
func AddSCMFlags(cmd *cobra.Command, defaultPlaybook string) *SCMFlags {
	f := &SCMFlags{}
	cmd.Flags().StringVar(&f.Playbook, "playbook", defaultPlaybook, "path to the playbook file")
	cmd.Flags().StringVar(&f.ClientID, "client-id", os.Getenv("SCM_CLIENT_ID"), "SCM service account client ID (env SCM_CLIENT_ID)")
	cmd.Flags().StringVar(&f.ClientSecret, "client-secret", os.Getenv("SCM_CLIENT_SECRET"), "SCM service account client secret (env SCM_CLIENT_SECRET)")
	cmd.Flags().StringVar(&f.TSGID, "tsg-id", os.Getenv("SCM_TSG_ID"), "SCM Tenant Service Group ID (env SCM_TSG_ID)")
	cmd.Flags().StringVar(&f.SharedConfig, "shared-config", DefaultSharedConfigPath, "path to a shared client_id/client_secret/tsg_id YAML file, used when a flag/env var is left unset")
	cmd.Flags().BoolVar(&f.DryRun, "dry-run", false, "print planned actions without calling the SCM API")
	cmd.Flags().BoolVar(&f.NoPush, "no-push", false, "skip the automatic config push even if the playbook sets push: true")
	return f
}

// sharedCredentials is SharedConfig's file shape: the same three
// credentials a --client-id/--client-secret/--tsg-id flag or
// SCM_CLIENT_ID/SCM_CLIENT_SECRET/SCM_TSG_ID env var would otherwise
// supply, so every playbook-driven tool can point at one shared file
// instead of re-exporting those env vars (or retyping the flags) for
// every run.
type sharedCredentials struct {
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
	TSGID        string `yaml:"tsg_id"`
}

// Validate fills in whichever of ClientID/ClientSecret/TSGID are still
// empty after flag/env resolution from the file at SharedConfig, then
// reports an error if any are still missing. Precedence is therefore
// --client-id/--client-secret/--tsg-id flag >
// SCM_CLIENT_ID/SCM_CLIENT_SECRET/SCM_TSG_ID env var > SharedConfig file
// -- a flag or env var always wins over the shared file, letting it stay
// in place as a fallback default rather than something that has to be
// overridden every time it doesn't apply.
//
// SharedConfig not existing is not an error -- most setups won't have
// one, and it's meant to be optional -- but a SharedConfig that exists
// and fails to parse is: a typo'd shared file should fail loudly, not
// silently act as if it were never there.
func (f *SCMFlags) Validate() error {
	if (f.ClientID == "" || f.ClientSecret == "" || f.TSGID == "") && f.SharedConfig != "" {
		if _, err := os.Stat(f.SharedConfig); err == nil {
			var shared sharedCredentials
			if err := LoadYAML(f.SharedConfig, &shared); err != nil {
				return fmt.Errorf("shared config %s: %w", f.SharedConfig, err)
			}
			if f.ClientID == "" {
				f.ClientID = shared.ClientID
			}
			if f.ClientSecret == "" {
				f.ClientSecret = shared.ClientSecret
			}
			if f.TSGID == "" {
				f.TSGID = shared.TSGID
			}
		}
	}

	if f.ClientID == "" || f.ClientSecret == "" || f.TSGID == "" {
		return fmt.Errorf("client-id, client-secret, and tsg-id are all required (flags, SCM_CLIENT_ID/SCM_CLIENT_SECRET/SCM_TSG_ID env vars, or a %s file)", DefaultSharedConfigPath)
	}
	return nil
}
