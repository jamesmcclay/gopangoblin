# update

`update` refreshes gopangoblin in place: it downloads the current source
zip from GitHub (the same `archive/refs/heads/<branch>.zip` URL
`setup.ps1` uses — no `git` checkout required), syncs it over the tool's
own source files, and rebuilds the binary.

```sh
pang update              # or: go run . update
```

Run it from the gopangoblin repo root (the directory containing `go.mod`
— e.g. `gopangoblin-main` if it was set up via `setup.ps1`). It requires
the `go` toolchain on `PATH`.

`update` refreshes gopangoblin's own repo files by an explicit allowlist
(`internal/update/source.go`'s `syncPaths`), not just `.go` files:
`main.go`, `go.mod`, `go.sum`, `setup.ps1`, `README.md`, `readme/`,
`.gitignore`, `LICENSE-APACHE`, `LICENSE-MIT`, and everything under
`internal/` (including non-Go files that might live there). Anything
outside that list — most importantly your `playbooks/` and `secret.txt`
— is never touched, so local config and credentials survive an update
untouched.

## Flags

| Flag       | Default                                     | Description                          |
|------------|----------------------------------------------|---------------------------------------|
| `--repo`    | `https://github.com/jamesmcclay/gopangoblin` | Repo to pull from                     |
| `--branch`  | `main`                                        | Branch to pull                        |
| `--output`  | `pang` (`pang.exe` on Windows)                | Path to write the rebuilt binary to   |

```sh
# Rebuild from a different branch, e.g. to try an in-progress feature
pang update --branch dev

# Rebuild to a distinct path instead of overwriting the current binary
pang update --output pang-new
```
