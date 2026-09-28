package tool

import (
	"os"
	"path/filepath"
	"testing"
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
