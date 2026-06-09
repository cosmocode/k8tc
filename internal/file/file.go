// Package file defines the directory-entry representation shared by both
// panels: the metadata k8tc displays for an entry and the order it displays
// entries in. It is imported by both internal/transfer (the remote side) and
// internal/local so the two panels render identically.
package file

import (
	"sort"
	"strings"
	"time"
)

// Info is the metadata k8tc displays and acts on for a single directory entry,
// on either the local or the remote side.
type Info struct {
	Name    string
	Size    int64
	Mode    string // e.g. "drwxr-xr-x"
	IsDir   bool
	ModTime time.Time
}

// Sort orders entries the way both panels display them: directories first,
// then files, each group case-insensitively by name.
func Sort(files []Info) {
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].IsDir != files[j].IsDir {
			return files[i].IsDir
		}
		return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
	})
}
