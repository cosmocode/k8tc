// Package local browses the local filesystem, producing the same file.Info the
// remote panel uses so both panels render identically. It is the local
// counterpart to internal/remote.
package local

import (
	"context"
	"os"
	"path/filepath"

	"github.com/cosmocode/k8tc/internal/file"
)

// FS is a lister over the local filesystem. It carries no state; it exists so
// the local side satisfies the same List interface as the remote side.
type FS struct{}

// List implements the panel lister for local directories.
func (FS) List(dir string) ([]file.Info, error) { return List(dir) }

// Delete removes the file or directory tree at p, recursively. It is the local
// counterpart to remote deletion. The context is accepted for interface
// symmetry with the remote side; a local remove runs to completion and is not
// cancelable.
func (FS) Delete(_ context.Context, p string) error { return os.RemoveAll(p) }

// Mkdir creates a single directory at p. It errors if p already exists or its
// parent does not (os.Mkdir, not MkdirAll), so the user gets a clear message
// rather than silently creating intermediate dirs. The context is accepted for
// interface symmetry with the remote side; a local mkdir is not cancelable.
func (FS) Mkdir(_ context.Context, p string) error { return os.Mkdir(p, 0o755) }

// List returns the entries of dir as file.Info, sorted dirs-first, with a
// synthesized ".." prepended unless dir is the filesystem root.
func List(dir string) ([]file.Info, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := make([]file.Info, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			// Entry vanished between ReadDir and Info, or is unreadable; skip
			// it rather than failing the whole listing.
			continue
		}
		files = append(files, file.Info{
			Name:    info.Name(),
			Size:    info.Size(),
			Mode:    info.Mode().String(),
			IsDir:   info.IsDir(),
			ModTime: info.ModTime(),
		})
	}
	file.Sort(files)
	if filepath.Clean(dir) != string(filepath.Separator) {
		files = append([]file.Info{{Name: "..", Mode: "drwxr-xr-x", IsDir: true}}, files...)
	}
	return files, nil
}
