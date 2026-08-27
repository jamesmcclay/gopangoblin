package scm

import (
	"fmt"
	"net/url"
)

// Device is a subset of the SCM "devices" resource.
// See https://pan.dev/scm/api/config/ngfw/setup/configuration-setup/
type Device struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Folder   string `json:"folder"`
	Hostname string `json:"hostname"`
}

type listDevicesResponse struct {
	Data   []Device `json:"data"`
	Limit  int      `json:"limit"`
	Offset int      `json:"offset"`
	Total  int      `json:"total"`
}

// ListDevices returns every device onboarded to this tenant, paging through
// the /devices endpoint as needed.
func (c *Client) ListDevices() ([]Device, error) {
	const pageSize = 200

	var all []Device
	offset := 0
	for {
		q := url.Values{}
		q.Set("limit", fmt.Sprintf("%d", pageSize))
		q.Set("offset", fmt.Sprintf("%d", offset))

		var page listDevicesResponse
		if err := c.doJSON("GET", "/config/setup/v1/devices", q, nil, &page); err != nil {
			return nil, fmt.Errorf("listing devices: %w", err)
		}
		all = append(all, page.Data...)

		offset += len(page.Data)
		if len(page.Data) == 0 || offset >= page.Total {
			break
		}
	}
	return all, nil
}

// ResolveDeviceBySerial finds the onboarded device whose id or name matches
// the given serial number. SCM identifies NGFW devices by serial number,
// but which field (id vs name) actually carries it isn't consistently
// documented, so both are checked.
func ResolveDeviceBySerial(devices []Device, serial string) (Device, error) {
	for _, d := range devices {
		if d.ID == serial || d.Name == serial {
			return d, nil
		}
	}
	return Device{}, fmt.Errorf("no onboarded device found with serial %q", serial)
}
