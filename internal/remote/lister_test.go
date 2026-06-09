package remote

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/cosmocode/k8tc/internal/file"
	"github.com/cosmocode/k8tc/internal/kube"
)

// fakeKubectl writes a shell script that mimics `kubectl exec`: it discards
// everything up to and including the `--` separator (namespace, pod, -c, -i)
// and runs the remaining command locally. The "pod filesystem" is therefore
// just the local filesystem, which lets us exercise listing without a cluster.
func fakeKubectl(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-kubectl")
	script := "#!/bin/sh\n" +
		"while [ \"$1\" != \"--\" ] && [ $# -gt 0 ]; do shift; done\n" +
		"shift\n" +
		"exec \"$@\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestListerList(t *testing.T) {
	if _, err := exec.LookPath("ls"); err != nil {
		t.Skip("ls not available")
	}
	l := &Lister{Client: &kube.Client{Bin: fakeKubectl(t), Pod: "pod"}}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := l.List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := map[string]file.Info{}
	for _, f := range files {
		got[f.Name] = f
	}
	if _, ok := got[".."]; !ok {
		t.Errorf("expected synthesized '..' entry")
	}
	if d, ok := got["assets"]; !ok || !d.IsDir {
		t.Errorf("assets missing or not a dir: %+v", d)
	}
	if f, ok := got["index.html"]; !ok || f.IsDir || f.Size != 5 {
		t.Errorf("index.html wrong: %+v", f)
	}
}
