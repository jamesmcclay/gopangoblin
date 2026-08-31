package scm

// ZonesPath is the base path for the /zones resource.
const ZonesPath = "/config/network/v1/zones"

// ZoneNetwork holds the zone's interface membership. A zone owns its
// interfaces (Layer3 lists interface names), rather than an interface
// pointing at a zone.
type ZoneNetwork struct {
	Layer3 []string `json:"layer3,omitempty"`
}

// Zone is the request/response body for the /zones endpoint.
type Zone struct {
	ID      string      `json:"id,omitempty"`
	Name    string      `json:"name"`
	Folder  string      `json:"folder,omitempty"`
	Snippet string      `json:"snippet,omitempty"`
	Device  string      `json:"device,omitempty"`
	Network ZoneNetwork `json:"network"`
}

func (c *Client) CreateZone(cfg Zone) (*Zone, error) {
	var out Zone
	if err := c.doJSON("POST", ZonesPath, nil, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateZone(id string, cfg Zone) (*Zone, error) {
	var out Zone
	if err := c.doJSON("PUT", ZonesPath+"/"+id, nil, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetZone(id string) (*Zone, error) {
	var out Zone
	if err := c.doJSON("GET", ZonesPath+"/"+id, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
