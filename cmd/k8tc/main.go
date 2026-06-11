// Command k8tc is a two-panel TUI for browsing the local filesystem and a
// Kubernetes pod's filesystem side by side and transferring files between them.
// Transfers stream tar over `kubectl exec`, preserving mode and mtime.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cosmocode/k8tc/internal/kube"
	"github.com/cosmocode/k8tc/internal/local"
	"github.com/cosmocode/k8tc/internal/picker"
	"github.com/cosmocode/k8tc/internal/remote"
	"github.com/cosmocode/k8tc/internal/transfer"
	"github.com/cosmocode/k8tc/internal/ui"
)

func main() {
	var (
		kubeContext string
		pod         string
		namespace   string
		container   string
		remotePath  string
		localPath   string
		preserve    bool
	)

	flag.StringVar(&kubeContext, "context", "", "kube context (kubectl --context); default current")
	flag.StringVar(&pod, "pod", "", "pod name (omit to pick interactively)")
	flag.StringVar(&namespace, "namespace", "", "namespace (kubectl -n)")
	flag.StringVar(&namespace, "n", "", "namespace (shorthand)")
	flag.StringVar(&container, "container", "", "container name (kubectl exec -c)")
	flag.StringVar(&container, "c", "", "container (shorthand)")
	flag.StringVar(&remotePath, "remote-path", "/", "initial remote directory")
	flag.StringVar(&localPath, "local-path", ".", "initial local directory")
	flag.BoolVar(&preserve, "preserve-ownership", false,
		"attempt to restore owner UID/GID on extract (--same-owner --numeric-owner); "+
			"only effective against a privileged extract target")
	flag.Parse()

	// Fail fast if kubectl is missing rather than surfacing it on first list.
	if _, err := exec.LookPath("kubectl"); err != nil {
		fmt.Fprintln(os.Stderr, "error: kubectl not found on PATH")
		os.Exit(1)
	}

	// With no --pod, open the interactive picker to choose the
	// namespace/pod/container. It runs as its own program and hands back the
	// target; --namespace, if given, seeds it past the namespace step. Quitting
	// the picker without a choice exits cleanly.
	if pod == "" {
		sel, ok, err := runPicker(kubeContext, namespace)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		if !ok {
			os.Exit(0)
		}
		kubeContext, namespace, pod, container = sel.Context, sel.Namespace, sel.Pod, sel.Container
	}

	if abs, err := filepath.Abs(localPath); err == nil {
		localPath = abs
	}

	// One kube.Client (pod/container bound for the session) backs both the
	// remote directory lister and the transfer manager.
	client := &kube.Client{Context: kubeContext, Namespace: namespace, Pod: pod, Container: container}
	remoteLister := &remote.Lister{Client: client}
	xfer := &transfer.Manager{Client: client, PreserveOwnership: preserve}

	remoteLabel := "POD " + pod
	if container != "" {
		remoteLabel += " [" + container + "]"
	}
	model := ui.New(local.FS{}, local.Paths{}, remoteLister, remote.Paths{}, xfer, remoteLabel, remotePath, localPath)

	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// runPicker runs the interactive target chooser and returns the selection. ok is
// false when the user quit without choosing; err is non-nil only on a Bubble Tea
// runtime failure. kubeContext (from --context) scopes discovery; seedNamespace
// (from -n) starts the picker on that namespace's pods.
func runPicker(kubeContext, seedNamespace string) (picker.Selection, bool, error) {
	p := tea.NewProgram(picker.New(&kube.Discovery{}, kubeContext, seedNamespace), tea.WithAltScreen())
	res, err := p.Run()
	if err != nil {
		return picker.Selection{}, false, err
	}
	sel, ok := res.(picker.Model).Result()
	return sel, ok, nil
}
