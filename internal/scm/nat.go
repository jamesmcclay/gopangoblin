package scm

// NATRulesPath is the base path for the /nat-rules resource.
const NATRulesPath = "/config/network/v1/nat-rules"

// NATRuleInterfaceAddress selects the egress interface used for a
// dynamic-ip-and-port (SNAT) translation. Confirmed live: specifying just
// Interface (no ip) is valid -- the interface's own address is used.
type NATRuleInterfaceAddress struct {
	Interface string `json:"interface"`
}

// NATRuleDynamicIPAndPort is the dynamic_ip_and_port source-translation type.
type NATRuleDynamicIPAndPort struct {
	InterfaceAddress *NATRuleInterfaceAddress `json:"interface_address,omitempty"`
}

// NATRuleSourceTranslation selects the source NAT method (only
// dynamic_ip_and_port is used here).
type NATRuleSourceTranslation struct {
	DynamicIPAndPort *NATRuleDynamicIPAndPort `json:"dynamic_ip_and_port,omitempty"`
}

// NATRule is the request/response body for the /nat-rules endpoint. Note
// Service is a single string here, unlike security-rules' array.
type NATRule struct {
	ID                string                    `json:"id,omitempty"`
	Name              string                    `json:"name"`
	Folder            string                    `json:"folder,omitempty"`
	Snippet           string                    `json:"snippet,omitempty"`
	Device            string                    `json:"device,omitempty"`
	From              []string                  `json:"from"`
	To                []string                  `json:"to"`
	Source            []string                  `json:"source"`
	Destination       []string                  `json:"destination"`
	Service           string                    `json:"service,omitempty"`
	SourceTranslation *NATRuleSourceTranslation `json:"source_translation,omitempty"`
}

func (c *Client) CreateNATRule(cfg NATRule) (*NATRule, error) {
	var out NATRule
	if err := c.doJSON("POST", NATRulesPath, nil, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateNATRule(id string, cfg NATRule) (*NATRule, error) {
	var out NATRule
	if err := c.doJSON("PUT", NATRulesPath+"/"+id, nil, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
