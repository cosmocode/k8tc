// Package transfer defines the abstraction used to talk to a remote pod's
// filesystem and the kubectl-backed implementation of it. The interface exists
// so the kubectl implementation can later be swapped for a client-go one
// without touching the TUI.
package transfer

import (
	"sort"
	"strings"
	"time"
)

// FileInfo is the metadata k8tc displays and acts on for a single directory
// entry, on either the local or the remote side.
type FileInfo struct {
	Name    string
	Size    int64
	Mode    string // e.g. "drwxr-xr-x"
	IsDir   bool
	ModTime time.Time
}

// Transfer abstracts the remote (pod) filesystem. The local filesystem panel
// does not go through this interface; see internal/local.
type Transfer interface {
	// List returns directory contents at path inside the pod.
	List(pod, container, path string) ([]FileInfo, error)
	// Pull copies remotePath (file or dir) from the pod into the directory
	// localPath, preserving metadata. progress is called with the cumulative
	// number of bytes streamed so far.
	Pull(pod, container, remotePath, localPath string, progress func(n int64)) error
	// Push copies localPath (file or dir) into the pod at directory remotePath,
	// preserving metadata. progress is called with the cumulative number of
	// bytes streamed so far.
	Push(pod, container, localPath, remotePath string, progress func(n int64)) error
}

// Sort orders entries the way both panels display them: directories first,
// then files, each group case-insensitively by name.
func Sort(files []FileInfo) {
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].IsDir != files[j].IsDir {
			return files[i].IsDir
		}
		return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
	})
}
