package kube

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// PodInfo names one pod and the containers declared in its spec, in spec order.
// It is what the picker needs to offer a pod (and, when there is more than one,
// a container) to browse.
type PodInfo struct {
	Name       string
	Containers []string
}

// Discovery runs read-only `kubectl get`/`kubectl config` commands to enumerate
// what a session could target. It is the discovery counterpart to Client: where
// Client is bound to one pod and execs inside it, Discovery is unbound and only
// reads cluster/kubeconfig metadata, so it lives as its own type rather than on
// Client.
type Discovery struct {
	// Bin overrides the kubectl executable. Empty means "kubectl" on PATH.
	Bin string
}

func (d *Discovery) bin() string {
	if d.Bin != "" {
		return d.Bin
	}
	return "kubectl"
}

// cmd builds `kubectl [--context X] <args...>` bound to ctx, so a slow listing
// is killed when the (Go) context is canceled. The kube context is per-call
// (not bound on Discovery) because the picker switches contexts as the user
// drills, so each listing names the context it wants. Unlike Client.Exec these
// are top-level kubectl subcommands, not `exec` into a pod.
func (d *Discovery) cmd(ctx context.Context, kubeCtx string, args ...string) *exec.Cmd {
	if kubeCtx != "" {
		args = append([]string{"--context", kubeCtx}, args...)
	}
	return exec.CommandContext(ctx, d.bin(), args...)
}

// namespacesArgs / podsJSONPath / podsArgs are split out from the methods so the
// exact kubectl invocation can be asserted in tests without shelling out, the
// way TestClientExecArgs checks Exec.
func contextsArgs() []string       { return []string{"config", "get-contexts", "-o", "name"} }
func currentContextArgs() []string { return []string{"config", "current-context"} }
func namespacesArgs() []string     { return []string{"get", "namespaces", "-o", "name"} }

// podsJSONPath emits one line per pod: "<name>\t<c1> <c2> …". It is a raw string
// so the \t and \n reach kubectl's jsonpath parser literally (it, not Go,
// expands them).
const podsJSONPath = `{range .items[*]}{.metadata.name}{"\t"}{range .spec.containers[*]}{.name}{" "}{end}{"\n"}{end}`

func podsArgs(namespace string) []string {
	args := []string{"get", "pods"}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	return append(args, "-o", "jsonpath="+podsJSONPath)
}

// Contexts lists the context names in the user's kubeconfig (`kubectl config
// get-contexts -o name`). It reads only local kubeconfig — no cluster calls —
// so the picker can offer a context to choose before touching any cluster.
func (d *Discovery) Contexts(ctx context.Context) ([]string, error) {
	out, err := d.run(ctx, "", contextsArgs()...)
	if err != nil {
		return nil, err
	}
	return parseNames(out, ""), nil
}

// CurrentContext returns kubeconfig's current context name (`kubectl config
// current-context`), so the picker can preselect it. It errors if no current
// context is set, which the picker treats as "no preselection".
func (d *Discovery) CurrentContext(ctx context.Context) (string, error) {
	out, err := d.run(ctx, "", currentContextArgs()...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Namespaces lists kubeCtx's namespaces (empty kubeCtx means the current
// context). It runs `kubectl get namespaces -o name`, which needs cluster-wide
// list permission; on a restricted cluster it returns the kubectl error, which
// the picker treats as "fall back to typing".
func (d *Discovery) Namespaces(ctx context.Context, kubeCtx string) ([]string, error) {
	out, err := d.run(ctx, kubeCtx, namespacesArgs()...)
	if err != nil {
		return nil, err
	}
	return parseNames(out, "namespace/"), nil
}

// Pods lists the pods in kubeCtx's namespace and the containers each declares.
func (d *Discovery) Pods(ctx context.Context, kubeCtx, namespace string) ([]PodInfo, error) {
	out, err := d.run(ctx, kubeCtx, podsArgs(namespace)...)
	if err != nil {
		return nil, err
	}
	return parsePods(out), nil
}

// KubeconfigNamespaces returns the namespace configured for kubeCtx (or the
// current context, if kubeCtx is empty) as a one-element list — or empty if that
// context sets none. It reads only local kubeconfig (no cluster calls), so it
// works even where Namespaces is forbidden, which is exactly when the picker
// uses it to prefill the typed-namespace fallback. Only this context's namespace
// is offered: namespaces from other contexts belong to other clusters and would
// be irrelevant (or misleading) suggestions here.
func (d *Discovery) KubeconfigNamespaces(ctx context.Context, kubeCtx string) ([]string, error) {
	// `--minify` reduces the view to kubeCtx (or the current context), so this
	// jsonpath yields just that context's namespace.
	out, err := d.run(ctx, kubeCtx, "config", "view", "--minify", "-o", "jsonpath={..namespace}")
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// run executes a kubectl command in kubeCtx (empty means the current context),
// returning stdout or a trimmed-stderr error, mirroring remote.Lister.run.
// Canceling ctx kills the process.
func (d *Discovery) run(ctx context.Context, kubeCtx string, args ...string) (string, error) {
	cmd := d.cmd(ctx, kubeCtx, args...)
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

// parseNames splits `-o name` output ("<kind>/<name>" per line) into bare names,
// stripping prefix and skipping blanks.
func parseNames(out, prefix string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		names = append(names, strings.TrimPrefix(line, prefix))
	}
	return names
}

// parsePods turns the podsJSONPath output into PodInfo. Each line is
// "<name>\t<space-separated containers>"; a line with no tab (a pod with no
// listed containers) yields a PodInfo with no containers.
func parsePods(out string) []PodInfo {
	var pods []PodInfo
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		name, rest, _ := strings.Cut(line, "\t")
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		var containers []string
		if cs := strings.Fields(rest); len(cs) > 0 {
			containers = cs
		}
		pods = append(pods, PodInfo{Name: name, Containers: containers})
	}
	return pods
}
