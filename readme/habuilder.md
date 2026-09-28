# habuilder

`habuilder` reconciles Strata Cloud Manager HA configs against a playbook
of firewall pairs. Every device is configured for basic active/passive HA,
with HA2 (data link) session sync over the raw ethernet transport.

Uses the same SCM credentials as every other tool here — see the main
[README's Credentials section](../README.md#credentials).

## Playbook format (`ha_pairs.yml`)

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

## Example commands

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
