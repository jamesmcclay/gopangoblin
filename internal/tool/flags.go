package tool

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// DefaultSharedConfigPath is where SCMFlags.Validate looks for a shared
// credentials/behavior file when a client-id/client-secret/tsg-id/dry-run
// flag is left unset (and, for the credential fields, their matching env
// var isn't set either) -- see AddSCMFlags/Validate's doc comments.
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
	AutoApply    bool
	NoPush       bool

	// Mode and Push override the playbook's own mode/push fields for this
	// run when set -- Mode is a raw string (each tool defines its own
	// named Mode type; a tool applies the override itself, converting as
	// needed) and empty means "not passed" exactly like the credential
	// fields above. Push has no such natural "unset" value (false is a
	// legitimate override, not just the zero value), so a tool must check
	// Changed("push") before trusting it. Both are silently unused by any
	// tool whose playbook has no matching concept (e.g. reset has no
	// mode) -- the flag still exists on that tool's CLI, it just never
	// gets read.
	Mode string
	Push bool

	// flags is cmd.Flags(), kept so Validate/a tool's own run() can tell
	// a flag the user actually typed apart from its own default value --
	// nil for an SCMFlags built directly (e.g. in tests) rather than via
	// AddSCMFlags, which Changed treats the same as "flag not passed".
	flags *pflag.FlagSet
}

// AddSCMFlags registers the common playbook/credential/dry-run/no-push
// flags on cmd, defaulting playbook to defaultPlaybook and the credential
// flags to the SCM_CLIENT_ID/SCM_CLIENT_SECRET/SCM_TSG_ID env vars. Call
// Validate once the command's RunE runs to confirm credentials were
// actually supplied -- it also applies SharedConfig, the third and last
// fallback (see its doc comment), and gates a real (non-dry-run) run
// behind an interactive confirmation unless auto_apply is set.
func AddSCMFlags(cmd *cobra.Command, defaultPlaybook string) *SCMFlags {
	f := &SCMFlags{}
	cmd.Flags().StringVar(&f.Playbook, "playbook", defaultPlaybook, "path to the playbook file")
	cmd.Flags().StringVar(&f.ClientID, "client-id", os.Getenv("SCM_CLIENT_ID"), "SCM service account client ID (env SCM_CLIENT_ID)")
	cmd.Flags().StringVar(&f.ClientSecret, "client-secret", os.Getenv("SCM_CLIENT_SECRET"), "SCM service account client secret (env SCM_CLIENT_SECRET)")
	cmd.Flags().StringVar(&f.TSGID, "tsg-id", os.Getenv("SCM_TSG_ID"), "SCM Tenant Service Group ID (env SCM_TSG_ID)")
	cmd.Flags().StringVar(&f.SharedConfig, "shared-config", DefaultSharedConfigPath, "path to a shared client_id/client_secret/tsg_id/dry_run/auto_apply YAML file, used when a flag is left unset")
	cmd.Flags().BoolVar(&f.DryRun, "dry-run", true, "print planned actions without calling the SCM API (default true unless overridden by shared-config's dry_run)")
	cmd.Flags().BoolVar(&f.NoPush, "no-push", false, "skip the automatic config push even if the playbook sets push: true")
	cmd.Flags().StringVar(&f.Mode, "mode", "", "override the playbook's mode for this run (e.g. install, install-override, uninstall) -- ignored by tools with no mode concept")
	cmd.Flags().BoolVar(&f.Push, "push", false, "override the playbook's push setting for this run -- ignored by tools with no push concept")
	f.flags = cmd.Flags()
	return f
}

// sharedConfig is SharedConfig's file shape: the credentials a
// --client-id/--client-secret/--tsg-id flag or
// SCM_CLIENT_ID/SCM_CLIENT_SECRET/SCM_TSG_ID env var would otherwise
// supply, plus two behavior defaults with no flag of their own:
//
//   - dry_run overrides --dry-run's own built-in default (true) when the
//     user didn't pass --dry-run explicitly -- e.g. set dry_run: false
//     here to make every run a real one by default without retyping
//     --dry-run=false every time.
//   - auto_apply, when true, skips the interactive confirmation prompt a
//     real (non-dry-run) run otherwise requires (see Validate). There's
//     deliberately no --auto-apply flag: this is meant to be a considered,
//     written-down opt-in, not something to bolt on for one risky run.
type sharedConfig struct {
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
	TSGID        string `yaml:"tsg_id"`
	DryRun       *bool  `yaml:"dry_run"`
	AutoApply    *bool  `yaml:"auto_apply"`
}

// Changed reports whether the flag named name (e.g. "push", "dry-run")
// was explicitly set on the command line -- always false for an SCMFlags
// built directly rather than via AddSCMFlags (f.flags is nil), which
// every caller treats the same as "the user didn't pass this flag".
func (f *SCMFlags) Changed(name string) bool {
	return f.flags != nil && f.flags.Changed(name)
}

// Validate fills in whichever of ClientID/ClientSecret/TSGID are still
// empty, and DryRun if --dry-run wasn't passed explicitly, from the file
// at SharedConfig (if it exists), then reports an error if any credential
// is still missing. Precedence is therefore --client-id/--client-secret/
// --tsg-id/--dry-run flag > SCM_CLIENT_ID/SCM_CLIENT_SECRET/SCM_TSG_ID env
// var (credentials only) > SharedConfig file > --dry-run's own built-in
// default of true -- a flag always wins over the shared file, letting it
// stay in place as a fallback default rather than something that has to
// be overridden every time it doesn't apply.
//
// SharedConfig not existing is not an error -- most setups won't have
// one, and it's meant to be optional -- but a SharedConfig that exists
// and fails to parse is: a typo'd shared file should fail loudly, not
// silently act as if it were never there.
//
// Finally, if the run that results is real (DryRun is false) and
// SharedConfig didn't set auto_apply: true, this blocks on an interactive
// "type yes to continue" confirmation (see promptConfirm) before
// returning -- so a plain "forgot --dry-run" mistake still gets a last
// chance to back out before this tool (or its caller) does anything to
// SCM, not just a printed warning.
func (f *SCMFlags) Validate() error {
	var shared sharedConfig
	if f.SharedConfig != "" {
		if _, err := os.Stat(f.SharedConfig); err == nil {
			if err := LoadYAML(f.SharedConfig, &shared); err != nil {
				return fmt.Errorf("shared config %s: %w", f.SharedConfig, err)
			}
		}
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

	if !f.Changed("dry-run") {
		switch {
		case shared.DryRun != nil:
			f.DryRun = *shared.DryRun
		case f.flags == nil:
			// Built directly rather than via AddSCMFlags (e.g. in a
			// test), so there's no flag layer that already applied
			// AddSCMFlags' own --dry-run default -- reproduce it
			// explicitly instead of silently defaulting to Go's zero
			// value (false) here.
			f.DryRun = true
		}
	}
	if shared.AutoApply != nil {
		f.AutoApply = *shared.AutoApply
	}

	if f.ClientID == "" || f.ClientSecret == "" || f.TSGID == "" {
		return fmt.Errorf("client-id, client-secret, and tsg-id are all required (flags, SCM_CLIENT_ID/SCM_CLIENT_SECRET/SCM_TSG_ID env vars, or a %s file)", DefaultSharedConfigPath)
	}

	if !f.DryRun && !f.AutoApply {
		if err := promptConfirm(os.Stdin, os.Stdout); err != nil {
			return err
		}
	}
	return nil
}

// promptConfirm asks the user to type "yes" (and only "yes", trimmed of
// surrounding whitespace but otherwise an exact, case-sensitive match --
// not "y", not "Yes", not an empty line/EOF) before a real run proceeds.
// Anything else, including in with nothing left to read, is treated as a
// decline.
func promptConfirm(in io.Reader, out io.Writer) error {
	fmt.Fprint(out, "This will make changes to your environment, you assume all risk. Do you want to proceed? (type \"yes\" to continue): ")
	line, _ := bufio.NewReader(in).ReadString('\n')
	if strings.TrimSpace(line) != "yes" {
		return fmt.Errorf("aborted: confirmation not received")
	}
	return nil
}
