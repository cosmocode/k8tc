package transfer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/cosmocode/k8tc/internal/kube"
)

// fakeKubectl writes a shell script that mimics `kubectl exec`: it discards
// everything up to and including the `--` separator (namespace, pod, -c, -i)
// and runs the remaining command locally. The "pod filesystem" is therefore
// just the local filesystem, which lets us exercise real tar streaming end to
// end without a cluster.
func fakeKubectl(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar not available")
	}
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

func TestPullPreservesMetadata(t *testing.T) {
	m := &Manager{Client: &kube.Client{Bin: fakeKubectl(t), Pod: "pod"}}

	// "Remote" tree.
	remote := t.TempDir()
	srcDir := filepath.Join(remote, "data")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	srcFile := filepath.Join(srcDir, "payload.bin")
	content := make([]byte, 256*1024) // big enough to span many reads
	for i := range content {
		content[i] = byte(i)
	}
	if err := os.WriteFile(srcFile, content, 0o640); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2021, 6, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(srcFile, mtime, mtime); err != nil {
		t.Fatal(err)
	}

	localDest := t.TempDir()
	var lastN int64
	if err := m.Pull(context.Background(), srcDir, localDest, func(n int64) { lastN = n }); err != nil {
		t.Fatalf("Pull: %v", err)
	}

	out := filepath.Join(localDest, "data", "payload.bin")
	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("pulled file missing: %v", err)
	}
	if info.Size() != int64(len(content)) {
		t.Errorf("size = %d, want %d", info.Size(), len(content))
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want -rw-r-----", info.Mode().Perm())
	}
	if !info.ModTime().UTC().Truncate(time.Second).Equal(mtime) {
		t.Errorf("mtime = %v, want %v", info.ModTime().UTC(), mtime)
	}
	if lastN < int64(len(content)) {
		t.Errorf("progress total = %d, want >= %d", lastN, len(content))
	}
}

func TestPushPreservesMetadata(t *testing.T) {
	m := &Manager{Client: &kube.Client{Bin: fakeKubectl(t), Pod: "pod"}}

	localSrcDir := t.TempDir()
	dir := filepath.Join(localSrcDir, "app")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(f, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	remoteDest := t.TempDir()
	var lastN int64
	if err := m.Push(context.Background(), dir, remoteDest, func(n int64) { lastN = n }); err != nil {
		t.Fatalf("Push: %v", err)
	}

	out := filepath.Join(remoteDest, "app", "run.sh")
	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("pushed file missing: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want -rwxr-xr-x", info.Mode().Perm())
	}
	if lastN == 0 {
		t.Errorf("progress callback never fired")
	}
}

func TestPushCanceledContext(t *testing.T) {
	m := &Manager{Client: &kube.Client{Bin: fakeKubectl(t), Pod: "pod"}}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "x"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // canceled before the transfer starts: it must not succeed
	if err := m.Push(ctx, filepath.Join(src, "x"), t.TempDir(), func(int64) {}); err == nil {
		t.Error("Push with a canceled context should fail, got nil")
	}
}

func TestMissingTarReported(t *testing.T) {
	// A fake kubectl that always reports tar missing, like a distroless image.
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-kubectl")
	script := "#!/bin/sh\n" +
		"echo 'OCI runtime exec failed: exec: \"tar\": executable file not found in $PATH' 1>&2\n" +
		"exit 126\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	m := &Manager{Client: &kube.Client{Bin: bin, Pod: "pod"}}

	// Push: the pod side (consumer) is the one that fails.
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "x"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := m.Push(context.Background(), filepath.Join(src, "x"), "/dest", func(int64) {})
	if err == nil || err.Error() != "pod has no `tar`; cannot transfer" {
		t.Errorf("Push error = %v, want missing-tar message", err)
	}
}

func TestClassifyMissingTar(t *testing.T) {
	cases := []string{
		`OCI runtime exec failed: exec failed: unable to start container process: exec: "tar": executable file not found in $PATH: unknown`,
		`sh: tar: not found`,
		`tar: command not found`,
	}
	for _, stderr := range cases {
		err := classify(errExit{}, stderr)
		if err == nil || err.Error() != "pod has no `tar`; cannot transfer" {
			t.Errorf("classify(%q) = %v, want missing-tar message", stderr, err)
		}
	}
}

func TestClassifyOtherError(t *testing.T) {
	err := classify(errExit{}, "tar: /root/secret: Cannot open: Permission denied")
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); got == "pod has no `tar`; cannot transfer" {
		t.Errorf("permission error misclassified as missing tar")
	}
}

type errExit struct{}

func (errExit) Error() string { return "exit status 2" }
