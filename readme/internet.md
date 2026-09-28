# internet

`internet` configures basic internet access on a folder, snippet, or
firewall: a trust interface with a LAN IP and DHCP server, an untrust
interface (DHCP client, or static with its own default route), an optional
second ("secondary WAN") untrust interface, a NAT rule for outbound SNAT
via each untrust interface, and a security rule allowing all trust→untrust
traffic.

Uses the same SCM credentials as every other tool here — see the main
[README's Credentials section](../README.md#credentials). The `sdwan`
tool builds on top of what `internet` configures here (same interfaces,
zones, and logical router), so run `internet` first if you want SD-WAN too.

Playbooks are parsed with [Viper](https://github.com/spf13/viper) — always
quote a `serial` (or any other field that's purely digits): unquoted, a
value like `007954000909285` gets parsed as a number and silently loses
its leading zeros. See [`habuilder`'s docs](habuilder.md) for the full
explanation (it also covers Viper's map-key lowercasing, which doesn't
apply to `internet.yml` since none of its `vars` keys use mixed case).

## Playbook format (`internet.yml`)

```yaml
name: James Internet Firewalls
push: true
mode: install-override        # install | install-override | uninstall
vars:
  default_trust_interface: "$eth-local"
  default_untrust_interface: "$eth-internet"
  default_lan_cidr: "$lan_cidr"
  default_dns_server: "8.8.8.8"
  default_dhcp_pool: "$lan_pool"     # optional -- see below
  default_lan_gw: "$lan_gw"          # optional -- see below
  default_wan_cidr: "$wan_cidr"      # optional -- see below
  default_wan_gw: "$wan_gw"          # optional -- see below
  default_trust_zone: "zone-internal"     # optional -- see "Zone/router names" below
  default_untrust_zone: "zone-internet"   # optional
  default_router: "scm_router"            # optional
item_list:
  - name: Lab Firewalls
    type: folder                # folder | snippet | firewall
variable_overrides:
  - name: Lab FW A
    serial: "007954000891379"
    var_list:
      - name: "$lan_cidr"
        value: "10.0.0.1/24"
      - name: "$lan_gw"
        value: "10.0.0.1"
      - name: "$lan_pool"
        value: "10.0.0.128-10.0.0.254"
      - name: "$wan_cidr"           # only if you want a static WAN IP
        value: "192.168.123.2/24"
      - name: "$wan_gw"             # only if you want a static WAN IP
        value: "192.168.123.1"
```

- **mode** — `install` configures only targets missing internet access
  (every piece below is checked, not just the untrust interface, so a
  partially-completed prior run can still be finished); `install-override`
  reconfigures every target regardless of current state; `uninstall`
  removes everything this tool created.
- **item_list** — each entry's `type` is `folder`, `snippet`, or
  `firewall`. A `firewall` entry needs `serial` (its own field — `name` is
  just a display label for all three types, not looked up in SCM).
- **trust_interface / untrust_interface** — an SCM interface name, either
  literal (`ethernet1/4`) or a `$variable` (e.g. SCM's built-in
  `$eth-local`/`$eth-internet`). The untrust interface is DHCP client by
  default; it only becomes static (using `wan_cidr`/`wan_gw`, plus a
  manual default route since DHCP's auto-route won't apply) when **both**
  resolve to a real value — so `wan_cidr`/`wan_gw` have no required
  default, unlike the other fields.
- **lan_cidr** — the trust interface's own static IP/netmask, and the
  network the LAN DHCP server serves.
- **dhcp_pool** — the DHCP server's address pool range (e.g.
  `10.0.0.128-10.0.0.254`). Optional when `lan_cidr` is a literal CIDR (a
  reasonable pool covering the upper half of the network is auto-derived);
  required when `lan_cidr` is a `$variable`, since there's then no
  concrete network to derive a pool from until SCM resolves it per device.
- **lan_gw** — the gateway address the DHCP server hands to LAN clients.
  Same optional/required rule as `dhcp_pool` (auto-derived from a literal
  `lan_cidr`, required when `lan_cidr` is a `$variable`). This must be a
  bare IP, no netmask — confirmed live, PAN-OS's DHCP gateway field
  rejects a `/nn` suffix.
- **untrust02_interface / wan02_cidr / wan02_gw** — an optional second WAN
  interface (e.g. a redundant/secondary ISP uplink), behaving like
  `untrust_interface`/`wan_cidr`/`wan_gw`. Leaving `untrust02_interface`
  (and `default_untrust02_interface`) unset simply disables this feature
  for the item. A custom `$variable` name given here needs its own
  `default_value` defined somewhere (via this item's own `var_list`, or an
  ancestor's) — a literal interface name is handled automatically instead.
- **trust_zone / untrust_zone / untrust02_zone / router** — name the
  zone(s)/logical-router each interface should belong to. All optional:
  left unset, they fall back to this tool's own `trust`/`untrust`/
  `untrust02`/`default` naming and never disturb an interface that's
  already zoned/routed elsewhere (e.g. SCM's built-ins are usually already
  zoned `local`/`internet`). Set explicitly, the named zone/router is
  authoritative — the interface is moved there if it's zoned/routed
  elsewhere at a scope this tool owns, or the run fails with a clear error
  if it's zoned/routed via a shared/inherited object this tool doesn't own
  (PAN-OS only allows an interface to belong to one zone and one router at
  a time — see "Shared/inherited config is customized via overrides, not
  edited directly" below). A zone can, and often should, hold more than
  one interface (e.g. both WAN interfaces in one `zone-internet`) — that's
  ordinary PAN-OS behavior, not something this tool restricts.
- **variable_overrides** — writes per-firewall values for SCM
  `$variable`s referenced above (device-scoped, or folder/snippet-scoped
  for a value that genuinely shares one value across every device under
  that scope). `internet` also creates a matching parent definition at the
  item's own folder/snippet/device scope automatically wherever needed
  (see "How `$variable`s are resolved" below) — you only need to supply
  the per-device override values here.

## What gets created

For each item, in order: the trust and untrust interface(s) (static or
DHCP client); zone membership for each; routing; a LAN DHCP server; a NAT
rule per WAN interface (`dynamic_ip_and_port` SNAT via that interface's
own address); and a security rule allowing all trust→untrust traffic
(`application`/`service`/`category` all `any` — see "Why not SCM's
'Internet Access Rule'" below).

## How `$variable`s are resolved

SCM's per-device template variables need a *definition* at (or above) the
scope of whatever references them, in addition to each device's own
*override* value from `variable_overrides` — confirmed live two ways: a
route's gateway field rejects a `$variable` outright as "not a valid
reference" without one, and even a field that accepts the reference
blindly at save time (an interface's own IP) can still silently fail to
resolve at actual push/deploy time without one. `internet` creates that
parent definition automatically, at the item's own scope, with a
placeholder value that's never actually deployed anywhere (every real
device's value still comes from `variable_overrides`) — you don't need to
declare it yourself.

## Shared/inherited config is customized via overrides, not edited directly

Built-in interfaces like `$eth-internet`/`$eth-local` are commonly already
members of a shared zone and logical-router defined several folders up
(e.g. at a tenant-wide "ngfw-shared"-style folder) — SCM enforces that an
interface can only belong to one zone and one router at a time.
`internet` never edits that shared object directly (which would apply the
change to every device/folder that inherits from it, not just the one
this item targets); instead, when a static WAN route is needed, it
creates a same-named **override** at the item's own scope, layered on top
of the shared object — the same override pattern SCM itself uses for
things like a device-specific interface IP. Confirmed live: SCM overrides
fully replace their corresponding entry rather than merging field-by-field,
so the override always re-declares the full inherited interface list
alongside the new route, or the override would silently drop routing for
the whole VR on every device under that scope.

## Why not SCM's "Internet Access Rule"

SCM has a simplified `policy_type: "Internet"` security rule feature
(PAN-OS's "Internet Access Rule"). This tool doesn't use it: every one of
its filtering fields (`allow_web_application`, `block_web_application`,
`allow_url_category`, `block_url_category`) is web/URL-specific, with no
field for unrestricted (non-web) traffic — confirmed live, a client behind
the LAN interface could browse the web but nothing else until the rule
was switched to a standard `policy_type: "Security"` rule with
`application`/`service`/`category` all set to `any`, which is what
`internet` actually creates.

## Example commands

```sh
# See what would change, without calling the SCM API
pang internet --dry-run

# Use the default playbook path (playbooks/internet.yml)
pang internet

# Point at a different playbook
pang internet --playbook playbooks/other_internet.yml

# Pass credentials as flags instead of env vars
pang internet \
  --client-id 'service1@12345.iam.panserviceaccount.com' \
  --client-secret 'xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx' \
  --tsg-id '12345'

# Run the playbook but skip the automatic push, even if it sets push: true
pang internet --no-push

# See all available flags
pang internet -h

# List every registered tool
pang help
```
