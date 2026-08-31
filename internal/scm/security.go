package scm

// SecurityRulesPath is the base path for the /security-rules resource.
const SecurityRulesPath = "/config/security/v1/security-rules"

// SecurityRule is the request/response body for the /security-rules
// endpoint, covering the standard policy_type: "Security" rule (the
// classic PAN-OS security policy shape).
//
// An earlier version of this tool used policy_type: "Internet" (SCM's
// simplified "Internet Access Rule" feature) instead. That rule type's
// only application-filtering fields (allow_web_application,
// block_web_application, allow_url_category, block_url_category) are all
// web/URL-specific -- there's no field in it for "any application"
// unrestricted traffic, and no pre-defined "All Applications" (non-web)
// filter object exists to reference either. Confirmed live: a standard
// Security-type rule with Application/Service/Category all set to ["any"]
// is what actually achieves unrestricted (not just web) traffic.
//
// Confirmed live: despite the spec listing base-rule-properties' "name"
// as the only universally-required field, the API actually requires
// From/To/Source/SourceUser/Destination/Service/Action/Category/
// Application together for this rule type.
type SecurityRule struct {
	ID          string   `json:"id,omitempty"`
	Name        string   `json:"name"`
	Folder      string   `json:"folder,omitempty"`
	Snippet     string   `json:"snippet,omitempty"`
	Device      string   `json:"device,omitempty"`
	PolicyType  string   `json:"policy_type,omitempty"`
	From        []string `json:"from"`
	To          []string `json:"to"`
	Source      []string `json:"source"`
	SourceUser  []string `json:"source_user"`
	Destination []string `json:"destination"`
	Service     []string `json:"service"`
	Application []string `json:"application"`
	Category    []string `json:"category"`
	Action      string   `json:"action,omitempty"`
}

func (c *Client) CreateSecurityRule(cfg SecurityRule) (*SecurityRule, error) {
	var out SecurityRule
	if err := c.doJSON("POST", SecurityRulesPath, nil, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateSecurityRule(id string, cfg SecurityRule) (*SecurityRule, error) {
	var out SecurityRule
	if err := c.doJSON("PUT", SecurityRulesPath+"/"+id, nil, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
