# gopangoblin

A Go CLI for running tools related to Palo Alto Networks technologies. Each
tool lives under `internal/<toolname>` and is registered with the top-level
`pang` command (the binary built from this repo).

## Tools

- **habuilder** — builds (or removes) Strata Cloud Manager (SCM) HA
  configurations for a list of firewall pairs defined in a YAML playbook.
- **reset** — wipes a device's own SCM-managed configuration (interfaces,
  zones, routing, security/NAT rules, objects, HA config) back to a vanilla
  baseline, for a list of firewalls defined in a YAML playbook.
- **internet** — configures basic internet access (trust/untrust interfaces,
  a LAN DHCP server, a default SNAT rule, and an allow-all security policy)
  on a list of folders, snippets, and/or firewalls defined in a YAML playbook.
- **update** — pulls the latest gopangoblin source from GitHub and rebuilds.

## Setup (Windows / PowerShell)

Paste this into PowerShell to download Go, pull the latest `gopangoblin`
source from GitHub, and build it:

```powershell
$root = Join-Path $env:USERPROFILE "gopangoblin-run"
mkdir $root -Force | Out-Null
Set-Location $root

if (!(Test-Path .\go\bin\go.exe)) {
  $v = (Invoke-WebRequest "https://go.dev/VERSION?m=text" -UseBasicParsing).Content.Split("`n")[0].Trim()
  curl.exe -L "https://go.dev/dl/$v.windows-amd64.zip" -o go.zip
  Expand-Archive go.zip . -Force
}

Remove-Item gopangoblin-main -Recurse -Force -ErrorAction Ignore
Invoke-WebRequest "https://github.com/jamesmcclay/gopangoblin/archive/refs/heads/main.zip" -OutFile repo.zip
Expand-Archive repo.zip . -Force
Set-Location gopangoblin-main

$env:PATH = "$root\go\bin;$env:PATH"
go build -o pang.exe .

Write-Host "Built $root\gopangoblin-main\pang.exe"
```

This script is also checked in at [`setup.ps1`](setup.ps1).

### Manual / other platforms

```sh
git clone https://github.com/jamesmcclay/gopangoblin.git
cd gopangoblin
go build -o pang .
```

## habuilder

`habuilder` reconciles Strata Cloud Manager HA configs against a playbook
of firewall pairs.

### Credentials

`habuilder` authenticates to SCM as a service account (OAuth2 client
credentials). Provide the following via flags or environment variables:

| Flag              | Env var             | Description                              |
|-------------------|----------------------|-------------------------------------------|
| `-client-id`      | `SCM_CLIENT_ID`      | Service account client ID (looks like an email, e.g. `svc@<tsg_id>.iam.panserviceaccount.com`) |
| `-client-secret`  | `SCM_CLIENT_SECRET`  | Service account client secret            |
| `-tsg-id`         | `SCM_TSG_ID`         | Tenant Service Group ID (the numeric segment of the client ID's domain) |

```sh
export SCM_CLIENT_ID='service1@12345.iam.panserviceaccount.com'
export SCM_CLIENT_SECRET='...'
export SCM_TSG_ID='12345'

pang habuilder -playbook playbooks/ha_pairs.yml
```

> **Note:** the service account must have a role bound in Strata Cloud
> Manager (Settings → Identity & Access → Service Accounts) that grants
> access to that TSG. A service account with no role assigned will
> authenticate for an *unscoped* token but fail with
> `"Error running access token modification plugin"` when requesting a
> `tsg_id`-scoped token, which is what every SCM config API call requires.

Use `-dry-run` to print the planned create/update/delete actions without
calling the SCM API. Use `-no-push` to skip the automatic config push for
one run even if the playbook has `push: true`.

### Playbook format (`ha_pairs.yml`)

```yaml
name: JamesTheGreat's HA FW List
mode: install               # install | install-override | uninstall
push: true                  # push changed configs to the firewalls automatically
vars:
  default_HA1_IP: 10.0.0.1
  default_HA2_IP: 10.0.0.2
  default_HA_netmask: 255.255.255.252
  default_control_link_interface: ethernet1/6
  default_data_link_interface: ethernet1/7
  default_HA1_data_IP: 10.0.0.5
  default_HA2_data_IP: 10.0.0.6
fw_list:
  - name: James Lab 1
    primary_serial: 12345
    secondary_serial: 67890
```

- **mode**
  - `install` — create HA config only for devices that don't already have one; existing configs are left untouched.
  - `install-override` — create or update HA config for every device in `fw_list`, regardless of current state.
  - `uninstall` — remove HA config from every device in `fw_list`.
- **vars** — defaults shared across `fw_list`. Any `default_*` var is applied automatically when the matching per-firewall field is omitted; a field can also explicitly reference a var with `vars.<key>` (e.g. `primary_ip: vars.default_HA1_IP`).
- **fw_list[].primary_ip / secondary_ip / netmask** — override `vars.default_HA1_IP` / `default_HA2_IP` / `default_HA_netmask` for this pair's HA1 (control link) addressing. `primary_ip` is the primary device's own control-link IP; `secondary_ip` is the secondary's. Each device's peer is configured with the other's IP.
- **fw_list[].primary_data_ip / secondary_data_ip** — override `vars.default_HA1_data_IP` / `default_HA2_data_IP` for this pair's HA2 (data link) addressing. SCM requires the data link to have its own IP/netmask per device even when using the ethernet transport; it shares `netmask` with the control link.
- **fw_list[].control_link_interface / data_link_interface** — override `vars.default_control_link_interface` / `default_data_link_interface` (the HA1 and HA2 ports, e.g. `ethernet1/6` / `ethernet1/7`).
- **fw_list[].group_id** — HA group ID (1-63). Defaults to `vars.default_group_id`, or `"1"` if that isn't set either.
- **push** (top-level, default `false`) — after reconciling, automatically push the candidate configuration to every device that was actually created, updated, or deleted this run (SCM config API changes land in the candidate config and otherwise sit un-deployed until pushed, e.g. from the SCM UI, by some other automation, or an operator manually running "Push Config"). Devices left untouched (e.g. `[skip]`ped in `install` mode) are never included in the push. Pushing is a single `CommitAndPush` job covering all touched devices; habuilder waits for that job to finish and reports success or failure. Override with `-no-push` on the command line to skip it for one run without editing the playbook.

Every device is configured for basic active/passive HA, with HA2 (data
link) session sync over the raw ethernet transport.

### Example commands

```sh
# See what would change, without calling the SCM API (safe against a live tenant)
pang habuilder -dry-run

# Use the default playbook path (playbooks/ha_pairs.yml)
pang habuilder

# Point at a different playbook
pang habuilder -playbook playbooks/other_pairs.yml

# Pass credentials as flags instead of env vars
pang habuilder \
  -client-id 'service1@12345.iam.panserviceaccount.com' \
  -client-secret 'xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx' \
  -tsg-id '12345'

# Run the playbook but skip the automatic push, even if it sets push: true
pang habuilder -no-push

# See all available flags
pang habuilder -h

# List every registered tool
pang help
```

## reset

`reset` wipes configuration back to a vanilla baseline for a list of
devices, folders, and/or snippets: network interfaces, zones, logical
routers, security/NAT rules, and objects (addresses, address-groups,
services, service-groups, tags), plus any HA configuration habuilder
created (device targets only — HA config is always device-scoped). It
does **not** touch a device's management interface or DNS settings — see
"What reset doesn't do" below — and it never touches SCM registration,
the folder/snippet objects themselves, or any device's `folder`/`snippet`
associations.

It uses the same SCM credentials as habuilder (flags or
`SCM_CLIENT_ID`/`SCM_CLIENT_SECRET`/`SCM_TSG_ID`).

### Playbook format (`reset.yml`)

```yaml
name: JamesTheGreat's Reset Firewalls
push: true                  # push device wipes to the firewalls automatically
fw_list:
  - name: James Lab A
    serial: 12345
  - name: James Lab B
    serial: 67890
folder_list:
  - name: Lab Firewalls
    id: 11111111-1111-1111-1111-111111111111
snippet_list:
  - name: Basic-Active-Passive-HA
    id: 22222222-2222-2222-2222-222222222222
```

- **push** — same semantics as habuilder: after wiping, automatically push
  the candidate config to every device actually affected this run.
  Override with `-no-push` for one run. Push only knows how to target
  devices, but wiping a folder or snippet changes the candidate config for
  every device that inherits from it, not just devices listed in
  `fw_list` — so `reset` works out which devices those are (by walking
  each device's folder ancestry, and each folder's attached snippets) and
  pushes to all of them automatically. That resolution only covers
  devices this service account can see via `ListDevices`; anything else
  inheriting the same folder/snippet (e.g. in a different tenant scope)
  still needs a separate push.
- **folder_list / snippet_list** — folders and snippets to wipe directly
  (not the devices that happen to inherit from them). `name` is required
  and is what SCM's object-scoping APIs actually key on (`folder=<name>`,
  `snippet=<name>`); `id` is optional and, if set, is cross-checked
  against the folder/snippet's real server-assigned id (from SCM's
  `/folders`/`/snippets`) before anything runs, catching a stale or
  mistyped `name` pointing at the wrong one. Folder/snippet wiping isn't
  recursive: a child folder must be listed separately if you want it
  wiped too.

### Safety: inherited config is never touched

SCM config objects can be inherited from an ancestor folder or snippet —
a device inherits from its containing folder, and a folder inherits from
its parent folder up the tree (e.g. every onboarded NGFW in this lab
inherits a default `ethernet1/3` "Internet Interface" from a shared
`ngfw-shared` folder template several levels up from the device itself).
SCM's scoped list API resolves that inherited config into whatever scope
you queried and echoes that scope back in the response as if the object
belonged there directly — confirmed live, including the same object id
being returned identically for every scope that inherits it. `reset`
re-verifies every candidate object with a second, unfiltered lookup
before deleting it, and only ever deletes objects confirmed to be owned
directly by the exact device/folder/snippet being wiped. Anything found
to actually be inherited is logged as `[shared] ... not removing ...`
and left alone — except SCM's own built-in template variables
(`internal/scm/wipe.go`'s `KnownBuiltInNames`, e.g. `$eth-internet`/
`$eth-local`, the default untrust/trust interfaces provided by the "All"
folder and resolved per device as `ethernet1/3`/`ethernet1/4` in this
lab), which are still never deleted but are expected to show up as
inherited on every run, so that specific log line is silenced for them.

A second, distinct case of this: a rule can be *materialized from a
snippet attached to a folder* and report its scope as that folder with no
visible sign it came from a snippet — passing the check above — yet SCM
still refuses to delete it via the folder, returning a `DELETE_NOT_ALLOWED`
error (confirmed live against this lab's `Basic-Active-Passive-HA`
snippet). `reset` treats that error as a permanent, non-fatal skip
(logged the same way) rather than aborting the run — it can only be
removed by detaching the snippet itself, which `reset` doesn't do.

### How the wipe works (and its limits)

SCM's config API has no "declare the desired config, drop everything else"
verb — there's no wholesale replace. It's structured like Kubernetes' or
Terraform's provider APIs: individually typed resources (zones, rules,
interfaces, objects, ...), each with its own list/get/create/update/delete
endpoints, not a single document you can PUT as a whole. So `reset` wipes
a device, folder, or snippet by enumerating a fixed list of known
resource types (`internal/scm/wipe.go`'s `WipeResources`) and deleting
whatever's found and confirmed directly owned by that scope.

That list isn't exhaustive — PBF rules, DoS/decryption/app-override/QoS/
SDWAN rules, sub-interfaces, external dynamic lists, and others aren't
covered yet, so a target using one of those config types isn't guaranteed
fully vanilla after a reset. Extend `WipeResources` as gaps are found; a
target with an unlisted resource type just won't have that particular
config removed, it won't cause an error.

### What reset doesn't do

Setting a device's management interface (IP/DHCP, gateway) or DNS
servers, and setting its local admin user/password, are **not**
implemented. Both were investigated and dropped:

- SCM's `management-interface` and `service-settings` (DNS) config APIs
  reject device-scoped create/update for these on-prem/self-registered
  lab devices with `"Device <serial> doesn't exist"`, before any field
  validation even runs — folder-scoped requests to the same endpoint go
  through fine. This looks like a real product limitation (centrally
  pushing a change to the very interface SCM uses to reach the box is a
  chicken-and-egg problem), not something fixable from this client.
- A device's local admin user/password isn't exposed by any SCM config
  API at all — PAN-OS's `mgt-config` administrators are a different thing
  from SCM's `/local-users` (which is the Local User Database used for
  auth policies, not device administrators).

Both would need direct PAN-OS XML API calls to each firewall's reachable
management IP using its current admin credentials, rather than the SCM
candidate-push model the rest of this tool uses. That's a meaningfully
different mechanism (and a different credential story), so it's left as
a possible future addition rather than half-implemented here.

### Example commands

```sh
# See what would be removed, without calling the SCM API
pang reset -dry-run

# Use the default playbook path (playbooks/reset.yml)
pang reset

# Point at a different playbook
pang reset -playbook playbooks/other_reset.yml

# Run the playbook but skip the automatic push, even if it sets push: true
pang reset -no-push
```

## internet

`internet` configures basic internet access on a folder, snippet, or
firewall: a trust interface with a LAN IP and DHCP server, an untrust
interface (DHCP client, or static with its own default route), a NAT rule
for outbound SNAT via the untrust interface, and a security rule allowing
all trust→untrust traffic. It uses the same SCM credentials as
habuilder/reset.

### Playbook format (`internet.yml`)

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
item_list:
  - name: Lab Firewalls
    type: folder                # folder | snippet | firewall
variable_overrides:
  - name: Lab FW A
    serial: 007954000891379
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
- **variable_overrides** — writes per-firewall values for SCM
  `$variable`s referenced above (device-scoped). `internet` also creates a
  matching parent definition at the item's own folder/snippet/device scope
  automatically wherever needed (see "How `$variable`s are resolved"
  below) — you only need to supply the per-device override values here.

### What gets created

For each item, in order: the trust and untrust interfaces (static or DHCP
client); zone membership for both (a zone named `trust`/`untrust` is
created if the interface isn't already zoned elsewhere — e.g. SCM's
built-ins are usually already zoned `local`/`internet`, and that's left
as-is rather than re-zoned); routing (likewise, an interface already
routed via an existing logical-router — commonly a shared one several
folders up — is left alone; only a genuinely unrouted interface gets a
new router); a LAN DHCP server; a NAT rule (`dynamic_ip_and_port` SNAT via
the untrust interface's own address); and a security rule allowing all
trust→untrust traffic (`application`/`service`/`category` all `any` — see
"Why not SCM's 'Internet Access Rule'" below).

### How `$variable`s are resolved

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

### Shared/inherited config is customized via overrides, not edited directly

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

### Why not SCM's "Internet Access Rule"

SCM has a simplified `policy_type: "Internet"` security rule feature
(PAN-OS's "Internet Access Rule"). This tool doesn't use it: every one of
its filtering fields (`allow_web_application`, `block_web_application`,
`allow_url_category`, `block_url_category`) is web/URL-specific, with no
field for unrestricted (non-web) traffic — confirmed live, a client behind
the LAN interface could browse the web but nothing else until the rule
was switched to a standard `policy_type: "Security"` rule with
`application`/`service`/`category` all set to `any`, which is what
`internet` actually creates.

### Example commands

```sh
# See what would change, without calling the SCM API
pang internet -dry-run

# Use the default playbook path (playbooks/internet.yml)
pang internet

# Point at a different playbook
pang internet -playbook playbooks/other_internet.yml

# Run the playbook but skip the automatic push, even if it sets push: true
pang internet -no-push
```

## update

`update` refreshes gopangoblin in place: it downloads the current source
zip from GitHub (the same `archive/refs/heads/<branch>.zip` URL `setup.ps1`
uses — no `git` checkout required), syncs it over the tool's own source
files, and rebuilds the binary.

```sh
pang update              # or: go run . update
```

Run it from the gopangoblin repo root (the directory containing `go.mod`
— e.g. `gopangoblin-main` if it was set up via `setup.ps1`). It requires the
`go` toolchain on `PATH`.

`update` refreshes gopangoblin's own repo files by an explicit allowlist, not
just `.go` files: `main.go`, `go.mod`, `go.sum`, `setup.ps1`, `README.md`,
`.gitignore`, `LICENSE-APACHE`, `LICENSE-MIT`, and everything under
`internal/` (including non-Go files that might live there). Anything outside
that list — most importantly your `playbooks/` and `secret.txt` — is never
touched, so local config and credentials survive an update untouched.

Flags:

| Flag       | Default                                     | Description                          |
|------------|----------------------------------------------|---------------------------------------|
| `-repo`    | `https://github.com/jamesmcclay/gopangoblin` | Repo to pull from                     |
| `-branch`  | `main`                                        | Branch to pull                        |
| `-output`  | `pang` (`pang.exe` on Windows)                | Path to write the rebuilt binary to   |

```sh
# Rebuild from a different branch, e.g. to try an in-progress feature
pang update -branch dev

# Rebuild to a distinct path instead of overwriting the current binary
pang update -output pang-new
```

## Project layout

```
main.go                         CLI entrypoint and tool dispatch
internal/tool/                  Tool registry
internal/habuilder/             habuilder tool: playbook parsing + reconciliation
internal/reset/                 reset tool: playbook parsing + config wipe
internal/internet/              internet tool: playbook parsing + basic internet access setup
internal/scm/                   Strata Cloud Manager API client (shared by all three)
internal/update/                update tool: pulls source from GitHub and rebuilds
playbooks/ha_pairs.yml          Example/working habuilder playbook
playbooks/reset.yml             Example/working reset playbook
playbooks/internet.yml          Example/working internet playbook
```

## License

Dual-licensed under either of:

- Apache License, Version 2.0 ([LICENSE-APACHE](LICENSE-APACHE))
- MIT license ([LICENSE-MIT](LICENSE-MIT))

at your option.
