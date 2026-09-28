# gopangoblin

A Go CLI for running tools related to Palo Alto Networks technologies,
primarily automating Strata Cloud Manager (SCM) configuration. Each tool
lives under `internal/<toolname>` with readme documentation under
`readme/<toolname>` and is registered with the top-level `pang` command 
(the binary built from this repo). The CLI itself is built on
[Cobra](https://github.com/spf13/cobra); playbooks are YAML files parsed
with [Viper](https://github.com/spf13/viper) — see
[`habuilder`'s docs](readme/habuilder.md) for two Viper quirks (numeric
strings, mixed-case keys) worth knowing before writing your own playbook.

## Disclaimer

This is an open-source project, provided as-is with no warranty of
any kind. Several of these tools (`reset` especially) delete configuration
and push changes to real firewalls. ⚠️**Read what a tool does before you run
it against anything you care about, and use `--dry-run` first.** If you
point this at the wrong tenant, playbook, or firewall and break something,
that's on you — the author is not responsible for any damage, data loss,
or downtime caused by using this software.

## Tools

Full docs, playbook formats, and example commands for each tool live in
[`readme/`](readme/):

- [**habuilder**](readme/habuilder.md) — builds (or removes) SCM HA
  configurations for a list of firewall pairs.
- [**reset**](readme/reset.md) — wipes SCM-managed configuration (network,
  security/NAT, objects, SD-WAN, HA, Auto VPN) back to a vanilla baseline
  for a list of devices, folders, and/or snippets.
- [**internet**](readme/internet.md) — configures basic internet access
  (trust/untrust interfaces, LAN DHCP, SNAT, an allow-all security rule) on
  a list of folders, snippets, and/or firewalls.
- [**sdwan**](readme/sdwan.md) — configures PAN-OS SD-WAN (interface/traffic
  distribution profiles, BGP, steering rules, and an Auto VPN hub-and-spoke
  cluster) on top of what `internet` already built.
- [**update**](readme/update.md) — pulls the latest gopangoblin source from
  GitHub and rebuilds.

Run `pang help` (or `pang -h`) to list every registered tool from the
binary itself.

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

## Updating

Once built, `pang update` refreshes gopangoblin in place (downloads the
current source from GitHub and rebuilds) without needing a `git` checkout:

```sh
pang update
```

Your `playbooks/` and `secret.txt` are never touched by an update — see
[`readme/update.md`](readme/update.md) for exactly what gets refreshed and
the available flags (`--repo`, `--branch`, `--output`).

## Credentials

`habuilder`, `reset`, `internet`, and `sdwan` all talk to the same SCM
config API and authenticate as a service account (OAuth2 client
credentials), resolved in this order — each one only used if the one
before it didn't supply a value:

| # | Source                                              | Example |
|---|------------------------------------------------------|---------|
| 1 | `--client-id` / `--client-secret` / `--tsg-id` flags | `pang sdwan --client-id '...' --client-secret '...' --tsg-id '12345'` |
| 2 | `SCM_CLIENT_ID` / `SCM_CLIENT_SECRET` / `SCM_TSG_ID` env vars | `export SCM_CLIENT_ID='service1@12345.iam.panserviceaccount.com'` |
| 3 | A shared YAML file (`--shared-config`, default `playbooks/shared.yml`) | see below |

A flag always wins over an env var, which always wins over the shared
file — so the shared file is a fallback default, never something you have
to override every time it doesn't apply.

```sh
export SCM_CLIENT_ID='service1@12345.iam.panserviceaccount.com'
export SCM_CLIENT_SECRET='...'
export SCM_TSG_ID='12345'
```

Or, so you never have to export those for a new shell session, put them
once in `playbooks/shared.yml` (create the file — `playbooks/` is
git-ignored, so this never gets committed):

```yaml
client_id: "service1@12345.iam.panserviceaccount.com"
client_secret: "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
tsg_id: "12345"
```

Every one of these four tools looks for that same file automatically —
nothing else to wire up per tool. A missing `shared.yml` is fine (it's
optional, source 3 above just contributes nothing); a `shared.yml` that
exists but fails to parse is a hard error rather than being silently
skipped.

> **Note:** the service account must have a role bound in Strata Cloud
> Manager (Settings → Identity & Access → Service Accounts) that grants
> access to that TSG. A service account with no role assigned will
> authenticate for an *unscoped* token but fail with
> `"Error running access token modification plugin"` when requesting a
> `tsg_id`-scoped token, which is what every SCM config API call requires.

Every one of these tools also supports `--dry-run` (print planned actions
without calling the SCM API) and `--no-push` (skip the automatic config
push for one run even if the playbook sets `push: true`).

## Project layout

```
main.go                         CLI entrypoint (builds the root Cobra command)
internal/tool/                  Tool registry + shared Cobra flag/Viper config helpers
internal/habuilder/             habuilder tool: playbook parsing + reconciliation
internal/reset/                 reset tool: playbook parsing + config wipe
internal/internet/              internet tool: playbook parsing + basic internet access setup
internal/sdwan/                 sdwan tool: playbook parsing + PAN-OS SD-WAN setup
internal/scm/                   Strata Cloud Manager API client (shared by every tool)
internal/update/                update tool: pulls source from GitHub and rebuilds
playbooks/habuilder.yml         Example/working habuilder playbook
playbooks/reset.yml             Example/working reset playbook
playbooks/internet.yml          Example/working internet playbook
playbooks/sdwan.yml             Example/working sdwan playbook
playbooks/shared.yml            Optional -- client_id/client_secret/tsg_id, see Credentials above
readme/                         Per-tool documentation
```

## License

Dual-licensed under either of:

- Apache License, Version 2.0 ([LICENSE-APACHE](LICENSE-APACHE))
- MIT license ([LICENSE-MIT](LICENSE-MIT))

at your option.
