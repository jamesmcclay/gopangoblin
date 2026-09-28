package sdwan

import (
	"fmt"

	"github.com/jamesmcclay/gopangoblin/internal/scm"
)

// Fixed names for the objects this tool creates, mirroring the lab's own
// manually-built deployment (see conversation.md / instructions.md) so a
// fresh install-override reproduces it exactly, and a run against the lab
// itself is idempotent.
const (
	vrfName = "default"

	interfaceProfileWAN01Name = "wan1"
	interfaceProfileWAN02Name = "wan2"

	distProfileCorpName     = "Best Path"
	distProfileInternetName = "Top Down"
	// trafficDistributionValue is SCM's only enum value for this field --
	// confirmed live, both the lab's "Best Path" and "Top Down" profiles
	// use it; see scm.SDWANTrafficDistributionProfile's doc comment.
	trafficDistributionValue = "Best Available Path"

	sdwanRuleCorpName     = "corp-traffic"
	sdwanRuleInternetName = "internet-traffic"
	sdwanRuleCatchAllName = "catch-all"

	routerIDVarName     = "$ROUTER_ID"
	asnVarName          = "$ASN"
	wanGatewayVarName   = "$wan_gw"   // set by the "internet" tool's variable_overrides
	wan02GatewayVarName = "$wan02_gw" // set by the "internet" tool's variable_overrides

	backhaulNATRuleName      = "gopangoblin-sdwan-backhaul-nat"
	backhaulSecurityRuleName = "gopangoblin-sdwan-backhaul-access"
	branchToBranchRuleName   = "gopangoblin-sdwan-branch-to-branch"
	branchToHubRuleName      = "gopangoblin-sdwan-branch-to-hub"

	// hubDefaultRedistributionProfileName is a second BGP redistribution
	// profile (alongside the playbook's own default_redistribution_profile,
	// e.g. "All-Connected-Routes", which only redistributes connected
	// routes) that additionally redistributes static routes -- used only
	// on a device-scoped router override for the hub(s), so the hub's own
	// local default route gets advertised into the fabric under
	// InternetRoutingBackhaul. Confirmed live this is actually necessary,
	// not just the DIA/AnyPath flag conversation.md calls out: PAN-OS's
	// SD-WAN rules only ever select among links/paths for a zone the RIB
	// already routed the destination to -- they don't redirect a
	// destination into a completely different zone by themselves. Without
	// something actually installing a route for "the internet" via the
	// overlay, a branch's own local static default route (installed by
	// the "internet" tool) remains its only path, and internet-traffic's
	// broadened zone list simply never matches anything -- confirmed via
	// the hub's backhaul NAT/security rule hit counts staying at 0 even
	// with a successful ping, which was quietly using ordinary local
	// breakout instead.
	hubDefaultRedistributionProfileName = "gopangoblin-sdwan-hub-default"

	// defaultRouteName/defaultRouteName02 match the "internet" tool's own
	// reconcile.go naming exactly (a soft cross-package coupling this file
	// already has elsewhere -- see wanGatewayVarName/wan02GatewayVarName)
	// so reconcileSharedDefaultRoutes can find and remove/restore exactly
	// the routes that tool created.
	defaultRouteName     = "gopangoblin-default-route"
	defaultRouteName02   = "gopangoblin-default-route-wan02"
	primaryRouteMetric   = 10
	secondaryRouteMetric = 20

	// hubHalfDefaultRouteName1/2 are the hub-only "split-default" routes
	// used for BGP redistribution -- see installHubBackhaulRedistribution's
	// doc comment for why these exist instead of a plain 0.0.0.0/0.
	hubHalfDefaultRouteName1 = "gopangoblin-sdwan-hub-half-default-1"
	hubHalfDefaultRouteName2 = "gopangoblin-sdwan-hub-half-default-2"
)

type reconciler struct {
	client *scm.Client
	dryRun bool
	mode   Mode
	folder string

	// deviceFolders maps each hub_list serial to its own SCM device folder
	// (e.g. "Hub") -- used by installHubBackhaulRedistribution to scope a
	// router override per hub group. Confirmed live: a device-scoped
	// create ("device": "<serial>") is flatly rejected for these
	// on-prem/self-registered lab devices ("Device <serial> doesn't
	// exist"), the same limitation reset.md already documents for
	// management-interface/service-settings -- but a folder-scoped create
	// at the device's own assigned folder works fine, and is exactly as
	// selective as long as that folder contains only devices meant to get
	// the same treatment (which hub_list/branch_list's grouping already
	// implies).
	deviceFolders map[string]string

	// touched collects the serials of every hub/branch device this run
	// actually reconciled, so a subsequent push targets them.
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

// findOwned finds the object at path, owned directly by this playbook's
// folder (not inherited from an ancestor), whose name equals name. Returns
// nil if none exists. Mirrors the "internet" tool's own findOwned -- see
// its doc comment for why the extra IsScopedTo check matters (a scoped
// list's own folder/snippet/device field can echo back the queried scope
// for an inherited object, not its real owner).
func (r *reconciler) findOwned(path, name, position string) (*scm.ScopedObject, error) {
	return r.findOwnedInFolder(path, r.folder, name, position)
}

// findOwnedInFolder is findOwned generalized to an explicit folder rather
// than always r.folder -- used for a device-group-specific override (e.g.
// "Hub"/"Branches") rather than this playbook's own top-level folder.
func (r *reconciler) findOwnedInFolder(path, folder, name, position string) (*scm.ScopedObject, error) {
	objs, err := r.client.ListByScope(path, "folder", folder, position)
	if err != nil {
		return nil, err
	}
	for _, obj := range objs {
		if obj.Name != name {
			continue
		}
		owned, full, err := r.client.IsScopedTo(path, obj.ID, "folder", folder)
		if err != nil {
			return nil, err
		}
		if owned {
			return full, nil
		}
	}
	return nil, nil
}

// findCluster finds the auto-vpn-clusters object named name, visible from
// this playbook's folder scope. Unlike every other resource this tool
// touches, an auto-vpn-clusters object carries no folder/snippet/device
// field at all -- confirmed live, both its list-response entries and a
// bare-id GET return "folder": null -- so neither ListByScope's own
// defensive scope-field filter nor findOwned's usual IsScopedTo ownership
// re-check (both of which require that field to match) can ever confirm
// ownership for this type; ListByScope would incorrectly filter out every
// real cluster, always reporting none found. There's also no inheritance
// concern to defend against here the way there is for zones/routers (a
// cluster isn't a shared object other scopes inherit into view): using
// ListVisible and matching by name alone is enough.
func (r *reconciler) findCluster(name string) (*scm.ScopedObject, error) {
	objs, err := r.client.ListVisible(scm.AutoVPNClustersPath, "folder", r.folder, "")
	if err != nil {
		return nil, err
	}
	for i := range objs {
		if objs[i].Name == name {
			return &objs[i], nil
		}
	}
	return nil, nil
}

func deleteIgnoreNotFound(client *scm.Client, path, id string) error {
	if err := client.DeleteByID(path, id); err != nil && !scm.IsNotFound(err) {
		return err
	}
	return nil
}

// reconcileNamed handles the common create/update/skip/uninstall state
// machine shared by every fixed-name object this tool owns outright
// (interface profiles, distribution profiles, sdwan rules, the Auto VPN
// cluster itself): given whether it already exists, dispatch to the right
// one of create/update/del depending on mode.
func (r *reconciler) reconcileNamed(name, label string, existing *scm.ScopedObject, create func() error, update func(id string) error, del func(id string) error) error {
	if r.mode == ModeUninstall {
		if existing == nil {
			return nil
		}
		fmt.Printf("  [uninstall] removing %s\n", label)
		if r.dryRun {
			return nil
		}
		return del(existing.ID)
	}

	if existing == nil {
		fmt.Printf("  [install] creating %s\n", label)
		if r.dryRun {
			return nil
		}
		return create()
	}

	if r.mode == ModeInstall {
		fmt.Printf("  [skip]   %s already exists\n", label)
		return nil
	}

	fmt.Printf("  [install] updating %s\n", label)
	if r.dryRun {
		return nil
	}
	return update(existing.ID)
}

// reconcileToggled handles a fixed-name object whose desired existence is
// driven by a playbook toggle (internet_routing, branch_to_branch,
// branch_to_hub) rather than by this tool's own overall mode: if want is
// true, behaves like reconcileNamed (respecting install's skip-if-exists
// vs install-override's always-update); if want is false, the object is
// removed if present regardless of mode -- switching a toggle off is
// itself a kind of uninstall for that one piece, even during a plain
// "install"/"install-override" run, so switching the playbook's setting
// converges correctly without needing mode: uninstall.
func (r *reconciler) reconcileToggled(label string, want bool, existing *scm.ScopedObject, create func() error, update func(id string) error, del func(id string) error) error {
	if !want {
		if existing == nil {
			return nil
		}
		fmt.Printf("  [remove] %s no longer wanted, removing\n", label)
		if r.dryRun {
			return nil
		}
		return del(existing.ID)
	}

	if existing == nil {
		fmt.Printf("  [install] creating %s\n", label)
		if r.dryRun {
			return nil
		}
		return create()
	}

	if r.mode == ModeInstall {
		fmt.Printf("  [skip]   %s already exists\n", label)
		return nil
	}

	fmt.Printf("  [install] updating %s\n", label)
	if r.dryRun {
		return nil
	}
	return update(existing.ID)
}

// installRedistributionProfile creates or removes a bgp-redistribution-profiles
// object redistributing connected and/or static routes.
func (r *reconciler) installRedistributionProfile(name string, want, connected, static bool) error {
	existing, err := r.findOwned(scm.BGPRedistributionProfilesPath, name, "")
	if err != nil {
		return err
	}

	trueVal := true
	unicast := &scm.BGPRedistributionUnicast{}
	if connected {
		unicast.Connected = &scm.BGPRedistributionUnicastSource{Enable: &trueVal}
	}
	if static {
		unicast.Static = &scm.BGPRedistributionUnicastSource{Enable: &trueVal}
	}
	target := scm.BGPRedistributionProfile{Name: name, Folder: r.folder, IPv4: &scm.BGPRedistributionIPv4{Unicast: unicast}}

	return r.reconcileToggled(fmt.Sprintf("BGP redistribution profile %q", name), want, existing,
		func() error { _, err := r.client.CreateBGPRedistributionProfile(target); return err },
		func(id string) error { _, err := r.client.UpdateBGPRedistributionProfile(id, target); return err },
		func(id string) error { return deleteIgnoreNotFound(r.client, scm.BGPRedistributionProfilesPath, id) },
	)
}

// installHubBackhaulRedistribution ensures a folder-scoped "scm_router"
// override exists at hubFolder (the hub device's own SCM device folder,
// e.g. "Hub" -- see reconciler.deviceFolders' doc comment for why this is
// folder-scoped rather than device-scoped), identical to the shared
// folder-owned router except that its BGP redistribution profile is
// redistProfile instead of the shared default -- this is what actually
// gets the hub's own local default route advertised into the fabric for
// InternetRoutingBackhaul. Removes the override (reverting the hub to the
// shared definition) when want is false.
func (r *reconciler) installHubBackhaulRedistribution(routerName, hubFolder string, pl *Resolved, redistProfile string, want bool) error {
	existing, err := r.findOwnedInFolder(scm.LogicalRoutersPath, hubFolder, routerName, "")
	if err != nil {
		return err
	}
	label := fmt.Sprintf("hub backhaul BGP redistribution override at folder %q", hubFolder)

	if !want {
		if existing == nil {
			return nil
		}
		fmt.Printf("  [remove] %s no longer wanted, removing\n", label)
		if r.dryRun {
			return nil
		}
		return deleteIgnoreNotFound(r.client, scm.LogicalRoutersPath, existing.ID)
	}

	sharedObj, err := r.findOwned(scm.LogicalRoutersPath, routerName, "")
	if err != nil {
		return err
	}
	if sharedObj == nil {
		return fmt.Errorf("router %q not found at folder %q -- run the internet tool first", routerName, r.folder)
	}
	shared, err := r.client.GetLogicalRouter(sharedObj.ID)
	if err != nil {
		return err
	}
	sharedVRF := findVRF(shared.VRF, vrfName)
	if sharedVRF == nil {
		return fmt.Errorf("router %q has no %q VRF", routerName, vrfName)
	}

	// Static routes are reconstructed explicitly (the same shape
	// reconcileSharedDefaultRoutes' restore path uses) rather than copied
	// from sharedVRF.RoutingTable -- confirmed live this matters: on any
	// run after the first, the shared object's own routes are already
	// suppressed by reconcileSharedDefaultRoutes by the time this function
	// reads it, so a plain copy silently produces an override with an
	// empty routing table forever after -- the hub then has no real
	// config-time default route to redistribute at all, only PAN-OS's own
	// unrelated auto-generated DIA path (see reconcileInternetRouting's
	// doc comment).
	// Deliberately "0.0.0.0/1" + "128.0.0.0/1" (the classic split-default
	// trick), not "0.0.0.0/0": confirmed live, BGP's redistribute-static
	// silently excludes the literal default route even with
	// unicast.static.enable=true and no route_map filtering it out (the
	// hub's own real "gopangoblin-default-route" never appeared in "show
	// advanced-routing bgp peer advertised-routes" no matter what) --
	// standard BGP behavior on most platforms, PAN-OS's Advanced Routing
	// Engine included, and there's no "default-originate"-equivalent
	// exposed on the Auto-VPN-managed peer-group via this API to work
	// around it directly. Splitting the default in half sidesteps that
	// exclusion (neither half is literally 0.0.0.0/0) while together still
	// covering the same address space -- and as a bonus, being MORE
	// specific (/1) than the DIA shortcut's /0, longest-prefix-match makes
	// a branch prefer these once actually learned via BGP, regardless of
	// admin distance or metric.
	routes := []scm.LogicalRouterStaticRoute{
		{
			Name:        hubHalfDefaultRouteName1,
			Destination: "0.0.0.0/1",
			Interface:   pl.WANInterface,
			Nexthop:     scm.LogicalRouterNexthop{IPAddress: wanGatewayVarName},
			Metric:      primaryRouteMetric,
		},
		{
			Name:        hubHalfDefaultRouteName2,
			Destination: "128.0.0.0/1",
			Interface:   pl.WANInterface,
			Nexthop:     scm.LogicalRouterNexthop{IPAddress: wanGatewayVarName},
			Metric:      primaryRouteMetric,
		},
	}

	vrf := scm.VRF{
		Name:      vrfName,
		Interface: sharedVRF.Interface,
		RoutingTable: &scm.LogicalRouterRoutingTable{
			IP: &scm.LogicalRouterRoutingTableIP{StaticRoute: routes},
		},
	}
	if sharedVRF.BGP != nil {
		bgpCopy := *sharedVRF.BGP
		bgpCopy.RedistributionProfile = &scm.VRFBGPRedistributionProfile{
			IPv4: &scm.VRFBGPRedistributionProfileIPv4{Unicast: redistProfile},
		}
		vrf.BGP = &bgpCopy
	}
	target := scm.LogicalRouter{Name: routerName, Folder: hubFolder, VRF: []scm.VRF{vrf}}

	return r.reconcileToggled(label, want, existing,
		func() error { _, err := r.client.CreateLogicalRouter(target); return err },
		func(id string) error { _, err := r.client.UpdateLogicalRouter(id, target); return err },
		func(id string) error { return deleteIgnoreNotFound(r.client, scm.LogicalRoutersPath, id) },
	)
}

// reconcileSharedDefaultRoutes removes (suppress=true) or restores
// (suppress=false) the "internet" tool's own static default routes
// directly on the SHARED folder-owned router -- necessary for
// InternetRoutingBackhaul, so branches (which have no override of their
// own) stop having a competing local default route and actually use the
// hub's BGP-redistributed one instead (installHubBackhaulRedistribution
// gives the hub its own copy via a "Hub"-folder override first, so only
// branches actually lose it).
//
// Confirmed live this direct edit is necessary, not a more-specific
// per-branch override with an empty/omitted routing_table: unlike
// Zone.Network.Layer3 (which fully replaces on override), SCM/PAN-OS
// treats a routing_table absent from a more-specific scope as "no
// opinion, inherit the parent's" rather than "explicitly none" -- even an
// override whose routing_table.ip.static_route was explicitly sent as an
// empty array came back from a bare-id re-fetch as just {} and left the
// branch's inherited default route fully intact and still selected
// (confirmed via the branch's own advanced-routing route table). Editing
// the shared object directly sidesteps the question entirely.
//
// Restoring reconstructs whichever of the two default routes are missing
// with the exact shape the "internet" tool itself would create (same
// name/destination/nexthop-variable/metric), so this stays a no-op once
// "internet" is next run regardless.
func (r *reconciler) reconcileSharedDefaultRoutes(routerName string, pl *Resolved, suppress bool) error {
	existing, err := r.findOwned(scm.LogicalRoutersPath, routerName, "")
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("router %q not found at folder %q -- run the internet tool first", routerName, r.folder)
	}
	full, err := r.client.GetLogicalRouter(existing.ID)
	if err != nil {
		return err
	}
	vrf := findVRF(full.VRF, vrfName)
	if vrf == nil {
		return fmt.Errorf("router %q has no %q VRF", routerName, vrfName)
	}
	var routes []scm.LogicalRouterStaticRoute
	if vrf.RoutingTable != nil && vrf.RoutingTable.IP != nil {
		routes = vrf.RoutingTable.IP.StaticRoute
	}

	if suppress {
		newRoutes := removeStaticRoute(removeStaticRoute(routes, defaultRouteName), defaultRouteName02)
		if len(newRoutes) == len(routes) {
			fmt.Printf("  [skip]   shared router %q already has no default route to suppress\n", routerName)
			return nil
		}
		fmt.Printf("  [install] removing default route(s) from shared router %q for backhaul\n", routerName)
		if r.dryRun {
			return nil
		}
		if vrf.RoutingTable == nil {
			vrf.RoutingTable = &scm.LogicalRouterRoutingTable{}
		}
		if vrf.RoutingTable.IP == nil {
			vrf.RoutingTable.IP = &scm.LogicalRouterRoutingTableIP{}
		}
		vrf.RoutingTable.IP.StaticRoute = newRoutes
		_, err = r.client.UpdateLogicalRouter(full.ID, *full)
		return err
	}

	restored := routes
	changed := false
	if !hasStaticRoute(restored, defaultRouteName) {
		restored = mergeStaticRoute(restored, scm.LogicalRouterStaticRoute{
			Name:        defaultRouteName,
			Destination: "0.0.0.0/0",
			Interface:   pl.WANInterface,
			Nexthop:     scm.LogicalRouterNexthop{IPAddress: wanGatewayVarName},
			Metric:      primaryRouteMetric,
		})
		changed = true
	}
	if pl.WAN02Interface != "" && !hasStaticRoute(restored, defaultRouteName02) {
		restored = mergeStaticRoute(restored, scm.LogicalRouterStaticRoute{
			Name:        defaultRouteName02,
			Destination: "0.0.0.0/0",
			Interface:   pl.WAN02Interface,
			Nexthop:     scm.LogicalRouterNexthop{IPAddress: wan02GatewayVarName},
			Metric:      secondaryRouteMetric,
		})
		changed = true
	}
	if !changed {
		fmt.Printf("  [skip]   shared router %q already has its default route(s)\n", routerName)
		return nil
	}
	fmt.Printf("  [install] restoring default route(s) on shared router %q\n", routerName)
	if r.dryRun {
		return nil
	}
	if vrf.RoutingTable == nil {
		vrf.RoutingTable = &scm.LogicalRouterRoutingTable{}
	}
	if vrf.RoutingTable.IP == nil {
		vrf.RoutingTable.IP = &scm.LogicalRouterRoutingTableIP{}
	}
	vrf.RoutingTable.IP.StaticRoute = restored
	_, err = r.client.UpdateLogicalRouter(full.ID, *full)
	return err
}

func hasStaticRoute(routes []scm.LogicalRouterStaticRoute, name string) bool {
	for _, r := range routes {
		if r.Name == name {
			return true
		}
	}
	return false
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
	for _, rt := range routes {
		if rt.Name != name {
			out = append(out, rt)
		}
	}
	return out
}

// Reconcile applies (or removes) the whole SD-WAN deployment described by
// pl. Ordering matters: install proceeds dependencies-first (profiles/BGP
// before the Auto VPN cluster that references them by name); uninstall
// proceeds in reverse, so nothing still-referenced is deleted first.
func (r *reconciler) Reconcile(pl *Resolved) error {
	if r.mode == ModeUninstall {
		return r.uninstall(pl)
	}
	return r.install(pl)
}

func (r *reconciler) install(pl *Resolved) error {
	if err := r.ensureVariableDefinition(routerIDVarName, "router-id"); err != nil {
		return fmt.Errorf("router-id variable: %w", err)
	}
	if err := r.ensureVariableDefinition(asnVarName, "as-number"); err != nil {
		return fmt.Errorf("as-number variable: %w", err)
	}
	if err := r.reconcileSiteVars(pl); err != nil {
		return err
	}

	if err := r.installLinkTag(pl.WANLinkTag); err != nil {
		return fmt.Errorf("link tag %s: %w", pl.WANLinkTag, err)
	}
	if err := r.installLinkTag(pl.WAN02LinkTag); err != nil {
		return fmt.Errorf("link tag %s: %w", pl.WAN02LinkTag, err)
	}
	if err := r.installInterfaceProfile(interfaceProfileWAN01Name, pl.WANLinkTag, 1); err != nil {
		return fmt.Errorf("interface profile %s: %w", interfaceProfileWAN01Name, err)
	}
	if err := r.installInterfaceProfile(interfaceProfileWAN02Name, pl.WAN02LinkTag, 2); err != nil {
		return fmt.Errorf("interface profile %s: %w", interfaceProfileWAN02Name, err)
	}
	if err := r.installDistributionProfile(distProfileCorpName, pl.WANLinkTag, pl.WAN02LinkTag); err != nil {
		return fmt.Errorf("distribution profile %s: %w", distProfileCorpName, err)
	}
	if err := r.installDistributionProfile(distProfileInternetName, pl.WANLinkTag, pl.WAN02LinkTag); err != nil {
		return fmt.Errorf("distribution profile %s: %w", distProfileInternetName, err)
	}
	if err := r.installBGP(pl.Router, pl.RedistributionProfile); err != nil {
		return fmt.Errorf("BGP: %w", err)
	}
	if err := r.reconcileInternetRouting(pl); err != nil {
		return fmt.Errorf("internet_routing: %w", err)
	}
	if err := r.reconcileBranchPolicies(pl); err != nil {
		return err
	}
	for _, spec := range sdwanRuleSpecs(pl) {
		if err := r.installSDWANRule(spec, pl.PathQualityProfile); err != nil {
			return fmt.Errorf("sdwan rule %s: %w", spec.name, err)
		}
	}
	if err := r.installCluster(pl); err != nil {
		return fmt.Errorf("auto vpn cluster %s: %w", pl.ClusterName, err)
	}

	return nil
}

func (r *reconciler) uninstall(pl *Resolved) error {
	if err := r.installCluster(pl); err != nil {
		return fmt.Errorf("auto vpn cluster %s: %w", pl.ClusterName, err)
	}
	for _, spec := range sdwanRuleSpecs(pl) {
		if err := r.installSDWANRule(spec, pl.PathQualityProfile); err != nil {
			return fmt.Errorf("sdwan rule %s: %w", spec.name, err)
		}
	}
	if err := r.reconcileBranchPolicies(pl); err != nil {
		return err
	}
	if err := r.reconcileInternetRouting(pl); err != nil {
		return fmt.Errorf("internet_routing: %w", err)
	}
	if err := r.installBGP(pl.Router, pl.RedistributionProfile); err != nil {
		return fmt.Errorf("BGP: %w", err)
	}
	if err := r.installDistributionProfile(distProfileCorpName, pl.WANLinkTag, pl.WAN02LinkTag); err != nil {
		return fmt.Errorf("distribution profile %s: %w", distProfileCorpName, err)
	}
	if err := r.installDistributionProfile(distProfileInternetName, pl.WANLinkTag, pl.WAN02LinkTag); err != nil {
		return fmt.Errorf("distribution profile %s: %w", distProfileInternetName, err)
	}
	if err := r.installInterfaceProfile(interfaceProfileWAN01Name, pl.WANLinkTag, 1); err != nil {
		return fmt.Errorf("interface profile %s: %w", interfaceProfileWAN01Name, err)
	}
	if err := r.installInterfaceProfile(interfaceProfileWAN02Name, pl.WAN02LinkTag, 2); err != nil {
		return fmt.Errorf("interface profile %s: %w", interfaceProfileWAN02Name, err)
	}
	// Link tags are removed last: both the interface profiles and
	// distribution profiles just removed above reference them by name.
	if err := r.installLinkTag(pl.WANLinkTag); err != nil {
		return fmt.Errorf("link tag %s: %w", pl.WANLinkTag, err)
	}
	if err := r.installLinkTag(pl.WAN02LinkTag); err != nil {
		return fmt.Errorf("link tag %s: %w", pl.WAN02LinkTag, err)
	}
	// Self-heal the shared placeholder (see ensureVariableDefinition's doc
	// comment) before stripping per-device overrides below -- otherwise a
	// device left with no override at all falls back to a broken "None"
	// value and the final push's variable-resolution preflight fails.
	if err := r.ensureVariableDefinition(routerIDVarName, "router-id"); err != nil {
		return fmt.Errorf("router-id variable: %w", err)
	}
	if err := r.ensureVariableDefinition(asnVarName, "as-number"); err != nil {
		return fmt.Errorf("as-number variable: %w", err)
	}
	if err := r.reconcileSiteVars(pl); err != nil {
		return err
	}

	return nil
}

// installLinkTag creates or removes the named link-tags object -- see
// scm.LinkTagsPath's doc comment for why this tool manages it explicitly
// rather than relying on SCM's auto-vivify-on-reference behavior.
func (r *reconciler) installLinkTag(name string) error {
	existing, err := r.findOwned(scm.LinkTagsPath, name, "")
	if err != nil {
		return err
	}

	target := scm.LinkTag{Name: name, Folder: r.folder}

	return r.reconcileNamed(name, fmt.Sprintf("link tag %q", name), existing,
		func() error { _, err := r.client.CreateLinkTag(target); return err },
		func(id string) error { _, err := r.client.UpdateLinkTag(id, target); return err },
		func(id string) error { return deleteIgnoreNotFound(r.client, scm.LinkTagsPath, id) },
	)
}

func (r *reconciler) reconcileSiteVars(pl *Resolved) error {
	for _, s := range pl.Hubs {
		if err := r.reconcileSite(s); err != nil {
			return err
		}
	}
	for _, s := range pl.Branches {
		if err := r.reconcileSite(s); err != nil {
			return err
		}
	}
	return nil
}

func (r *reconciler) reconcileSite(s ResolvedSite) error {
	if err := r.reconcileDeviceVar(s.Serial, routerIDVarName, "router-id", s.RouterID, s.Name); err != nil {
		return fmt.Errorf("%s: %w", s.Name, err)
	}
	if err := r.reconcileDeviceVar(s.Serial, asnVarName, "as-number", s.ASN, s.Name); err != nil {
		return fmt.Errorf("%s: %w", s.Name, err)
	}
	r.markTouched(s.Serial)
	return nil
}

// ensureVariableDefinition makes sure a $ROUTER_ID/$ASN variable
// *definition* exists at this playbook's folder scope -- a placeholder
// parent value that every hub_list/branch_list entry's own device-scoped
// reconcileDeviceVar call then overrides with its real value. Never removed
// on uninstall (mirrors the "internet" tool's own ensureVariableDefined):
// harmless to leave in place, and another tool/manual config may already
// depend on it existing.
func (r *reconciler) ensureVariableDefinition(name, varType string) error {
	vars, err := r.client.ListVariablesByScope("folder", r.folder)
	if err != nil {
		return fmt.Errorf("listing variables: %w", err)
	}
	for _, v := range vars {
		if v.Name != name || v.Folder != r.folder {
			continue
		}
		if v.Value != "" && v.Value != "None" {
			return nil
		}
		// Confirmed live: this placeholder can end up with a literal
		// stored value of "None" instead of placeholderFor's requested
		// value -- SCM silently discards the value on some
		// router-id/as-number creates. Harmless while every
		// hub_list/branch_list device has its own resolving override
		// (the normal case), but a push preflight fails on it
		// ("unresolved variables") the moment a device genuinely has no
		// override left, e.g. mid-uninstall once reconcileSiteVars has
		// removed it. Self-heal regardless of mode so uninstall also
		// converges.
		fmt.Printf("  [install] repairing unresolved variable %s at folder %q\n", name, r.folder)
		if r.dryRun {
			return nil
		}
		_, err := r.client.UpdateVariable(v.ID, scm.Variable{Name: name, Type: varType, Value: placeholderFor(varType), Folder: r.folder})
		return err
	}
	if r.mode == ModeUninstall {
		return nil
	}
	fmt.Printf("  [install] defining variable %s at folder %q\n", name, r.folder)
	if r.dryRun {
		return nil
	}
	_, err = r.client.CreateVariable(scm.Variable{Name: name, Type: varType, Value: placeholderFor(varType), Folder: r.folder})
	return err
}

func placeholderFor(varType string) string {
	switch varType {
	case "as-number":
		return "1"
	case "router-id":
		return "0.0.0.1"
	default:
		return "0.0.0.0/32"
	}
}

// reconcileDeviceVar writes (or on uninstall, removes) name as a
// device-scoped SCM variable override for serial.
func (r *reconciler) reconcileDeviceVar(serial, name, varType, value, label string) error {
	vars, err := r.client.ListVariablesByScope("device", serial)
	if err != nil {
		return fmt.Errorf("listing variables: %w", err)
	}
	var existing *scm.Variable
	for i := range vars {
		if vars[i].Name == name && vars[i].Device == serial {
			existing = &vars[i]
			break
		}
	}

	if r.mode == ModeUninstall {
		if existing == nil {
			return nil
		}
		fmt.Printf("  [uninstall] %s: removing variable %s\n", label, name)
		if r.dryRun {
			return nil
		}
		if err := r.client.DeleteVariable(existing.ID); err != nil && !scm.IsNotFound(err) {
			return fmt.Errorf("deleting variable %s: %w", name, err)
		}
		return nil
	}

	target := scm.Variable{Name: name, Type: varType, Value: value, Device: serial}
	if existing == nil {
		fmt.Printf("  [install] %s: setting variable %s = %s\n", label, name, value)
		if r.dryRun {
			return nil
		}
		_, err := r.client.CreateVariable(target)
		return err
	}
	if r.mode == ModeInstall || existing.Value == value {
		fmt.Printf("  [skip]   %s: variable %s already set\n", label, name)
		return nil
	}
	fmt.Printf("  [install] %s: updating variable %s = %s\n", label, name, value)
	if r.dryRun {
		return nil
	}
	_, err = r.client.UpdateVariable(existing.ID, target)
	return err
}

// installInterfaceProfile creates or updates the SD-WAN interface profile
// named name, matching the lab's own wan1/wan2 shape exactly (path
// monitoring, probe frequency, etc.) -- see scm.SDWANInterfaceProfile.
func (r *reconciler) installInterfaceProfile(name, linkTag string, failoverMetric int) error {
	existing, err := r.findOwned(scm.SDWANInterfaceProfilesPath, name, "")
	if err != nil {
		return err
	}

	noErrorCorrection := false
	vpnDataTunnelSupport := true
	target := scm.SDWANInterfaceProfile{
		Name:                 name,
		Folder:               r.folder,
		LinkTag:              linkTag,
		LinkType:             "Ethernet",
		PathMonitoring:       "Aggressive",
		ProbeFrequency:       3,
		FailbackHoldTime:     20,
		ErrorCorrection:      &noErrorCorrection,
		VPNDataTunnelSupport: &vpnDataTunnelSupport,
		VPNFailoverMetric:    failoverMetric,
	}

	return r.reconcileNamed(name, fmt.Sprintf("SD-WAN interface profile %q", name), existing,
		func() error { _, err := r.client.CreateSDWANInterfaceProfile(target); return err },
		func(id string) error { _, err := r.client.UpdateSDWANInterfaceProfile(id, target); return err },
		func(id string) error { return deleteIgnoreNotFound(r.client, scm.SDWANInterfaceProfilesPath, id) },
	)
}

// installDistributionProfile creates or updates a traffic distribution
// profile spanning both WAN link tags -- see
// scm.SDWANTrafficDistributionProfile's doc comment for why both "Best
// Path" and "Top Down" use the identical TrafficDistribution value.
func (r *reconciler) installDistributionProfile(name, wanLinkTag, wan02LinkTag string) error {
	existing, err := r.findOwned(scm.SDWANTrafficDistributionProfilesPath, name, "")
	if err != nil {
		return err
	}

	target := scm.SDWANTrafficDistributionProfile{
		Name:                name,
		Folder:              r.folder,
		TrafficDistribution: trafficDistributionValue,
		LinkTags:            []scm.SDWANLinkTag{{Name: wanLinkTag}, {Name: wan02LinkTag}},
	}

	return r.reconcileNamed(name, fmt.Sprintf("traffic distribution profile %q", name), existing,
		func() error { _, err := r.client.CreateSDWANTrafficDistributionProfile(target); return err },
		func(id string) error {
			_, err := r.client.UpdateSDWANTrafficDistributionProfile(id, target)
			return err
		},
		func(id string) error {
			return deleteIgnoreNotFound(r.client, scm.SDWANTrafficDistributionProfilesPath, id)
		},
	)
}

func findVRF(vrfs []scm.VRF, name string) *scm.VRF {
	for i := range vrfs {
		if vrfs[i].Name == name {
			return &vrfs[i]
		}
	}
	return nil
}

// installBGP enables BGP on routerName's "default" VRF, peering as
// $ASN/$ROUTER_ID (each device's own override, set by reconcileSite) and
// redistributing connected routes via redistributionProfile. Confirmed
// live safe to round-trip against the lab's own scm_router, which already
// carries far more BGP sub-fields than this tool models (graceful_restart,
// med, admin_dists, ...) -- see scm.VRFBGP's doc comment.
func (r *reconciler) installBGP(routerName, redistributionProfile string) error {
	existing, err := r.findOwned(scm.LogicalRoutersPath, routerName, "")
	if err != nil {
		return err
	}

	if r.mode == ModeUninstall {
		if existing == nil {
			return nil
		}
		full, err := r.client.GetLogicalRouter(existing.ID)
		if err != nil {
			return err
		}
		vrf := findVRF(full.VRF, vrfName)
		if vrf == nil || vrf.BGP == nil {
			return nil
		}
		fmt.Printf("  [uninstall] removing BGP from router %q\n", routerName)
		if r.dryRun {
			return nil
		}
		vrf.BGP = nil
		_, err = r.client.UpdateLogicalRouter(full.ID, *full)
		return err
	}

	var target scm.LogicalRouter
	if existing == nil {
		target = scm.LogicalRouter{Name: routerName, Folder: r.folder}
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

	already := vrf.BGP != nil && vrf.BGP.Enable != nil && *vrf.BGP.Enable &&
		vrf.BGP.LocalAS == asnVarName && vrf.BGP.RouterID == routerIDVarName &&
		vrf.BGP.RedistributionProfile != nil && vrf.BGP.RedistributionProfile.IPv4 != nil &&
		vrf.BGP.RedistributionProfile.IPv4.Unicast == redistributionProfile
	if already {
		fmt.Printf("  [skip]   BGP already enabled on router %q\n", routerName)
		return nil
	}

	enable := true
	vrf.BGP = &scm.VRFBGP{
		Enable:   &enable,
		LocalAS:  asnVarName,
		RouterID: routerIDVarName,
		RedistributionProfile: &scm.VRFBGPRedistributionProfile{
			IPv4: &scm.VRFBGPRedistributionProfileIPv4{Unicast: redistributionProfile},
		},
	}

	fmt.Printf("  [install] enabling BGP on router %q (local_as=%s router_id=%s redistribution_profile=%s)\n",
		routerName, asnVarName, routerIDVarName, redistributionProfile)
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

// sdwanRuleSpec is one SD-WAN steering rule this tool manages.
type sdwanRuleSpec struct {
	name                string
	from                []string
	to                  []string
	destination         []string
	negateDestination   bool
	service             []string
	distributionProfile string
}

// sdwanRuleSpecs returns this tool's two unconditional steering rules: corp
// traffic (LAN <-> hub/branch zones, best path) and a final catch-all
// (any -> any, best path) -- see conversation.md's "SD-WAN Policy Rules &
// Steering" section. The internet-traffic rule is conditional on
// pl.InternetRouting instead -- see reconcileInternetRouting.
func sdwanRuleSpecs(pl *Resolved) []sdwanRuleSpec {
	return []sdwanRuleSpec{
		{
			name:                sdwanRuleCorpName,
			from:                []string{pl.LANZone},
			to:                  []string{pl.BranchZone, pl.HubZone},
			service:             []string{"application-default"},
			distributionProfile: distProfileCorpName,
		},
		{
			name:                sdwanRuleCatchAllName,
			from:                []string{"any"},
			to:                  []string{"any"},
			service:             []string{"any"},
			distributionProfile: distProfileCorpName,
		},
	}
}

func sdwanRuleTarget(folder string, spec sdwanRuleSpec, pathQualityProfile string) scm.SDWANRule {
	destination := spec.destination
	if destination == nil {
		destination = []string{"any"}
	}
	return scm.SDWANRule{
		Name:               spec.name,
		Folder:             folder,
		From:               spec.from,
		To:                 spec.to,
		Source:             []string{"any"},
		SourceUser:         []string{"any"},
		Destination:        destination,
		NegateDestination:  spec.negateDestination,
		Application:        []string{"any"},
		Service:            spec.service,
		PathQualityProfile: pathQualityProfile,
		Action:             scm.SDWANRuleAction{TrafficDistributionProfile: spec.distributionProfile},
	}
}

func (r *reconciler) installSDWANRule(spec sdwanRuleSpec, pathQualityProfile string) error {
	existing, err := r.findOwned(scm.SDWANRulesPath, spec.name, "")
	if err != nil {
		return err
	}

	target := sdwanRuleTarget(r.folder, spec, pathQualityProfile)

	return r.reconcileNamed(spec.name, fmt.Sprintf("SD-WAN rule %q", spec.name), existing,
		func() error { _, err := r.client.CreateSDWANRule(target); return err },
		func(id string) error { _, err := r.client.UpdateSDWANRule(id, target); return err },
		func(id string) error { return deleteIgnoreNotFound(r.client, scm.SDWANRulesPath, id) },
	)
}

// installLocalBreakoutRuleAt creates or removes spec (the local-breakout
// "internet-traffic" rule) at an explicit folder rather than r.folder --
// see reconcileInternetRouting's doc comment on wantSharedLocalRule for why
// this needs to exist exclusively at each hub's own device folder under
// InternetRoutingBackhaul, instead of the shared scope every device
// inherits from.
func (r *reconciler) installLocalBreakoutRuleAt(folder string, spec sdwanRuleSpec, pathQualityProfile string, want bool) error {
	existing, err := r.findOwnedInFolder(scm.SDWANRulesPath, folder, spec.name, "")
	if err != nil {
		return err
	}
	target := sdwanRuleTarget(folder, spec, pathQualityProfile)
	return r.reconcileToggled(fmt.Sprintf("SD-WAN rule %q at folder %q", spec.name, folder), want, existing,
		func() error { _, err := r.client.CreateSDWANRule(target); return err },
		func(id string) error { _, err := r.client.UpdateSDWANRule(id, target); return err },
		func(id string) error { return deleteIgnoreNotFound(r.client, scm.SDWANRulesPath, id) },
	)
}

// installCluster creates or updates the single Auto VPN cluster this
// playbook owns, wholesale replacing its Gateways/Branches lists with
// exactly what hub_list/branch_list describe -- this tool assumes it's the
// sole owner of the named cluster, not a shared object other config
// contributes members to (unlike zones/routers). Uninstall deletes the
// whole cluster object outright for the same reason, rather than trying to
// strip out just its own hub/branch entries.
func (r *reconciler) installCluster(pl *Resolved) error {
	existing, err := r.findCluster(pl.ClusterName)
	if err != nil {
		return err
	}

	noUpstreamNAT := false
	// allowDIAVPNFailover ("Allow DIA VPN" in the SCM UI): despite
	// conversation.md's step 1 framing it as what "permits DIA traffic to
	// traverse the overlay", its literal meaning per the field name (and
	// confirmed live) is the opposite -- it's what makes PAN-OS keep a
	// low-metric local-breakout path ready as a *failover* for when the
	// overlay is down (its metric is exactly vpn_failover_metric*5,
	// confirmed against wan1/wan2's own metrics of 1/2 producing observed
	// route metrics of 5/10). With it true, that failover path's admin
	// distance (10, "static") beats any BGP-learned route (20) and wins
	// every time regardless of the overlay being perfectly healthy, so
	// backhaul-bound traffic never actually leaves locally -- confirmed
	// live via the hub's own backhaul NAT/security rule hit counts sitting
	// at 0 while local breakout kept absorbing every flow. False here
	// means backhaul traffic has no local-breakout safety net if the
	// overlay ever goes down -- a real tradeoff, not a bug -- but it's
	// what actually makes InternetRoutingBackhaul work as specified.
	allowDIAVPNFailover := false
	enableSDWAN := true

	linkInterfaces := func() []scm.AutoVPNInterface {
		return []scm.AutoVPNInterface{
			{
				Name: pl.WANInterface,
				SDWANLinkSettings: &scm.AutoVPNSDWANLinkSettings{
					SDWANInterfaceProfile: interfaceProfileWAN01Name,
					UpstreamNAT:           scm.AutoVPNUpstreamNAT{Enable: &noUpstreamNAT},
					SDWANGateway:          wanGatewayVarName,
				},
			},
			{
				Name: pl.WAN02Interface,
				SDWANLinkSettings: &scm.AutoVPNSDWANLinkSettings{
					SDWANInterfaceProfile: interfaceProfileWAN02Name,
					UpstreamNAT:           scm.AutoVPNUpstreamNAT{Enable: &noUpstreamNAT},
					SDWANGateway:          wan02GatewayVarName,
				},
			},
		}
	}

	// The hub's own BGP redistribution profile is a genuinely separate
	// config surface from the scm_router's own protocol.bgp settings
	// (see installHubBackhaulRedistribution): this field, set directly on
	// the cluster's own gateway entry, is what Auto VPN's internally
	// managed BGP peer-groups actually honor for what gets redistributed
	// to spokes -- confirmed live, changing scm_router's own
	// redistribution_profile via a folder override never affected what a
	// branch received via BGP at all, regardless of what static routes
	// existed to redistribute.
	hubRedistributionProfile := pl.RedistributionProfile
	if pl.InternetRouting == InternetRoutingBackhaul {
		hubRedistributionProfile = hubDefaultRedistributionProfileName
	}
	var gateways []scm.AutoVPNGateway
	for _, h := range pl.Hubs {
		gateways = append(gateways, scm.AutoVPNGateway{
			Name:                     h.Serial,
			Site:                     h.Site,
			LogicalRouter:            pl.Router,
			Priority:                 h.Priority,
			BGPRedistributionProfile: hubRedistributionProfile,
			AllowDIAVPNFailover:      &allowDIAVPNFailover,
			Interfaces:               linkInterfaces(),
		})
	}
	var branches []scm.AutoVPNBranch
	for _, b := range pl.Branches {
		branches = append(branches, scm.AutoVPNBranch{
			Name:                     b.Serial,
			Site:                     b.Site,
			LogicalRouter:            pl.Router,
			BGPRedistributionProfile: pl.RedistributionProfile,
			Interfaces:               linkInterfaces(),
		})
	}

	target := scm.AutoVPNCluster{
		Name:        pl.ClusterName,
		Folder:      r.folder,
		Type:        "hub-spoke",
		EnableSDWAN: &enableSDWAN,
		Gateways:    gateways,
		Branches:    branches,
	}

	label := fmt.Sprintf("Auto VPN cluster %q (%d hub(s), %d branch(es))", pl.ClusterName, len(gateways), len(branches))
	return r.reconcileNamed(pl.ClusterName, label, existing,
		func() error { _, err := r.client.CreateAutoVPNCluster(target); return err },
		func(id string) error { _, err := r.client.UpdateAutoVPNCluster(id, target); return err },
		func(id string) error { return deleteIgnoreNotFound(r.client, scm.AutoVPNClustersPath, id) },
	)
}

// installSecurityRule creates or removes a simple allow-all security rule
// between the given zones -- shared by branch_to_branch, branch_to_hub, and
// the backhaul hub-egress rule. Folder-scoped like everything else this
// tool creates, so it's a harmless no-op wherever the named zones aren't
// actually populated with live interfaces (e.g. BranchZone only has
// members on the hub, never on a branch itself).
func (r *reconciler) installSecurityRule(name string, from, to []string, want bool) error {
	existing, err := r.findOwned(scm.SecurityRulesPath, name, "pre")
	if err != nil {
		return err
	}

	target := scm.SecurityRule{
		Name:        name,
		Folder:      r.folder,
		From:        from,
		To:          to,
		Source:      []string{"any"},
		SourceUser:  []string{"any"},
		Destination: []string{"any"},
		Service:     []string{"any"},
		Application: []string{"any"},
		Category:    []string{"any"},
		Action:      "allow",
	}

	return r.reconcileToggled(fmt.Sprintf("security rule %q", name), want, existing,
		func() error { _, err := r.client.CreateSecurityRule(target); return err },
		func(id string) error { _, err := r.client.UpdateSecurityRule(id, target); return err },
		func(id string) error { return deleteIgnoreNotFound(r.client, scm.SecurityRulesPath, id) },
	)
}

// installBackhaulNAT creates or removes the hub-egress NAT rule for
// backhauled branch traffic arriving via the overlay: dynamic-ip-and-port
// SNAT via egressInterface (the hub's own primary WAN), for traffic from
// any of the from zones destined to toZone. Named and owned separately
// from the "internet" tool's own NAT rule so a later "internet" run never
// clobbers it -- that tool blindly overwrites its own rule's From list
// every install-override run.
func (r *reconciler) installBackhaulNAT(name string, from []string, toZone, egressInterface string, want bool) error {
	existing, err := r.findOwned(scm.NATRulesPath, name, "pre")
	if err != nil {
		return err
	}

	target := scm.NATRule{
		Name:        name,
		Folder:      r.folder,
		From:        from,
		To:          []string{toZone},
		Source:      []string{"any"},
		Destination: []string{"any"},
		Service:     "any",
		SourceTranslation: &scm.NATRuleSourceTranslation{
			DynamicIPAndPort: &scm.NATRuleDynamicIPAndPort{
				InterfaceAddress: &scm.NATRuleInterfaceAddress{Interface: egressInterface},
			},
		},
	}

	return r.reconcileToggled(fmt.Sprintf("NAT rule %q", name), want, existing,
		func() error { _, err := r.client.CreateNATRule(target); return err },
		func(id string) error { _, err := r.client.UpdateNATRule(id, target); return err },
		func(id string) error { return deleteIgnoreNotFound(r.client, scm.NATRulesPath, id) },
	)
}

// uniqueDeviceFolders returns the distinct device folders (per
// reconciler.deviceFolders) that sites resolve to, in stable order.
func uniqueDeviceFolders(deviceFolders map[string]string, sites []ResolvedSite) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range sites {
		f := deviceFolders[s.Serial]
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// reconcileInternetRouting applies pl.InternetRouting. The real fix, found
// only by SSHing directly into the lab firewalls and comparing BGP/routing
// state against the config (the SCM API alone never surfaced this): two
// separate things had to both be true, neither of which is what
// conversation.md's step-by-step guidance describes.
//
// First, the branch's own local-breakout SD-WAN rule (internet-traffic)
// must be genuinely absent, not just out-matched by a more specific rule --
// confirmed live, PAN-OS auto-generates a low-metric "sdwan.90N" static
// route (visible via `show sdwan details vif` as "is-dia: 1", i.e. this is
// literally PAN-OS's own Direct Internet Access path) for any device with
// a WAN-tagged SD-WAN interface profile bound via the cluster, regardless
// of which policy rules exist or match -- so it lives at r.folder (shared)
// under breakout, or exclusively at each hub's own device folder under
// backhaul (the same folder-instead-of-device-scope technique
// installHubBackhaulRedistribution uses), never both.
//
// Second, and this is what actually makes backhaul work once the DIA
// shortcut above is out of the branch's way: the hub needs a REAL
// competing route into BGP. This does NOT go through the scm_router
// object's own protocol.bgp settings at all (confirmed live: overriding
// that via a device-folder override changed nothing a branch ever
// received) -- Auto VPN's own internally-managed BGP peer-groups instead
// honor the auto-vpn-clusters gateway entry's own bgp_redistribution_profile
// field directly (see installCluster). And confirmed live a second time:
// BGP's redistribute-static silently excludes the literal 0.0.0.0/0
// default route even with it explicitly enabled and no filtering route-map
// -- standard behavior on most BGP implementations, PAN-OS's Advanced
// Routing Engine included, with no "default-originate"-equivalent exposed
// on the Auto-VPN-managed peer-group to work around it directly. Splitting
// the hub's own default route into "0.0.0.0/1" + "128.0.0.0/1" (the
// classic split-default trick -- see installHubBackhaulRedistribution)
// sidesteps that exclusion entirely, and as a bonus is MORE specific than
// the branch's own DIA shortcut, so longest-prefix-match prefers it over
// admin distance/metric once actually learned.
//
// With both of those true, the existing "catch-all" SD-WAN rule (already
// present regardless of InternetRouting, using the ordinary "Best Path"
// profile) is sufficient on its own to carry backhauled traffic --
// confirmed live via the hub's own backhaul NAT/security rule hit counts
// finally incrementing exactly in step with real ping traffic. No
// dedicated "Overlay" link tag/interface-profile/distribution-profile/rule
// combo is needed (an earlier attempt built exactly that, following
// conversation.md's literal steps, and it never carried a single packet --
// WAN1/WAN2's own vpn_data_tunnel_support already exposes both the direct
// underlay and the tunnel group as candidates to any distribution profile
// referencing them, confirmed live via `show sdwan session distribution
// policy-name catch-all` listing both sdwan.901 and sdwan.902).
//
// InternetRoutingNone removes all of it, leaving whatever the "internet"
// tool alone provides (local breakout, since that's the only thing it
// ever configures).
//
// Safe to call regardless of r.mode: everything not wanted for the current
// InternetRouting value is actively removed via reconcileToggled, not just
// skipped, so switching values converges correctly on a plain "install"
// run -- and a genuine uninstall (mode == ModeUninstall) removes
// everything by construction, since uninstalling forces every want to
// false.
func (r *reconciler) reconcileInternetRouting(pl *Resolved) error {
	uninstalling := r.mode == ModeUninstall
	wantBackhaul := !uninstalling && pl.InternetRouting == InternetRoutingBackhaul
	wantSharedLocalRule := !uninstalling && pl.InternetRouting == InternetRoutingBreakout

	localSpec := sdwanRuleSpec{
		name:                sdwanRuleInternetName,
		from:                []string{pl.LANZone, pl.HubZone, pl.BranchZone},
		to:                  []string{pl.WANZone},
		service:             []string{"application-default"},
		distributionProfile: distProfileInternetName,
	}
	existingLocalRule, err := r.findOwned(scm.SDWANRulesPath, localSpec.name, "")
	if err != nil {
		return fmt.Errorf("internet-traffic sdwan rule: %w", err)
	}
	localTarget := sdwanRuleTarget(r.folder, localSpec, pl.PathQualityProfile)
	if err := r.reconcileToggled(fmt.Sprintf("SD-WAN rule %q", localSpec.name), wantSharedLocalRule, existingLocalRule,
		func() error { _, err := r.client.CreateSDWANRule(localTarget); return err },
		func(id string) error { _, err := r.client.UpdateSDWANRule(id, localTarget); return err },
		func(id string) error { return deleteIgnoreNotFound(r.client, scm.SDWANRulesPath, id) },
	); err != nil {
		return fmt.Errorf("internet-traffic sdwan rule: %w", err)
	}
	for _, folder := range uniqueDeviceFolders(r.deviceFolders, pl.Hubs) {
		if err := r.installLocalBreakoutRuleAt(folder, localSpec, pl.PathQualityProfile, wantBackhaul); err != nil {
			return fmt.Errorf("hub folder %q: %w", folder, err)
		}
	}

	// Ordering depends on direction: creating the hub router override
	// references the redistribution profile by name (must exist first),
	// but removing it must happen in the opposite order -- confirmed live,
	// deleting the profile while a router override still references it
	// fails with a 409 "NON_ZERO_REFS" from SCM.
	if wantBackhaul {
		if err := r.installRedistributionProfile(hubDefaultRedistributionProfileName, wantBackhaul, true, true); err != nil {
			return fmt.Errorf("hub redistribution profile: %w", err)
		}
	}
	for _, folder := range uniqueDeviceFolders(r.deviceFolders, pl.Hubs) {
		if err := r.installHubBackhaulRedistribution(pl.Router, folder, pl, hubDefaultRedistributionProfileName, wantBackhaul); err != nil {
			return fmt.Errorf("hub folder %q: %w", folder, err)
		}
	}
	if !wantBackhaul {
		if err := r.installRedistributionProfile(hubDefaultRedistributionProfileName, wantBackhaul, true, true); err != nil {
			return fmt.Errorf("hub redistribution profile: %w", err)
		}
	}
	if err := r.reconcileSharedDefaultRoutes(pl.Router, pl, wantBackhaul); err != nil {
		return fmt.Errorf("shared default route(s): %w", err)
	}

	if err := r.installSecurityRule(backhaulSecurityRuleName, []string{pl.BranchZone}, []string{pl.WANZone}, wantBackhaul); err != nil {
		return fmt.Errorf("backhaul security rule: %w", err)
	}
	if err := r.installBackhaulNAT(backhaulNATRuleName, []string{pl.BranchZone}, pl.WANZone, pl.WANInterface, wantBackhaul); err != nil {
		return fmt.Errorf("backhaul NAT rule: %w", err)
	}

	return nil
}

// reconcileBranchPolicies applies pl.BranchToBranch/pl.BranchToHub. Routing
// (BGP's All-Connected-Routes redistribution) and SD-WAN steering
// (corp-traffic) already reach both destinations -- what's actually
// missing is security *policy*: corp-traffic only ever selected a path, it
// never granted permission, so LAN<->overlay traffic has had no explicit
// allow at all up to now (confirmed live: a branch's own outbound leg from
// its LAN into the tunnel had no matching rule whatsoever, security or
// otherwise, hitting the default interzone deny before traffic even left
// the branch).
//
// branch_to_hub covers "a branch reaches the hub's LAN" (and the reverse)
// as one folder-shared rule, bidirectional across all three zones
// involved: LANZone->HubZone (leaving a branch into its tunnel),
// BranchZone->LANZone (arriving at the hub's own LAN), and the reverse of
// each. Each half is only ever live on the device type it actually
// applies to (branches never populate BranchZone, the hub never populates
// HubZone), so one rule safely covers every direction without needing
// device-specific targeting. This also happens to provide the "get
// on/off the fabric at a branch" legs branch_to_branch itself depends on
// (see its own doc comment) -- confirmed live: without HubZone->LANZone
// specifically (a destination branch receiving hairpinned traffic back
// off its own tunnel), branch-to-branch traffic got approved at the hub's
// hairpin but was then silently dropped on arrival at the second branch.
//
// branch_to_branch covers the one leg unique to branch-to-branch traffic:
// the hub's own hairpin (BranchZone->BranchZone, forwarding from one
// branch's tunnel out another's). On its own this only completes transit
// AT the hub -- branch_to_hub must also be enabled for a branch's own LAN
// traffic to actually reach that hairpin in the first place, and to be
// accepted back into the destination branch's own LAN (confirmed live:
// enabling only branch_to_branch reproduced the exact same failure, since
// both branch-side legs were still missing).
//
// Safe to call regardless of r.mode for the same reason as
// reconcileInternetRouting.
func (r *reconciler) reconcileBranchPolicies(pl *Resolved) error {
	uninstalling := r.mode == ModeUninstall
	wantBranchToBranch := !uninstalling && pl.BranchToBranch
	wantBranchToHub := !uninstalling && pl.BranchToHub

	if err := r.installSecurityRule(branchToBranchRuleName, []string{pl.BranchZone}, []string{pl.BranchZone}, wantBranchToBranch); err != nil {
		return fmt.Errorf("branch-to-branch security rule: %w", err)
	}
	branchHubZones := []string{pl.LANZone, pl.BranchZone, pl.HubZone}
	if err := r.installSecurityRule(branchToHubRuleName, branchHubZones, branchHubZones, wantBranchToHub); err != nil {
		return fmt.Errorf("branch-to-hub security rule: %w", err)
	}
	return nil
}
