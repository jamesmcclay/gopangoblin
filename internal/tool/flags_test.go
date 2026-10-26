package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func writeSharedConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shared.yml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing shared config: %v", err)
	}
	return path
}

func TestValidateFillsFromSharedConfig(t *testing.T) {
	path := writeSharedConfig(t, `
client_id: from-shared@example.iam.panserviceaccount.com
client_secret: shared-secret
tsg_id: "12345"
`)
	f := &SCMFlags{SharedConfig: path}
	if err := f.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if f.ClientID != "from-shared@example.iam.panserviceaccount.com" || f.ClientSecret != "shared-secret" || f.TSGID != "12345" {
		t.Fatalf("credentials not filled from shared config: %+v", f)
	}
}

func TestValidateFlagOverridesSharedConfig(t *testing.T) {
	path := writeSharedConfig(t, `
client_id: from-shared@example.iam.panserviceaccount.com
client_secret: shared-secret
tsg_id: "12345"
`)
	f := &SCMFlags{
		SharedConfig: path,
		ClientID:     "from-flag@example.iam.panserviceaccount.com",
	}
	if err := f.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if f.ClientID != "from-flag@example.iam.panserviceaccount.com" {
		t.Errorf("ClientID = %q, want the flag-provided value to win", f.ClientID)
	}
	if f.ClientSecret != "shared-secret" || f.TSGID != "12345" {
		t.Errorf("remaining fields should still fill from shared config: %+v", f)
	}
}

func TestValidateMissingSharedConfigIsNotAnError(t *testing.T) {
	f := &SCMFlags{SharedConfig: filepath.Join(t.TempDir(), "does-not-exist.yml")}
	err := f.Validate()
	if err == nil {
		t.Fatal("expected an error (no credentials from any source), but not about the missing file")
	}
	if got := err.Error(); got == "" {
		t.Fatal("expected a non-empty error message")
	}
}

func TestValidateBrokenSharedConfigErrors(t *testing.T) {
	path := writeSharedConfig(t, "client_id: [this is not valid\n")
	f := &SCMFlags{SharedConfig: path}
	if err := f.Validate(); err == nil {
		t.Fatal("expected an error for a malformed shared config file")
	}
}

func TestValidateDryRunDefaultsTrueWithNoSharedConfig(t *testing.T) {
	f := &SCMFlags{
		SharedConfig: filepath.Join(t.TempDir(), "does-not-exist.yml"),
		ClientID:     "id", ClientSecret: "secret", TSGID: "12345",
	}
	if err := f.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !f.DryRun {
		t.Error("DryRun should default to true when nothing says otherwise")
	}
}

func TestValidateSharedConfigCanTurnDryRunOff(t *testing.T) {
	path := writeSharedConfig(t, `
client_id: id
client_secret: secret
tsg_id: "12345"
dry_run: false
auto_apply: true
`)
	f := &SCMFlags{SharedConfig: path}
	if err := f.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if f.DryRun {
		t.Error("DryRun should be false when shared config sets dry_run: false")
	}
	if !f.AutoApply {
		t.Error("AutoApply should be true when shared config sets auto_apply: true")
	}
}

func TestValidateAutoApplySkipsPrompt(t *testing.T) {
	// A real run (DryRun false) with auto_apply: true must not block on
	// stdin at all -- Validate isn't given a reader here, so if it tried
	// to prompt anyway this test would hang instead of failing cleanly.
	path := writeSharedConfig(t, `
client_id: id
client_secret: secret
tsg_id: "12345"
dry_run: false
auto_apply: true
`)
	f := &SCMFlags{SharedConfig: path}
	if err := f.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestChangedReflectsActualFlagUse(t *testing.T) {
	cmd := &cobra.Command{Use: "test", RunE: func(*cobra.Command, []string) error { return nil }}
	f := AddSCMFlags(cmd, "playbooks/test.yml")
	cmd.SetArgs([]string{"--push", "--mode=uninstall"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !f.Changed("push") {
		t.Error(`Changed("push") = false, want true (flag was passed)`)
	}
	if !f.Changed("mode") {
		t.Error(`Changed("mode") = false, want true (flag was passed)`)
	}
	if f.Changed("no-push") {
		t.Error(`Changed("no-push") = true, want false (flag was never passed)`)
	}
	if f.Mode != "uninstall" {
		t.Errorf("Mode = %q, want %q", f.Mode, "uninstall")
	}
	if !f.Push {
		t.Error("Push = false, want true")
	}
}

func TestChangedFalseForDirectlyConstructedFlags(t *testing.T) {
	f := &SCMFlags{Push: true}
	if f.Changed("push") {
		t.Error(`Changed("push") should be false for an SCMFlags with no flag layer (built directly, not via AddSCMFlags)`)
	}
}

func TestPromptConfirmRequiresExactYes(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
	}{
		{"yes\n", false},
		{"yes", false},
		{"  yes  \n", false},
		{"y\n", true},
		{"Yes\n", true},
		{"YES\n", true},
		{"\n", true},
		{"", true},
		{"no\n", true},
	}
	for _, tt := range tests {
		var out strings.Builder
		err := promptConfirm(strings.NewReader(tt.input), &out)
		if (err != nil) != tt.wantErr {
			t.Errorf("promptConfirm(%q): err = %v, wantErr = %v", tt.input, err, tt.wantErr)
		}
	}
}
