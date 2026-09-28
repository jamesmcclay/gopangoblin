package reset

import "testing"

func TestLoadPlaybookResolvesEntries(t *testing.T) {
	pb, err := LoadPlaybook("../../playbooks/reset.yml")
	if err != nil {
		t.Fatalf("LoadPlaybook: %v", err)
	}

	fws, err := pb.Resolved()
	if err != nil {
		t.Fatalf("Resolved: %v", err)
	}
	if len(fws) != 3 {
		t.Fatalf("expected 3 fw_list entries, got %d", len(fws))
	}

	want := ResolvedFirewall{Name: "James Lab A", Serial: "007954000909285"}
	if fws[0] != want {
		t.Fatalf("resolved entry mismatch:\n got: %+v\nwant: %+v", fws[0], want)
	}
}

func TestResolveMissingSerialErrors(t *testing.T) {
	fw := FirewallEntry{Name: "no serial"}
	if _, err := fw.Resolve(); err == nil {
		t.Fatal("expected error when serial is missing")
	}
}
