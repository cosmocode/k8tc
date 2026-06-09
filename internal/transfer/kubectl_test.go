package transfer

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
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

func TestKubectlList(t *testing.T) {
	k := &Kubectl{Bin: fakeKubectl(t)}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := k.List("pod", "", root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := map[string]FileInfo{}
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

func TestKubectlPullPreservesMetadata(t *testing.T) {
	k := &Kubectl{Bin: fakeKubectl(t)}

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
	if err := k.Pull("pod", "", srcDir, localDest, func(n int64) { lastN = n }); err != nil {
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

func TestKubectlPushPreservesMetadata(t *testing.T) {
	k := &Kubectl{Bin: fakeKubectl(t)}

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
	if err := k.Push("pod", "", dir, remoteDest, func(n int64) { lastN = n }); err != nil {
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

func TestKubectlMissingTarReported(t *testing.T) {
	// A fake kubectl that always reports tar missing, like a distroless image.
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-kubectl")
	script := "#!/bin/sh\n" +
		"echo 'OCI runtime exec failed: exec: \"tar\": executable file not found in $PATH' 1>&2\n" +
		"exit 126\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	k := &Kubectl{Bin: bin}

	// Push: the pod side (consumer) is the one that fails.
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "x"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := k.Push("pod", "", filepath.Join(src, "x"), "/dest", func(int64) {})
	if err == nil || err.Error() != "pod has no `tar`; cannot transfer" {
		t.Errorf("Push error = %v, want missing-tar message", err)
	}
}
