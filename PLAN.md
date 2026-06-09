# PLAN: `k8tc` — A Two-Panel TUI for Kubernetes Pod File Transfer

## Goal

Build a terminal UI tool, written in **Go**, that lets a user browse the local
filesystem and a Kubernetes pod's filesystem side-by-side in a two-panel
(Midnight Commander style) layout, and transfer files/directories between them.
Transfers use **`tar` streamed over `kubectl exec`** so that timestamps and file
permissions are preserved.

Ship as a **single static binary**.

## Non-Goals (v1)

- No `client-go` integration. v1 shells out to the user's existing `kubectl`.
  (Keep the transfer layer behind an interface so `client-go` can be added later
  without touching the TUI.)
- No editing of remote files in-place.
- No multi-pod parallel transfers.
- No Windows-specific polish (target Linux/macOS; it may work on Windows but
  that is not a v1 requirement).

---

## Tech Stack

- **Language:** Go (1.22+)
- **TUI framework:** [Bubble Tea](https://github.com/charmbracelet/bubbletea)
  (`github.com/charmbracelet/bubbletea`) with
  [Bubbles](https://github.com/charmbracelet/bubbles) components and
  [Lip Gloss](https://github.com/charmbracelet/lipgloss) for styling.
- **External dependency at runtime:** `kubectl` must be on the user's `PATH`
  and configured (valid kubeconfig / current context). The target pod must have
  `tar` available in the chosen container.

---

## Architecture

### Package layout

```
cmd/k8tc/main.go        # entrypoint, flag parsing, bubbletea program start
internal/transfer/      # the Transfer interface + kubectl implementation
  transfer.go           # interface + shared types (FileInfo)
  kubectl.go            # kubectl-backed implementation
internal/local/         # local filesystem browsing (List/Stat helpers)
internal/ui/            # bubbletea model, panels, key handling, rendering
  model.go
  panel.go
  keys.go
  styles.go
```

### Core interface

The transfer layer is abstracted so the kubectl implementation can later be
swapped for a `client-go` one:

```go
package transfer

import "time"

type FileInfo struct {
    Name    string
    Size    int64
    Mode    string    // e.g. "drwxr-xr-x"
    IsDir   bool
    ModTime time.Time
}

type Transfer interface {
    // List returns directory contents at path inside the pod.
    List(pod, container, path string) ([]FileInfo, error)
    // Pull copies remotePath (file or dir) from the pod to localPath, preserving metadata.
    Pull(pod, container, remotePath, localPath string, progress func(n int64)) error
    // Push copies localPath (file or dir) into the pod at remotePath, preserving metadata.
    Push(pod, container, localPath, remotePath string, progress func(n int64)) error
}
```

The local filesystem panel does **not** go through `Transfer`; it uses the
`internal/local` helpers directly. Only the remote panel uses `Transfer`.

---

## Transfer Mechanics (the important bit)

All remote operations shell out to `kubectl`. Build commands with
`os/exec.CommandContext` and stream stdin/stdout — **never** buffer whole files
in memory.

### Listing remote files

```
kubectl exec <pod> [-c <container>] -- ls -la --full-time <path>
```

Parse the output into `[]FileInfo`. Notes:
- Use `--full-time` (GNU coreutils) for a parseable ISO timestamp. If that fails
  (BusyBox), fall back to `ls -la` and accept coarser/absent mtimes rather than
  erroring out.
- Skip the `total N` first line.
- Always synthesize a `..` entry for navigation (unless at `/`).
- Detect directories from the leading `d` in the mode string.

### Pull (pod → local), metadata-preserving

```
kubectl exec <pod> [-c <container>] -- tar cf - -C <remoteParent> <remoteBase> \
  | tar xpf - --no-same-owner -C <localDest>
```

- `tar c` on the remote side, piped to `tar xp` locally (`-p` preserves mode +
  mtime). `--no-same-owner` is the default for pulling — see "tar flags &
  ownership" below for why.
- Run the local `tar` via `exec.Command` and connect the kubectl stdout to its
  stdin with an `io.Pipe` (or `cmd.StdoutPipe()` → `cmd2.Stdin`).
- Wrap the pipe in a counting `io.Reader` to drive the `progress` callback.

### Push (local → pod), metadata-preserving

```
tar cf - -C <localParent> <localBase> \
  | kubectl exec -i <pod> [-c <container>] -- tar xpf - --no-same-owner -C <remoteDest>
```

- Note the `-i` on `kubectl exec` so stdin is forwarded.
- `--no-same-owner` again by default — in a rootless container the extract
  cannot chown anyway (see below); this makes the intent explicit and avoids
  warnings.
- Same counting-reader trick for progress.

### tar flags & ownership

**Mode bits and mtime are reliably preserved without privilege. Owner UID/GID
is not — treat it as best-effort.**

When `tar x` runs without `CAP_CHOWN` (extracting on your local machine as a
normal user, or inside a rootless pod), the `chown()` calls fail with `EPERM`.
GNU tar's default for a non-root extract is to *silently drop* ownership restore
and create files owned by the extracting user — it does **not** hard-fail. So a
blanket `--numeric-owner` on extract buys nothing in the common case: it only
controls *how* a UID is chosen (by number vs. name lookup), not whether tar is
*allowed* to apply it.

Defaults, therefore:

- **Create side:** `tar --numeric-owner -cf - ...`
  Numeric is harmless here and avoids name-lookup surprises when packing.
- **Extract side (default):** `tar -xpf - --no-same-owner ...`
  Preserves mode + mtime, and explicitly tells tar not to attempt chown. This is
  the right default for both directions:
  - Pulling to local: you almost never want the pod's UIDs applied on your
    machine anyway (UID 1000 in the pod ≠ you).
  - Pushing to a rootless pod: the chown would no-op regardless, so don't pretend
    otherwise.

**Opt-in ownership preservation:** add a `--preserve-ownership` flag to `k8tc`.
When set, use `tar --same-owner --numeric-owner -xpf - ...` on the extract side.
This only does anything useful when the extracting end is privileged (root in
the container, or root locally); otherwise it degrades to the same best-effort
behavior. Document this clearly so users aren't surprised when UIDs don't carry
across into a rootless target.

**What actually hard-fails** is unrelated to ownership: writing into a directory
you lack write permission for, or a restored directory mode that locks tar out
mid-extract. Those surface as `EPERM`/`EACCES` on the file ops themselves and
should be reported per-transfer (see Error Handling).

---

## TUI Behavior

### Layout

Two equal-width panels filling the terminal, a header line, and a footer/status
line.

```
┌─ LOCAL: /home/user/project ──┐┌─ POD nginx-abc:/var/www ──────┐
│ ..                           ││ ..                             │
│ > src/                       ││   index.html                   │
│   README.md                  ││   assets/                      │
│   go.mod                     ││                                │
│                              ││                                │
└──────────────────────────────┘└────────────────────────────────┘
 Tab: switch  ↑↓: move  ⏎: open  F5: copy  q: quit      [status...]
```

- The **focused** panel has a highlighted border; the cursor row is highlighted.
- Each panel maintains its own `cwd`, file list, cursor index, and scroll
  offset.

### Keybindings

| Key        | Action                                                        |
|------------|---------------------------------------------------------------|
| `Tab`      | Switch focus between local and remote panel                   |
| `↑` / `↓`  | Move cursor                                                    |
| `PgUp/PgDn`| Page cursor                                                   |
| `Enter`    | If dir: descend; if `..`: go up; if file: no-op (v1)          |
| `F5` / `c` | Copy highlighted entry from focused panel → other panel's cwd |
| `r`        | Refresh focused panel                                          |
| `q` / `Ctrl+C` | Quit                                                      |

### Async transfers

Transfers must not block the event loop. Use the Bubble Tea pattern:

- On `F5`, dispatch a `tea.Cmd` that runs the `Pull`/`Push` in a goroutine and
  returns a `transferDoneMsg{err}` (and intermediate `transferProgressMsg{n}`
  via a channel + `tea.Tick` or a custom message pump).
- While in flight, show progress/byte-count in the status line and disable
  further copy actions.
- On completion, refresh the destination panel and clear status.

---

## CLI

```
k8tc --pod <name> [--namespace <ns>] [--container <name>] [--remote-path <path>] [--local-path <path>]
```

- `--pod` (required for v1)
- `--namespace` / `-n` → passed through as `kubectl -n`
- `--container` / `-c` → passed through as `kubectl exec -c`; if omitted, let
  kubectl pick the default container
- `--remote-path` initial remote dir (default `/`)
- `--local-path` initial local dir (default `.`)
- `--preserve-ownership` attempt to restore owner UID/GID on extract
  (`--same-owner --numeric-owner`). Off by default; only effective when the
  extracting end is privileged. See "tar flags & ownership."

(Stretch: a pod picker if `--pod` is omitted, via `kubectl get pods -o json`.)

---

## Error Handling & Edge Cases

The agent must handle these explicitly, surfacing errors in the status line
rather than crashing:

1. **`kubectl` not found on PATH** → fail fast at startup with a clear message.
2. **`tar` missing in the pod** (distroless/scratch images) → detect the exec
   failure and show: "pod has no `tar`; cannot transfer." Do **not** hang.
3. **Multi-container pod with no `--container`** → kubectl will error; surface
   its message and hint to pass `-c`.
4. **Permission denied** on read (local or remote) → show per-transfer error,
   keep the UI alive.
5. **BusyBox `ls`** lacking `--full-time` → fall back gracefully (see Listing).
6. **Broken pipe / context cancel** mid-transfer → clean up both processes
   (`CommandContext` + `cmd.Wait()` on both ends; kill the partner on failure).
7. **Empty directories** and the root `/` (no `..`).
8. **Large files** → never read fully into memory; always stream.
9. **Spaces / special chars in paths** → pass paths as separate `exec.Command`
   args (no shell string interpolation); when piping two `exec.Cmd`s, do it in
   Go via pipes, not via a `sh -c "... | ..."` string.

---

## Suggested Build Order (milestones)

1. **Transfer interface + kubectl `List`.** CLI prints a remote `ls`. Verify
   parsing against a real pod.
2. **Local `List`.** Mirror the same `FileInfo` for the local FS.
3. **Static two-panel render** (Lip Gloss) with both panels populated, no
   interaction.
4. **Navigation:** focus switching, cursor movement, `Enter` to descend/ascend,
   scroll offset, refresh.
5. **`Pull` (pod → local)** synchronous first, then move it onto the async
   `tea.Cmd` pattern with a status line.
6. **`Push` (local → pod)** same shape as Pull.
7. **Progress reporting** via counting reader → status line.
8. **Edge-case hardening** from the list above (tar-missing, busybox ls,
   cancellation).
9. **Polish:** styling, help footer, `--namespace`/`--container` plumbing.

Milestones 1–6 are the usable prototype. 7–9 are the path to "done."

---

## Acceptance Criteria

- [ ] Launches with `k8tc --pod <p>` and shows local + remote panels.
- [ ] Tab switches focus; arrows + Enter navigate both filesystems.
- [ ] F5 copies the highlighted file **or directory** in the focused panel into
      the other panel's current directory.
- [ ] Transferred files retain original mtime and permission (mode) bits
      (verify with `stat` on both ends). Owner UID/GID is best-effort: preserved
      only with `--preserve-ownership` against a privileged extract target.
- [ ] Directory transfers are recursive and also preserve metadata.
- [ ] A transfer of a large file does not freeze the UI and shows progress.
- [ ] Missing `tar` in the pod produces a clear error, not a hang or panic.
- [ ] Builds to a single static binary: `CGO_ENABLED=0 go build`.

---

## Future (post-v1, do not build now)

- Swap the kubectl-backed `Transfer` for a `client-go` implementation
  (exec via `remotecommand` SPDY) to drop the `kubectl` runtime dependency.
- Pod/namespace picker UI.
- Multi-select and queued transfers.
- File preview / view pane.
- Delete / rename / mkdir operations.
