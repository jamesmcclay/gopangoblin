# gopangoblin

A Go CLI for running tools related to Palo Alto Networks technologies. Each
tool lives under `internal/<toolname>` and is registered with the top-level
`pang` command (the binary built from this repo).

## Tools

- **habuilder** — builds (or removes) Strata Cloud Manager (SCM) HA
  configurations for a list of firewall pairs defined in a YAML playbook.
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

This script is also checked in at [`setup.ps`](setup.ps).

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

## update

`update` refreshes gopangoblin in place: it downloads the current source
zip from GitHub (the same `archive/refs/heads/<branch>.zip` URL `setup.ps`
uses — no `git` checkout required), syncs it over the tool's own source
files, and rebuilds the binary.

```sh
pang update              # or: go run . update
```

Run it from the gopangoblin repo root (the directory containing `go.mod`
— e.g. `gopangoblin-main` if it was set up via `setup.ps`). It requires the
`go` toolchain on `PATH`.

Only gopangoblin's own code is refreshed — `main.go`, `go.mod`, `go.sum`,
`setup.ps`, `README.md`, `.gitignore`, and everything under `internal/`.
Your `playbooks/` and `secret.txt` are never touched.

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
internal/habuilder/scm/         Minimal Strata Cloud Manager API client
internal/update/                update tool: pulls source from GitHub and rebuilds
playbooks/ha_pairs.yml          Example/working habuilder playbook
```

## License

Dual-licensed under either of:

- Apache License, Version 2.0 ([LICENSE-APACHE](LICENSE-APACHE))
- MIT license ([LICENSE-MIT](LICENSE-MIT))

at your option.
