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
	"github.com/cosmocode/k8tc/internal/remote"
	"github.com/cosmocode/k8tc/internal/transfer"
	"github.com/cosmocode/k8tc/internal/ui"
)

func main() {
	var (
		pod        string
		namespace  string
		container  string
		remotePath string
		localPath  string
		preserve   bool
	)

	flag.StringVar(&pod, "pod", "", "pod name (required)")
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

	if pod == "" {
		fmt.Fprintln(os.Stderr, "error: --pod is required")
		flag.Usage()
		os.Exit(2)
	}

	// Fail fast if kubectl is missing rather than surfacing it on first list.
	if _, err := exec.LookPath("kubectl"); err != nil {
		fmt.Fprintln(os.Stderr, "error: kubectl not found on PATH")
		os.Exit(1)
	}

	if abs, err := filepath.Abs(localPath); err == nil {
		localPath = abs
	}

	// One kube.Client (pod/container bound for the session) backs both the
	// remote directory lister and the transfer manager.
	client := &kube.Client{Namespace: namespace, Pod: pod, Container: container}
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
