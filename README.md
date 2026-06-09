# k8tc

A two-panel terminal UI (Midnight Commander style) for transferring files
between your local machine and a Kubernetes pod. Transfers stream `tar` over
`kubectl exec`, so file **mode** bits and **mtime** are preserved.

```
╭─ LOCAL: /home/user/project ─────╮╭─ POD nginx-abc: /var/www ───────╮
│ ..                              ││ ..                              │
│ assets/                         ││ assets/                         │
│ index.html                 1.2K ││ index.html                 1.2K │
│ go.mod                      56B ││ go.mod                      56B │
╰─────────────────────────────────╯╰─────────────────────────────────╯
 Tab switch  ↑↓ move  ⏎ open  F5 copy  r refresh  q quit   <status>
```

## Requirements

- `kubectl` on your `PATH`, configured with a valid context.
- The target pod's container must have `tar` available (the standard busybox
  `tar` is fine). Distroless/scratch images without `tar` cannot be used for
  transfers — k8tc reports this clearly rather than hanging.
- A local `tar` (GNU or BSD).

## Install

```sh
CGO_ENABLED=0 go build -o k8tc ./cmd/k8tc
```

Produces a single static binary.

## Usage

```sh
k8tc --pod <name> [flags]
```

| Flag                   | Meaning                                                        |
|------------------------|----------------------------------------------------------------|
| `--pod`                | Pod name (required).                                           |
| `--namespace`, `-n`    | Namespace (passed to `kubectl -n`).                           |
| `--container`, `-c`    | Container name (`kubectl exec -c`); omit for the default.     |
| `--remote-path`        | Initial remote directory (default `/`).                       |
| `--local-path`         | Initial local directory (default `.`).                        |
| `--preserve-ownership` | Attempt to restore owner UID/GID on extract. See below.       |

### Keybindings

| Key            | Action                                                        |
|----------------|---------------------------------------------------------------|
| `Tab`          | Switch focus between the local and remote panel               |
| `↑`/`↓`, `k`/`j` | Move the cursor                                             |
| `PgUp`/`PgDn`  | Page the cursor                                               |
| `Enter`        | Descend into a directory / ascend via `..`                    |
| `F5` or `c`    | Copy the highlighted file or directory to the other panel     |
| `r`            | Refresh the focused panel                                     |
| `q`, `Ctrl+C`  | Quit                                                          |

`F5` copies the highlighted entry from the **focused** panel into the **other**
panel's current directory. Directory copies are recursive. Transfers run
asynchronously, showing a byte count in the status line; the UI stays responsive
and a large transfer never freezes it.

## A note on ownership

Mode bits and mtime are preserved without any special privilege. **Owner
UID/GID is best-effort.** When `tar` extracts without `CAP_CHOWN` (a normal user
locally, or a rootless container), it silently creates files owned by the
extracting user rather than failing.

- By default k8tc extracts with `--no-same-owner` (mode + mtime preserved,
  ownership left to the extracting user). This is the sensible default in both
  directions — the pod's UID 1000 is not your UID, and a rootless pod can't
  chown anyway.
- `--preserve-ownership` extracts with `--same-owner --numeric-owner`. This only
  has an effect when the extracting end is **privileged** (root in the
  container, or root locally). Against a rootless target it degrades to the same
  best-effort behavior, so don't be surprised if UIDs don't carry across.

## Architecture

```
cmd/k8tc/main.go        entrypoint, flags, program start
internal/transfer/      Transfer interface + kubectl-backed implementation
internal/local/         local filesystem browsing
internal/ui/            Bubble Tea model, panels, keys, styling
```

The remote filesystem is reached only through the `transfer.Transfer`
interface, so the `kubectl`-backed implementation can later be swapped for a
`client-go` one without touching the TUI.
