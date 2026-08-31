package scm

// DHCPInterfacesPath is the base path for the /dhcp-interfaces resource.
const DHCPInterfacesPath = "/config/network/v1/dhcp-interfaces"

// DHCPServerDNS holds DNS servers handed out by the DHCP server.
type DHCPServerDNS struct {
	Primary   string `json:"primary,omitempty"`
	Secondary string `json:"secondary,omitempty"`
}

// DHCPServerOption holds the DHCP lease options handed out to clients.
// Gateway/SubnetMask are left unset when the bound interface's own
// address is dynamic (e.g. a "$variable") -- PAN-OS infers them from the
// interface's own configured address when omitted (confirmed live).
type DHCPServerOption struct {
	Gateway    string         `json:"gateway,omitempty"`
	SubnetMask string         `json:"subnet_mask,omitempty"`
	DNS        *DHCPServerDNS `json:"dns,omitempty"`
}

// DHCPServer is the "server" object of a DHCPInterface. SCM requires
// either IPPool or Reserved to be set (confirmed live: "Either ip-pool or
// reserved has to be configured").
type DHCPServer struct {
	Mode   string           `json:"mode"` // "auto", "enabled", "disabled"
	Option DHCPServerOption `json:"option,omitempty"`
	IPPool []string         `json:"ip_pool,omitempty"`
}

// DHCPInterface is the request/response body for the /dhcp-interfaces
// endpoint. Name is the interface the DHCP server binds to (e.g.
// "ethernet1/4" or a "$variable").
type DHCPInterface struct {
	ID      string      `json:"id,omitempty"`
	Name    string      `json:"name"`
	Folder  string      `json:"folder,omitempty"`
	Snippet string      `json:"snippet,omitempty"`
	Device  string      `json:"device,omitempty"`
	Server  *DHCPServer `json:"server,omitempty"`
}

func (c *Client) CreateDHCPInterface(cfg DHCPInterface) (*DHCPInterface, error) {
	var out DHCPInterface
	if err := c.doJSON("POST", DHCPInterfacesPath, nil, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateDHCPInterface(id string, cfg DHCPInterface) (*DHCPInterface, error) {
	var out DHCPInterface
	if err := c.doJSON("PUT", DHCPInterfacesPath+"/"+id, nil, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
