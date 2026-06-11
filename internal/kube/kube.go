// Package kube is the seam between k8tc and the cluster. A Client knows how to
// run a command inside one pod's container and hand back an *exec.Cmd wired to
// kubectl; everything above it — directory listing (internal/remote) and tar
// streaming (internal/transfer) — is built on this one primitive. Isolating it
// here is what lets a future client-go backend replace the kubectl shell-out
// without touching the code above.
package kube

import (
	"context"
	"os/exec"
)

// Client runs commands inside a single pod/container. Pod and Container are
// fixed for a session, so they are bound on the Client rather than passed to
// every call.
type Client struct {
	// Context is passed as `kubectl --context`. Empty means kubectl's current
	// context.
	Context string
	// Namespace is passed as `kubectl -n`. Empty means kubectl's default.
	Namespace string
	// Pod is the target pod name.
	Pod string
	// Container is the target container (`kubectl exec -c`). Empty means the
	// pod's default container.
	Container string
	// Bin overrides the kubectl executable. Empty means "kubectl" on PATH.
	Bin string
}

func (c *Client) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "kubectl"
}

// Exec builds `kubectl [-n ns] exec [-i] <pod> [-c <container>] -- <cmd...>`.
// Set stdin when the command needs to read from stdin (the -i flag), as a tar
// extract does on the receiving end of a push.
func (c *Client) Exec(stdin bool, cmd ...string) *exec.Cmd {
	return c.ExecContext(context.Background(), stdin, cmd...)
}

// ExecContext is Exec bound to a context, so the kubectl process is killed when
// the context is canceled — this is how an in-flight transfer is aborted.
func (c *Client) ExecContext(ctx context.Context, stdin bool, cmd ...string) *exec.Cmd {
	args := []string{}
	if c.Context != "" {
		args = append(args, "--context", c.Context)
	}
	if c.Namespace != "" {
		args = append(args, "-n", c.Namespace)
	}
	args = append(args, "exec")
	if stdin {
		args = append(args, "-i")
	}
	args = append(args, c.Pod)
	if c.Container != "" {
		args = append(args, "-c", c.Container)
	}
	args = append(args, "--")
	args = append(args, cmd...)
	return exec.CommandContext(ctx, c.bin(), args...)
}
