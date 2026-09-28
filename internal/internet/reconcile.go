package internet

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/jamesmcclay/gopangoblin/internal/scm"
)

// Fixed names for the objects this tool creates, following ordinary
// PAN-OS convention. Zones and the logical router are treated as
// potentially pre-existing/shared (very common names in any real
// deployment): install merges our interface into them rather than
// replacing them outright, and uninstall only removes our interface from
// them, never deletes the zone/router object itself.
const (
	trustZoneName    = "trust"
	untrustZoneName  = "untrust"
	routerName       = "default"
	vrfName          = "default"
	natRuleName      = "gopangoblin-internet-nat"
	securityRuleName = "gopangoblin-internet-access"
	defaultRouteName = "gopangoblin-default-route"

	// untrust02ZoneName/natRuleName02/defaultRouteName02 are the analogous
	// names used for an item's optional second WAN interface (see
	// ResolvedItem.Untrust02Interface). Unlike the primary NAT rule, wan02 gets
	// its own separate NAT rule rather than sharing natRuleName's: a
	// dynamic-ip-and-port source translation is tied to one specific
	// egress interface, so two WAN interfaces can't share one rule.
	// The security rule IS shared -- its To list simply gains
	// untrust02ZoneName alongside untrustZoneName -- since both are just
	// "allow trust to reach the internet" regardless of egress path.
	untrust02ZoneName  = "untrust02"
	natRuleName02      = "gopangoblin-internet-nat-wan02"
	defaultRouteName02 = "gopangoblin-default-route-wan02"

	// primaryRouteMetric/secondaryRouteMetric: PAN-OS's default static
	// route metric is 10, and it rejects two routes to the same
	// destination (0.0.0.0/0 here) sharing one metric outright ("...is
	// not unique among static routes to destination 0.0.0.0/0") --
	// confirmed live once a secondary WAN's default route was added
	// alongside the primary's. secondaryRouteMetric is deliberately
	// higher (a backup path, used only if the primary is down) rather
	// than equal (which would need PAN-OS ECMP explicitly enabled on the
	// logical router to be meaningful, which this tool doesn't manage).
	primaryRouteMetric   = 10
	secondaryRouteMetric = 20
)

// isStaticWAN reports whether a WAN interface should be configured with a
// static IP (both cidr and gateway set) rather than as a DHCP client.
func isStaticWAN(cidr, gateway string) bool {
	return cidr != "" && gateway != ""
}

// resolveConfiguredName returns configured if the playbook set it
// explicitly (e.g. it.TrustZone, it.Router), otherwise reconcile.go's
// hardcoded fallback -- explicit reports which case applied, since
// ensureInterfaceZoned/ensureInterfaceRouted treat them differently.
func resolveConfiguredName(configured, fallback string) (name string, explicit bool) {
	if configured != "" {
		return configured, true
	}
	return fallback, false
}

// syntheticInterfaceVarPrefix marks a "$variable" name normalizeInterfaceName
// invents on behalf of a literal interface name -- see its doc comment.
const syntheticInterfaceVarPrefix = "$gopangoblin-iface-"

// normalizeInterfaceName returns the name SCM's ethernet-interfaces "name"
// field should actually use for name at this scope. Confirmed live: a
// literal interface name (e.g. "ethernet1/2") is only valid at device
// scope -- at folder/snippet scope SCM rejects it outright
// ("INVALID_STRING_REGEX", even for an interface name already in live use
// elsewhere at device scope) and requires a "$variable" reference instead,
// regardless of whether every device under that scope actually shares the
// same literal port. So a literal name given for a folder/snippet-scoped
// item (e.g. a playbook's untrust02_interface: "ethernet1/2") is
// transparently wrapped in a synthetic variable unique to that literal
// (reversible via syntheticInterfaceLiteral), which installEthernetInterface gives a
// matching default_value the first time it's created there -- the same
// default_value indirection mechanism SCM's own built-in
// $eth-internet/$eth-local use for exactly this reason.
func normalizeInterfaceName(scopeParam, name string) string {
	if scopeParam == "device" || strings.HasPrefix(name, "$") {
		return name
	}
	return syntheticInterfaceVarPrefix + strings.ReplaceAll(name, "/", "-")
}

// syntheticInterfaceLiteral reverses normalizeInterfaceName: if name is one
// of its synthetic variables, returns the original literal interface name
// and true; otherwise "", false.
func syntheticInterfaceLiteral(name string) (string, bool) {
	if !strings.HasPrefix(name, syntheticInterfaceVarPrefix) {
		return "", false
	}
	return strings.ReplaceAll(strings.TrimPrefix(name, syntheticInterfaceVarPrefix), "-", "/"), true
}

type reconciler struct {
	client  *scm.Client
	dryRun  bool
	mode    Mode
	devices []scm.Device
	folders []scm.Folder

	// touched collects the serials of devices actually affected this run
	// (a firewall item directly, or any device under an affected
	// folder/snippet item or variable_overrides entry), so a subsequent
	// push targets everything actually changed.
	touched map[string]bool
}

func (r *reconciler) markTouched(serial string) {
	if r.touched == nil {
		r.touched = map[string]bool{}
	}
	r.touched[serial] = true
}

func (r *reconciler) touchedSerials() []string {
	out := make([]string, 0, len(r.touched))
	for s := range r.touched {
		out = append(out, s)
	}
	return out
}

// markAffected marks every device reachable from the given scope as
// touched: itself for a device scope, or every device inheriting from a
// folder/snippet scope.
func (r *reconciler) markAffected(scopeParam, scopeValue string) {
	switch scopeParam {
	case "device":
		r.markTouched(scopeValue)
	case "folder":
		for _, d := range scm.DevicesUnderFolder(r.devices, r.folders, scopeValue) {
			r.markTouched(d.ID)
		}
	case "snippet":
		for _, d := range scm.DevicesUnderSnippet(r.devices, r.folders, scopeValue) {
			r.markTouched(d.ID)
		}
	}
}

// scopeFields returns the (folder, snippet, device) triple to set on a
// resource body for the given scope.
func scopeFields(scopeParam, scopeValue string) (folder, snippet, device string) {
	switch scopeParam {
	case "folder":
		return scopeValue, "", ""
	case "snippet":
		return "", scopeValue, ""
	default:
		return "", "", scopeValue
	}
}

// findOwned finds the object at path, scoped to scopeParam=scopeValue,
// whose name equals name and is confirmed truly owned by that scope (not
// inherited from an ancestor folder/snippet -- see scm.IsScopedTo).
// Returns nil if none exists.
func (r *reconciler) findOwned(path, scopeParam, scopeValue, name, position string) (*scm.ScopedObject, error) {
	objs, err := r.client.ListByScope(path, scopeParam, scopeValue, position)
	if err != nil {
		return nil, err
	}
	for _, obj := range objs {
		if obj.Name != name {
			continue
		}
		owned, full, err := r.client.IsScopedTo(path, obj.ID, scopeParam, scopeValue)
		if err != nil {
			return nil, err
		}
		if owned {
			return full, nil
		}
	}
	return nil, nil
}

func mergeString(list []string, value string) []string {
	for _, v := range list {
		if v == value {
			return list
		}
	}
	return append(list, value)
}

func removeString(list []string, value string) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		if v != value {
			out = append(out, v)
		}
	}
	return out
}

// reconcileItem is the entry point for one item_list entry. It normalizes
// any literal interface names to SCM's required form before dispatching
// (see normalizeInterfaceName) -- every downstream function operates on
// the normalized ResolvedItem.
func (r *reconciler) reconcileItem(scopeParam, scopeValue, label string, it ResolvedItem) error {
	it.TrustInterface = normalizeInterfaceName(scopeParam, it.TrustInterface)
	it.UntrustInterface = normalizeInterfaceName(scopeParam, it.UntrustInterface)
	if it.Untrust02Interface != "" {
		it.Untrust02Interface = normalizeInterfaceName(scopeParam, it.Untrust02Interface)
	}

	if r.mode == ModeUninstall {
		return r.uninstallItem(scopeParam, scopeValue, label, it)
	}

	if r.mode == ModeInstall {
		complete, err := r.itemFullyConfigured(scopeParam, scopeValue, it)
		if err != nil {
			return fmt.Errorf("checking existing state: %w", err)
		}
		if complete {
			fmt.Printf("  [skip]   %s already configured\n", label)
			return nil
		}
	}

	return r.installItem(scopeParam, scopeValue, label, it)
}

// itemFullyConfigured reports whether every piece installItem creates is
// already in place, so ModeInstall can decide to skip the whole item.
// Checking only whether the untrust interface exists (an earlier version
// of this function) isn't enough: a prior run that failed partway
// through (e.g. after creating interfaces but before the NAT rule) would
// otherwise look "already configured" forever, and ModeInstall would
// never complete it -- only ModeInstallOverride would. Each install*
// function is itself idempotent (create-if-missing, update-if-present),
// so proceeding to installItem whenever any piece is missing is safe:
// pieces that already match are simply left as a harmless no-op update.
func (r *reconciler) itemFullyConfigured(scopeParam, scopeValue string, it ResolvedItem) (bool, error) {
	staticWAN := isStaticWAN(it.WANCIDR, it.WANGateway)
	hasWAN02 := it.Untrust02Interface != ""
	staticWAN02 := isStaticWAN(it.WAN02CIDR, it.WAN02Gateway)

	for _, item := range it.VarList {
		ok, err := r.varListItemSatisfied(scopeParam, scopeValue, item)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}

	ownedChecks := []struct {
		path string
		name string
		pos  string
	}{
		{scm.EthernetInterfacesPath, it.TrustInterface, ""},
		{scm.EthernetInterfacesPath, it.UntrustInterface, ""},
		{scm.DHCPInterfacesPath, it.TrustInterface, ""},
		{scm.NATRulesPath, natRuleName, "pre"},
		{scm.SecurityRulesPath, securityRuleName, "pre"},
	}
	if hasWAN02 {
		ownedChecks = append(ownedChecks,
			struct {
				path string
				name string
				pos  string
			}{scm.EthernetInterfacesPath, it.Untrust02Interface, ""},
			struct {
				path string
				name string
				pos  string
			}{scm.NATRulesPath, natRuleName02, "pre"},
		)
	}
	for _, c := range ownedChecks {
		obj, err := r.findOwned(c.path, scopeParam, scopeValue, c.name, c.pos)
		if err != nil {
			return false, err
		}
		if obj == nil {
			return false, nil
		}
	}

	trustZoneTarget, trustExplicit := resolveConfiguredName(it.TrustZone, trustZoneName)
	untrustZoneTarget, untrustExplicit := resolveConfiguredName(it.UntrustZone, untrustZoneName)
	zoneChecks := []struct {
		iface    string
		target   string
		explicit bool
	}{
		{it.TrustInterface, trustZoneTarget, trustExplicit},
		{it.UntrustInterface, untrustZoneTarget, untrustExplicit},
	}
	if hasWAN02 {
		untrust02ZoneTarget, untrust02Explicit := resolveConfiguredName(it.Untrust02Zone, untrust02ZoneName)
		zoneChecks = append(zoneChecks, struct {
			iface    string
			target   string
			explicit bool
		}{it.Untrust02Interface, untrust02ZoneTarget, untrust02Explicit})
	}
	routerTarget, routerExplicit := resolveConfiguredName(it.Router, routerName)
	for _, c := range zoneChecks {
		zone, err := r.findZoneWithInterface(scopeParam, scopeValue, c.iface)
		if err != nil {
			return false, err
		}
		if zone == nil {
			return false, nil
		}
		if c.explicit && zone.Name != c.target {
			return false, nil
		}

		router, err := r.findRouterWithInterface(scopeParam, scopeValue, c.iface)
		if err != nil {
			return false, err
		}
		if router == nil {
			return false, nil
		}
		if routerExplicit && router.Name != routerTarget {
			return false, nil
		}
	}

	if staticWAN {
		router, err := r.findRouterWithInterface(scopeParam, scopeValue, it.UntrustInterface)
		if err != nil {
			return false, err
		}
		vrf := vrfContaining(router.VRF, it.UntrustInterface)
		if vrf == nil || vrf.RoutingTable == nil || vrf.RoutingTable.IP == nil || !hasStaticRoute(vrf.RoutingTable.IP.StaticRoute, defaultRouteName) {
			return false, nil
		}
	}
	if hasWAN02 && staticWAN02 {
		router, err := r.findRouterWithInterface(scopeParam, scopeValue, it.Untrust02Interface)
		if err != nil {
			return false, err
		}
		vrf := vrfContaining(router.VRF, it.Untrust02Interface)
		if vrf == nil || vrf.RoutingTable == nil || vrf.RoutingTable.IP == nil || !hasStaticRoute(vrf.RoutingTable.IP.StaticRoute, defaultRouteName02) {
			return false, nil
		}
	}

	if hasWAN02 {
		zone, err := r.findZoneWithInterface(scopeParam, scopeValue, it.Untrust02Interface)
		if err != nil {
			return false, err
		}
		secRuleObj, err := r.findOwned(scm.SecurityRulesPath, scopeParam, scopeValue, securityRuleName, "pre")
		if err != nil {
			return false, err
		}
		if secRuleObj == nil {
			return false, nil
		}
		full, err := r.client.GetSecurityRule(secRuleObj.ID)
		if err != nil {
			return false, err
		}
		if zone == nil || !containsString(full.To, zone.Name) {
			return false, nil
		}
	}

	return true, nil
}

func containsString(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}

func hasStaticRoute(routes []scm.LogicalRouterStaticRoute, name string) bool {
	for _, r := range routes {
		if r.Name == name {
			return true
		}
	}
	return false
}

func (r *reconciler) installItem(scopeParam, scopeValue, label string, it ResolvedItem) error {
	folder, snippet, device := scopeFields(scopeParam, scopeValue)
	staticWAN := isStaticWAN(it.WANCIDR, it.WANGateway)
	hasWAN02 := it.Untrust02Interface != ""
	staticWAN02 := isStaticWAN(it.WAN02CIDR, it.WAN02Gateway)

	if _, err := r.reconcileVarList(scopeParam, scopeValue, label, it.VarList); err != nil {
		return fmt.Errorf("var_list: %w", err)
	}

	varsToDefine := []string{it.LANCIDR, it.WANCIDR, it.WANGateway, it.DNSServer, it.DHCPPool, it.LANGateway}
	if hasWAN02 {
		varsToDefine = append(varsToDefine, it.WAN02CIDR, it.WAN02Gateway)
	}
	for _, v := range varsToDefine {
		if err := r.ensureVariableDefined(scopeParam, scopeValue, folder, snippet, device, v); err != nil {
			return fmt.Errorf("defining variable %s: %w", v, err)
		}
	}

	trustLayer3 := scm.EthernetInterfaceLayer3{
		IP: []scm.EthernetInterfaceStaticIP{{Name: it.LANCIDR}},
	}
	if err := r.installEthernetInterface(scopeParam, scopeValue, folder, snippet, device, it.TrustInterface, trustLayer3); err != nil {
		return fmt.Errorf("trust interface: %w", err)
	}

	var untrustLayer3 scm.EthernetInterfaceLayer3
	if staticWAN {
		untrustLayer3 = scm.EthernetInterfaceLayer3{
			IP: []scm.EthernetInterfaceStaticIP{{Name: it.WANCIDR}},
		}
	} else {
		enable, createRoute := true, true
		untrustLayer3 = scm.EthernetInterfaceLayer3{
			DHCPClient: &scm.EthernetInterfaceDHCPClient{Enable: &enable, CreateDefaultRoute: &createRoute},
		}
	}
	if err := r.installEthernetInterface(scopeParam, scopeValue, folder, snippet, device, it.UntrustInterface, untrustLayer3); err != nil {
		return fmt.Errorf("untrust interface: %w", err)
	}

	trustZoneTarget, trustExplicit := resolveConfiguredName(it.TrustZone, trustZoneName)
	trustZone, err := r.ensureInterfaceZoned(scopeParam, scopeValue, folder, snippet, device, it.TrustInterface, trustZoneTarget, trustExplicit)
	if err != nil {
		return fmt.Errorf("trust zone: %w", err)
	}
	untrustZoneTarget, untrustExplicit := resolveConfiguredName(it.UntrustZone, untrustZoneName)
	untrustZone, err := r.ensureInterfaceZoned(scopeParam, scopeValue, folder, snippet, device, it.UntrustInterface, untrustZoneTarget, untrustExplicit)
	if err != nil {
		return fmt.Errorf("untrust zone: %w", err)
	}

	var untrustZone02 string
	if hasWAN02 {
		var wan02Layer3 scm.EthernetInterfaceLayer3
		if staticWAN02 {
			wan02Layer3 = scm.EthernetInterfaceLayer3{
				IP: []scm.EthernetInterfaceStaticIP{{Name: it.WAN02CIDR}},
			}
		} else {
			enable, createRoute := true, true
			wan02Layer3 = scm.EthernetInterfaceLayer3{
				DHCPClient: &scm.EthernetInterfaceDHCPClient{Enable: &enable, CreateDefaultRoute: &createRoute},
			}
		}
		if err := r.installEthernetInterface(scopeParam, scopeValue, folder, snippet, device, it.Untrust02Interface, wan02Layer3); err != nil {
			return fmt.Errorf("secondary WAN interface: %w", err)
		}
		untrust02ZoneTarget, untrust02Explicit := resolveConfiguredName(it.Untrust02Zone, untrust02ZoneName)
		untrustZone02, err = r.ensureInterfaceZoned(scopeParam, scopeValue, folder, snippet, device, it.Untrust02Interface, untrust02ZoneTarget, untrust02Explicit)
		if err != nil {
			return fmt.Errorf("secondary WAN zone: %w", err)
		}
	}

	if err := r.installRouter(scopeParam, scopeValue, folder, snippet, device, it, staticWAN, staticWAN02); err != nil {
		return fmt.Errorf("logical router: %w", err)
	}

	if err := r.installDHCP(scopeParam, scopeValue, folder, snippet, device, it); err != nil {
		return fmt.Errorf("DHCP server: %w", err)
	}

	if err := r.installNAT(scopeParam, scopeValue, folder, snippet, device, natRuleName, trustZone, untrustZone, it.UntrustInterface); err != nil {
		return fmt.Errorf("NAT rule: %w", err)
	}
	if hasWAN02 {
		if err := r.installNAT(scopeParam, scopeValue, folder, snippet, device, natRuleName02, trustZone, untrustZone02, it.Untrust02Interface); err != nil {
			return fmt.Errorf("secondary WAN NAT rule: %w", err)
		}
	}

	toZones := []string{untrustZone}
	if hasWAN02 {
		toZones = append(toZones, untrustZone02)
	}
	if err := r.installSecurityRule(scopeParam, scopeValue, folder, snippet, device, trustZone, toZones); err != nil {
		return fmt.Errorf("security rule: %w", err)
	}

	fmt.Printf("  [install] %s internet access configured\n", label)
	r.markAffected(scopeParam, scopeValue)
	return nil
}

// installEthernetInterface creates or updates the ethernet-interfaces
// override for name at this scope. It always carries forward whatever
// DefaultValue is already in effect for name (from our own existing
// override, or from an ancestor's definition if this is a brand-new
// override) -- see scm.EthernetInterface's doc comment for why omitting
// it silently breaks every future push to this scope.
func (r *reconciler) installEthernetInterface(scopeParam, scopeValue, folder, snippet, device, name string, layer3 scm.EthernetInterfaceLayer3) error {
	target := scm.EthernetInterface{Name: name, Folder: folder, Snippet: snippet, Device: device, Layer3: layer3}

	existing, err := r.findOwned(scm.EthernetInterfacesPath, scopeParam, scopeValue, name, "")
	if err != nil {
		return err
	}

	if existing != nil {
		full, err := r.client.GetEthernetInterface(existing.ID)
		if err != nil {
			return err
		}
		target.DefaultValue = full.DefaultValue
		if r.dryRun {
			return nil
		}
		_, err = r.client.UpdateEthernetInterface(existing.ID, target)
		return err
	}

	defaultValue, err := r.findInterfaceDefaultValue(scopeParam, scopeValue, name)
	if err != nil {
		return err
	}
	if defaultValue == "" {
		// A genuinely new synthetic variable (see normalizeInterfaceName)
		// has no ancestor definition to inherit -- give it its own,
		// pointing at the literal interface it stands in for.
		if literal, ok := syntheticInterfaceLiteral(name); ok {
			defaultValue = literal
		}
	}
	target.DefaultValue = defaultValue

	if r.dryRun {
		return nil
	}
	_, err = r.client.CreateEthernetInterface(target)
	return err
}

// findInterfaceDefaultValue searches the whole visible hierarchy (not
// just this exact scope) for an existing ethernet-interfaces object
// named name, returning its DefaultValue if found, "" otherwise (e.g. a
// genuinely new, never-before-seen custom interface name).
func (r *reconciler) findInterfaceDefaultValue(scopeParam, scopeValue, name string) (string, error) {
	objs, err := r.client.ListVisible(scm.EthernetInterfacesPath, scopeParam, scopeValue, "")
	if err != nil {
		return "", err
	}
	for _, obj := range objs {
		if obj.Name != name {
			continue
		}
		full, err := r.client.GetEthernetInterface(obj.ID)
		if err != nil {
			return "", err
		}
		if full.DefaultValue != "" {
			return full.DefaultValue, nil
		}
	}
	return "", nil
}

// ensureInterfaceZoned makes sure ifaceName ends up a member of the zone
// named zoneName, scoped at (scopeParam, scopeValue) -- creating that zone
// (owned at this exact scope) if it doesn't already exist, or merging
// ifaceName into it if it does -- and returns zoneName. explicit
// distinguishes two callers:
//
//   - explicit=false (zoneName is reconcile.go's hardcoded fallback, e.g.
//     trustZoneName, because the playbook left trust_zone/etc. unset):
//     preserves this tool's original behavior of never disturbing an
//     interface that's already zoned anywhere else. Confirmed live: SCM's
//     built-in $eth-local/$eth-internet are already members of shared
//     zones literally named "local"/"internet" (not "trust"/"untrust")
//     several folders up -- if ifaceName is already zoned anywhere, that
//     existing zone's real name is returned unchanged and nothing is
//     modified, rather than also cramming it into zoneName too.
//   - explicit=true (the playbook set trust_zone/untrust_zone/
//     untrust02_zone explicitly): zoneName is authoritative. If ifaceName
//     is zoned elsewhere and this scope owns that other zone, it's moved
//     (removed from the old zone, added to zoneName). If it's a
//     shared/inherited zone this scope doesn't own (e.g. the built-in
//     "internet"/"local" case above), this returns an error instead of
//     silently doing nothing: confirmed live, SCM enforces "one zone per
//     interface" globally against the shared zone's own definition
//     regardless of scope-level overrides -- creating a same-named
//     override here with ifaceName removed (mirroring the override
//     pattern that works fine for logical-routers) does NOT actually free
//     ifaceName up, so there is no safe way to complete this reassignment
//     without editing the shared object directly, which this tool won't
//     do (its blast radius reaches every other folder/device that still
//     inherits it).
//
// PAN-OS only allows an interface to belong to one zone, so explicit=true
// callers are responsible for the whole point of this field: actually
// moving an interface off SCM's default built-in zoning onto a
// user-chosen name -- which only works for an interface that isn't
// already zoned via a shared ancestor to begin with.
func (r *reconciler) ensureInterfaceZoned(scopeParam, scopeValue, folder, snippet, device, ifaceName, zoneName string, explicit bool) (string, error) {
	found, err := r.findZoneWithInterface(scopeParam, scopeValue, ifaceName)
	if err != nil {
		return "", err
	}
	if found != nil {
		if !explicit || found.Name == zoneName {
			return found.Name, nil
		}
		owned, _, err := r.client.IsScopedTo(scm.ZonesPath, found.ID, scopeParam, scopeValue)
		if err != nil {
			return "", err
		}
		if !owned {
			return "", fmt.Errorf("interface %q is already zoned %q (inherited, not owned at this scope) and can't be moved to %q -- this tool won't edit a shared zone directly, since that affects every other folder/device that still inherits it; give this interface a variable that isn't already zoned instead", ifaceName, found.Name, zoneName)
		}
		if r.dryRun {
			return zoneName, nil
		}
		if err := r.removeInterfaceFromZone(scopeParam, scopeValue, found.Name, ifaceName, new(bool)); err != nil {
			return "", err
		}
	}

	existing, err := r.findOwned(scm.ZonesPath, scopeParam, scopeValue, zoneName, "")
	if err != nil {
		return "", err
	}

	if existing == nil {
		if r.dryRun {
			return zoneName, nil
		}
		target := scm.Zone{Name: zoneName, Folder: folder, Snippet: snippet, Device: device, Network: scm.ZoneNetwork{Layer3: []string{ifaceName}}}
		if _, err := r.client.CreateZone(target); err != nil {
			return "", err
		}
		return zoneName, nil
	}

	if r.dryRun {
		return zoneName, nil
	}
	full, err := r.client.GetZone(existing.ID)
	if err != nil {
		return "", err
	}
	full.Network.Layer3 = mergeString(full.Network.Layer3, ifaceName)
	if _, err := r.client.UpdateZone(full.ID, *full); err != nil {
		return "", err
	}
	return zoneName, nil
}

// findZoneWithInterface searches every zone visible from this scope
// (deliberately unfiltered by ownership, unlike findOwned) for one whose
// network.layer3 already lists ifaceName as a member.
func (r *reconciler) findZoneWithInterface(scopeParam, scopeValue, ifaceName string) (*scm.Zone, error) {
	objs, err := r.client.ListVisible(scm.ZonesPath, scopeParam, scopeValue, "")
	if err != nil {
		return nil, err
	}
	for _, obj := range objs {
		full, err := r.client.GetZone(obj.ID)
		if err != nil {
			return nil, err
		}
		for _, iface := range full.Network.Layer3 {
			if iface == ifaceName {
				return full, nil
			}
		}
	}
	return nil, nil
}

func findVRF(vrfs []scm.VRF, name string) *scm.VRF {
	for i := range vrfs {
		if vrfs[i].Name == name {
			return &vrfs[i]
		}
	}
	return nil
}

// installRouter makes sure both interfaces are routed and, for a static
// WAN, that a default route exists. See ensureInterfaceRouted and
// ensureDefaultRoute for why this searches the whole visible hierarchy
// rather than only routers owned at this exact scope: confirmed live,
// $eth-local/$eth-internet are commonly already members of a shared
// "default" logical-router several folders up (e.g. at "ngfw-shared"),
// and PAN-OS enforces that an interface can only belong to one router.
func (r *reconciler) installRouter(scopeParam, scopeValue, folder, snippet, device string, it ResolvedItem, staticWAN, staticWAN02 bool) error {
	routerTarget, routerExplicit := resolveConfiguredName(it.Router, routerName)
	if err := r.ensureInterfaceRouted(scopeParam, scopeValue, folder, snippet, device, it.TrustInterface, routerTarget, routerExplicit); err != nil {
		return fmt.Errorf("trust interface: %w", err)
	}
	if err := r.ensureInterfaceRouted(scopeParam, scopeValue, folder, snippet, device, it.UntrustInterface, routerTarget, routerExplicit); err != nil {
		return fmt.Errorf("untrust interface: %w", err)
	}
	if staticWAN {
		if err := r.ensureDefaultRoute(scopeParam, scopeValue, folder, snippet, device, it.UntrustInterface, it.WANGateway, defaultRouteName, primaryRouteMetric); err != nil {
			return err
		}
	}

	if it.Untrust02Interface == "" {
		return nil
	}
	if err := r.ensureInterfaceRouted(scopeParam, scopeValue, folder, snippet, device, it.Untrust02Interface, routerTarget, routerExplicit); err != nil {
		return fmt.Errorf("secondary WAN interface: %w", err)
	}
	if staticWAN02 {
		if err := r.ensureDefaultRoute(scopeParam, scopeValue, folder, snippet, device, it.Untrust02Interface, it.WAN02Gateway, defaultRouteName02, secondaryRouteMetric); err != nil {
			return fmt.Errorf("secondary WAN interface: %w", err)
		}
	}
	return nil
}

// findRouterWithInterface searches every logical-router visible from
// this scope (deliberately unfiltered by ownership, unlike findOwned) for
// one whose VRF already lists ifaceName as a member. Returns nil if none
// does.
func (r *reconciler) findRouterWithInterface(scopeParam, scopeValue, ifaceName string) (*scm.LogicalRouter, error) {
	objs, err := r.client.ListVisible(scm.LogicalRoutersPath, scopeParam, scopeValue, "")
	if err != nil {
		return nil, err
	}
	for _, obj := range objs {
		full, err := r.client.GetLogicalRouter(obj.ID)
		if err != nil {
			return nil, err
		}
		if vrfContaining(full.VRF, ifaceName) != nil {
			return full, nil
		}
	}
	return nil, nil
}

func vrfContaining(vrfs []scm.VRF, ifaceName string) *scm.VRF {
	for i := range vrfs {
		for _, iface := range vrfs[i].Interface {
			if iface == ifaceName {
				return &vrfs[i]
			}
		}
	}
	return nil
}

// ensureInterfaceRouted makes sure ifaceName ends up a VRF member of the
// logical-router named routerTarget, scoped at (scopeParam, scopeValue),
// creating it (owned at this exact scope) if it doesn't exist or merging
// ifaceName into it if it does. explicit distinguishes two callers,
// exactly mirroring ensureInterfaceZoned's TrustZone/UntrustZone
// semantics (see its doc comment) for the same underlying PAN-OS
// constraint (an interface belongs to only one logical-router):
//
//   - explicit=false (routerTarget is reconcile.go's hardcoded fallback,
//     because the playbook left router unset): if ifaceName is already
//     routed anywhere (e.g. a shared ancestor router), nothing is
//     modified.
//   - explicit=true (the playbook set router explicitly): routerTarget is
//     authoritative. If ifaceName is routed elsewhere and this scope owns
//     that other router, it's moved. If it's a shared/inherited router
//     this scope doesn't own, this returns an error rather than silently
//     doing nothing or attempting an override this tool has confirmed (for
//     the zones case) doesn't actually work -- SCM validates "one
//     logical-router per interface" against the shared router's own
//     definition regardless of scope-level overrides.
func (r *reconciler) ensureInterfaceRouted(scopeParam, scopeValue, folder, snippet, device, ifaceName, routerTarget string, explicit bool) error {
	found, err := r.findRouterWithInterface(scopeParam, scopeValue, ifaceName)
	if err != nil {
		return err
	}
	if found != nil {
		if !explicit || found.Name == routerTarget {
			return nil
		}
		owned, _, err := r.client.IsScopedTo(scm.LogicalRoutersPath, found.ID, scopeParam, scopeValue)
		if err != nil {
			return err
		}
		if !owned {
			return fmt.Errorf("interface %q is already routed via %q (inherited, not owned at this scope) and can't be moved to %q -- this tool won't edit a shared logical-router directly, since that affects every other folder/device that still inherits it; give this interface a variable that isn't already routed instead", ifaceName, found.Name, routerTarget)
		}
		if r.dryRun {
			return nil
		}
		full, err := r.client.GetLogicalRouter(found.ID)
		if err != nil {
			return err
		}
		if vrf := findVRF(full.VRF, vrfName); vrf != nil {
			vrf.Interface = removeString(vrf.Interface, ifaceName)
			if _, err := r.client.UpdateLogicalRouter(full.ID, *full); err != nil {
				return err
			}
		}
	}

	existing, err := r.findOwned(scm.LogicalRoutersPath, scopeParam, scopeValue, routerTarget, "")
	if err != nil {
		return err
	}
	var target scm.LogicalRouter
	if existing == nil {
		target = scm.LogicalRouter{Name: routerTarget, Folder: folder, Snippet: snippet, Device: device}
	} else {
		full, err := r.client.GetLogicalRouter(existing.ID)
		if err != nil {
			return err
		}
		target = *full
	}

	vrf := findVRF(target.VRF, vrfName)
	if vrf == nil {
		target.VRF = append(target.VRF, scm.VRF{Name: vrfName})
		vrf = &target.VRF[len(target.VRF)-1]
	}
	vrf.Interface = mergeString(vrf.Interface, ifaceName)

	if r.dryRun {
		return nil
	}
	if existing == nil {
		_, err := r.client.CreateLogicalRouter(target)
		return err
	}
	_, err = r.client.UpdateLogicalRouter(target.ID, target)
	return err
}

// ensureDefaultRoute adds (or updates) a static default route named
// routeName, via gateway, for ifaceName, scoped as an override at
// (scopeParam, scopeValue) of whichever router ifaceName is actually
// routed through -- which is commonly a shared ancestor router (e.g. SCM's
// built-in "default" several folders up). That's fine: the nexthop is
// itself a per-device "$variable" in the common case, so an override at
// our own scope still resolves correctly per device. Called once for the
// primary WAN (ifaceName=UntrustInterface) and, if configured, again for
// the optional secondary WAN (ifaceName=Untrust02Interface, a distinct
// routeName so both can coexist).
//
// Confirmed live: this must be a genuine override at (scopeParam,
// scopeValue) -- same router Name, our own folder/snippet/device -- NOT
// a direct edit of whatever object findRouterWithInterface locates.
// Editing that object directly (an earlier, buggy version of this
// function did exactly that) would silently rewrite the actual shared
// ancestor router itself, applying our route to every device that
// inherits from it, not just the ones this item's scope targets. And
// because SCM overrides fully replace their corresponding vrf entry, the
// override must explicitly re-declare the inherited interface list
// alongside the new route -- omitting it silently drops interface
// membership for every device under this scope (confirmed live: this
// exact mistake produced a real PAN-OS commit failure, "Interface ...
// has no logical-router configured").
func (r *reconciler) ensureDefaultRoute(scopeParam, scopeValue, folder, snippet, device, ifaceName, gateway, routeName string, metric int) error {
	source, err := r.findRouterWithInterface(scopeParam, scopeValue, ifaceName)
	if err != nil {
		return err
	}
	if source == nil {
		return fmt.Errorf("internal error: interface %q should already be routed", ifaceName)
	}
	sourceVRF := vrfContaining(source.VRF, ifaceName)

	existing, err := r.findOwned(scm.LogicalRoutersPath, scopeParam, scopeValue, source.Name, "")
	if err != nil {
		return err
	}

	var target scm.LogicalRouter
	if existing == nil {
		target = scm.LogicalRouter{Name: source.Name, Folder: folder, Snippet: snippet, Device: device}
	} else {
		full, err := r.client.GetLogicalRouter(existing.ID)
		if err != nil {
			return err
		}
		target = *full
	}

	vrf := findVRF(target.VRF, sourceVRF.Name)
	if vrf == nil {
		target.VRF = append(target.VRF, scm.VRF{Name: sourceVRF.Name})
		vrf = &target.VRF[len(target.VRF)-1]
	}
	vrf.Interface = sourceVRF.Interface

	route := scm.LogicalRouterStaticRoute{
		Name:        routeName,
		Destination: "0.0.0.0/0",
		Interface:   ifaceName,
		Nexthop:     scm.LogicalRouterNexthop{IPAddress: gateway},
		Metric:      metric,
	}
	if vrf.RoutingTable == nil {
		vrf.RoutingTable = &scm.LogicalRouterRoutingTable{}
	}
	if vrf.RoutingTable.IP == nil {
		vrf.RoutingTable.IP = &scm.LogicalRouterRoutingTableIP{}
	}
	vrf.RoutingTable.IP.StaticRoute = mergeStaticRoute(vrf.RoutingTable.IP.StaticRoute, route)

	if r.dryRun {
		return nil
	}
	if existing == nil {
		_, err := r.client.CreateLogicalRouter(target)
		return err
	}
	_, err = r.client.UpdateLogicalRouter(target.ID, target)
	return err
}

func mergeStaticRoute(routes []scm.LogicalRouterStaticRoute, route scm.LogicalRouterStaticRoute) []scm.LogicalRouterStaticRoute {
	for i, existing := range routes {
		if existing.Name == route.Name {
			routes[i] = route
			return routes
		}
	}
	return append(routes, route)
}

func removeStaticRoute(routes []scm.LogicalRouterStaticRoute, name string) []scm.LogicalRouterStaticRoute {
	out := make([]scm.LogicalRouterStaticRoute, 0, len(routes))
	for _, r := range routes {
		if r.Name != name {
			out = append(out, r)
		}
	}
	return out
}

// resolveDHCPPool determines the DHCP server's ip_pool range: it.DHCPPool
// if set, otherwise auto-derived from lan_cidr when that's a literal
// CIDR. Errors if neither is available (lan_cidr is a $variable and no
// dhcp_pool was given).
func resolveDHCPPool(it ResolvedItem) (string, error) {
	if it.DHCPPool != "" {
		return it.DHCPPool, nil
	}
	_, ipnet, ok := parseStaticCIDR(it.LANCIDR)
	if !ok {
		return "", fmt.Errorf("dhcp_pool not set and lan_cidr %q is not a literal CIDR -- set dhcp_pool explicitly", it.LANCIDR)
	}
	return lastHalfPool(ipnet)
}

// resolveLANGateway determines the LAN DHCP server's gateway option: a
// bare IP (no mask -- confirmed live, PAN-OS's DHCP gateway field rejects
// a "/nn" suffix, so lan_cidr's own value can't be reused directly).
// it.LANGateway if set, otherwise auto-derived as lan_cidr's own host
// address when that's a literal CIDR. Errors if neither is available.
func resolveLANGateway(it ResolvedItem) (string, error) {
	if it.LANGateway != "" {
		return it.LANGateway, nil
	}
	ip, _, ok := parseStaticCIDR(it.LANCIDR)
	if !ok {
		return "", fmt.Errorf("lan_gw not set and lan_cidr %q is not a literal CIDR -- set lan_gw explicitly", it.LANCIDR)
	}
	return ip.String(), nil
}

func (r *reconciler) installDHCP(scopeParam, scopeValue, folder, snippet, device string, it ResolvedItem) error {
	pool, err := resolveDHCPPool(it)
	if err != nil {
		return err
	}
	gateway, err := resolveLANGateway(it)
	if err != nil {
		return err
	}

	target := scm.DHCPInterface{
		Name:    it.TrustInterface,
		Folder:  folder,
		Snippet: snippet,
		Device:  device,
		Server: &scm.DHCPServer{
			Mode:   "enabled",
			Option: scm.DHCPServerOption{Gateway: gateway, DNS: &scm.DHCPServerDNS{Primary: it.DNSServer}},
			IPPool: []string{pool},
		},
	}

	existing, err := r.findOwned(scm.DHCPInterfacesPath, scopeParam, scopeValue, it.TrustInterface, "")
	if err != nil {
		return err
	}
	if r.dryRun {
		return nil
	}
	if existing == nil {
		_, err := r.client.CreateDHCPInterface(target)
		return err
	}
	_, err = r.client.UpdateDHCPInterface(existing.ID, target)
	return err
}

// installNAT creates or updates a dynamic-ip-and-port SNAT rule named name,
// translating trustZone->untrustZone traffic to egressInterface's own
// address. Called once for the primary WAN (name=natRuleName,
// egressInterface=UntrustInterface) and, if configured, again for the
// optional secondary WAN (name=natRuleName02, egressInterface=
// Untrust02Interface, its own untrustZone02) -- a dynamic-ip-and-port
// translation is tied to one specific interface, so the two WANs can't
// share a single rule the way the security rule's To list can.
func (r *reconciler) installNAT(scopeParam, scopeValue, folder, snippet, device, name, trustZone, untrustZone, egressInterface string) error {
	target := scm.NATRule{
		Name:        name,
		Folder:      folder,
		Snippet:     snippet,
		Device:      device,
		From:        []string{trustZone},
		To:          []string{untrustZone},
		Source:      []string{"any"},
		Destination: []string{"any"},
		Service:     "any",
		SourceTranslation: &scm.NATRuleSourceTranslation{
			DynamicIPAndPort: &scm.NATRuleDynamicIPAndPort{
				InterfaceAddress: &scm.NATRuleInterfaceAddress{Interface: egressInterface},
			},
		},
	}

	existing, err := r.findOwned(scm.NATRulesPath, scopeParam, scopeValue, name, "pre")
	if err != nil {
		return err
	}
	if r.dryRun {
		return nil
	}
	if existing == nil {
		_, err := r.client.CreateNATRule(target)
		return err
	}
	_, err = r.client.UpdateNATRule(existing.ID, target)
	return err
}

// installSecurityRule creates a standard Security-type rule allowing all
// traffic from the trust zone to every zone in toZones (the primary
// untrust zone, plus the secondary WAN's untrust02 zone if configured --
// unlike NAT, one rule's To list can simply list both, since the
// allow/any/any/any policy itself doesn't depend on egress interface). See
// scm.SecurityRule's doc comment for why this is a standard rule
// (application/service/category all "any") rather than SCM's simplified
// "Internet Access Rule" (policy_type: "Internet") feature -- that type
// turned out to be inherently scoped to web/URL traffic, with no way to
// express unrestricted (any-application) access.
func (r *reconciler) installSecurityRule(scopeParam, scopeValue, folder, snippet, device string, trustZone string, toZones []string) error {
	target := scm.SecurityRule{
		Name:        securityRuleName,
		Folder:      folder,
		Snippet:     snippet,
		Device:      device,
		From:        []string{trustZone},
		To:          toZones,
		Source:      []string{"any"},
		SourceUser:  []string{"any"},
		Destination: []string{"any"},
		Service:     []string{"any"},
		Application: []string{"any"},
		Category:    []string{"any"},
		Action:      "allow",
	}

	existing, err := r.findOwned(scm.SecurityRulesPath, scopeParam, scopeValue, securityRuleName, "pre")
	if err != nil {
		return err
	}
	if r.dryRun {
		return nil
	}
	if existing == nil {
		_, err := r.client.CreateSecurityRule(target)
		return err
	}
	_, err = r.client.UpdateSecurityRule(existing.ID, target)
	return err
}

func (r *reconciler) uninstallItem(scopeParam, scopeValue, label string, it ResolvedItem) error {
	var changed bool

	if err := r.deleteIfExists(scm.SecurityRulesPath, scopeParam, scopeValue, securityRuleName, "pre", &changed); err != nil {
		return fmt.Errorf("security rule: %w", err)
	}
	if err := r.deleteIfExists(scm.NATRulesPath, scopeParam, scopeValue, natRuleName, "pre", &changed); err != nil {
		return fmt.Errorf("NAT rule: %w", err)
	}
	if it.Untrust02Interface != "" {
		if err := r.deleteIfExists(scm.NATRulesPath, scopeParam, scopeValue, natRuleName02, "pre", &changed); err != nil {
			return fmt.Errorf("secondary WAN NAT rule: %w", err)
		}
	}
	if err := r.deleteIfExists(scm.DHCPInterfacesPath, scopeParam, scopeValue, it.TrustInterface, "", &changed); err != nil {
		return fmt.Errorf("DHCP server: %w", err)
	}
	if err := r.removeInterfaceFromRouter(scopeParam, scopeValue, it, &changed); err != nil {
		return fmt.Errorf("logical router: %w", err)
	}
	untrustZoneTarget, _ := resolveConfiguredName(it.UntrustZone, untrustZoneName)
	if err := r.removeInterfaceFromZone(scopeParam, scopeValue, untrustZoneTarget, it.UntrustInterface, &changed); err != nil {
		return fmt.Errorf("untrust zone: %w", err)
	}
	trustZoneTarget, _ := resolveConfiguredName(it.TrustZone, trustZoneName)
	if err := r.removeInterfaceFromZone(scopeParam, scopeValue, trustZoneTarget, it.TrustInterface, &changed); err != nil {
		return fmt.Errorf("trust zone: %w", err)
	}
	if it.Untrust02Interface != "" {
		untrust02ZoneTarget, _ := resolveConfiguredName(it.Untrust02Zone, untrust02ZoneName)
		if err := r.removeInterfaceFromZone(scopeParam, scopeValue, untrust02ZoneTarget, it.Untrust02Interface, &changed); err != nil {
			return fmt.Errorf("secondary WAN zone: %w", err)
		}
		if err := r.deleteIfExists(scm.EthernetInterfacesPath, scopeParam, scopeValue, it.Untrust02Interface, "", &changed); err != nil {
			return fmt.Errorf("secondary WAN interface: %w", err)
		}
	}
	if err := r.deleteIfExists(scm.EthernetInterfacesPath, scopeParam, scopeValue, it.UntrustInterface, "", &changed); err != nil {
		return fmt.Errorf("untrust interface: %w", err)
	}
	if err := r.deleteIfExists(scm.EthernetInterfacesPath, scopeParam, scopeValue, it.TrustInterface, "", &changed); err != nil {
		return fmt.Errorf("trust interface: %w", err)
	}
	// Runs last: a var_list entry defining one of this item's own
	// interface fields (e.g. $eth-internet02) is typically the very same
	// object just deleted above, in which case this is a harmless no-op
	// (findOwned inside reconcileVarList finds nothing) -- deleting it
	// earlier, while the zone/router above still referenced it, would
	// have hit a 409 conflict instead.
	if listChanged, err := r.reconcileVarList(scopeParam, scopeValue, label, it.VarList); err != nil {
		return fmt.Errorf("var_list: %w", err)
	} else if listChanged {
		changed = true
	}

	if !changed {
		fmt.Printf("  [skip]   %s already has no internet-access config to remove\n", label)
		return nil
	}
	fmt.Printf("  [uninstall] %s internet access removed\n", label)
	r.markAffected(scopeParam, scopeValue)
	return nil
}

func (r *reconciler) deleteIfExists(path, scopeParam, scopeValue, name, position string, changed *bool) error {
	existing, err := r.findOwned(path, scopeParam, scopeValue, name, position)
	if err != nil {
		return err
	}
	if existing == nil {
		return nil
	}
	if r.dryRun {
		*changed = true
		return nil
	}
	if err := r.client.DeleteByID(path, existing.ID); err != nil && !scm.IsNotFound(err) {
		return err
	}
	*changed = true
	return nil
}

func (r *reconciler) removeInterfaceFromZone(scopeParam, scopeValue, zoneName, ifaceName string, changed *bool) error {
	existing, err := r.findOwned(scm.ZonesPath, scopeParam, scopeValue, zoneName, "")
	if err != nil {
		return err
	}
	if existing == nil {
		return nil
	}
	full, err := r.client.GetZone(existing.ID)
	if err != nil {
		return err
	}
	newList := removeString(full.Network.Layer3, ifaceName)
	if len(newList) == len(full.Network.Layer3) {
		return nil
	}
	if r.dryRun {
		*changed = true
		return nil
	}
	full.Network.Layer3 = newList
	if _, err := r.client.UpdateZone(full.ID, *full); err != nil {
		return err
	}
	*changed = true
	return nil
}

// removeInterfaceFromRouter undoes installRouter for both the primary and
// (if configured) secondary WAN. The static route is only ever removed
// from an override we own at this exact scope -- mirroring
// ensureDefaultRoute, this must never directly edit whatever router
// findRouterWithInterface locates, since that's commonly a shared ancestor
// router used by devices/folders well beyond this item's scope. If we
// never created an override here (e.g. this item never had a static WAN
// gateway), there's nothing of ours to remove. Interface membership
// itself is, likewise, only ever removed from a router owned at this
// exact scope, never from an inherited/shared one.
func (r *reconciler) removeInterfaceFromRouter(scopeParam, scopeValue string, it ResolvedItem, changed *bool) error {
	if err := r.removeDefaultRoute(scopeParam, scopeValue, it.UntrustInterface, defaultRouteName, changed); err != nil {
		return err
	}
	if it.Untrust02Interface != "" {
		if err := r.removeDefaultRoute(scopeParam, scopeValue, it.Untrust02Interface, defaultRouteName02, changed); err != nil {
			return fmt.Errorf("secondary WAN: %w", err)
		}
	}

	routerTarget, _ := resolveConfiguredName(it.Router, routerName)
	existing, err := r.findOwned(scm.LogicalRoutersPath, scopeParam, scopeValue, routerTarget, "")
	if err != nil {
		return err
	}
	if existing == nil {
		return nil
	}
	full, err := r.client.GetLogicalRouter(existing.ID)
	if err != nil {
		return err
	}
	vrf := findVRF(full.VRF, vrfName)
	if vrf == nil {
		return nil
	}
	before := len(vrf.Interface)
	vrf.Interface = removeString(vrf.Interface, it.TrustInterface)
	vrf.Interface = removeString(vrf.Interface, it.UntrustInterface)
	if it.Untrust02Interface != "" {
		vrf.Interface = removeString(vrf.Interface, it.Untrust02Interface)
	}
	if len(vrf.Interface) == before {
		return nil
	}
	if r.dryRun {
		*changed = true
		return nil
	}
	if _, err := r.client.UpdateLogicalRouter(full.ID, *full); err != nil {
		return err
	}
	*changed = true
	return nil
}

// removeDefaultRoute removes the named static route for ifaceName from
// whichever router owns it -- but only if we hold an override of that
// router at this exact scope (see removeInterfaceFromRouter's doc
// comment).
func (r *reconciler) removeDefaultRoute(scopeParam, scopeValue, ifaceName, routeName string, changed *bool) error {
	source, err := r.findRouterWithInterface(scopeParam, scopeValue, ifaceName)
	if err != nil {
		return err
	}
	if source == nil {
		return nil
	}
	ours, err := r.findOwned(scm.LogicalRoutersPath, scopeParam, scopeValue, source.Name, "")
	if err != nil {
		return err
	}
	if ours == nil {
		return nil
	}
	full, err := r.client.GetLogicalRouter(ours.ID)
	if err != nil {
		return err
	}
	vrf := vrfContaining(full.VRF, ifaceName)
	if vrf == nil || vrf.RoutingTable == nil || vrf.RoutingTable.IP == nil {
		return nil
	}
	before := len(vrf.RoutingTable.IP.StaticRoute)
	vrf.RoutingTable.IP.StaticRoute = removeStaticRoute(vrf.RoutingTable.IP.StaticRoute, routeName)
	if len(vrf.RoutingTable.IP.StaticRoute) == before {
		return nil
	}
	if r.dryRun {
		*changed = true
		return nil
	}
	if _, err := r.client.UpdateLogicalRouter(full.ID, *full); err != nil {
		return err
	}
	*changed = true
	return nil
}

// reconcileVariableOverride writes (or removes) one variable_overrides
// entry's var_list against SCM, scoped to whichever of Serial/Folder/
// Snippet the entry names (see VariableOverride.Scope).
func (r *reconciler) reconcileVariableOverride(vo VariableOverride) error {
	scopeParam, scopeValue := vo.Scope()
	changed, err := r.reconcileVarList(scopeParam, scopeValue, vo.Name, vo.VarList)
	if err != nil {
		return err
	}
	if changed {
		r.markAffected(scopeParam, scopeValue)
	}
	return nil
}

// interfaceNamePattern matches a literal PAN-OS interface identifier (e.g.
// "ethernet1/2", "ae1", "loopback.5") -- see looksLikeInterfaceName.
var interfaceNamePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9]*[0-9](/[0-9]+)*(\.[0-9]+)?$`)

// looksLikeInterfaceName reports whether value is shaped like a literal
// PAN-OS interface identifier rather than an IP/CIDR/range-shaped value.
// Every real IP-shaped value this tool ever writes starts with a digit, so
// requiring a leading letter cleanly separates the two without needing to
// know which var_list entry is "supposed" to be which kind.
func looksLikeInterfaceName(value string) bool {
	return interfaceNamePattern.MatchString(value)
}

// reconcileVarList writes (or in uninstall mode, removes) each entry of
// list at (scopeParam, scopeValue), dispatching per entry to either a
// regular SCM variable (reconcileVar) or an ethernet-interfaces object's
// default_value (reconcileInterfaceVar) -- see looksLikeInterfaceName.
// Shared by variable_overrides entries and item_list entries' own
// var_list. label is used only for log lines.
func (r *reconciler) reconcileVarList(scopeParam, scopeValue, label string, list []VarItem) (bool, error) {
	var changed bool
	for _, item := range list {
		var itemChanged bool
		var err error
		if looksLikeInterfaceName(item.Value) {
			itemChanged, err = r.reconcileInterfaceVar(scopeParam, scopeValue, label, item)
		} else {
			itemChanged, err = r.reconcileVar(scopeParam, scopeValue, label, item)
		}
		if err != nil {
			return changed, err
		}
		changed = changed || itemChanged
	}
	return changed, nil
}

// reconcileVar writes (or removes) item as a regular SCM variable at
// (scopeParam, scopeValue).
func (r *reconciler) reconcileVar(scopeParam, scopeValue, label string, item VarItem) (bool, error) {
	folder, snippet, device := scopeFields(scopeParam, scopeValue)

	existing, err := r.client.ListVariablesByScope(scopeParam, scopeValue)
	if err != nil {
		return false, fmt.Errorf("listing variables: %w", err)
	}
	var existingVar *scm.Variable
	for i := range existing {
		v := existing[i]
		if v.Name == item.Name && v.Folder == folder && v.Snippet == snippet && v.Device == device {
			existingVar = &v
			break
		}
	}

	if r.mode == ModeUninstall {
		if existingVar == nil {
			return false, nil
		}
		fmt.Printf("  [uninstall] %s: removing variable %s\n", label, item.Name)
		if r.dryRun {
			return true, nil
		}
		if err := r.client.DeleteVariable(existingVar.ID); err != nil && !scm.IsNotFound(err) {
			return false, fmt.Errorf("deleting variable %s: %w", item.Name, err)
		}
		return true, nil
	}

	target := scm.Variable{Name: item.Name, Type: inferVariableType(item.Value), Value: item.Value, Folder: folder, Snippet: snippet, Device: device}

	if existingVar == nil {
		fmt.Printf("  [install] %s: setting variable %s = %s\n", label, item.Name, item.Value)
		if r.dryRun {
			return true, nil
		}
		if _, err := r.client.CreateVariable(target); err != nil {
			return false, fmt.Errorf("creating variable %s: %w", item.Name, err)
		}
		return true, nil
	}

	if r.mode == ModeInstall {
		fmt.Printf("  [skip]   %s: variable %s already set\n", label, item.Name)
		return false, nil
	}
	if existingVar.Value == item.Value && existingVar.Type == target.Type {
		fmt.Printf("  [skip]   %s: variable %s already set\n", label, item.Name)
		return false, nil
	}
	fmt.Printf("  [install] %s: updating variable %s = %s\n", label, item.Name, item.Value)
	if r.dryRun {
		return true, nil
	}
	if _, err := r.client.UpdateVariable(existingVar.ID, target); err != nil {
		return false, fmt.Errorf("updating variable %s: %w", item.Name, err)
	}
	return true, nil
}

// reconcileInterfaceVar writes (or removes) item as a custom interface
// variable's default_value -- SCM's only mechanism for this (confirmed
// live: the generic /variables resource has no "interface" type, so a
// literal interface-shaped value is rejected by every real type in its
// enum). This may be the very same ethernet-interfaces object
// installEthernetInterface later manages for one of this item's own
// interface fields (e.g. untrust02_interface: "$eth-internet02" paired
// with a var_list entry defining it) -- installEthernetInterface's
// existing fetch-and-merge already carries this default_value forward
// when that happens, so no special-casing is needed here beyond
// preserving Layer3 on our own update path, for the same reason.
func (r *reconciler) reconcileInterfaceVar(scopeParam, scopeValue, label string, item VarItem) (bool, error) {
	folder, snippet, device := scopeFields(scopeParam, scopeValue)

	existing, err := r.findOwned(scm.EthernetInterfacesPath, scopeParam, scopeValue, item.Name, "")
	if err != nil {
		return false, err
	}

	if r.mode == ModeUninstall {
		if existing == nil {
			return false, nil
		}
		fmt.Printf("  [uninstall] %s: removing interface variable %s\n", label, item.Name)
		if r.dryRun {
			return true, nil
		}
		if err := r.client.DeleteByID(scm.EthernetInterfacesPath, existing.ID); err != nil && !scm.IsNotFound(err) {
			return false, fmt.Errorf("deleting interface variable %s: %w", item.Name, err)
		}
		return true, nil
	}

	if existing == nil {
		fmt.Printf("  [install] %s: defining interface variable %s = %s\n", label, item.Name, item.Value)
		if r.dryRun {
			return true, nil
		}
		target := scm.EthernetInterface{Name: item.Name, Folder: folder, Snippet: snippet, Device: device, DefaultValue: item.Value}
		if _, err := r.client.CreateEthernetInterface(target); err != nil {
			return false, fmt.Errorf("defining interface variable %s: %w", item.Name, err)
		}
		return true, nil
	}

	full, err := r.client.GetEthernetInterface(existing.ID)
	if err != nil {
		return false, err
	}
	if r.mode == ModeInstall || full.DefaultValue == item.Value {
		fmt.Printf("  [skip]   %s: interface variable %s already defined\n", label, item.Name)
		return false, nil
	}
	fmt.Printf("  [install] %s: updating interface variable %s = %s\n", label, item.Name, item.Value)
	if r.dryRun {
		return true, nil
	}
	target := scm.EthernetInterface{Name: item.Name, Folder: folder, Snippet: snippet, Device: device, DefaultValue: item.Value, Layer3: full.Layer3}
	if _, err := r.client.UpdateEthernetInterface(existing.ID, target); err != nil {
		return false, fmt.Errorf("updating interface variable %s: %w", item.Name, err)
	}
	return true, nil
}

// varListItemSatisfied reports whether item is already correctly defined
// at (scopeParam, scopeValue), read-only -- mirrors reconcileVarList's two
// mechanisms, used by itemFullyConfigured.
func (r *reconciler) varListItemSatisfied(scopeParam, scopeValue string, item VarItem) (bool, error) {
	if looksLikeInterfaceName(item.Value) {
		obj, err := r.findOwned(scm.EthernetInterfacesPath, scopeParam, scopeValue, item.Name, "")
		if err != nil || obj == nil {
			return false, err
		}
		full, err := r.client.GetEthernetInterface(obj.ID)
		if err != nil {
			return false, err
		}
		return full.DefaultValue == item.Value, nil
	}

	folder, snippet, device := scopeFields(scopeParam, scopeValue)
	vars, err := r.client.ListVariablesByScope(scopeParam, scopeValue)
	if err != nil {
		return false, err
	}
	for _, v := range vars {
		if v.Name == item.Name && v.Folder == folder && v.Snippet == snippet && v.Device == device {
			return v.Value == item.Value, nil
		}
	}
	return false, nil
}

// inferVariableType picks an SCM variable "type" for value. SCM's type
// enum (percent, count, ip-netmask, zone, ip-range, ip-wildcard, ...) has
// no dedicated plain "IP address" type; ip-netmask is confirmed live to
// accept both CIDRs and bare IPs, so it's the default for everything this
// tool writes, except a "x.x.x.x-y.y.y.y" pool-range-shaped value, which
// uses ip-range.
func inferVariableType(value string) string {
	if strings.Contains(value, "-") && !strings.Contains(value, "/") {
		return "ip-range"
	}
	return "ip-netmask"
}

// placeholderValueFor returns a syntactically-valid, never-actually-used
// value for a variable of the given type, suitable as a parent
// definition's own "value" (see ensureVariableDefined) -- every real
// device gets its own value via variable_overrides, so this is never the
// value SCM actually deploys anywhere.
func placeholderValueFor(varType string) string {
	if varType == "ip-range" {
		return "0.0.0.0-0.0.0.1"
	}
	return "0.0.0.0/32"
}

// ensureVariableDefined makes sure value, if it's a "$variable"
// reference, has a definition at this item's own scope -- not just
// per-device overrides from variable_overrides. Confirmed live two ways:
// a strict field (a logical-router static route's nexthop.ip_address)
// rejects a "$variable" reference outright as "not a valid reference"
// unless a definition is visible at-or-above the referencing object's
// own scope; and even a lenient field that accepts the reference blindly
// at save time (an interface's ip field) can still fail to resolve at
// actual push/deploy time without one. This check is scope-exact (does a
// definition exist AT this item's own scope specifically), not
// ancestor-aware -- acceptable here since real ancestor-level variables
// aren't part of this tool's design, but would need extending if that
// ever becomes a real case (a variable already defined higher up should
// count as already satisfying this, not need a redundant duplicate).
func (r *reconciler) ensureVariableDefined(scopeParam, scopeValue, folder, snippet, device, value string) error {
	if !strings.HasPrefix(value, "$") {
		return nil
	}

	existing, err := r.client.ListVariablesByScope(scopeParam, scopeValue)
	if err != nil {
		return err
	}
	for _, v := range existing {
		if v.Name == value {
			return nil
		}
	}

	// No real value to run inferVariableType's heuristic against yet
	// (unlike its other caller, which infers from an actual
	// variable_overrides value) -- guess from the name instead. Every
	// real device's value still comes from variable_overrides; this
	// placeholder's type only needs to be broadly compatible.
	varType := "ip-netmask"
	if strings.Contains(strings.ToLower(value), "pool") {
		varType = "ip-range"
	}

	_, err = r.client.CreateVariable(scm.Variable{
		Name:    value,
		Type:    varType,
		Value:   placeholderValueFor(varType),
		Folder:  folder,
		Snippet: snippet,
		Device:  device,
	})
	return err
}
