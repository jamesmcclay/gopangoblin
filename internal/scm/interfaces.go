package scm

// EthernetInterfacesPath is the base path for the /ethernet-interfaces resource.
const EthernetInterfacesPath = "/config/network/v1/ethernet-interfaces"

// EthernetInterfaceStaticIP is one static IP/netmask entry. Name holds
// the CIDR value itself (e.g. "192.168.1.1/24" or a "$variable"), the
// same convention already seen on HA interface links.
type EthernetInterfaceStaticIP struct {
	Name string `json:"name"`
}

// EthernetInterfaceDHCPClient is the DHCP-client branch of an interface's
// layer3 config.
type EthernetInterfaceDHCPClient struct {
	Enable             *bool `json:"enable,omitempty"`
	CreateDefaultRoute *bool `json:"create_default_route,omitempty"`
}

// EthernetInterfaceLayer3 selects static vs DHCP-client addressing
// (mutually exclusive). Confirmed against the raw OpenAPI spec (and
// live): the interface's top-level "layer3" object directly contains
// "ip" (static) or "dhcp_client" (DHCP) as sibling keys -- there is no
// extra nesting level (an earlier version of this code wrongly nested
// "layer3" inside itself, based on a paraphrased summary rather than the
// raw spec, and silently produced an empty layer3 -- SCM accepted the
// write with no error but the interface's addressing was never actually
// set).
type EthernetInterfaceLayer3 struct {
	IP         []EthernetInterfaceStaticIP  `json:"ip,omitempty"`
	DHCPClient *EthernetInterfaceDHCPClient `json:"dhcp_client,omitempty"`
}

// EthernetInterface is the request/response body for the
// /ethernet-interfaces endpoint. DefaultValue matters specifically for a
// "$variable"-named interface (e.g. SCM's built-in "$eth-internet"): it's
// the literal interface name (e.g. "ethernet1/3") the variable falls back
// to. Confirmed live: an override of such an interface at a new scope
// that omits DefaultValue silently blanks it for that scope, which then
// makes the variable "unresolved" and permanently aborts every push to
// any device under that scope with a "Failed variables resolution check"
// error -- the parent push job still reports success, since that error
// only surfaces on the per-device child job (see Job's doc comment in
// push.go). Any code creating/updating an override of an existing
// "$variable"-named interface MUST fetch the existing object first (see
// GetEthernetInterface) and carry its DefaultValue forward.
type EthernetInterface struct {
	ID           string                  `json:"id,omitempty"`
	Name         string                  `json:"name"`
	DefaultValue string                  `json:"default_value,omitempty"`
	Folder       string                  `json:"folder,omitempty"`
	Snippet      string                  `json:"snippet,omitempty"`
	Device       string                  `json:"device,omitempty"`
	Layer3       EthernetInterfaceLayer3 `json:"layer3"`
}

func (c *Client) CreateEthernetInterface(cfg EthernetInterface) (*EthernetInterface, error) {
	var out EthernetInterface
	if err := c.doJSON("POST", EthernetInterfacesPath, nil, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateEthernetInterface(id string, cfg EthernetInterface) (*EthernetInterface, error) {
	var out EthernetInterface
	if err := c.doJSON("PUT", EthernetInterfacesPath+"/"+id, nil, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetEthernetInterface fetches an ethernet-interfaces object by its bare
// id, with no folder/snippet/device query filter.
func (c *Client) GetEthernetInterface(id string) (*EthernetInterface, error) {
	var out EthernetInterface
	if err := c.doJSON("GET", EthernetInterfacesPath+"/"+id, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
