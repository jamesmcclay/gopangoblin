package habuilder

import "testing"

func TestLoadPlaybookResolvesDefaults(t *testing.T) {
	pb, err := LoadPlaybook("../../playbooks/ha_pairs.yml")
	if err != nil {
		t.Fatalf("LoadPlaybook: %v", err)
	}

	pairs, err := pb.Resolved()
	if err != nil {
		t.Fatalf("Resolved: %v", err)
	}
	if len(pairs) != 1 {
		t.Fatalf("expected 1 fw pair, got %d", len(pairs))
	}

	p := pairs[0]
	want := ResolvedFirewallPair{
		Name:                 "James Lab 1",
		PrimarySerial:        "007954000891379",
		SecondarySerial:      "007954000909285",
		PrimaryIP:            "10.0.0.1",
		SecondaryIP:          "10.0.0.2",
		Netmask:              "255.255.255.252",
		PrimaryDataIP:        "10.0.0.5",
		SecondaryDataIP:      "10.0.0.6",
		ControlLinkInterface: "ethernet1/6",
		DataLinkInterface:    "ethernet1/7",
		GroupID:              "1",
	}
	if p != want {
		t.Fatalf("resolved pair mismatch:\n got: %+v\nwant: %+v", p, want)
	}
}

func TestResolveFieldExplicitOverride(t *testing.T) {
	vars := map[string]string{
		"default_HA1_IP":                 "10.0.0.1",
		"default_HA2_IP":                 "10.0.0.2",
		"default_HA_netmask":             "255.255.255.252",
		"default_control_link_interface": "ethernet1/6",
		"default_data_link_interface":    "ethernet1/7",
		"default_HA1_data_IP":            "10.0.0.5",
		"default_HA2_data_IP":            "10.0.0.6",
	}
	fw := FirewallPair{
		Name:            "override test",
		PrimarySerial:   "111",
		SecondarySerial: "222",
		PrimaryIP:       "192.0.2.1",           // explicit override, not "vars.*"
		SecondaryIP:     "vars.default_HA2_IP", // explicit vars reference
	}
	r, err := fw.Resolve(vars)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.PrimaryIP != "192.0.2.1" {
		t.Errorf("PrimaryIP = %q, want explicit override", r.PrimaryIP)
	}
	if r.SecondaryIP != "10.0.0.2" {
		t.Errorf("SecondaryIP = %q, want resolved vars reference", r.SecondaryIP)
	}
}

func TestResolveMissingRequiredVarErrors(t *testing.T) {
	fw := FirewallPair{
		Name:            "missing var",
		PrimarySerial:   "111",
		SecondarySerial: "222",
	}
	if _, err := fw.Resolve(map[string]string{}); err == nil {
		t.Fatal("expected error when no default vars are defined")
	}
}
