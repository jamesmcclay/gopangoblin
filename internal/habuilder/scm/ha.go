package scm

import (
	"net/url"
)

// HAInterfaceLink is one physical HA interface (HA1, HA1-backup, HA2, HA2-backup).
// IPAddress/Netmask/Gateway are only required by SCM when Port is not "management".
type HAInterfaceLink struct {
	Port      string `json:"port"`
	IPAddress string `json:"ip_address,omitempty"`
	Netmask   string `json:"netmask,omitempty"`
	Gateway   string `json:"gateway,omitempty"`
}

// HAInterfaces is the "interface" object of a HA configuration.
type HAInterfaces struct {
	HA1       HAInterfaceLink  `json:"ha1"`
	HA1Backup *HAInterfaceLink `json:"ha1_backup,omitempty"`
	HA2       HAInterfaceLink  `json:"ha2"`
	HA2Backup *HAInterfaceLink `json:"ha2_backup,omitempty"`
}

// HAElectionOption controls device role and priority within the HA group.
type HAElectionOption struct {
	HARole         string `json:"ha_role"` // "primary" or "secondary"
	DevicePriority string `json:"device_priority,omitempty"`
	Preemptive     *bool  `json:"preemptive,omitempty"`
}

// HAActivePassiveMode holds active/passive-specific settings.
type HAActivePassiveMode struct{}

// HAMode selects active/passive vs active/active (mutually exclusive).
type HAMode struct {
	ActivePassive *HAActivePassiveMode `json:"active_passive,omitempty"`
}

// HAStateSynchronization controls the HA2 session sync transport.
type HAStateSynchronization struct {
	Transport string `json:"transport,omitempty"` // "ethernet", "ip", or "udp"
}

// HAGroup is the "group" object of a HA configuration.
type HAGroup struct {
	GroupID              string                  `json:"group_id"`
	ElectionOption       HAElectionOption        `json:"election_option"`
	Mode                 HAMode                  `json:"mode"`
	PeerIP               string                  `json:"peer_ip"`
	PeerSerial           string                  `json:"peer_serial"`
	StateSynchronization *HAStateSynchronization `json:"state_synchronization,omitempty"`
}

// HAConfiguration is the request/response body for the /ha-configurations endpoint.
// See https://pan.dev/scm/api/config/ngfw/device/get-ha-configuration/
type HAConfiguration struct {
	Device    string       `json:"device,omitempty"`
	Enabled   *bool        `json:"enabled,omitempty"`
	Interface HAInterfaces `json:"interface"`
	Group     HAGroup      `json:"group"`
}

// GetHAConfiguration retrieves the HA configuration for a device. Returns an
// *APIError with StatusCode 404 (check with IsNotFound) if none is configured.
func (c *Client) GetHAConfiguration(deviceID string) (*HAConfiguration, error) {
	q := url.Values{}
	q.Set("device", deviceID)

	var cfg HAConfiguration
	if err := c.doJSON("GET", "/config/device/v1/ha-configurations", q, nil, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// CreateHAConfiguration creates a new HA configuration for a device.
func (c *Client) CreateHAConfiguration(cfg HAConfiguration) (*HAConfiguration, error) {
	var out HAConfiguration
	if err := c.doJSON("POST", "/config/device/v1/ha-configurations", nil, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateHAConfiguration replaces the HA configuration for a device.
func (c *Client) UpdateHAConfiguration(cfg HAConfiguration) (*HAConfiguration, error) {
	var out HAConfiguration
	if err := c.doJSON("PUT", "/config/device/v1/ha-configurations", nil, cfg, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteHAConfiguration removes the HA configuration from a device.
func (c *Client) DeleteHAConfiguration(deviceID string) error {
	q := url.Values{}
	q.Set("device", deviceID)
	return c.doJSON("DELETE", "/config/device/v1/ha-configurations", q, nil, nil)
}
