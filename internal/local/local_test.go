package local

import (
	"os"
	"path/filepath"
	"testing"
)

func TestList(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "zsub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "asub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "readme.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Expected: "..", then dirs alpha (asub, zsub), then files (readme.md).
	want := []string{"..", "asub", "zsub", "readme.md"}
	if len(files) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(files), len(want), files)
	}
	for i, name := range want {
		if files[i].Name != name {
			t.Errorf("entry %d = %q, want %q", i, files[i].Name, name)
		}
	}
	// Mode strings should look like ls output.
	if files[1].Mode == "" || files[1].Mode[0] != 'd' {
		t.Errorf("dir mode = %q, want leading 'd'", files[1].Mode)
	}
}

func TestListRootNoDotDot(t *testing.T) {
	files, err := List("/")
	if err != nil {
		t.Fatalf("List(/): %v", err)
	}
	for _, f := range files {
		if f.Name == ".." {
			t.Fatalf("root listing should not contain '..'")
		}
	}
}
