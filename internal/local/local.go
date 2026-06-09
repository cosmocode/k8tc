// Package local browses the local filesystem, producing the same FileInfo the
// remote panel uses so both panels render identically.
package local

import (
	"os"
	"path/filepath"

	"github.com/cosmocode/k8tc/internal/transfer"
)

// List returns the entries of dir as transfer.FileInfo, sorted dirs-first, with
// a synthesized ".." prepended unless dir is the filesystem root.
func List(dir string) ([]transfer.FileInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := make([]transfer.FileInfo, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			// Entry vanished between ReadDir and Info, or is unreadable; skip
			// it rather than failing the whole listing.
			continue
		}
		files = append(files, transfer.FileInfo{
			Name:    info.Name(),
			Size:    info.Size(),
			Mode:    info.Mode().String(),
			IsDir:   info.IsDir(),
			ModTime: info.ModTime(),
		})
	}
	transfer.Sort(files)
	if filepath.Clean(dir) != string(filepath.Separator) {
		files = append([]transfer.FileInfo{{Name: "..", Mode: "drwxr-xr-x", IsDir: true}}, files...)
	}
	return files, nil
}
