package internet

import (
	"fmt"
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
)

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

// reconcileItem is the entry point for one item_list entry.
func (r *reconciler) reconcileItem(scopeParam, scopeValue, label string, it ResolvedItem) error {
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
	staticWAN := it.WANCIDR != "" && it.WANGateway != ""

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
	for _, c := range ownedChecks {
		obj, err := r.findOwned(c.path, scopeParam, scopeValue, c.name, c.pos)
		if err != nil {
			return false, err
		}
		if obj == nil {
			return false, nil
		}
	}

	for _, iface := range []string{it.TrustInterface, it.UntrustInterface} {
		zone, err := r.findZoneWithInterface(scopeParam, scopeValue, iface)
		if err != nil {
			return false, err
		}
		if zone == nil {
			return false, nil
		}

		router, err := r.findRouterWithInterface(scopeParam, scopeValue, iface)
		if err != nil {
			return false, err
		}
		if router == nil {
			return false, nil
		}
	}

	if staticWAN {
		router, err := r.findRouterWithInterface(scopeParam, scopeValue, it.UntrustInterface)
		if err != nil {
			return false, err
		}
		vrf := vrfContaining(router.VRF, it.UntrustInterface)
		if vrf == nil || vrf.RoutingTable == nil || vrf.RoutingTable.IP == nil || !hasStaticRoute(vrf.RoutingTable.IP.StaticRoute, "gopangoblin-default-route") {
			return false, nil
		}
	}

	return true, nil
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
	staticWAN := it.WANCIDR != "" && it.WANGateway != ""

	for _, v := range []string{it.LANCIDR, it.WANCIDR, it.WANGateway, it.DNSServer, it.DHCPPool, it.LANGateway} {
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

	trustZone, err := r.ensureInterfaceZoned(scopeParam, scopeValue, folder, snippet, device, it.TrustInterface, trustZoneName)
	if err != nil {
		return fmt.Errorf("trust zone: %w", err)
	}
	untrustZone, err := r.ensureInterfaceZoned(scopeParam, scopeValue, folder, snippet, device, it.UntrustInterface, untrustZoneName)
	if err != nil {
		return fmt.Errorf("untrust zone: %w", err)
	}

	if err := r.installRouter(scopeParam, scopeValue, folder, snippet, device, it, staticWAN); err != nil {
		return fmt.Errorf("logical router: %w", err)
	}

	if err := r.installDHCP(scopeParam, scopeValue, folder, snippet, device, it); err != nil {
		return fmt.Errorf("DHCP server: %w", err)
	}

	if err := r.installNAT(scopeParam, scopeValue, folder, snippet, device, it, trustZone, untrustZone); err != nil {
		return fmt.Errorf("NAT rule: %w", err)
	}

	if err := r.installSecurityRule(scopeParam, scopeValue, folder, snippet, device, trustZone, untrustZone); err != nil {
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

// ensureInterfaceZoned makes sure ifaceName is a member of some zone
// visible from this scope, and returns that zone's actual name. Confirmed
// live: SCM's built-in $eth-local/$eth-internet are already members of
// shared zones literally named "local"/"internet" (not "trust"/
// "untrust") several folders up, and PAN-OS enforces that an interface
// can only belong to one zone -- so if it's already zoned anywhere, that
// zone's real name is returned unchanged and nothing is modified.
// Otherwise ifaceName is added to a zone named preferredName, owned at
// this exact scope (created if needed), and preferredName is returned.
// The returned name is what NAT/security rules should use for
// from/to -- never assume it's preferredName.
func (r *reconciler) ensureInterfaceZoned(scopeParam, scopeValue, folder, snippet, device, ifaceName, preferredName string) (string, error) {
	found, err := r.findZoneWithInterface(scopeParam, scopeValue, ifaceName)
	if err != nil {
		return "", err
	}
	if found != nil {
		return found.Name, nil
	}

	existing, err := r.findOwned(scm.ZonesPath, scopeParam, scopeValue, preferredName, "")
	if err != nil {
		return "", err
	}

	if existing == nil {
		if r.dryRun {
			return preferredName, nil
		}
		target := scm.Zone{Name: preferredName, Folder: folder, Snippet: snippet, Device: device, Network: scm.ZoneNetwork{Layer3: []string{ifaceName}}}
		if _, err := r.client.CreateZone(target); err != nil {
			return "", err
		}
		return preferredName, nil
	}

	if r.dryRun {
		return preferredName, nil
	}
	full, err := r.client.GetZone(existing.ID)
	if err != nil {
		return "", err
	}
	full.Network.Layer3 = mergeString(full.Network.Layer3, ifaceName)
	if _, err := r.client.UpdateZone(full.ID, *full); err != nil {
		return "", err
	}
	return preferredName, nil
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
func (r *reconciler) installRouter(scopeParam, scopeValue, folder, snippet, device string, it ResolvedItem, staticWAN bool) error {
	if err := r.ensureInterfaceRouted(scopeParam, scopeValue, folder, snippet, device, it.TrustInterface); err != nil {
		return fmt.Errorf("trust interface: %w", err)
	}
	if err := r.ensureInterfaceRouted(scopeParam, scopeValue, folder, snippet, device, it.UntrustInterface); err != nil {
		return fmt.Errorf("untrust interface: %w", err)
	}

	if !staticWAN {
		return nil
	}
	return r.ensureDefaultRoute(scopeParam, scopeValue, folder, snippet, device, it)
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

// ensureInterfaceRouted makes sure ifaceName is a VRF member of some
// logical-router visible from this scope. If it's already routed
// anywhere (e.g. a shared ancestor router), nothing is changed. Otherwise
// it's added to a "default" router owned at this exact scope, created if
// needed.
func (r *reconciler) ensureInterfaceRouted(scopeParam, scopeValue, folder, snippet, device, ifaceName string) error {
	found, err := r.findRouterWithInterface(scopeParam, scopeValue, ifaceName)
	if err != nil {
		return err
	}
	if found != nil {
		return nil
	}

	existing, err := r.findOwned(scm.LogicalRoutersPath, scopeParam, scopeValue, routerName, "")
	if err != nil {
		return err
	}
	var target scm.LogicalRouter
	if existing == nil {
		target = scm.LogicalRouter{Name: routerName, Folder: folder, Snippet: snippet, Device: device}
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

// ensureDefaultRoute adds (or updates) a static default route via
// it.WANGateway, scoped as an override at (scopeParam, scopeValue) of
// whichever router the untrust interface is actually routed through --
// which is commonly a shared ancestor router (e.g. SCM's built-in
// "default" several folders up). That's fine: the nexthop is itself a
// per-device "$variable" in the common case, so an override at our own
// scope still resolves correctly per device.
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
func (r *reconciler) ensureDefaultRoute(scopeParam, scopeValue, folder, snippet, device string, it ResolvedItem) error {
	source, err := r.findRouterWithInterface(scopeParam, scopeValue, it.UntrustInterface)
	if err != nil {
		return err
	}
	if source == nil {
		return fmt.Errorf("internal error: untrust interface %q should already be routed", it.UntrustInterface)
	}
	sourceVRF := vrfContaining(source.VRF, it.UntrustInterface)

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
		Name:        "gopangoblin-default-route",
		Destination: "0.0.0.0/0",
		Interface:   it.UntrustInterface,
		Nexthop:     scm.LogicalRouterNexthop{IPAddress: it.WANGateway},
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

func (r *reconciler) installNAT(scopeParam, scopeValue, folder, snippet, device string, it ResolvedItem, trustZone, untrustZone string) error {
	target := scm.NATRule{
		Name:        natRuleName,
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
				InterfaceAddress: &scm.NATRuleInterfaceAddress{Interface: it.UntrustInterface},
			},
		},
	}

	existing, err := r.findOwned(scm.NATRulesPath, scopeParam, scopeValue, natRuleName, "pre")
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
// traffic from the trust zone to the untrust zone. See scm.SecurityRule's
// doc comment for why this is a standard rule (application/service/
// category all "any") rather than SCM's simplified "Internet Access
// Rule" (policy_type: "Internet") feature -- that type turned out to be
// inherently scoped to web/URL traffic, with no way to express
// unrestricted (any-application) access.
func (r *reconciler) installSecurityRule(scopeParam, scopeValue, folder, snippet, device string, trustZone, untrustZone string) error {
	target := scm.SecurityRule{
		Name:        securityRuleName,
		Folder:      folder,
		Snippet:     snippet,
		Device:      device,
		From:        []string{trustZone},
		To:          []string{untrustZone},
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
	if err := r.deleteIfExists(scm.DHCPInterfacesPath, scopeParam, scopeValue, it.TrustInterface, "", &changed); err != nil {
		return fmt.Errorf("DHCP server: %w", err)
	}
	if err := r.removeInterfaceFromRouter(scopeParam, scopeValue, it, &changed); err != nil {
		return fmt.Errorf("logical router: %w", err)
	}
	if err := r.removeInterfaceFromZone(scopeParam, scopeValue, untrustZoneName, it.UntrustInterface, &changed); err != nil {
		return fmt.Errorf("untrust zone: %w", err)
	}
	if err := r.removeInterfaceFromZone(scopeParam, scopeValue, trustZoneName, it.TrustInterface, &changed); err != nil {
		return fmt.Errorf("trust zone: %w", err)
	}
	if err := r.deleteIfExists(scm.EthernetInterfacesPath, scopeParam, scopeValue, it.UntrustInterface, "", &changed); err != nil {
		return fmt.Errorf("untrust interface: %w", err)
	}
	if err := r.deleteIfExists(scm.EthernetInterfacesPath, scopeParam, scopeValue, it.TrustInterface, "", &changed); err != nil {
		return fmt.Errorf("trust interface: %w", err)
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

// removeInterfaceFromRouter undoes installRouter. The static route is
// only ever removed from an override we own at this exact scope --
// mirroring ensureDefaultRoute, this must never directly edit whatever
// router findRouterWithInterface locates, since that's commonly a shared
// ancestor router used by devices/folders well beyond this item's scope.
// If we never created an override here (e.g. this item never had a
// static WAN gateway), there's nothing of ours to remove. Interface
// membership itself is, likewise, only ever removed from a router owned
// at this exact scope, never from an inherited/shared one.
func (r *reconciler) removeInterfaceFromRouter(scopeParam, scopeValue string, it ResolvedItem, changed *bool) error {
	if source, err := r.findRouterWithInterface(scopeParam, scopeValue, it.UntrustInterface); err != nil {
		return err
	} else if source != nil {
		if ours, err := r.findOwned(scm.LogicalRoutersPath, scopeParam, scopeValue, source.Name, ""); err != nil {
			return err
		} else if ours != nil {
			full, err := r.client.GetLogicalRouter(ours.ID)
			if err != nil {
				return err
			}
			vrf := vrfContaining(full.VRF, it.UntrustInterface)
			if vrf != nil && vrf.RoutingTable != nil && vrf.RoutingTable.IP != nil {
				before := len(vrf.RoutingTable.IP.StaticRoute)
				vrf.RoutingTable.IP.StaticRoute = removeStaticRoute(vrf.RoutingTable.IP.StaticRoute, "gopangoblin-default-route")
				if len(vrf.RoutingTable.IP.StaticRoute) != before {
					if r.dryRun {
						*changed = true
					} else {
						if _, err := r.client.UpdateLogicalRouter(full.ID, *full); err != nil {
							return err
						}
						*changed = true
					}
				}
			}
		}
	}

	existing, err := r.findOwned(scm.LogicalRoutersPath, scopeParam, scopeValue, routerName, "")
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

// reconcileVariableOverride writes (or removes) one variable_overrides
// entry's var_list against SCM, device-scoped.
func (r *reconciler) reconcileVariableOverride(vo VariableOverride) error {
	existing, err := r.client.ListVariablesByDevice(vo.Serial)
	if err != nil {
		return fmt.Errorf("listing variables: %w", err)
	}
	byName := map[string]scm.Variable{}
	for _, v := range existing {
		if v.Device == vo.Serial {
			byName[v.Name] = v
		}
	}

	var changed bool
	for _, item := range vo.VarList {
		if r.mode == ModeUninstall {
			if v, ok := byName[item.Name]; ok {
				fmt.Printf("  [uninstall] %s: removing variable %s\n", vo.Name, item.Name)
				if !r.dryRun {
					if err := r.client.DeleteVariable(v.ID); err != nil && !scm.IsNotFound(err) {
						return fmt.Errorf("deleting variable %s: %w", item.Name, err)
					}
				}
				changed = true
			}
			continue
		}

		target := scm.Variable{Name: item.Name, Type: inferVariableType(item.Value), Value: item.Value, Device: vo.Serial}

		existingVar, ok := byName[item.Name]
		if !ok {
			fmt.Printf("  [install] %s: setting variable %s = %s\n", vo.Name, item.Name, item.Value)
			if !r.dryRun {
				if _, err := r.client.CreateVariable(target); err != nil {
					return fmt.Errorf("creating variable %s: %w", item.Name, err)
				}
			}
			changed = true
			continue
		}

		if r.mode == ModeInstall {
			fmt.Printf("  [skip]   %s: variable %s already set\n", vo.Name, item.Name)
			continue
		}
		if existingVar.Value == item.Value && existingVar.Type == target.Type {
			fmt.Printf("  [skip]   %s: variable %s already set\n", vo.Name, item.Name)
			continue
		}
		fmt.Printf("  [install] %s: updating variable %s = %s\n", vo.Name, item.Name, item.Value)
		if !r.dryRun {
			if _, err := r.client.UpdateVariable(existingVar.ID, target); err != nil {
				return fmt.Errorf("updating variable %s: %w", item.Name, err)
			}
		}
		changed = true
	}

	if changed {
		r.markTouched(vo.Serial)
	}
	return nil
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
