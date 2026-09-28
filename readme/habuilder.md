# habuilder

`habuilder` reconciles Strata Cloud Manager HA configs against a playbook
of firewall pairs. Every device is configured for basic active/passive HA,
with HA2 (data link) session sync over the raw ethernet transport.

Uses the same SCM credentials as every other tool here — see the main
[README's Credentials section](../README.md#credentials).

## Playbook format (`habuilder.yml`)

```yaml
name: JamesTheGreat's HA FW List
mode: install               # install | install-override | uninstall
push: true                  # push changed configs to the firewalls automatically
vars:
  default_ha1_ip: 10.0.0.1
  default_ha2_ip: 10.0.0.2
  default_ha_netmask: 255.255.255.252
  default_control_link_interface: ethernet1/6
  default_data_link_interface: ethernet1/7
  default_ha1_data_ip: 10.0.0.5
  default_ha2_data_ip: 10.0.0.6
fw_list:
  - name: James Lab 1
    primary_serial: "12345"
    secondary_serial: "67890"
```

- **mode**
  - `install` — create HA config only for devices that don't already have one; existing configs are left untouched.
  - `install-override` — create or update HA config for every device in `fw_list`, regardless of current state.
  - `uninstall` — remove HA config from every device in `fw_list`.
- **vars** — defaults shared across `fw_list`. Any `default_*` var (see **var_names** below for the actual key names) is applied automatically when the matching per-firewall field is omitted; a field can also explicitly reference a var with `vars.<key>` (e.g. `primary_ip: vars.default_ha1_ip`). **Write vars keys lowercase** — playbooks are parsed with [Viper](https://github.com/spf13/viper), which folds every config map key to lowercase internally with no way to opt out, so a mixed-case key like `default_HA1_IP` is actually stored (and looked up) as `default_ha1_ip` regardless of how it's written in the file.
- **fw_list[].primary_serial / secondary_serial** — always quote these (`"007954000909285"`, not `007954000909285`). An unquoted value that looks numeric gets parsed as a number by the YAML/Viper pipeline and loses any leading zeros, corrupting the serial silently at parse time (it'll then just fail to match any real device — loud, but avoidable). The same applies to any other field that's a string of digits, e.g. `claim_key` in a ZTP-style playbook.
- **fw_list[].primary_ip / secondary_ip / netmask** — override the `primary_ip`/`secondary_ip`/`netmask` vars key (`default_ha1_ip`/`default_ha2_ip`/`default_ha_netmask` unless renamed via `var_names`) for this pair's HA1 (control link) addressing. `primary_ip` is the primary device's own control-link IP; `secondary_ip` is the secondary's. Each device's peer is configured with the other's IP.
- **fw_list[].primary_data_ip / secondary_data_ip** — override the `primary_data_ip`/`secondary_data_ip` vars key (`default_ha1_data_ip`/`default_ha2_data_ip` by default) for this pair's HA2 (data link) addressing. SCM requires the data link to have its own IP/netmask per device even when using the ethernet transport; it shares `netmask` with the control link.
- **fw_list[].control_link_interface / data_link_interface** — override the `control_link_interface`/`data_link_interface` vars key (`default_control_link_interface`/`default_data_link_interface` by default) — the HA1 and HA2 ports, e.g. `ethernet1/6` / `ethernet1/7`.
- **fw_list[].group_id** — HA group ID (1-63). Defaults to the `group_id` vars key (`default_group_id` by default), or `"1"` if that isn't set either.
- **var_names** (top-level, optional) — renames which vars key each HA field falls back to, instead of the fixed `default_*` names above being hardcoded in Go. Keyed by the semantic field name, valued by the vars key to actually look up:
  ```yaml
  var_names:
    primary_ip: my_ha1_addr   # fw_list entries with no primary_ip now default to vars.my_ha1_addr
  ```
  Recognized keys: `primary_ip`, `secondary_ip`, `netmask`, `primary_data_ip`, `secondary_data_ip`, `control_link_interface`, `data_link_interface`, `group_id`. Any left out of `var_names` keep their built-in default name. Write values here lowercase too, for the same reason as `vars` keys above.
- **push** (top-level, default `false`) — after reconciling, automatically push the candidate configuration to every device that was actually created, updated, or deleted this run (SCM config API changes land in the candidate config and otherwise sit un-deployed until pushed, e.g. from the SCM UI, by some other automation, or an operator manually running "Push Config"). Devices left untouched (e.g. `[skip]`ped in `install` mode) are never included in the push. Pushing is a single `CommitAndPush` job covering all touched devices; habuilder waits for that job to finish and reports success or failure. Override with `--no-push` on the command line to skip it for one run without editing the playbook.

## Example commands

```sh
# See what would change, without calling the SCM API (safe against a live tenant)
pang habuilder --dry-run

# Use the default playbook path (playbooks/habuilder.yml)
pang habuilder

# Point at a different playbook
pang habuilder --playbook playbooks/other_pairs.yml

# Pass credentials as flags instead of env vars
pang habuilder \
  --client-id 'service1@12345.iam.panserviceaccount.com' \
  --client-secret 'xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx' \
  --tsg-id '12345'

# Run the playbook but skip the automatic push, even if it sets push: true
pang habuilder --no-push

# See all available flags
pang habuilder -h

# List every registered tool
pang help
```
