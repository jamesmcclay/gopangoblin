# reset

`reset` wipes configuration back to a vanilla baseline for a list of
devices, folders, and/or snippets: network interfaces, zones, logical
routers, security/NAT rules, objects (addresses, address-groups, services,
service-groups, tags), SD-WAN configuration (interface/traffic
distribution profiles, link tags, SD-WAN steering rules), plus any HA
configuration `habuilder` created (device targets only — HA config is
always device-scoped) and any Auto VPN cluster (tenant-global — see
"Global" below). It does **not** touch a device's management interface or
DNS settings — see "What reset doesn't do" below — and it never touches
SCM registration, the folder/snippet objects themselves, or any device's
`folder`/`snippet` associations.

Uses the same SCM credentials as every other tool here — see the main
[README's Credentials section](../README.md#credentials).

Playbooks are parsed with [Viper](https://github.com/spf13/viper) — always
quote a `serial` (or any other field that's purely digits): unquoted, a
value like `007954000909285` gets parsed as a number and silently loses
its leading zeros. See [`habuilder`'s docs](habuilder.md) for the full
explanation (it also covers Viper's map-key lowercasing, which doesn't
apply to `reset.yml` since it has no `vars`).

## Playbook format (`reset.yml`)

```yaml
name: JamesTheGreat's Reset Firewalls
push: true                  # push device wipes to the firewalls automatically
fw_list:
  - name: James Lab A
    serial: "12345"
  - name: James Lab B
    serial: "67890"
folder_list:
  # Hub/Branches (or whatever device folders your hub_list/branch_list
  # devices actually live in) must come before the folder sdwan.yml
  # targets -- see "Global" below.
  - name: Hub
  - name: Branches
  - name: Lab Firewalls
    id: 11111111-1111-1111-1111-111111111111
  - name: Global
snippet_list:
  - name: Basic-Active-Passive-HA
    id: 22222222-2222-2222-2222-222222222222
```

- **push** — same semantics as habuilder: after wiping, automatically push
  the candidate config to every device actually affected this run.
  Override with `--no-push` for one run. Push only knows how to target
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

### "Global" — tenant-wide Auto VPN clusters

`"Global"` in `folder_list` is a special sentinel, not a real SCM folder —
confirmed live, it doesn't appear in SCM's own `/folders` list, and most
resource types reject it outright with `"Folder Global doesn't exist"`.
It exists because an Auto VPN cluster (created by the `sdwan` tool) is a
genuinely tenant-global object: it carries no `folder`/`snippet`/`device`
field at all, and its own `folder` query parameter has no filtering effect
whatsoever (confirmed live: querying with `folder=Global`, `folder=Lab
Firewalls`, or no folder param at all all return the identical tenant-wide
list). Including `Global` in `folder_list` tells `reset` to also delete
every Auto VPN cluster in the tenant, and to mark every hub/branch device
it referenced as touched for the subsequent push.

`Global` is always processed **first**, before any device/folder/snippet
wipe — an Auto VPN cluster names ethernet-interfaces, logical-routers, and
SD-WAN interface profiles by reference, and SCM enforces that referential
integrity on delete. Removing the cluster first can only reduce what's
referenced elsewhere, never add to it, so this ordering is always safe
regardless of what else the playbook targets.

A `Global` failure aborts the whole run immediately, before touching
anything else — confirmed live, SCM occasionally 403s a cluster delete
transiently (retrying the exact same call seconds later, no code change,
succeeds), but every later folder/device wipe assumes the cluster is
already gone. Pressing on regardless was observed to cascade into a wall
of confusing `409 NON_ZERO_REFS` "still referenced" deadlocks that don't
explain the real cause, and to still attempt a final push against a
half-wiped, referentially-inconsistent candidate config, which hung and
timed out rather than failing cleanly. Since nothing else has been touched
yet at that point, simply re-running `reset` is the correct recovery. For
the same reason, if any *other* folder/device/snippet wipe fails partway
through, the final push is skipped entirely rather than attempted against
config left in an unknown state.

Separately: even with `Global` wiping the cluster object itself, the Auto
VPN cluster also materializes its own per-device-folder `scm_router`
shadow object (holding its auto-generated tunnel/loopback interfaces) at
each hub's/branch's own SCM **device folder** (e.g. `Hub`, `Branches`) —
this is not something `Global` (or `sdwan`) directly owns or cleans up.
Deleting the cluster removes what *references* that shadow object, but the
shadow object itself, and (under `sdwan`'s `internet_routing: backhaul`) a
hub-folder `internet-traffic` SD-WAN rule override, still need their own
`folder_list` entries — see [`sdwan`'s docs](sdwan.md) for exactly what
gets left there. List those device folders **before** the folder your
`internet`/`sdwan` playbooks target, so their shadow `scm_router` is gone
before the shared `scm_router`/interfaces/link-tags are attempted.

## Safety: inherited config is never touched

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
directly by the exact device/folder/snippet being wiped (the Auto VPN
cluster case above is the one exception, since it has no owning scope to
verify against in the first place). Anything found to actually be
inherited is logged as `[shared] ... not removing ...` and left alone —
except SCM's own built-in template variables (`internal/scm/wipe.go`'s
`KnownBuiltInNames`, e.g. `$eth-internet`/`$eth-local`, the default
untrust/trust interfaces provided by the "All" folder and resolved per
device as `ethernet1/3`/`ethernet1/4` in this lab), which are still never
deleted but are expected to show up as inherited on every run, so that
specific log line is silenced for them.

A second, distinct case of this: a rule can be *materialized from a
snippet attached to a folder* and report its scope as that folder with no
visible sign it came from a snippet — passing the check above — yet SCM
still refuses to delete it via the folder, returning a `DELETE_NOT_ALLOWED`
error (confirmed live against this lab's `Basic-Active-Passive-HA`
snippet). `reset` treats that error as a permanent, non-fatal skip
(logged the same way) rather than aborting the run — it can only be
removed by detaching the snippet itself, which `reset` doesn't do.

## How the wipe works (and its limits)

SCM's config API has no "declare the desired config, drop everything else"
verb — there's no wholesale replace. It's structured like Kubernetes' or
Terraform's provider APIs: individually typed resources (zones, rules,
interfaces, objects, ...), each with its own list/get/create/update/delete
endpoints, not a single document you can PUT as a whole. So `reset` wipes
a device, folder, or snippet by enumerating a fixed list of known
resource types (`internal/scm/wipe.go`'s `WipeResources`) and deleting
whatever's found and confirmed directly owned by that scope, retrying in
rounds so undocumented deletion-order dependencies between resource types
(e.g. an SD-WAN rule referencing a traffic distribution profile, which in
turn references a link tag) resolve themselves.

That list isn't exhaustive — PBF rules, DoS/decryption/app-override/QoS
rules, sub-interfaces, external dynamic lists, and others aren't covered
yet, so a target using one of those config types isn't guaranteed fully
vanilla after a reset. Extend `WipeResources` as gaps are found; a target
with an unlisted resource type just won't have that particular config
removed, it won't cause an error.

One resource-specific gotcha worth knowing if you're extending this list:
`sdwan-rules` has a pre/post rulebase split like `security-rules`/
`nat-rules` at **folder** scope, but confirmed live, a **device**-scoped
query ignores the `position` filter entirely and returns the identical
full list for both `pre` and `post` — `reset` dedupes candidates by
`(path, id)` to avoid double-processing the same object because of this.

## What reset doesn't do

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

## Example commands

```sh
# See what would be removed, without calling the SCM API
pang reset --dry-run

# Use the default playbook path (playbooks/reset.yml)
pang reset

# Point at a different playbook
pang reset --playbook playbooks/other_reset.yml

# Run the playbook but skip the automatic push, even if it sets push: true
pang reset --no-push
```
