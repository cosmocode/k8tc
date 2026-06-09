// Package remote is the pod side of the two-panel view. It lists a directory
// inside the pod by running `ls` through a kube.Client and parsing the output
// into file.Info — the same representation the local side produces, so both
// panels render identically. It is the remote counterpart to internal/local.
package remote

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/cosmocode/k8tc/internal/file"
	"github.com/cosmocode/k8tc/internal/kube"
)

// Lister lists directories inside a pod via its kube.Client.
type Lister struct {
	Client *kube.Client
}

// List runs `ls -la --full-time` in the pod and parses it. If --full-time is
// not understood (BusyBox), it falls back to plain `ls -la` and accepts coarser
// timestamps rather than erroring out.
func (l *Lister) List(p string) ([]file.Info, error) {
	out, err := l.runLS(p, true)
	if err != nil {
		// Could be BusyBox (no --full-time) or a genuine error (bad path).
		// Retry plain; if that also fails it's genuine and we surface it.
		out, err = l.runLS(p, false)
		if err != nil {
			return nil, err
		}
		return parseLS(out, p, false), nil
	}
	return parseLS(out, p, true), nil
}

// Delete removes the file or directory tree at p inside the pod, recursively,
// by running `rm -rf -- <p>`. The `--` guards entries whose names begin with a
// dash. Canceling ctx kills the kubectl process, which is how an in-flight
// delete is aborted.
func (l *Lister) Delete(ctx context.Context, p string) error {
	return l.run(ctx, "rm", "-rf", "--", p)
}

// Mkdir creates a single directory at p inside the pod by running
// `mkdir -- <p>` (no `-p`), so it errors if p already exists or its parent does
// not. The `--` guards names that begin with a dash. Canceling ctx kills the
// kubectl process.
func (l *Lister) Mkdir(ctx context.Context, p string) error {
	return l.run(ctx, "mkdir", "--", p)
}

// Paths is the pod side's path-string semantics: POSIX separators, via the path
// package, since the pod is Linux. It is stateless and kept separate from
// Lister so this pure path math stays untangled from the kube I/O — the UI
// selects it for the remote panel. It is the remote counterpart to local.Paths.
type Paths struct{}

func (Paths) Join(dir, name string) string { return path.Join(dir, name) }
func (Paths) Dir(p string) string          { return path.Dir(p) }
func (Paths) Base(p string) string         { return path.Base(p) }

// run executes a pod command whose stdout we don't need, surfacing a trimmed
// stderr message on failure. Canceling ctx kills the kubectl process.
func (l *Lister) run(ctx context.Context, args ...string) error {
	cmd := l.Client.ExecContext(ctx, false, args...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			return fmt.Errorf("%s", msg)
		}
		return err
	}
	return nil
}

func (l *Lister) runLS(p string, fullTime bool) (string, error) {
	cmdArgs := []string{"ls", "-la"}
	if fullTime {
		cmdArgs = append(cmdArgs, "--full-time")
	}
	cmdArgs = append(cmdArgs, p)
	cmd := l.Client.Exec(false, cmdArgs...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			return "", fmt.Errorf("%s", msg)
		}
		return "", err
	}
	return out.String(), nil
}
