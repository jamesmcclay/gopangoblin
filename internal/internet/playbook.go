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

	// TrustZone/UntrustZone name the zone each interface should belong to.
	// Optional: left unset, they fall back to reconcile.go's
	// trustZoneName/untrustZoneName ("trust"/"untrust") constants -- the
	// same defaults this tool has always used. Either way, the named zone
	// is looked up and created if missing, or the interface is merged into
	// it if it already exists. If the interface is already a member of
	// some OTHER zone this scope owns, it's moved. If it's already a
	// member of a shared/inherited zone this scope doesn't own (e.g. SCM's
	// built-in $eth-internet/$eth-local, already zoned "internet"/"local"
	// several folders up), reassigning it fails with a clear error instead
	// of silently doing nothing -- confirmed live: SCM enforces "one zone
	// per interface" globally, checked against the shared zone's own
	// definition regardless of scope-level overrides, so there's no safe
	// way to free the interface up without editing that shared object
	// directly (which this tool won't do -- its blast radius reaches every
	// other folder/device that still inherits it). Give the interface
	// field its own never-before-zoned variable instead (see
	// Untrust02Interface's doc comment on custom variables) if you need it
	// in a specific named zone.
	TrustZone   string `yaml:"trust_zone"`
	UntrustZone string `yaml:"untrust_zone"`

	// Router names the logical-router this item's interfaces should be
	// routed through. Optional: left unset, it falls back to
	// reconcile.go's routerName ("default") constant -- the same default
	// this tool has always used. Subject to the exact same "already routed
	// via a shared/inherited router this scope doesn't own" limitation as
	// TrustZone/UntrustZone above (PAN-OS only allows an interface to
	// belong to one logical-router too).
	Router string `yaml:"router"`

	// Untrust02Interface names an optional second WAN interface (e.g. a
	// redundant/secondary ISP uplink). Unlike TrustInterface/
	// UntrustInterface, there's no built-in SCM template variable for a
	// third port, so this has no required default: leaving it (and
	// default_untrust02_interface) unset simply disables the whole
	// secondary WAN feature for this item. A custom $variable name given
	// here (e.g. "$eth-internet02") needs its own default_value defined
	// somewhere -- either via this item's own var_list, or an ancestor's
	// -- the same way SCM's built-in $eth-internet/$eth-local already are;
	// a literal name (e.g. "ethernet1/2") is handled automatically instead
	// (see normalizeInterfaceName). WAN02CIDR/WAN02Gateway behave like
	// WANCIDR/WANGateway -- static when both are set, DHCP client
	// otherwise.
	Untrust02Interface string `yaml:"untrust02_interface"`
	WAN02CIDR          string `yaml:"wan02_cidr"`
	WAN02Gateway       string `yaml:"wan02_gw"`

	// Untrust02Zone behaves like UntrustZone, falling back to
	// reconcile.go's untrust02ZoneName ("untrust02") constant.
	Untrust02Zone string `yaml:"untrust02_zone"`

	// VarList defines/overrides real SCM template-variable values at this
	// item's own scope (folder/snippet/device) -- e.g. a custom interface
	// variable like $eth-internet02 that Untrust02Interface references,
	// which (unlike $eth-internet/$eth-local) SCM has no built-in
	// definition for. See VariableOverride's doc comment for how this
	// differs from a vars.default_* playbook fallback.
	VarList []VarItem `yaml:"var_list"`

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

// VariableOverride writes SCM template-variable values scoped to exactly
// one of Serial, Folder, or Snippet (validated by LoadPlaybook) -- Name is
// just a display label, like an item_list entry's. A device-scoped entry
// is the common case (a variable that resolves differently per device,
// e.g. $wan_cidr); folder/snippet-scoped entries are for a variable that
// genuinely shares one real value across every device under that scope,
// as opposed to a vars.default_* playbook-side fallback, which only
// affects this tool's own field resolution and never actually writes an
// SCM variable definition anyone else's config could reference.
type VariableOverride struct {
	Name    string    `yaml:"name"`
	Serial  string    `yaml:"serial"`
	Folder  string    `yaml:"folder"`
	Snippet string    `yaml:"snippet"`
	VarList []VarItem `yaml:"var_list"`
}

// Scope returns the (scopeParam, scopeValue) pair this override targets.
func (vo VariableOverride) Scope() (scopeParam, scopeValue string) {
	switch {
	case vo.Folder != "":
		return "folder", vo.Folder
	case vo.Snippet != "":
		return "snippet", vo.Snippet
	default:
		return "device", vo.Serial
	}
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

	// TrustZone/UntrustZone being "" means reconcile.go's hardcoded
	// trustZoneName/untrustZoneName fallback applies -- see
	// Item.TrustZone.
	TrustZone   string
	UntrustZone string

	// Router being "" means reconcile.go's hardcoded routerName fallback
	// applies -- see Item.Router.
	Router string

	// Untrust02Interface being "" means the secondary WAN feature is
	// disabled for this item -- see Item.Untrust02Interface.
	Untrust02Interface string
	WAN02CIDR          string
	WAN02Gateway       string
	Untrust02Zone      string

	VarList []VarItem
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
	if r.TrustZone, err = resolveOptionalField(vars, it.TrustZone, "default_trust_zone"); err != nil {
		return r, fmt.Errorf("item_list entry %q: trust_zone: %w", it.Name, err)
	}
	if r.UntrustZone, err = resolveOptionalField(vars, it.UntrustZone, "default_untrust_zone"); err != nil {
		return r, fmt.Errorf("item_list entry %q: untrust_zone: %w", it.Name, err)
	}
	if r.Router, err = resolveOptionalField(vars, it.Router, "default_router"); err != nil {
		return r, fmt.Errorf("item_list entry %q: router: %w", it.Name, err)
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
	// untrust02_interface/wan02_* fields are all optional, like
	// wan_cidr/wan_gw: the secondary WAN feature is simply disabled for
	// this item when untrust02_interface resolves empty.
	if r.Untrust02Interface, err = resolveOptionalField(vars, it.Untrust02Interface, "default_untrust02_interface"); err != nil {
		return r, fmt.Errorf("item_list entry %q: untrust02_interface: %w", it.Name, err)
	}
	if r.WAN02CIDR, err = resolveOptionalField(vars, it.WAN02CIDR, "default_wan02_cidr"); err != nil {
		return r, fmt.Errorf("item_list entry %q: wan02_cidr: %w", it.Name, err)
	}
	if r.WAN02Gateway, err = resolveOptionalField(vars, it.WAN02Gateway, "default_wan02_gw"); err != nil {
		return r, fmt.Errorf("item_list entry %q: wan02_gw: %w", it.Name, err)
	}
	if r.Untrust02Zone, err = resolveOptionalField(vars, it.Untrust02Zone, "default_untrust02_zone"); err != nil {
		return r, fmt.Errorf("item_list entry %q: untrust02_zone: %w", it.Name, err)
	}
	r.VarList = it.VarList

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

	for _, it := range pb.ItemList {
		for _, v := range it.VarList {
			if v.Name == "" || v.Value == "" {
				return nil, fmt.Errorf("item_list entry %q: var_list entries require both name and value", it.Name)
			}
		}
	}

	for _, vo := range pb.VariableOverrides {
		set := 0
		for _, v := range []string{vo.Serial, vo.Folder, vo.Snippet} {
			if v != "" {
				set++
			}
		}
		if set != 1 {
			return nil, fmt.Errorf("variable_overrides entry %q: exactly one of serial, folder, or snippet is required", vo.Name)
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
