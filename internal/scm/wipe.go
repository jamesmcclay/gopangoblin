package scm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// WipeResource identifies one folder/snippet/device-scoped SCM resource
// type that reset can enumerate and delete for a single device, folder,
// or snippet.
type WipeResource struct {
	Name string // human-readable, e.g. "security-rules"
	Path string // e.g. "/config/security/v1/security-rules"
	// Positions lists the "position" query values (pre/post) this
	// resource's list operation requires; nil means no position param.
	Positions []string
}

// WipeResources is the set of resource types reset wipes, covering the
// config the SCM API confirms is folder/snippet/device-scoped.
// This list isn't exhaustive of every possible NGFW config resource (e.g.
// sub-interfaces, DoS/decryption/app-override/QoS/SDWAN rules, PBF, EDLs
// aren't included), so a device with config in one of those isn't
// guaranteed fully vanilla after a reset -- extend this list as needed.
var WipeResources = []WipeResource{
	{Name: "security-rules", Path: "/config/security/v1/security-rules", Positions: []string{"pre", "post"}},
	{Name: "nat-rules", Path: "/config/network/v1/nat-rules", Positions: []string{"pre", "post"}},
	{Name: "address-groups", Path: "/config/objects/v1/address-groups"},
	{Name: "service-groups", Path: "/config/objects/v1/service-groups"},
	{Name: "addresses", Path: "/config/objects/v1/addresses"},
	{Name: "services", Path: "/config/objects/v1/services"},
	{Name: "tags", Path: "/config/objects/v1/tags"},
	{Name: "zones", Path: "/config/network/v1/zones"},
	{Name: "ethernet-interfaces", Path: "/config/network/v1/ethernet-interfaces"},
	{Name: "logical-routers", Path: "/config/network/v1/logical-routers"},
}

// KnownBuiltInNames lists SCM's own built-in shared template variables --
// e.g. "$eth-internet"/"$eth-local", the default untrust/trust interface
// variables provided by the "All" folder and resolved per device as
// ethernet1/3 and ethernet1/4 in this lab. These are always inherited,
// never device/folder/snippet-owned, and are expected to show up on
// every device -- so finding one isn't noteworthy the way finding some
// other, unexpected shared object would be. reset never deletes them
// either way (they fail the ownership check like anything else
// inherited); this list only silences the "[shared] ... not removing"
// log line for them specifically. Matched against an object's real
// underlying name (as returned by a bare-id GetScopedObject fetch), not
// whatever display name a scoped list resolved it to.
var KnownBuiltInNames = map[string]bool{
	"$eth-internet": true,
	"$eth-local":    true,
}

// ScopedObject is the common shape shared by every folder/snippet/device
// scoped SCM object list item: enough to identify an object and confirm
// exactly which scope it belongs to, without needing each resource's full
// schema (reset only ever deletes these, never creates/updates them).
type ScopedObject struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Folder  string `json:"folder,omitempty"`
	Snippet string `json:"snippet,omitempty"`
	Device  string `json:"device,omitempty"`
}

type listScopedResponse struct {
	Data   []ScopedObject `json:"data"`
	Limit  int            `json:"limit"`
	Offset int            `json:"offset"`
	Total  int            `json:"total"`
}

// scopeField returns the field of obj that corresponds to scopeParam
// ("device", "folder", or "snippet").
func scopeField(obj ScopedObject, scopeParam string) string {
	switch scopeParam {
	case "folder":
		return obj.Folder
	case "snippet":
		return obj.Snippet
	default:
		return obj.Device
	}
}

// ListByScope returns every object at path that is scoped directly to
// scopeParam=scopeValue ("device", "folder", or "snippet"), paginating as
// needed. If position is non-empty it's passed as the "position" query
// param required by rule-family resources.
//
// Objects are filtered to require the matching scope field to equal
// scopeValue even though the query parameter should already guarantee
// that: SCM's scoped list endpoints resolve config inherited from an
// ancestor folder/snippet into the queried scope's view and echo that
// scope back in the response regardless of the object's real owner, so
// this is enforced again defensively rather than trusted on the server's
// filtering alone.
func (c *Client) ListByScope(path, scopeParam, scopeValue, position string) ([]ScopedObject, error) {
	const pageSize = 200

	var all []ScopedObject
	offset := 0
	for {
		q := url.Values{}
		q.Set(scopeParam, scopeValue)
		q.Set("limit", fmt.Sprintf("%d", pageSize))
		q.Set("offset", fmt.Sprintf("%d", offset))
		if position != "" {
			q.Set("position", position)
		}

		var page listScopedResponse
		if err := c.doJSON("GET", path, q, nil, &page); err != nil {
			return nil, fmt.Errorf("listing %s: %w", path, err)
		}
		for _, obj := range page.Data {
			if scopeField(obj, scopeParam) == scopeValue {
				all = append(all, obj)
			}
		}

		offset += len(page.Data)
		if len(page.Data) == 0 || offset >= page.Total {
			break
		}
	}
	return all, nil
}

// DeleteByID deletes the object with the given id at path.
func (c *Client) DeleteByID(path, id string) error {
	return c.doJSON("DELETE", path+"/"+id, nil, nil, nil)
}

// GetScopedObject fetches an object by its bare id, with no
// folder/snippet/device query filter, and returns just its identity/scope
// fields (ignoring whatever other fields that resource type actually has).
//
// This matters because SCM's scoped list endpoints resolve config
// inherited from an ancestor folder/snippet into the queried scope's view
// and echo that scope back in the response's "device"/"folder"/"snippet"
// field, indistinguishably from an object truly owned by that scope --
// confirmed live against the lab: a shared "$eth-internet" template
// stored under folder "ngfw-shared" is returned with "device": "<serial>"
// and no folder/snippet field for every device that inherits it, using
// the identical object id in every case; the same inheritance applies up
// the folder hierarchy (e.g. folder "Lab Firewalls" inheriting from
// parent folder "ngfw-shared"). A bare-id fetch (no query params) returns
// the object's real stored scope instead, so it's the only reliable way
// to confirm an object found via a scoped list is actually safe to
// delete or overwrite.
func (c *Client) GetScopedObject(path, id string) (*ScopedObject, error) {
	var obj ScopedObject
	if err := c.doJSON("GET", path+"/"+id, nil, nil, &obj); err != nil {
		return nil, err
	}
	return &obj, nil
}

// IsScopedTo reports whether the object at path/id is truly scoped
// directly to scopeParam=scopeValue, as opposed to being inherited from
// an ancestor folder/snippet and resolved into the queried scope's view
// (see GetScopedObject). It also returns the object as fetched by that
// required bare-id lookup, so callers needing its real underlying name
// (e.g. to check AlwaysWipeNames) don't need a second fetch.
func (c *Client) IsScopedTo(path, id, scopeParam, scopeValue string) (bool, *ScopedObject, error) {
	obj, err := c.GetScopedObject(path, id)
	if err != nil {
		return false, nil, err
	}
	if scopeField(*obj, scopeParam) != scopeValue {
		return false, obj, nil
	}
	// Confirm the other two scope fields are empty, since a truly owned
	// object should have exactly one of folder/snippet/device set.
	switch scopeParam {
	case "folder":
		return obj.Snippet == "" && obj.Device == "", obj, nil
	case "snippet":
		return obj.Folder == "" && obj.Device == "", obj, nil
	default:
		return obj.Folder == "" && obj.Snippet == "", obj, nil
	}
}

// IsConflict reports whether err is a 409 response, which SCM returns when
// an object can't be deleted because something else still references it
// (e.g. a zone still used by a security rule).
func IsConflict(err error) bool {
	apiErr, ok := err.(*APIError)
	return ok && apiErr.StatusCode == http.StatusConflict
}

// IsDeleteNotAllowed reports whether err is SCM's "DELETE_NOT_ALLOWED"
// business-rule rejection. Confirmed live: a security-rule and nat-rule
// materialized from a snippet attached to a folder both report their
// scope as that folder (passing an IsScopedTo check, with no snippet
// field visible anywhere in their JSON body) yet still reject deletion
// with this error -- SCM tracks snippet provenance internally, invisibly
// to IsScopedTo's plain field check, and only enforces it at delete time.
// Unlike a 409 conflict, retrying this later won't help: the object can
// only be removed by detaching the snippet itself, which reset doesn't
// do, so callers should treat this as a permanent skip.
func IsDeleteNotAllowed(err error) bool {
	apiErr, ok := err.(*APIError)
	if !ok {
		return false
	}
	var parsed struct {
		Errors []struct {
			Details struct {
				Errors []struct {
					Type string `json:"type"`
				} `json:"errors"`
			} `json:"details"`
		} `json:"_errors"`
	}
	if json.Unmarshal(apiErr.Body, &parsed) != nil {
		return false
	}
	for _, e := range parsed.Errors {
		for _, d := range e.Details.Errors {
			if d.Type == "DELETE_NOT_ALLOWED" {
				return true
			}
		}
	}
	return false
}
