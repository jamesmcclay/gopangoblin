package sdwan

import "testing"

func TestLoadPlaybookResolvesExample(t *testing.T) {
	pb, err := LoadPlaybook("../../playbooks/sdwan.yml")
	if err != nil {
		t.Fatalf("LoadPlaybook: %v", err)
	}

	r, err := pb.Resolved()
	if err != nil {
		t.Fatalf("Resolved: %v", err)
	}

	if r.Folder != "Lab Firewalls" {
		t.Errorf("Folder = %q, want %q", r.Folder, "Lab Firewalls")
	}
	if r.ClusterName != "james-cluster" {
		t.Errorf("ClusterName = %q, want %q", r.ClusterName, "james-cluster")
	}
	if r.Router != "scm_router" {
		t.Errorf("Router = %q, want %q", r.Router, "scm_router")
	}
	if r.WANInterface != "$eth-wan01" || r.WAN02Interface != "$eth-wan02" {
		t.Errorf("WAN interfaces = %q/%q, want $eth-wan01/$eth-wan02", r.WANInterface, r.WAN02Interface)
	}
	if r.WANLinkTag != "WAN1" || r.WAN02LinkTag != "WAN2" {
		t.Errorf("link tags = %q/%q, want WAN1/WAN2", r.WANLinkTag, r.WAN02LinkTag)
	}

	if len(r.Hubs) != 1 {
		t.Fatalf("expected 1 hub, got %d", len(r.Hubs))
	}
	hub := r.Hubs[0]
	if hub.Serial != "007954000919494" || hub.RouterID != "1.1.1.1" || hub.ASN != "65001" || hub.Priority != "1" {
		t.Errorf("hub = %+v, unexpected values", hub)
	}

	if len(r.Branches) != 2 {
		t.Fatalf("expected 2 branches, got %d", len(r.Branches))
	}
	if r.Branches[0].Serial != "007954000909285" || r.Branches[1].Serial != "007954000891379" {
		t.Errorf("branches = %+v, unexpected serials", r.Branches)
	}
}

func TestHubPriorityDefaultsToOne(t *testing.T) {
	s := Site{Name: "hub", Serial: "1", Site: "hub01", RouterID: "1.1.1.1", ASN: "65001"}
	r, err := s.resolve("hub_list")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if r.Priority != "" {
		t.Errorf("Site.resolve should leave Priority for the caller to default, got %q", r.Priority)
	}
}

func TestSiteRequiresSerial(t *testing.T) {
	s := Site{Name: "hub", Site: "hub01", RouterID: "1.1.1.1", ASN: "65001"}
	if _, err := s.resolve("hub_list"); err == nil {
		t.Fatal("expected error when serial is missing")
	}
}

func TestSiteRequiresRouterIDAndASN(t *testing.T) {
	base := Site{Name: "hub", Serial: "1", Site: "hub01"}
	if _, err := base.resolve("hub_list"); err == nil {
		t.Fatal("expected error when router_id and asn are missing")
	}
	withRouterID := base
	withRouterID.RouterID = "1.1.1.1"
	if _, err := withRouterID.resolve("hub_list"); err == nil {
		t.Fatal("expected error when asn is missing")
	}
}
