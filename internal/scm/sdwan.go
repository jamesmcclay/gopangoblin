package scm

// This file covers the SD-WAN-specific SCM resources confirmed live against
// the lab tenant's manually-built PAN-OS SD-WAN deployment (see
// conversation.md / instructions.md in the repo root): SD-WAN interface
// profiles (per-link-tag path monitoring), traffic distribution profiles,
// SD-WAN policy (steering) rules, and Auto VPN clusters. Ordinary
// zones/logical-routers/ethernet-interfaces/NAT/security-rules are shared
// with the "internet" tool's own scm files -- SD-WAN layers on top of those,
// it doesn't replace them.

// LinkTagsPath is the base path for the /link-tags resource: a bare
// name/scope object with no other fields (confirmed live via a bare-id
// fetch -- just {id, name, folder|snippet|device}). It's a genuinely
// separate resource from SDWANInterfaceProfile.LinkTag (the string field on
// an interface profile, and on a traffic distribution profile's LinkTags
// list, both of which just reference a link-tags object by name) --
// confirmed live the hard way: creating an interface profile with
// link_tag: "WAN1" silently auto-vivifies a link-tags object named "WAN1"
// if none exists yet, which is easy to miss since nothing in the interface
// profile's own response ever surfaces it. This tool creates it explicitly
// instead of relying on that side effect, so it's a first-class reconciled
// object reset can find and remove.
const LinkTagsPath = "/config/network/v1/link-tags"

// LinkTag is the request/response body for the /link-tags endpoint.
type LinkTag struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	Folder  string `json:"folder,omitempty"`
	Snippet string `json:"snippet,omitempty"`
	Device  string `json:"device,omitempty"`
}

func (c *Client) CreateLinkTag(cfg LinkTag) (*LinkTag, error) {
	var out LinkTag
	if err := c.doJSON("POST", LinkTagsPath, nil, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateLinkTag(id string, cfg LinkTag) (*LinkTag, error) {
	var out LinkTag
	if err := c.doJSON("PUT", LinkTagsPath+"/"+id, nil, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SDWANInterfaceProfilesPath is the base path for the
// /sdwan-interface-profiles resource: per-WAN-link path monitoring/failover
// behavior, referenced by name from an Auto VPN cluster interface's
// sdwan_link_settings.sdwan_interface_profile.
const SDWANInterfaceProfilesPath = "/config/network/v1/sdwan-interface-profiles"

// SDWANInterfaceProfile is the request/response body for the
// /sdwan-interface-profiles endpoint. Confirmed live against the lab's own
// "wan1"/"wan2" profiles -- LinkTag is the free-form label (e.g. "WAN1")
// that traffic distribution profiles and Auto VPN cluster interfaces
// reference; it's unrelated to the interface's own SCM name.
type SDWANInterfaceProfile struct {
	ID                   string `json:"id,omitempty"`
	Name                 string `json:"name"`
	Folder               string `json:"folder,omitempty"`
	Snippet              string `json:"snippet,omitempty"`
	Device               string `json:"device,omitempty"`
	LinkTag              string `json:"link_tag"`
	LinkType             string `json:"link_type"` // confirmed live: "Ethernet"
	PathMonitoring       string `json:"path_monitoring,omitempty"`
	ProbeFrequency       int    `json:"probe_frequency,omitempty"`
	FailbackHoldTime     int    `json:"failback_hold_time,omitempty"`
	ErrorCorrection      *bool  `json:"error_correction,omitempty"`
	VPNDataTunnelSupport *bool  `json:"vpn_data_tunnel_support,omitempty"`
	VPNFailoverMetric    int    `json:"vpn_failover_metric,omitempty"`
}

func (c *Client) CreateSDWANInterfaceProfile(cfg SDWANInterfaceProfile) (*SDWANInterfaceProfile, error) {
	var out SDWANInterfaceProfile
	if err := c.doJSON("POST", SDWANInterfaceProfilesPath, nil, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateSDWANInterfaceProfile(id string, cfg SDWANInterfaceProfile) (*SDWANInterfaceProfile, error) {
	var out SDWANInterfaceProfile
	if err := c.doJSON("PUT", SDWANInterfaceProfilesPath+"/"+id, nil, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SDWANTrafficDistributionProfilesPath is the base path for the
// /sdwan-traffic-distribution-profiles resource.
const SDWANTrafficDistributionProfilesPath = "/config/network/v1/sdwan-traffic-distribution-profiles"

// SDWANLinkTag references an SDWANInterfaceProfile by its LinkTag value.
type SDWANLinkTag struct {
	Name string `json:"name"`
}

// SDWANTrafficDistributionProfile is the request/response body for the
// /sdwan-traffic-distribution-profiles endpoint. Confirmed live: both the
// lab's "Best Path" and "Top Down" profiles use the identical
// TrafficDistribution value "Best Available Path" -- SCM has no separate
// "top-down" enum value for this field, despite the profile's own display
// name; a rule's actual steering behavior is determined by which named
// profile it references (action.traffic_distribution_profile on an
// SDWANRule), not by a distinct value here.
type SDWANTrafficDistributionProfile struct {
	ID                  string         `json:"id,omitempty"`
	Name                string         `json:"name"`
	Folder              string         `json:"folder,omitempty"`
	Snippet             string         `json:"snippet,omitempty"`
	Device              string         `json:"device,omitempty"`
	TrafficDistribution string         `json:"traffic_distribution"`
	LinkTags            []SDWANLinkTag `json:"link_tags,omitempty"`
}

func (c *Client) CreateSDWANTrafficDistributionProfile(cfg SDWANTrafficDistributionProfile) (*SDWANTrafficDistributionProfile, error) {
	var out SDWANTrafficDistributionProfile
	if err := c.doJSON("POST", SDWANTrafficDistributionProfilesPath, nil, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateSDWANTrafficDistributionProfile(id string, cfg SDWANTrafficDistributionProfile) (*SDWANTrafficDistributionProfile, error) {
	var out SDWANTrafficDistributionProfile
	if err := c.doJSON("PUT", SDWANTrafficDistributionProfilesPath+"/"+id, nil, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SDWANRulesPath is the base path for the /sdwan-rules resource: the SD-WAN
// steering policy rulebase (Configuration > NGFW > Network Policies > SD
// WAN Policy in the SCM UI). Like security-rules/nat-rules, it has a
// pre/post position split -- confirmed live (the hard way, via reset
// leaving a position=post "catchall" rule undiscovered and stuck
// referencing a distribution profile): a position-less list only returns
// pre-rulebase rules, silently omitting anything at position=post. This
// tool's own rules (sdwanRuleSpecs in the "sdwan" package) are all created
// with no explicit position and land in "pre" by default.
const SDWANRulesPath = "/config/network/v1/sdwan-rules"

// SDWANRuleAction selects the traffic distribution profile a matching
// SDWANRule steers traffic through.
type SDWANRuleAction struct {
	TrafficDistributionProfile string `json:"traffic_distribution_profile"`
}

// SDWANRule is the request/response body for the /sdwan-rules endpoint.
// Confirmed live against the lab's own "corp-traffic"/"internet-traffic"
// rules.
type SDWANRule struct {
	ID                 string          `json:"id,omitempty"`
	Name               string          `json:"name"`
	Folder             string          `json:"folder,omitempty"`
	Snippet            string          `json:"snippet,omitempty"`
	Device             string          `json:"device,omitempty"`
	Disabled           bool            `json:"disabled"`
	From               []string        `json:"from"`
	To                 []string        `json:"to"`
	Source             []string        `json:"source"`
	SourceUser         []string        `json:"source_user"`
	Destination        []string        `json:"destination"`
	NegateDestination  bool            `json:"negate_destination,omitempty"`
	Application        []string        `json:"application"`
	Service            []string        `json:"service"`
	PathQualityProfile string          `json:"path_quality_profile,omitempty"`
	Action             SDWANRuleAction `json:"action"`
}

func (c *Client) CreateSDWANRule(cfg SDWANRule) (*SDWANRule, error) {
	var out SDWANRule
	if err := c.doJSON("POST", SDWANRulesPath, nil, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateSDWANRule(id string, cfg SDWANRule) (*SDWANRule, error) {
	var out SDWANRule
	if err := c.doJSON("PUT", SDWANRulesPath+"/"+id, nil, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AutoVPNClustersPath is the base path for the /auto-vpn-clusters resource:
// SCM's "Auto VPN" hub-and-spoke orchestration feature, which auto-generates
// the underlying IKE gateways/IPSec tunnels/tunnel-interfaces for every
// hub<->branch pair -- this tool never manages those directly, only the
// cluster definition itself.
const AutoVPNClustersPath = "/config/network/v1/auto-vpn-clusters"

// AutoVPNUpstreamNAT controls whether the SD-WAN link's upstream (e.g. ISP
// router) performs NAT -- confirmed live: false in the lab (no upstream
// NAT between the firewall's WAN interface and its actual gateway).
type AutoVPNUpstreamNAT struct {
	Enable *bool `json:"enable"`
}

// AutoVPNSDWANLinkSettings binds one Auto VPN interface entry to an
// SDWANInterfaceProfile (by name) and its next-hop gateway.
type AutoVPNSDWANLinkSettings struct {
	SDWANInterfaceProfile string             `json:"sdwan_interface_profile"`
	UpstreamNAT           AutoVPNUpstreamNAT `json:"upstream_nat"`
	SDWANGateway          string             `json:"sdwan_gateway"`
}

// AutoVPNInterface is one WAN interface entry under an AutoVPNGateway or
// AutoVPNBranch. Name is the interface (commonly a "$variable", e.g.
// "$eth-wan01") whose SD-WAN behavior SDWANLinkSettings configures.
type AutoVPNInterface struct {
	Name              string                    `json:"name"`
	SDWANLinkSettings *AutoVPNSDWANLinkSettings `json:"sdwan_link_settings,omitempty"`
}

// AutoVPNGateway is one hub entry under an AutoVPNCluster's Gateways list.
// Name is the hub device's serial number. BGPRedistributionProfile
// references an existing bgp-redistribution-profiles object by name (e.g.
// SCM's own "All-Connected-Routes", auto-provisioned the first time any
// Auto VPN cluster is created in the tenant -- this tool only ever
// references it, never creates it; see BGPRedistributionProfilesPath).
type AutoVPNGateway struct {
	Name                     string             `json:"name"`
	Site                     string             `json:"site"`
	LogicalRouter            string             `json:"logical_router"`
	Priority                 string             `json:"priority,omitempty"`
	BGPRedistributionProfile string             `json:"bgp_redistribution_profile,omitempty"`
	AllowDIAVPNFailover      *bool              `json:"allow_dia_vpn_failover,omitempty"`
	Interfaces               []AutoVPNInterface `json:"interfaces,omitempty"`
}

// AutoVPNBranch is one branch (spoke) entry under an AutoVPNCluster's
// Branches list. Name is the branch device's serial number.
type AutoVPNBranch struct {
	Name                     string             `json:"name"`
	Site                     string             `json:"site"`
	LogicalRouter            string             `json:"logical_router"`
	BGPRedistributionProfile string             `json:"bgp_redistribution_profile,omitempty"`
	Interfaces               []AutoVPNInterface `json:"interfaces,omitempty"`
}

// AutoVPNCluster is the request/response body for the /auto-vpn-clusters
// endpoint.
type AutoVPNCluster struct {
	ID          string           `json:"id,omitempty"`
	Name        string           `json:"name"`
	Folder      string           `json:"folder,omitempty"`
	Snippet     string           `json:"snippet,omitempty"`
	Device      string           `json:"device,omitempty"`
	Type        string           `json:"type"` // confirmed live: "hub-spoke"
	EnableSDWAN *bool            `json:"enable_sdwan,omitempty"`
	Gateways    []AutoVPNGateway `json:"gateways,omitempty"`
	Branches    []AutoVPNBranch  `json:"branches,omitempty"`
}

func (c *Client) CreateAutoVPNCluster(cfg AutoVPNCluster) (*AutoVPNCluster, error) {
	var out AutoVPNCluster
	if err := c.doJSON("POST", AutoVPNClustersPath, nil, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateAutoVPNCluster(id string, cfg AutoVPNCluster) (*AutoVPNCluster, error) {
	var out AutoVPNCluster
	if err := c.doJSON("PUT", AutoVPNClustersPath+"/"+id, nil, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetAutoVPNCluster(id string) (*AutoVPNCluster, error) {
	var out AutoVPNCluster
	if err := c.doJSON("GET", AutoVPNClustersPath+"/"+id, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// BGPRedistributionProfilesPath is the base path for the
// /bgp-redistribution-profiles resource. Confirmed live: this tool's
// service account gets "Access denied" (403) on most other SD-WAN plumbing
// under a *differently-named* path (the plain /redistribution-profiles and
// /config/objects/v1/redistribution-profiles both 403 regardless of
// query), which looked at first like this whole resource type was
// read-only -- but this specific path (note the "bgp-" prefix) is fully
// readable AND writable (confirmed live: POST/PUT both succeed). SCM
// auto-provisions one profile here ("All-Connected-Routes", redistributing
// connected routes only) as part of an "Auto-VPN-Default-Snippet" snippet
// the first time any Auto VPN cluster is set up in the tenant -- this tool
// references that one by name (playbook vars.default_redistribution_profile)
// rather than recreating it, but creates its own additional profile for
// hub-side default-route redistribution (see installHubBackhaul in
// reconcile.go).
const BGPRedistributionProfilesPath = "/config/network/v1/bgp-redistribution-profiles"

// BGPRedistributionUnicastSource enables redistributing one route source
// (connected or static) into BGP. RouteMap is optional -- confirmed live
// against SCM's own "All-Connected-Routes" profile, which sets one
// ("All-Connected-Routes-Filter") to scope which connected routes qualify;
// omitting it redistributes every route of that source unfiltered.
type BGPRedistributionUnicastSource struct {
	Enable   *bool  `json:"enable,omitempty"`
	RouteMap string `json:"route_map,omitempty"`
}

// BGPRedistributionUnicast selects which route sources a redistribution
// profile pulls from.
type BGPRedistributionUnicast struct {
	Connected *BGPRedistributionUnicastSource `json:"connected,omitempty"`
	Static    *BGPRedistributionUnicastSource `json:"static,omitempty"`
}

// BGPRedistributionIPv4 is a redistribution profile's ipv4 object.
type BGPRedistributionIPv4 struct {
	Unicast *BGPRedistributionUnicast `json:"unicast,omitempty"`
}

// BGPRedistributionProfile is the request/response body for the
// /bgp-redistribution-profiles endpoint.
type BGPRedistributionProfile struct {
	ID      string                 `json:"id,omitempty"`
	Name    string                 `json:"name"`
	Folder  string                 `json:"folder,omitempty"`
	Snippet string                 `json:"snippet,omitempty"`
	Device  string                 `json:"device,omitempty"`
	IPv4    *BGPRedistributionIPv4 `json:"ipv4,omitempty"`
}

func (c *Client) CreateBGPRedistributionProfile(cfg BGPRedistributionProfile) (*BGPRedistributionProfile, error) {
	var out BGPRedistributionProfile
	if err := c.doJSON("POST", BGPRedistributionProfilesPath, nil, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateBGPRedistributionProfile(id string, cfg BGPRedistributionProfile) (*BGPRedistributionProfile, error) {
	var out BGPRedistributionProfile
	if err := c.doJSON("PUT", BGPRedistributionProfilesPath+"/"+id, nil, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
