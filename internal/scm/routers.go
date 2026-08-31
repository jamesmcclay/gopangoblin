package scm

// LogicalRoutersPath is the base path for the /logical-routers resource
// (SCM's virtual-router equivalent).
const LogicalRoutersPath = "/config/network/v1/logical-routers"

// LogicalRouterNexthop selects the next-hop type for a static route (only
// the ip_address variant is used here).
type LogicalRouterNexthop struct {
	IPAddress string `json:"ip_address,omitempty"`
}

// LogicalRouterStaticRoute is one static route entry.
type LogicalRouterStaticRoute struct {
	Name        string               `json:"name"`
	Destination string               `json:"destination"`
	Interface   string               `json:"interface,omitempty"`
	Nexthop     LogicalRouterNexthop `json:"nexthop"`
}

// LogicalRouterRoutingTableIP holds the VRF's IPv4 static routes.
type LogicalRouterRoutingTableIP struct {
	StaticRoute []LogicalRouterStaticRoute `json:"static_route,omitempty"`
}

// LogicalRouterRoutingTable is the VRF's routing_table object.
type LogicalRouterRoutingTable struct {
	IP *LogicalRouterRoutingTableIP `json:"ip,omitempty"`
}

// VRF is one virtual-router-forwarding entry within a logical router.
type VRF struct {
	Name         string                     `json:"name"`
	Interface    []string                   `json:"interface,omitempty"`
	RoutingTable *LogicalRouterRoutingTable `json:"routing_table,omitempty"`
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
