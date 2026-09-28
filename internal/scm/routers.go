package scm

// LogicalRoutersPath is the base path for the /logical-routers resource
// (SCM's virtual-router equivalent).
const LogicalRoutersPath = "/config/network/v1/logical-routers"

// LogicalRouterNexthop selects the next-hop type for a static route (only
// the ip_address variant is used here).
type LogicalRouterNexthop struct {
	IPAddress string `json:"ip_address,omitempty"`
}

// LogicalRouterStaticRoute is one static route entry. Metric is omitted
// (PAN-OS defaults it to 10) unless explicitly set -- confirmed live:
// PAN-OS commit rejects two static routes to the same destination sharing
// one metric ("... is not unique among static routes to destination
// 0.0.0.0/0"), which matters once a second default route (the optional
// secondary WAN's) coexists with the primary's.
type LogicalRouterStaticRoute struct {
	Name        string               `json:"name"`
	Destination string               `json:"destination"`
	Interface   string               `json:"interface,omitempty"`
	Nexthop     LogicalRouterNexthop `json:"nexthop"`
	Metric      int                  `json:"metric,omitempty"`
}

// LogicalRouterRoutingTableIP holds the VRF's IPv4 static routes.
type LogicalRouterRoutingTableIP struct {
	StaticRoute []LogicalRouterStaticRoute `json:"static_route,omitempty"`
}

// LogicalRouterRoutingTable is the VRF's routing_table object.
type LogicalRouterRoutingTable struct {
	IP *LogicalRouterRoutingTableIP `json:"ip,omitempty"`
}

// VRFBGPRedistributionProfileIPv4 names the redistribution profile (by
// name, e.g. "All-Connected-Routes") BGP redistributes IPv4 unicast routes
// through.
type VRFBGPRedistributionProfileIPv4 struct {
	Unicast string `json:"unicast,omitempty"`
}

// VRFBGPRedistributionProfile is BGP's redistribution_profile object.
type VRFBGPRedistributionProfile struct {
	IPv4 *VRFBGPRedistributionProfileIPv4 `json:"ipv4,omitempty"`
}

// VRFBGP is the VRF's BGP protocol settings -- only the fields this tool
// actually manages (Enable/LocalAS/RouterID/RedistributionProfile) are
// modeled; every other field PAN-OS defaults on its own (graceful_restart,
// med, admin_dists, etc., all confirmed live to already be present on the
// lab's manually-configured scm_router). Confirmed live: SCM's
// logical-routers PUT merges rather than replaces -- installRouter/
// ensureDefaultRoute have round-tripped this same scm_router object many
// times (fetch full router, modify one field, PUT the whole object back)
// without ever stripping its BGP config, even though this struct (and VRF's
// own AdminDists/RIBFilter, also unmodeled) can't carry those fields
// forward -- so setting just these four fields here is safe.
type VRFBGP struct {
	Enable                *bool                        `json:"enable,omitempty"`
	LocalAS               string                       `json:"local_as,omitempty"`
	RouterID              string                       `json:"router_id,omitempty"`
	RedistributionProfile *VRFBGPRedistributionProfile `json:"redistribution_profile,omitempty"`
}

// VRF is one virtual-router-forwarding entry within a logical router.
type VRF struct {
	Name         string                     `json:"name"`
	Interface    []string                   `json:"interface,omitempty"`
	RoutingTable *LogicalRouterRoutingTable `json:"routing_table,omitempty"`
	BGP          *VRFBGP                    `json:"bgp,omitempty"`
}

// LogicalRouter is the request/response body for the /logical-routers
// endpoint.
type LogicalRouter struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	Folder  string `json:"folder,omitempty"`
	Snippet string `json:"snippet,omitempty"`
	Device  string `json:"device,omitempty"`
	VRF     []VRF  `json:"vrf,omitempty"`
}

func (c *Client) CreateLogicalRouter(cfg LogicalRouter) (*LogicalRouter, error) {
	var out LogicalRouter
	if err := c.doJSON("POST", LogicalRoutersPath, nil, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateLogicalRouter(id string, cfg LogicalRouter) (*LogicalRouter, error) {
	var out LogicalRouter
	if err := c.doJSON("PUT", LogicalRoutersPath+"/"+id, nil, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetLogicalRouter(id string) (*LogicalRouter, error) {
	var out LogicalRouter
	if err := c.doJSON("GET", LogicalRoutersPath+"/"+id, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
