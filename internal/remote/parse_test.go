package remote

import (
	"testing"

	"github.com/cosmocode/k8tc/internal/file"
)

func TestParseLSFullTime(t *testing.T) {
	out := `total 12
drwxr-xr-x 3 root root 4096 2024-01-15 10:30:00.000000000 +0000 .
drwxr-xr-x 5 root root 4096 2024-01-15 10:00:00.000000000 +0000 ..
drwxr-xr-x 2 root root 4096 2024-01-15 10:31:00.123456789 +0000 assets
-rw-r----- 1 root root   12 2024-01-15 10:30:00.000000000 +0000 file.txt
-rw-r--r-- 1 root root    3 2024-01-15 10:30:00.000000000 +0000 my file.txt
lrwxrwxrwx 1 root root    7 2024-01-15 10:30:00.000000000 +0000 link -> target
`
	files := parseLS(out, "/var/www", true)

	// Expected order: ".." synthesized first, then dirs, then files alpha.
	want := []struct {
		name  string
		isDir bool
	}{
		{"..", true},
		{"assets", true},
		{"file.txt", false},
		{"link", false}, // symlink, target stripped
		{"my file.txt", false},
	}
	if len(files) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(files), len(want), files)
	}
	for i, w := range want {
		if files[i].Name != w.name || files[i].IsDir != w.isDir {
			t.Errorf("entry %d = {%q,%v}, want {%q,%v}", i, files[i].Name, files[i].IsDir, w.name, w.isDir)
		}
	}

	// Sizes and a parsed timestamp.
	var fileTxt file.Info
	for _, f := range files {
		if f.Name == "file.txt" {
			fileTxt = f
		}
	}
	if fileTxt.Size != 12 {
		t.Errorf("file.txt size = %d, want 12", fileTxt.Size)
	}
	if fileTxt.ModTime.IsZero() {
		t.Errorf("file.txt mtime not parsed")
	}
	if got := fileTxt.ModTime.UTC().Format("2006-01-02 15:04:05"); got != "2024-01-15 10:30:00" {
		t.Errorf("file.txt mtime = %s, want 2024-01-15 10:30:00", got)
	}
}

func TestParseLSBusyBox(t *testing.T) {
	// BusyBox `ls -la` (no --full-time): coarse date, name at field 8.
	out := `total 8
drwxr-xr-x    2 root     root          4096 Jan  1 00:00 .
drwxr-xr-x    3 root     root          4096 Jan  1 00:00 ..
drwxr-xr-x    2 1000     1000          4096 Jan  1 00:00 sub
-rw-r--r--    1 root     root            12 Jan  1 00:00 index.html
`
	files := parseLS(out, "/srv", false)
	if len(files) != 3 { // "..", sub, index.html
		t.Fatalf("got %d entries, want 3: %+v", len(files), files)
	}
	if files[0].Name != ".." || files[1].Name != "sub" || !files[1].IsDir {
		t.Errorf("unexpected ordering: %+v", files)
	}
	if files[2].Name != "index.html" || files[2].Size != 12 {
		t.Errorf("file parse wrong: %+v", files[2])
	}
	// No timestamp available without --full-time; should be zero, not garbage.
	if !files[2].ModTime.IsZero() {
		t.Errorf("expected zero mtime for busybox listing, got %v", files[2].ModTime)
	}
}

func TestParseLSRootHasNoDotDot(t *testing.T) {
	out := `total 0
drwxr-xr-x 2 root root 4096 2024-01-15 10:30:00.000000000 +0000 .
drwxr-xr-x 2 root root 4096 2024-01-15 10:30:00.000000000 +0000 ..
drwxr-xr-x 2 root root 4096 2024-01-15 10:30:00.000000000 +0000 etc
`
	files := parseLS(out, "/", true)
	for _, f := range files {
		if f.Name == ".." {
			t.Fatalf("root listing should not contain '..': %+v", files)
		}
	}
	if len(files) != 1 || files[0].Name != "etc" {
		t.Fatalf("got %+v, want just [etc]", files)
	}
}
