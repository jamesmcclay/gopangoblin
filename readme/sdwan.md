# sdwan

`sdwan` layers a PAN-OS SD-WAN deployment on top of the base internet-access
config the `internet` tool already builds (interfaces, zones, logical
router): SD-WAN link tags and interface profiles, traffic distribution
profiles, BGP (peering as `$ASN`/`$ROUTER_ID`, redistributing connected
routes), SD-WAN steering rules, an Auto VPN hub-and-spoke cluster, and
security policy for three optional behaviors — internet routing
(local breakout vs. backhaul through the hub), branch-to-branch, and
branch-to-hub traffic (see "Optional behaviors" below). **Run `internet`
first** (or manually build your own stuff) — `sdwan` reuses its 
interfaces/zones/router by name rather than creating its own, and doesn't 
create them if missing.

Uses the same SCM credentials as every other tool here — see the main
[README's Credentials section](../README.md#credentials).

## Playbook format (`sdwan.yml`)

Unlike `internet.yml` (an `item_list` of independently-scoped
folders/snippets/firewalls), a PAN-OS SD-WAN deployment is one
interconnected whole — one Auto VPN cluster, one shared set of SD-WAN
profiles/rules, all owned at a single SCM folder scope — so this playbook
has just one of each, plus a `hub_list`/`branch_list` naming which devices
participate.

```yaml
name: James SD-WAN
push: true
mode: install                 # install | install-override | uninstall
folder: Lab Firewalls
internet_routing: backhaul    # breakout | backhaul | none
branch_to_branch: true
branch_to_hub: true
vars:
  default_router: scm_router
  default_wan_interface: "$eth-wan01"
  default_wan02_interface: "$eth-wan02"
  default_wan_link_tag: "WAN1"
  default_wan02_link_tag: "WAN2"
  default_lan_zone: zone-internal
  default_wan_zone: zone-internet
  default_hub_zone: zone-to-hub
  default_branch_zone: zone-to-branch
  default_redistribution_profile: All-Connected-Routes
  default_path_quality_profile: general-business
cluster_name: james-cluster
hub_list:
  - name: Hub A
    serial: 12345
    site: hub01
    priority: "1"
    router_id: 1.1.1.1
    asn: "65001"
branch_list:
  - name: Branch A
    serial: 67890
    site: branch01
    router_id: 2.2.2.2
    asn: "65002"
  - name: Branch B
    serial: 54321
    site: branch02
    router_id: 3.3.3.3
    asn: "65003"
```

- **mode** — `install` creates only what's missing (skips anything already
  present); `install-override` re-asserts every object regardless of
  current state, including wholesale-replacing the Auto VPN cluster's
  hub/branch membership with exactly what `hub_list`/`branch_list`
  describe; `uninstall` removes everything this tool created. Note:
  `internet_routing`/`branch_to_branch`/`branch_to_hub` are reconciled
  against their *current* setting regardless of `mode` — switching one off
  (or switching `internet_routing` to a different value) removes whatever
  it previously created even on a plain `install` run, not just on
  `uninstall`.
- **folder** — the single SCM folder scope everything is owned at. Must
  already exist (this tool doesn't create folders) and should be the same
  folder `internet.yml`'s `item_list` targets.
- **internet_routing** — `breakout` (default if unset), `backhaul`, or
  `none`. See "Optional behaviors" below.
- **branch_to_branch** / **branch_to_hub** — `true`/`false`. See "Optional
  behaviors" below.
- **vars** — all required, no per-entry override mechanism (these are
  single global settings, not a per-item list like `internet.yml`'s):
  - `default_router` / `default_wan_interface` / `default_wan02_interface`
    — must match the logical router and WAN interface names `internet`
    already created.
  - `default_wan_link_tag` / `default_wan02_link_tag` — the SD-WAN link
    tag names (see "SD-WAN link tags" below).
  - `default_lan_zone` / `default_wan_zone` — must match the zones
    `internet` already created.
  - `default_hub_zone` / `default_branch_zone` — the Auto VPN zones (e.g.
    `zone-to-hub`/`zone-to-branch`) SCM auto-provisions the first time an
    Auto VPN cluster is set up in the tenant; this tool only references
    them by name, never creates them.
  - `default_redistribution_profile` — an existing
    `bgp-redistribution-profiles` object by name (e.g. SCM's own
    `All-Connected-Routes`, also auto-provisioned alongside the Auto VPN
    zones above). Never created here — see `scm.BGPRedistributionProfilesPath`'s
    doc comment for why (this tool's service account gets `Access denied`
    creating/listing the raw `redistribution-profiles` endpoint, though
    the read-only `bgp-redistribution-profiles` one works fine).
  - `default_path_quality_profile` — an existing SD-WAN path quality
    profile by name (e.g. SCM's predefined `general-business`).
- **cluster_name** — the Auto VPN cluster's name. This tool assumes it's
  the sole owner of the named cluster (not a shared object other config
  contributes members to), so `install-override` fully replaces its
  membership and `uninstall` deletes the whole object outright.
- **hub_list / branch_list** — the devices participating in the Auto VPN
  cluster. `serial`, `site`, `router_id`, and `asn` are all required per
  entry — `router_id`/`asn` are written as that device's own
  `$ROUTER_ID`/`$ASN` SCM variable overrides (every device needs a
  distinct pair for BGP peering to actually work). `priority` is only
  meaningful on a `hub_list` entry when there's more than one hub (lower
  wins); it's ignored for branches and defaults to `"1"` if unset.

## SD-WAN link tags

SD-WAN link tags (`WAN1`/`WAN2` by default) are a genuinely separate SCM
resource (`/config/network/v1/link-tags`) from an SD-WAN interface
profile's own `link_tag` field — confirmed live the hard way: creating an
interface profile with `link_tag: "WAN1"` silently auto-vivifies a
`link-tags` object named `WAN1` if none exists yet, with no sign of it in
the interface profile's own API response. This tool creates the link tag
explicitly as its own reconciled object (rather than relying on that side
effect) so it's visible, idempotent, and something `reset` can actually
find and remove — see [`reset`'s docs](reset.md) for the cleanup side of
this.

## Optional behaviors

Routing (BGP's `default_redistribution_profile`, e.g.
`All-Connected-Routes`) and SD-WAN steering (`corp-traffic`) already get
traffic to the right place for branch-to-branch and branch-to-hub — what's
actually missing without these toggles is security *policy*:
`corp-traffic` only ever selects a *path*, it never grants permission, so
without them LAN traffic destined for the overlay hits the default
interzone deny with no matching rule at all.

- **branch_to_hub: true** creates one folder-shared, bidirectional security
  rule spanning `default_lan_zone`/`default_branch_zone`/`default_hub_zone`
  (`gopangoblin-sdwan-branch-to-hub`), covering a branch's LAN traffic
  leaving into its tunnel, arriving at the hub's own LAN, and the reverse
  of each. Each half is only ever live on the device type it actually
  applies to (a branch never populates `default_hub_zone`; the hub never
  populates `default_branch_zone`), so one rule safely covers every
  direction.
- **branch_to_branch: true** creates the one leg unique to
  branch-to-branch traffic: the hub's own hairpin
  (`default_branch_zone` → `default_branch_zone`,
  `gopangoblin-sdwan-branch-to-branch`), forwarding from one branch's
  tunnel out another's. On its own this only completes transit *at* the
  hub — `branch_to_hub` must also be `true` for a branch's own LAN traffic
  to reach that hairpin in the first place, and to be accepted back into
  the destination branch's own LAN.

### internet_routing

- **breakout** (default) creates `internet-traffic` (LAN/hub/branch zones →
  WAN zone, via the `Top Down` distribution profile) at the shared
  `folder` scope, so every hub and branch independently sends internet
  traffic out its own local WAN link.
- **none** creates no `internet-traffic` rule at all, leaving whatever the
  `internet` tool alone provides (which is local breakout too, since
  that's the only thing it ever configures — `sdwan` just doesn't add its
  own SD-WAN-aware copy of that rule on top).
- **backhaul** routes branch internet traffic through the hub's own WAN
  link instead, NATing and allowing it there
  (`gopangoblin-sdwan-backhaul-nat` / `gopangoblin-sdwan-backhaul-access`,
  both `default_branch_zone` → `default_wan_zone` at the hub).

  Getting `backhaul` actually working took directly SSHing into the lab
  firewalls and comparing live BGP/routing state against the pushed
  config — the SCM API alone never surfaced why it wasn't working. Two
  separate things, neither obvious from PAN-OS's own docs, both have to be
  true:

  1. **The branch's own local-breakout rule must be genuinely absent, not
     just out-matched.** PAN-OS auto-generates a low-metric "DIA" (Direct
     Internet Access) static default route the moment a WAN-tagged SD-WAN
     interface profile is bound via the Auto VPN cluster — visible as
     `sdwan.90N` with `is-dia: 1` in `show sdwan details vif` — regardless
     of which SD-WAN policy rules exist or match. So under `backhaul`,
     `internet-traffic` isn't just removed from the shared folder; it's
     recreated exclusively at each hub's own SCM **device folder** (e.g.
     `Hub`) instead. This is the same folder-instead-of-device-scope
     technique used throughout this tool: a device-scoped config *create*
     is flatly rejected by SCM for these on-prem/self-registered devices
     (`"Device <serial> doesn't exist"`), but a folder-scoped one works
     fine and is exactly as selective, since `hub_list`/`branch_list`'s
     grouping already implies which devices share a folder.
  2. **The hub needs a real competing route in BGP.** This does *not* go
     through the shared logical router's own `protocol.bgp` settings at
     all — confirmed live, overriding that via a folder override changed
     nothing a branch ever received. Auto VPN's own internally-managed BGP
     peer-groups instead honor the **Auto VPN cluster's own gateway
     entry** (`bgp_redistribution_profile`, set directly on the
     `auto-vpn-clusters` object's `gateways[]` entry — a genuinely separate
     config surface from the shared router's BGP config). And BGP's
     `redistribute-static` silently excludes the literal `0.0.0.0/0`
     default route even with it explicitly enabled and no filtering
     route-map — standard behavior on most BGP implementations, PAN-OS's
     Advanced Routing Engine included, with no `default-originate`
     equivalent exposed on the Auto-VPN-managed peer-group. Splitting the
     hub's own default route into `0.0.0.0/1` + `128.0.0.0/1` (the classic
     split-default trick) sidesteps that exclusion entirely, and as a
     bonus is *more specific* than the branch's own DIA shortcut, so
     longest-prefix-match prefers it once actually learned via BGP,
     regardless of admin distance or metric.

  With both of those true, the pre-existing `catch-all` SD-WAN rule
  (present regardless of `internet_routing`, using the ordinary `Best
  Path` profile) is sufficient on its own to carry backhauled traffic —
  `wan1`/`wan2`'s own `vpn_data_tunnel_support` already exposes both the
  direct underlay and the VPN tunnel group as path candidates to any
  distribution profile referencing them. No dedicated "overlay-only" link
  tag/interface-profile/distribution-profile/rule combination is needed
  (an earlier attempt built exactly that and it never carried a single
  packet in testing — see git history on this file/`reconcile.go` if
  curious).

  Concretely, `backhaul` also creates: a `gopangoblin-sdwan-hub-default`
  `bgp-redistribution-profiles` object (redistributing both connected and
  static routes, unlike `default_redistribution_profile`'s
  connected-only); a folder-scoped `scm_router` override at each hub's own
  device folder, identical to the shared router except its BGP
  redistribution profile is that new profile and its static routes are
  the two split-default routes above; and (on the shared router only,
  restored automatically if `internet_routing` is later switched away
  from `backhaul`) removal of the `internet` tool's own
  `gopangoblin-default-route`/`-wan02` routes, so branches actually prefer
  the hub's BGP-learned routes instead of their own local default.

  `allow_dia_vpn_failover` on the hub's Auto VPN gateway entry is always
  set to `false` regardless of `internet_routing` — despite its framing in
  some PAN-OS docs as "permits DIA traffic to traverse the overlay",
  confirmed live it's actually the opposite: it's what keeps a low-metric
  local-breakout path ready as a failover for when the overlay is down.
  With it `true`, that failover path's admin distance beat any BGP-learned
  route every time regardless of the overlay being perfectly healthy, so
  backhaul-bound traffic never actually left the branch. `false` means
  backhaul traffic has no local-breakout safety net if the overlay ever
  goes down — a real tradeoff, not a bug, but what actually makes
  `backhaul` work as specified.

## What gets created

In dependency order (reversed on `uninstall`): the two link tags; two
SD-WAN interface profiles (`wan1`/`wan2`, bound to the link tags, with
aggressive path monitoring); two traffic distribution profiles (`Best
Path`, `Top Down` — see note below); BGP enabled on the shared logical
router's `default` VRF (`local_as`/`router_id` as the `$ASN`/`$ROUTER_ID`
variables, redistributing connected routes via
`default_redistribution_profile`); two unconditional SD-WAN steering rules
(`corp-traffic`: LAN → hub/branch zones via Best Path; `catch-all`: any →
any via Best Path, also what carries backhauled internet traffic — see
"Optional behaviors" above); whatever `internet_routing`/
`branch_to_branch`/`branch_to_hub` currently call for; and the Auto VPN
cluster itself (hub-spoke, both WAN links on every hub/branch bound to
their interface profile and next-hop gateway variable, each gateway's own
`bgp_redistribution_profile` set per "Optional behaviors" above).

Confirmed live: both `Best Path` and `Top Down` traffic distribution
profiles use the identical `traffic_distribution: "Best Available Path"`
value — SCM has no separate "top-down" enum value for this field. A rule's
actual steering behavior comes from which named profile it references
(`action.traffic_distribution_profile`), not from a distinct value here.

Creating the Auto VPN cluster auto-generates the underlying IKE
gateways/IPSec tunnels/tunnel interfaces per device pair — this tool never
manages those directly, only the cluster definition. Confirmed live: on
push, SCM also auto-generates a device-scoped `"Underlay Routing"` SD-WAN
rule and `"SDWAN-Default"` distribution profile per device as part of the
Auto VPN feature itself — `reset` picks these up too (they're not
something `sdwan` creates or manages, just a side effect to be aware of).

Also confirmed live: the Auto VPN cluster materializes its own
per-device-folder `scm_router` shadow object (holding the auto-generated
tunnel/loopback interfaces above) at each hub's and branch's own SCM
**device folder** (e.g. `Hub`, `Branches`) — not something this tool
writes, a side effect of the cluster existing at all. If you're wiping
this deployment with [`reset`](reset.md) rather than `sdwan -mode
uninstall`, those device folders need their own `folder_list` entries
(ordered *before* the folder `sdwan.yml` itself targets) alongside
`Global` — see reset.md's own notes on folder wipe ordering — otherwise
the shadow `scm_router` (and, under `internet_routing: backhaul`, the
hub-folder `internet-traffic` override) will block deletion of the shared
`scm_router`/interfaces/link-tags with a `409 NON_ZERO_REFS` error.

## Example commands

```sh
# See what would change, without calling the SCM API
pang sdwan -dry-run

# Use the default playbook path (playbooks/sdwan.yml)
pang sdwan

# Point at a different playbook
pang sdwan -playbook playbooks/other_sdwan.yml

# Run the playbook but skip the automatic push, even if it sets push: true
pang sdwan -no-push
```

To tear down just the SD-WAN layer (leaving `internet`'s base config in
place), set `mode: uninstall` in the playbook and run `pang sdwan` again —
or use [`reset`](reset.md) with `Global` plus the hub/branch device
folders in `folder_list` (see "What gets created" above) to remove
everything, including the Auto VPN cluster, tenant-wide alongside
everything else.
