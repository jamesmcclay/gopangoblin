package habuilder

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Mode controls how habuilder reconciles the playbook against SCM.
type Mode string

const (
	ModeInstall         Mode = "install"          // create only HA configs that are missing
	ModeInstallOverride Mode = "install-override" // create/update every HA config regardless of current state
	ModeUninstall       Mode = "uninstall"        // remove HA config from every device in the playbook
)

// Playbook is the parsed structure of a ha_pairs.yml file.
type Playbook struct {
	Name   string            `yaml:"name"`
	Mode   Mode              `yaml:"mode"`
	Push   bool              `yaml:"push"`
	Vars   map[string]string `yaml:"vars"`
	FwList []FirewallPair    `yaml:"fw_list"`
}

// FirewallPair is one HA pair entry under fw_list. Fields left blank fall
// back to the matching "default_*" key in the playbook's vars, and any
// string field may instead reference a var explicitly with "vars.<key>".
type FirewallPair struct {
	Name            string `yaml:"name"`
	PrimarySerial   string `yaml:"primary_serial"`
	SecondarySerial string `yaml:"secondary_serial"`

	PrimaryIP   string `yaml:"primary_ip"`
	SecondaryIP string `yaml:"secondary_ip"`
	Netmask     string `yaml:"netmask"`

	PrimaryDataIP   string `yaml:"primary_data_ip"`
	SecondaryDataIP string `yaml:"secondary_data_ip"`

	ControlLinkInterface string `yaml:"control_link_interface"`
	DataLinkInterface    string `yaml:"data_link_interface"`

	GroupID string `yaml:"group_id"`
}

// varRef resolves "vars.<key>" references. Any other string is returned unchanged.
func varRef(vars map[string]string, value string) (string, error) {
	const prefix = "vars."
	if !strings.HasPrefix(value, prefix) {
		return value, nil
	}
	key := strings.TrimPrefix(value, prefix)
	v, ok := vars[key]
	if !ok {
		return "", fmt.Errorf("references undefined var %q", key)
	}
	return v, nil
}

// resolveField returns the field value if set (resolving an explicit
// "vars.<key>" reference), otherwise falls back to vars[defaultKey].
func resolveField(vars map[string]string, field, defaultKey string) (string, error) {
	if field != "" {
		return varRef(vars, field)
	}
	v, ok := vars[defaultKey]
	if !ok {
		return "", fmt.Errorf("no value set and no default var %q defined", defaultKey)
	}
	return v, nil
}

// ResolvedFirewallPair is a FirewallPair with every field fully resolved
// against the playbook's vars, ready to build an SCM HA configuration from.
type ResolvedFirewallPair struct {
	Name string

	PrimarySerial   string
	SecondarySerial string

	PrimaryIP   string
	SecondaryIP string
	Netmask     string

	PrimaryDataIP   string
	SecondaryDataIP string

	ControlLinkInterface string
	DataLinkInterface    string

	GroupID string
}

// Resolve fills in defaults from vars and validates required fields.
func (fw FirewallPair) Resolve(vars map[string]string) (ResolvedFirewallPair, error) {
	var r ResolvedFirewallPair
	r.Name = fw.Name

	if fw.PrimarySerial == "" || fw.SecondarySerial == "" {
		return r, fmt.Errorf("fw_list entry %q: primary_serial and secondary_serial are required", fw.Name)
	}
	r.PrimarySerial = fw.PrimarySerial
	r.SecondarySerial = fw.SecondarySerial

	var err error
	if r.PrimaryIP, err = resolveField(vars, fw.PrimaryIP, "default_HA1_IP"); err != nil {
		return r, fmt.Errorf("fw_list entry %q: primary_ip: %w", fw.Name, err)
	}
	if r.SecondaryIP, err = resolveField(vars, fw.SecondaryIP, "default_HA2_IP"); err != nil {
		return r, fmt.Errorf("fw_list entry %q: secondary_ip: %w", fw.Name, err)
	}
	if r.Netmask, err = resolveField(vars, fw.Netmask, "default_HA_netmask"); err != nil {
		return r, fmt.Errorf("fw_list entry %q: netmask: %w", fw.Name, err)
	}
	if r.PrimaryDataIP, err = resolveField(vars, fw.PrimaryDataIP, "default_HA1_data_IP"); err != nil {
		return r, fmt.Errorf("fw_list entry %q: primary_data_ip: %w", fw.Name, err)
	}
	if r.SecondaryDataIP, err = resolveField(vars, fw.SecondaryDataIP, "default_HA2_data_IP"); err != nil {
		return r, fmt.Errorf("fw_list entry %q: secondary_data_ip: %w", fw.Name, err)
	}
	if r.ControlLinkInterface, err = resolveField(vars, fw.ControlLinkInterface, "default_control_link_interface"); err != nil {
		return r, fmt.Errorf("fw_list entry %q: control_link_interface: %w", fw.Name, err)
	}
	if r.DataLinkInterface, err = resolveField(vars, fw.DataLinkInterface, "default_data_link_interface"); err != nil {
		return r, fmt.Errorf("fw_list entry %q: data_link_interface: %w", fw.Name, err)
	}

	r.GroupID = fw.GroupID
	if r.GroupID == "" {
		if v, ok := vars["default_group_id"]; ok {
			r.GroupID = v
		} else {
			r.GroupID = "1"
		}
	} else {
		if resolved, err := varRef(vars, r.GroupID); err != nil {
			return r, fmt.Errorf("fw_list entry %q: group_id: %w", fw.Name, err)
		} else {
			r.GroupID = resolved
		}
	}

	return r, nil
}

// LoadPlaybook reads and parses a ha_pairs.yml file.
func LoadPlaybook(path string) (*Playbook, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading playbook: %w", err)
	}

	var pb Playbook
	if err := yaml.Unmarshal(data, &pb); err != nil {
		return nil, fmt.Errorf("parsing playbook: %w", err)
	}

	switch pb.Mode {
	case ModeInstall, ModeInstallOverride, ModeUninstall:
	default:
		return nil, fmt.Errorf("playbook mode must be one of %q, %q, %q, got %q",
			ModeInstall, ModeInstallOverride, ModeUninstall, pb.Mode)
	}

	if len(pb.FwList) == 0 {
		return nil, fmt.Errorf("playbook has no fw_list entries")
	}

	return &pb, nil
}

// Resolved returns every fw_list entry fully resolved against the playbook's vars.
func (pb *Playbook) Resolved() ([]ResolvedFirewallPair, error) {
	out := make([]ResolvedFirewallPair, 0, len(pb.FwList))
	for _, fw := range pb.FwList {
		r, err := fw.Resolve(pb.Vars)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
