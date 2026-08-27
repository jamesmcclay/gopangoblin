package scm

import (
	"fmt"
	"net/http"
	"net/url"
)

// WipeResource identifies one folder/snippet/device-scoped SCM resource
// type that reset can enumerate and delete for a single device.
type WipeResource struct {
	Name string // human-readable, e.g. "security-rules"
	Path string // e.g. "/config/security/v1/security-rules"
	// Positions lists the "position" query values (pre/post) this
	// resource's list operation requires; nil means no position param.
	Positions []string
}

// WipeResources is the set of resource types reset wipes for a device,
// covering the config the SCM API confirms is folder/snippet/device-scoped.
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

// ListByDevice returns every object at path that is scoped directly to
// deviceID, paginating as needed. If position is non-empty it's passed as
// the "position" query param required by rule-family resources.
//
// Objects are filtered to require Device == deviceID even though the
// device query param should already exclude folder/snippet-scoped
// objects: shared folder/snippet config must never be deleted by reset,
// so this is enforced again defensively rather than trusted on the
// server's filtering alone.
func (c *Client) ListByDevice(path, deviceID, position string) ([]ScopedObject, error) {
	const pageSize = 200

	var all []ScopedObject
	offset := 0
	for {
		q := url.Values{}
		q.Set("device", deviceID)
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
			if obj.Device == deviceID {
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
// This matters because SCM's device-filtered list endpoints resolve
// folder/snippet-scoped (shared) objects into a device's view and echo
// that device back in the response's "device" field, indistinguishably
// from a truly device-owned object -- confirmed live against the lab: a
// shared "$eth-internet" template stored under folder "ngfw-shared" is
// returned with "device": "<serial>" and no folder/snippet field for
// every device that inherits it, using the identical object id in every
// case. A bare-id fetch (no query params) returns the object's real
// stored scope instead, so it's the only reliable way to confirm an
// object found via a device-filtered list is actually safe to delete or
// overwrite.
func (c *Client) GetScopedObject(path, id string) (*ScopedObject, error) {
	var obj ScopedObject
	if err := c.doJSON("GET", path+"/"+id, nil, nil, &obj); err != nil {
		return nil, err
	}
	return &obj, nil
}

// IsDeviceOwned reports whether the object at path/id is truly scoped
// directly to deviceID, as opposed to a folder/snippet-shared object
// resolved into that device's view (see GetScopedObject).
func (c *Client) IsDeviceOwned(path, id, deviceID string) (bool, error) {
	obj, err := c.GetScopedObject(path, id)
	if err != nil {
		return false, err
	}
	return obj.Device == deviceID && obj.Folder == "" && obj.Snippet == "", nil
}

// IsConflict reports whether err is a 409 response, which SCM returns when
// an object can't be deleted because something else still references it
// (e.g. a zone still used by a security rule).
func IsConflict(err error) bool {
	apiErr, ok := err.(*APIError)
	return ok && apiErr.StatusCode == http.StatusConflict
}
