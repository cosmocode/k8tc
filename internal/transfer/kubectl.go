package transfer

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Kubectl is a Transfer implementation that shells out to the user's kubectl
// binary. Whole files are never buffered in memory: directories are streamed as
// a tar archive over the kubectl exec stdio.
type Kubectl struct {
	// Namespace is passed through as `kubectl -n`. Empty means kubectl's
	// default namespace.
	Namespace string
	// PreserveOwnership, when set, asks tar to restore owner UID/GID on
	// extract (--same-owner --numeric-owner). Only effective when the
	// extracting end is privileged; see PLAN.md "tar flags & ownership".
	PreserveOwnership bool
	// Bin overrides the kubectl executable. Empty means "kubectl" on PATH.
	Bin string
}

func (k *Kubectl) bin() string {
	if k.Bin != "" {
		return k.Bin
	}
	return "kubectl"
}

// execCmd builds `kubectl [-n ns] exec [-i] <pod> [-c <container>] -- <remoteCmd...>`.
func (k *Kubectl) execCmd(stdin bool, pod, container string, remoteCmd ...string) *exec.Cmd {
	args := []string{}
	if k.Namespace != "" {
		args = append(args, "-n", k.Namespace)
	}
	args = append(args, "exec")
	if stdin {
		args = append(args, "-i")
	}
	args = append(args, pod)
	if container != "" {
		args = append(args, "-c", container)
	}
	args = append(args, "--")
	args = append(args, remoteCmd...)
	return exec.Command(k.bin(), args...)
}

// List runs `ls -la --full-time` in the pod and parses it. If --full-time is
// not understood (BusyBox), it falls back to plain `ls -la` and accepts coarser
// timestamps rather than erroring out.
func (k *Kubectl) List(pod, container, p string) ([]FileInfo, error) {
	out, err := k.runLS(pod, container, p, true)
	if err != nil {
		// Could be BusyBox (no --full-time) or a genuine error (bad path).
		// Retry plain; if that also fails it's genuine and we surface it.
		out, err = k.runLS(pod, container, p, false)
		if err != nil {
			return nil, err
		}
		return parseLS(out, p, false), nil
	}
	return parseLS(out, p, true), nil
}

func (k *Kubectl) runLS(pod, container, p string, fullTime bool) (string, error) {
	cmdArgs := []string{"ls", "-la"}
	if fullTime {
		cmdArgs = append(cmdArgs, "--full-time")
	}
	cmdArgs = append(cmdArgs, p)
	cmd := k.execCmd(false, pod, container, cmdArgs...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			return "", fmt.Errorf("%s", msg)
		}
		return "", err
	}
	return out.String(), nil
}

// parseLS turns `ls -la` output into FileInfo. With fullTime the timestamp is
// parsed from the ISO columns; otherwise ModTime is left zero. A synthesized
// ".." entry is prepended unless dir is the root.
func parseLS(out, dir string, fullTime bool) []FileInfo {
	var files []FileInfo
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "total ") {
			continue
		}
		fields := strings.Fields(line)
		// mode links owner group size <date columns...> name
		// Both `--full-time` and plain `ls -la` put the name at field index 8.
		if len(fields) < 9 || len(fields[0]) < 10 {
			continue
		}
		mode := fields[0]
		size, _ := strconv.ParseInt(fields[4], 10, 64)
		name := strings.Join(fields[8:], " ")

		var mtime time.Time
		if fullTime {
			// e.g. "2024-01-15 10:30:00.123456789 +0000"
			mtime, _ = time.Parse("2006-01-02 15:04:05.999999999 -0700",
				fields[5]+" "+fields[6]+" "+fields[7])
		}

		// Strip the "-> target" suffix from symlink listings.
		if mode[0] == 'l' {
			if i := strings.Index(name, " -> "); i >= 0 {
				name = name[:i]
			}
		}
		if name == "." || name == ".." {
			continue
		}
		files = append(files, FileInfo{
			Name:    name,
			Size:    size,
			Mode:    mode,
			IsDir:   mode[0] == 'd',
			ModTime: mtime,
		})
	}
	Sort(files)
	if path.Clean(dir) != "/" {
		files = append([]FileInfo{{Name: "..", Mode: "drwxr-xr-x", IsDir: true}}, files...)
	}
	return files
}

// Pull streams `kubectl exec -- tar c` from the pod into a local `tar x`.
func (k *Kubectl) Pull(pod, container, remotePath, localPath string, progress func(int64)) error {
	parent := path.Dir(remotePath)
	base := path.Base(remotePath)
	src := k.execCmd(false, pod, container, append([]string{"tar"}, createArgs(parent, base)...)...)
	dst := exec.Command("tar", extractArgs(k.PreserveOwnership, localPath)...)
	return pipe(src, dst, progress)
}

// Push streams a local `tar c` into `kubectl exec -i -- tar x` in the pod.
func (k *Kubectl) Push(pod, container, localPath, remotePath string, progress func(int64)) error {
	parent := filepath.Dir(localPath)
	base := filepath.Base(localPath)
	src := exec.Command("tar", createArgs(parent, base)...)
	dst := k.execCmd(true, pod, container, append([]string{"tar"}, extractArgs(k.PreserveOwnership, remotePath)...)...)
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
