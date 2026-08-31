package internet

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Mode controls how the internet tool reconciles the playbook against SCM.
type Mode string

const (
	ModeInstall         Mode = "install"          // configure only targets that don't already have it
	ModeInstallOverride Mode = "install-override" // configure every target regardless of current state
	ModeUninstall       Mode = "uninstall"        // remove the configuration from every target
)

// ItemType selects what kind of SCM scope an item_list entry targets.
type ItemType string

const (
	ItemFolder   ItemType = "folder"
	ItemSnippet  ItemType = "snippet"
	ItemFirewall ItemType = "firewall"
)

// Playbook is the parsed structure of an internet.yml file.
type Playbook struct {
	Name              string             `yaml:"name"`
	Mode              Mode               `yaml:"mode"`
	Push              bool               `yaml:"push"`
	Vars              map[string]string  `yaml:"vars"`
	ItemList          []Item             `yaml:"item_list"`
	VariableOverrides []VariableOverride `yaml:"variable_overrides"`
}

// Item is one entry under item_list: a folder, snippet, or firewall to
// configure basic internet access on. Fields left blank fall back to the
// matching "default_*" key in the playbook's vars, and any string field
// may instead reference a var explicitly with "vars.<key>". Interface
// fields may hold either a literal SCM interface name (e.g. "ethernet1/4")
// or a SCM template variable name (e.g. "$eth-local") -- both are just
// passed through as-is to SCM.
type Item struct {
	Name string   `yaml:"name"`
	Type ItemType `yaml:"type"`

	// Serial identifies the device for a type: firewall item. It's
	// unrelated to Name (a firewall item's Name is just a display label,
	// not looked up in SCM) and is required when Type is ItemFirewall.
	Serial string `yaml:"serial"`

	TrustInterface   string `yaml:"trust_interface"`
	UntrustInterface string `yaml:"untrust_interface"`
	WANCIDR          string `yaml:"wan_cidr"`
	WANGateway       string `yaml:"wan_gw"`
	LANCIDR          string `yaml:"lan_cidr"`
	DNSServer        string `yaml:"dns_server"`

	// DHCPPool is the LAN DHCP server's address pool range (e.g.
	// "10.0.0.128-10.0.0.254"). Optional: when unset and lan_cidr is a
	// literal CIDR (not a $variable), it's auto-derived as the upper half
	// of lan_cidr's usable host addresses. It's required (an error if
	// unset) when lan_cidr is a $variable, since there's then no concrete
	// network to derive a pool from until SCM resolves it per device.
	DHCPPool string `yaml:"dhcp_pool"`

	// LANGateway is the gateway address the LAN DHCP server hands out to
	// clients (bare IP, no mask -- confirmed live: PAN-OS's DHCP gateway
	// field rejects a "/nn" suffix, "address must be /32 or without
	// subnet mask", so it can't just reuse lan_cidr's value directly).
	// Optional: when unset and lan_cidr is a literal CIDR, it's
	// auto-derived as lan_cidr's own host address. Required (an error if
	// unset) when lan_cidr is a $variable, for the same reason as
	// dhcp_pool -- there's no concrete address to derive it from until
	// SCM resolves it per device.
	LANGateway string `yaml:"lan_gw"`
}

// VariableOverride writes per-firewall values for SCM template variables
// defined at a folder/snippet level (e.g. a $trust_interface variable
// that resolves differently per device).
type VariableOverride struct {
	Name    string    `yaml:"name"`
	Serial  string    `yaml:"serial"`
	VarList []VarItem `yaml:"var_list"`
}

// VarItem is one variable name/value pair under a VariableOverride's var_list.
type VarItem struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
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

// resolveOptionalField is like resolveField, but returns "" instead of an
// error when neither the field nor the default var is set.
func resolveOptionalField(vars map[string]string, field, defaultKey string) (string, error) {
	if field != "" {
		return varRef(vars, field)
	}
	if v, ok := vars[defaultKey]; ok {
		return v, nil
	}
	return "", nil
}

// ResolvedItem is an Item with every field fully resolved against the
// playbook's vars, ready to build SCM config from.
type ResolvedItem struct {
	Name   string
	Type   ItemType
	Serial string // only set when Type == ItemFirewall

	TrustInterface   string
	UntrustInterface string
	WANCIDR          string
	WANGateway       string
	LANCIDR          string
	DNSServer        string
	DHCPPool         string // "" means auto-derive from LANCIDR if possible
	LANGateway       string // "" means auto-derive from LANCIDR if possible
}

// Resolve fills in defaults from vars and validates required fields.
func (it Item) Resolve(vars map[string]string) (ResolvedItem, error) {
	var r ResolvedItem
	r.Name = it.Name

	switch it.Type {
	case ItemFolder, ItemSnippet, ItemFirewall:
		r.Type = it.Type
	default:
		return r, fmt.Errorf("item_list entry %q: type must be one of %q, %q, %q, got %q",
			it.Name, ItemFolder, ItemSnippet, ItemFirewall, it.Type)
	}

	if it.Type == ItemFirewall {
		if it.Serial == "" {
			return r, fmt.Errorf("item_list entry %q: serial is required for type %q", it.Name, ItemFirewall)
		}
		r.Serial = it.Serial
	}

	var err error
	if r.TrustInterface, err = resolveField(vars, it.TrustInterface, "default_trust_interface"); err != nil {
		return r, fmt.Errorf("item_list entry %q: trust_interface: %w", it.Name, err)
	}
	if r.UntrustInterface, err = resolveField(vars, it.UntrustInterface, "default_untrust_interface"); err != nil {
		return r, fmt.Errorf("item_list entry %q: untrust_interface: %w", it.Name, err)
	}
	// wan_cidr/wan_gw are optional, unlike the other fields: the untrust
	// interface is DHCP client by default, and only becomes a static
	// interface (using these two values, plus a manual default route via
	// wan_gw since DHCP's auto-route won't apply) when both are actually
	// set -- so there's no requirement to give them a playbook-level
	// default the way trust_interface/lan_cidr/etc. have.
	if r.WANCIDR, err = resolveOptionalField(vars, it.WANCIDR, "default_wan_cidr"); err != nil {
		return r, fmt.Errorf("item_list entry %q: wan_cidr: %w", it.Name, err)
	}
	if r.WANGateway, err = resolveOptionalField(vars, it.WANGateway, "default_wan_gw"); err != nil {
		return r, fmt.Errorf("item_list entry %q: wan_gw: %w", it.Name, err)
	}
	if r.LANCIDR, err = resolveField(vars, it.LANCIDR, "default_lan_cidr"); err != nil {
		return r, fmt.Errorf("item_list entry %q: lan_cidr: %w", it.Name, err)
	}
	if r.DNSServer, err = resolveField(vars, it.DNSServer, "default_dns_server"); err != nil {
		return r, fmt.Errorf("item_list entry %q: dns_server: %w", it.Name, err)
	}
	if r.DHCPPool, err = resolveOptionalField(vars, it.DHCPPool, "default_dhcp_pool"); err != nil {
		return r, fmt.Errorf("item_list entry %q: dhcp_pool: %w", it.Name, err)
	}
	if r.LANGateway, err = resolveOptionalField(vars, it.LANGateway, "default_lan_gw"); err != nil {
		return r, fmt.Errorf("item_list entry %q: lan_gw: %w", it.Name, err)
	}

	return r, nil
}

// LoadPlaybook reads and parses an internet.yml file.
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

	if len(pb.ItemList) == 0 {
		return nil, fmt.Errorf("playbook has no item_list entries")
	}

	for _, vo := range pb.VariableOverrides {
		if vo.Serial == "" {
			return nil, fmt.Errorf("variable_overrides entry %q: serial is required", vo.Name)
		}
		if len(vo.VarList) == 0 {
			return nil, fmt.Errorf("variable_overrides entry %q: var_list has no entries", vo.Name)
		}
		for _, v := range vo.VarList {
			if v.Name == "" || v.Value == "" {
				return nil, fmt.Errorf("variable_overrides entry %q: var_list entries require both name and value", vo.Name)
			}
		}
	}

	return &pb, nil
}

// Resolved returns every item_list entry fully resolved against the playbook's vars.
func (pb *Playbook) Resolved() ([]ResolvedItem, error) {
	out := make([]ResolvedItem, 0, len(pb.ItemList))
	for _, it := range pb.ItemList {
		r, err := it.Resolve(pb.Vars)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
