package sdwan

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Mode controls how the sdwan tool reconciles the playbook against SCM.
type Mode string

const (
	ModeInstall         Mode = "install"          // configure only what's missing
	ModeInstallOverride Mode = "install-override" // configure everything regardless of current state
	ModeUninstall       Mode = "uninstall"        // remove the SD-WAN configuration entirely
)

// InternetRouting selects how internet-bound traffic (not matched by any
// corp/overlay route) leaves the fabric.
type InternetRouting string

const (
	// InternetRoutingBreakout: every site sends its own internet-bound
	// traffic out its own local WAN links (an "internet-traffic" SD-WAN
	// steering rule, LAN -> this site's own WAN zone, Top Down) -- this is
	// ordinary local breakout, already what internet-tool-only routing
	// does on its own; the SD-WAN rule just adds WAN-link load
	// balancing/failover preference on top of it.
	InternetRoutingBreakout InternetRouting = "breakout"
	// InternetRoutingBackhaul: branches send internet-bound traffic to the
	// hub over the Auto VPN fabric instead of breaking out locally, and
	// the hub NATs/forwards it out its own WAN. See reconcile.go's
	// installHubBackhaul for how this actually works -- PAN-OS's static
	// route admin distance (10) always beats a BGP-learned one (20/200)
	// regardless of BGP metrics, so genuine backhaul needs each branch's
	// own local default route suppressed via a device-scoped router
	// override, not just an SD-WAN policy tweak.
	InternetRoutingBackhaul InternetRouting = "backhaul"
	// InternetRoutingNone: sdwan doesn't manage internet-bound routing at
	// all -- no steering rule, no backhaul. Whatever the "internet" tool
	// set up (local breakout, since that's the only thing it ever
	// configures) is left as the only behavior in effect.
	InternetRoutingNone InternetRouting = "none"
)

// Playbook is the parsed structure of an sdwan.yml file. Unlike internet.yml
// (an item_list of independently-scoped folders/snippets/firewalls), a
// PAN-OS SD-WAN deployment is one interconnected whole -- one Auto VPN
// cluster, one shared set of SD-WAN interface/distribution profiles and
// steering rules, all owned at a single SCM folder scope -- so this
// playbook has just one of each, plus a hub_list/branch_list naming which
// devices participate.
//
// This tool is meant to layer on top of a folder the "internet" tool has
// already configured (see playbooks/internet.yml): it reuses that tool's
// interfaces ($eth-wan01/$eth-wan02), zones (zone-internal/zone-internet),
// and logical router (scm_router) by name rather than creating its own.
type Playbook struct {
	Name        string            `yaml:"name"`
	Mode        Mode              `yaml:"mode"`
	Push        bool              `yaml:"push"`
	Folder      string            `yaml:"folder"`
	Vars        map[string]string `yaml:"vars"`
	ClusterName string            `yaml:"cluster_name"`
	HubList     []Site            `yaml:"hub_list"`
	BranchList  []Site            `yaml:"branch_list"`

	// InternetRouting defaults to "breakout" when left unset, matching
	// this tool's original (pre-option) behavior.
	InternetRouting InternetRouting `yaml:"internet_routing"`
	// BranchToBranch, when true, allows branch-to-branch traffic hairpinned
	// through the hub (routing already reaches it via BGP's
	// "All-Connected-Routes" redistribution -- only a security policy is
	// missing, since it otherwise hits the default interzone deny).
	BranchToBranch bool `yaml:"branch_to_branch"`
	// BranchToHub, when true, allows a branch to reach the hub's own LAN
	// (same story as BranchToBranch: routing already works, only the
	// security policy is missing).
	BranchToHub bool `yaml:"branch_to_hub"`
}

// Site is one hub_list or branch_list entry: a device participating in the
// Auto VPN cluster. RouterID/ASN are written as this device's own
// $ROUTER_ID/$ASN SCM variable overrides -- every device needs a distinct
// pair for BGP peering between hub and branches to actually work. Priority
// is only meaningful on a hub_list entry when there's more than one hub
// (lower wins); it's ignored for branches.
type Site struct {
	Name     string `yaml:"name"`
	Serial   string `yaml:"serial"`
	Site     string `yaml:"site"`
	Priority string `yaml:"priority"`
	RouterID string `yaml:"router_id"`
	ASN      string `yaml:"asn"`
}

// ResolvedSite is a Site with defaults applied and required fields validated.
type ResolvedSite struct {
	Name     string
	Serial   string
	Site     string
	Priority string
	RouterID string
	ASN      string
}

func (s Site) resolve(listName string) (ResolvedSite, error) {
	var r ResolvedSite
	if s.Serial == "" {
		return r, fmt.Errorf("%s entry %q: serial is required", listName, s.Name)
	}
	if s.Site == "" {
		return r, fmt.Errorf("%s entry %q: site is required", listName, s.Name)
	}
	if s.RouterID == "" {
		return r, fmt.Errorf("%s entry %q: router_id is required", listName, s.Name)
	}
	if s.ASN == "" {
		return r, fmt.Errorf("%s entry %q: asn is required", listName, s.Name)
	}
	r.Name, r.Serial, r.Site = s.Name, s.Serial, s.Site
	r.Priority, r.RouterID, r.ASN = s.Priority, s.RouterID, s.ASN
	return r, nil
}

// Resolved is the playbook fully resolved against its vars, ready to
// reconcile against SCM.
type Resolved struct {
	Folder      string
	ClusterName string

	Router         string
	WANInterface   string
	WAN02Interface string
	WANLinkTag     string
	WAN02LinkTag   string

	LANZone    string
	WANZone    string
	HubZone    string
	BranchZone string

	RedistributionProfile string
	PathQualityProfile    string

	InternetRouting InternetRouting
	BranchToBranch  bool
	BranchToHub     bool

	Hubs     []ResolvedSite
	Branches []ResolvedSite
}

func requiredVar(vars map[string]string, key string) (string, error) {
	v, ok := vars[key]
	if !ok || v == "" {
		return "", fmt.Errorf("vars.%s is required", key)
	}
	return v, nil
}

// Resolved fills in defaults from vars and validates required fields.
func (pb *Playbook) Resolved() (*Resolved, error) {
	r := &Resolved{
		Folder:          pb.Folder,
		ClusterName:     pb.ClusterName,
		InternetRouting: pb.InternetRouting,
		BranchToBranch:  pb.BranchToBranch,
		BranchToHub:     pb.BranchToHub,
	}
	if r.InternetRouting == "" {
		r.InternetRouting = InternetRoutingBreakout
	}

	for _, field := range []struct {
		key string
		dst *string
	}{
		{"default_router", &r.Router},
		{"default_wan_interface", &r.WANInterface},
		{"default_wan02_interface", &r.WAN02Interface},
		{"default_wan_link_tag", &r.WANLinkTag},
		{"default_wan02_link_tag", &r.WAN02LinkTag},
		{"default_lan_zone", &r.LANZone},
		{"default_wan_zone", &r.WANZone},
		{"default_hub_zone", &r.HubZone},
		{"default_branch_zone", &r.BranchZone},
		{"default_redistribution_profile", &r.RedistributionProfile},
		{"default_path_quality_profile", &r.PathQualityProfile},
	} {
		v, err := requiredVar(pb.Vars, field.key)
		if err != nil {
			return nil, err
		}
		*field.dst = v
	}

	for _, h := range pb.HubList {
		rh, err := h.resolve("hub_list")
		if err != nil {
			return nil, err
		}
		if rh.Priority == "" {
			rh.Priority = "1"
		}
		r.Hubs = append(r.Hubs, rh)
	}
	for _, b := range pb.BranchList {
		rb, err := b.resolve("branch_list")
		if err != nil {
			return nil, err
		}
		r.Branches = append(r.Branches, rb)
	}

	return r, nil
}

// LoadPlaybook reads and parses an sdwan.yml file.
func LoadPlaybook(path string) (*Playbook, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading playbook: %w", err)
	}

	var pb Playbook
	if err := yaml.Unmarshal(data, &pb); err != nil {
		return nil, fmt.Errorf("parsing playbook: %w", err)
	}

	switch pb.Mode {
	case ModeInstall, ModeInstallOverride, ModeUninstall:
	default:
		return nil, fmt.Errorf("playbook mode must be one of %q, %q, %q, got %q",
			ModeInstall, ModeInstallOverride, ModeUninstall, pb.Mode)
	}
	if pb.Folder == "" {
		return nil, fmt.Errorf("playbook has no folder set")
	}
	if pb.ClusterName == "" {
		return nil, fmt.Errorf("playbook has no cluster_name set")
	}
	if len(pb.HubList) == 0 {
		return nil, fmt.Errorf("playbook has no hub_list entries")
	}
	if len(pb.BranchList) == 0 {
		return nil, fmt.Errorf("playbook has no branch_list entries")
	}
	switch pb.InternetRouting {
	case "", InternetRoutingBreakout, InternetRoutingBackhaul, InternetRoutingNone:
	default:
		return nil, fmt.Errorf("playbook internet_routing must be one of %q, %q, %q, got %q",
			InternetRoutingBreakout, InternetRoutingBackhaul, InternetRoutingNone, pb.InternetRouting)
	}

	return &pb, nil
}
