// Package remote is the pod side of the two-panel view. It lists a directory
// inside the pod by running `ls` through a kube.Client and parsing the output
// into file.Info — the same representation the local side produces, so both
// panels render identically. It is the remote counterpart to internal/local.
package remote

import (
	"bytes"
	"fmt"
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
