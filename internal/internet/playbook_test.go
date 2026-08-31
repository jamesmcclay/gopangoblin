package internet

import "testing"

func TestLoadPlaybookResolvesExample(t *testing.T) {
	pb, err := LoadPlaybook("../../playbooks/internet.yml")
	if err != nil {
		t.Fatalf("LoadPlaybook: %v", err)
	}

	items, err := pb.Resolved()
	if err != nil {
		t.Fatalf("Resolved: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item_list entry, got %d", len(items))
	}

	want := ResolvedItem{
		Name:             "Lab Firewalls",
		Type:             ItemFolder,
		TrustInterface:   "$eth-local",
		UntrustInterface: "$eth-internet",
		WANCIDR:          "$wan_cidr",
		WANGateway:       "$wan_gw",
		LANCIDR:          "$lan_cidr",
		DNSServer:        "8.8.8.8",
		DHCPPool:         "$lan_pool",
		LANGateway:       "$lan_gw",
	}
	if items[0] != want {
		t.Fatalf("resolved item mismatch:\n got: %+v\nwant: %+v", items[0], want)
	}

	if len(pb.VariableOverrides) != 2 {
		t.Fatalf("expected 2 variable_overrides entries, got %d", len(pb.VariableOverrides))
	}
}

func TestFirewallItemRequiresSerial(t *testing.T) {
	it := Item{Name: "some firewall", Type: ItemFirewall}
	if _, err := it.Resolve(map[string]string{}); err == nil {
		t.Fatal("expected error when a firewall item has no serial")
	}
}

func TestFirewallItemNameIsJustALabel(t *testing.T) {
	vars := map[string]string{
		"default_trust_interface":   "ethernet1/4",
		"default_untrust_interface": "ethernet1/3",
		"default_lan_cidr":          "192.168.1.1/24",
		"default_dns_server":        "8.8.8.8",
	}
	it := Item{Name: "My Lab Firewall", Type: ItemFirewall, Serial: "12345"}
	r, err := it.Resolve(vars)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Serial != "12345" {
		t.Errorf("Serial = %q, want 12345", r.Serial)
	}
	if r.Name != "My Lab Firewall" {
		t.Errorf("Name = %q, want the display label unchanged", r.Name)
	}
}
