// Package transfer moves files and directory trees between local disk and a
// pod, streaming them as a tar archive so whole files are never buffered in
// memory. The pod end of each stream is reached through a kube.Client; the
// local end is a local tar process.
package transfer

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/cosmocode/k8tc/internal/kube"
)

// Manager streams tar archives between local disk and a pod.
type Manager struct {
	// Client reaches the pod end of each transfer.
	Client *kube.Client
	// PreserveOwnership asks the extracting tar to restore owner UID/GID
	// (--same-owner --numeric-owner). Only effective when the extracting end is
	// privileged; see PLAN.md "tar flags & ownership".
	PreserveOwnership bool
}

// Pull streams `kubectl exec -- tar c` from the pod into a local `tar x`,
// copying remotePath (file or dir) into the directory localPath. progress is
// called with the cumulative number of bytes streamed so far.
func (m *Manager) Pull(remotePath, localPath string, progress func(int64)) error {
	parent := path.Dir(remotePath)
	base := path.Base(remotePath)
	src := m.Client.Exec(false, append([]string{"tar"}, createArgs(parent, base)...)...)
	dst := exec.Command("tar", extractArgs(m.PreserveOwnership, localPath)...)
	return pipe(src, dst, progress)
}

// Push streams a local `tar c` into `kubectl exec -i -- tar x` in the pod,
// copying localPath (file or dir) into the directory remotePath. progress is
// called with the cumulative number of bytes streamed so far.
func (m *Manager) Push(localPath, remotePath string, progress func(int64)) error {
	parent := filepath.Dir(localPath)
	base := filepath.Base(localPath)
	src := exec.Command("tar", createArgs(parent, base)...)
	dst := m.Client.Exec(true, append([]string{"tar"}, extractArgs(m.PreserveOwnership, remotePath)...)...)
	return pipe(src, dst, progress)
}

// createArgs are the tar flags for the producing side. Numeric ownership is
// harmless when packing and avoids name-lookup surprises.
func createArgs(parent, base string) []string {
	return []string{"--numeric-owner", "-cf", "-", "-C", parent, base}
}

// extractArgs are the tar flags for the consuming side. By default ownership
// restore is disabled (-p still preserves mode + mtime). --preserve-ownership
// opts into --same-owner, only useful against a privileged extract target.
func extractArgs(preserveOwnership bool, dest string) []string {
	args := []string{"-xpf", "-"}
	if preserveOwnership {
		args = append(args, "--same-owner", "--numeric-owner")
	} else {
		args = append(args, "--no-same-owner")
	}
	return append(args, "-C", dest)
}

// pipe wires src.stdout -> (counting reader) -> dst.stdin and runs both,
// returning the first meaningful error. It never buffers the stream in memory
// and cleans up the partner process if one side fails.
func pipe(src, dst *exec.Cmd, progress func(int64)) error {
	var srcErrBuf, dstErrBuf bytes.Buffer
	src.Stderr = &srcErrBuf
	dst.Stderr = &dstErrBuf

	pr, pw := io.Pipe()
	src.Stdout = pw
	dst.Stdin = &countingReader{r: pr, progress: progress}

	if err := dst.Start(); err != nil {
		return fmt.Errorf("starting consumer: %w", err)
	}
	if err := src.Start(); err != nil {
		pw.CloseWithError(err)
		_ = dst.Wait()
		return fmt.Errorf("starting producer: %w", err)
	}

	srcDone := make(chan error, 1)
	go func() {
		e := src.Wait()
		// Closing the writer signals EOF to the consumer (or propagates the
		// producer's failure as a read error).
		pw.CloseWithError(e)
		srcDone <- e
	}()

	dstErr := dst.Wait()
	if dstErr != nil && src.Process != nil {
		// Consumer died early; kill the producer so it doesn't block writing
		// into a pipe nobody is reading.
		_ = src.Process.Kill()
	}
	pr.CloseWithError(io.ErrClosedPipe)
	srcErr := <-srcDone

	// Attribute the failure. A producer we had to SIGKILL is a symptom of the
	// consumer dying, not the root cause, so prefer the consumer's error (and
	// its stderr) in that case. Otherwise the producer's failure — e.g. a
	// missing tar on the pull side — is the real cause.
	switch {
	case srcErr != nil && !killedBySignal(srcErr):
		return classify(srcErr, srcErrBuf.String())
	case dstErr != nil:
		return classify(dstErr, dstErrBuf.String())
	case srcErr != nil:
		return classify(srcErr, srcErrBuf.String())
	}
	return nil
}

// killedBySignal reports whether err is an exit caused by a signal (i.e. the
// process we Kill()ed), as opposed to the process exiting on its own.
func killedBySignal(err error) bool {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
			return ws.Signaled()
		}
	}
	return false
}

// classify turns a process failure into a user-facing error, special-casing a
// missing tar in the pod (distroless/scratch images).
func classify(err error, stderr string) error {
	stderr = strings.TrimSpace(stderr)
	low := strings.ToLower(stderr)
	if (strings.Contains(low, "tar") && strings.Contains(low, "executable file not found")) ||
		strings.Contains(low, "tar: not found") ||
		(strings.Contains(low, "tar") && strings.Contains(low, "command not found")) {
		return errors.New("pod has no `tar`; cannot transfer")
	}
	if stderr != "" {
		return fmt.Errorf("%v: %s", err, stderr)
	}
	return err
}

// countingReader reports the running byte total through a callback as data
// flows through the pipe.
type countingReader struct {
	r        io.Reader
	total    int64
	progress func(int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.total += int64(n)
		if c.progress != nil {
			c.progress(c.total)
		}
	}
	return n, err
}
