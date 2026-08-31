package scm

import (
	"fmt"
	"net/url"
)

// VariablesPath is the base path for the /variables resource: SCM's
// template-variable ($name) definitions and per-device overrides.
const VariablesPath = "/config/setup/v1/variables"

// Variable is the request/response body for the /variables endpoint.
type Variable struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Value   string `json:"value"`
	Folder  string `json:"folder,omitempty"`
	Snippet string `json:"snippet,omitempty"`
	Device  string `json:"device,omitempty"`
}

type listVariablesResponse struct {
	Data   []Variable `json:"data"`
	Limit  int        `json:"limit"`
	Offset int        `json:"offset"`
	Total  int        `json:"total"`
}

// ListVariablesByDevice returns every variable definition/override visible
// for deviceID, paginating as needed. Unlike ethernet-interfaces, the
// scope fields here are confirmed live to reflect each variable's real
// owner even in a device-filtered list (a folder-inherited variable comes
// back with its own "folder" field set, not "device"), so no separate
// bare-id re-verification is needed to find ones truly owned by the
// device: just filter on Device == deviceID.
func (c *Client) ListVariablesByDevice(deviceID string) ([]Variable, error) {
	const pageSize = 200

	var all []Variable
	offset := 0
	for {
		q := url.Values{}
		q.Set("device", deviceID)
		q.Set("limit", fmt.Sprintf("%d", pageSize))
		q.Set("offset", fmt.Sprintf("%d", offset))

		var page listVariablesResponse
		if err := c.doJSON("GET", VariablesPath, q, nil, &page); err != nil {
			return nil, fmt.Errorf("listing variables: %w", err)
		}
		all = append(all, page.Data...)

		offset += len(page.Data)
		if len(page.Data) == 0 || offset >= page.Total {
			break
		}
	}
	return all, nil
}

// ListVariablesByScope returns every variable definition/override visible
// from scopeParam=scopeValue ("folder", "snippet", or "device"),
// paginating as needed. Unlike most scoped resources, a variable's
// folder/snippet/device field is confirmed live to reflect its real
// owner even when queried from a descendant's view (e.g. a folder-level
// variable retains "folder" in its own field when listed with
// device=<descendant>, rather than being echoed back under the query's
// own scope key the way inherited config objects are) -- so no separate
// bare-id ownership re-verification is needed here.
func (c *Client) ListVariablesByScope(scopeParam, scopeValue string) ([]Variable, error) {
	const pageSize = 200

	var all []Variable
	offset := 0
	for {
		q := url.Values{}
		q.Set(scopeParam, scopeValue)
		q.Set("limit", fmt.Sprintf("%d", pageSize))
		q.Set("offset", fmt.Sprintf("%d", offset))

		var page listVariablesResponse
		if err := c.doJSON("GET", VariablesPath, q, nil, &page); err != nil {
			return nil, fmt.Errorf("listing variables: %w", err)
		}
		all = append(all, page.Data...)

		offset += len(page.Data)
		if len(page.Data) == 0 || offset >= page.Total {
			break
		}
	}
	return all, nil
}

func (c *Client) CreateVariable(cfg Variable) (*Variable, error) {
	var out Variable
	if err := c.doJSON("POST", VariablesPath, nil, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateVariable(id string, cfg Variable) (*Variable, error) {
	var out Variable
	if err := c.doJSON("PUT", VariablesPath+"/"+id, nil, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteVariable(id string) error {
	return c.doJSON("DELETE", VariablesPath+"/"+id, nil, nil, nil)
}
