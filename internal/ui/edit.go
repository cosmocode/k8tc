package ui

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// editState tracks an in-progress edit. The file is fetched into a private temp
// directory, opened in $EDITOR there, and — only if its mtime changed — written
// back to where it came from. side/dir/name locate the original; editor is the
// $EDITOR value captured when the edit started; tmpDir/tmpFile are the working
// copy; origMod is the working copy's mtime right after the fetch, the baseline
// a save is detected against. Unlike copy and delete it is not a batch: it is a
// single file with no queue and nothing to abort.
type editState struct {
	side    focus  // panel the file lives on
	dir     string // its directory (side panel's cwd)
	name    string // its entry name
	editor  string // $EDITOR value, captured at launch
	tmpDir  string // temp directory holding the working copy
	tmpFile string // full path to the working copy (tmpDir/name)

	origMod time.Time // working copy's mtime after the fetch
}

// editFetchedMsg reports the result of fetching a file into its temp directory.
// On success it carries the fully-populated editState (temp paths and baseline
// mtime filled in) so the model can launch the editor against it.
type editFetchedMsg struct {
	state editState
	err   error
}

// editorFinishedMsg reports that $EDITOR exited; err is non-nil if it failed to
// launch or returned a nonzero status.
type editorFinishedMsg struct{ err error }

// editSentMsg reports the result of writing an edited file back to its origin.
type editSentMsg struct{ err error }

// handleEdit opens the focused panel's highlighted file in $EDITOR. Directories
// and ".." aren't editable, and with no $EDITOR set there's nothing to launch —
// in either case it just reports why. Otherwise it records the edit and kicks
// off the fetch; the editor opens once the working copy is ready.
func (m Model) handleEdit() (tea.Model, tea.Cmd) {
	p := m.focusedPanel()
	sel := p.selected()
	if sel == nil || sel.IsDir || sel.Name == ".." {
		return m, nil // only regular files are editable
	}
	editor := strings.TrimSpace(os.Getenv("EDITOR"))
	if editor == "" {
		m.status = "$EDITOR is not set."
		m.statusErr = true
		return m, nil
	}
	m.edit = editState{side: m.focus, dir: p.cwd, name: sel.Name, editor: editor}
	m.mode = modeEdit
	m.status = "Fetching " + sel.Name + "…"
	m.statusErr = false
	return m, m.fetchForEditCmd(m.edit)
}

// handleEditKey swallows input while an edit is in flight. The only moments the
// panel is on screen during modeEdit are the brief fetch and write-back windows
// — the editor owns the terminal in between — and letting keys through then
// could navigate away or interrupt a write-back mid-stream. Ctrl+C still quits,
// so a stalled pod fetch can't lock the user out.
func (m Model) handleEditKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		m.quitting = true
		return m, tea.Quit
	}
	return m, nil
}

// fetchForEditCmd copies the file into a fresh temp directory — Pull from the
// pod, or a plain copy locally — and records its mtime as the change baseline.
// The working copy keeps the original name inside the temp dir so the editor
// shows the real filename and the write-back lands on the right name. On any
// failure the temp dir is removed so a half-fetched edit leaves nothing behind.
func (m Model) fetchForEditCmd(es editState) tea.Cmd {
	srcFull := m.pathsFor(es.side).Join(es.dir, es.name)
	isRemote := es.side == focusRemote
	xfer := m.xfer
	return func() tea.Msg {
		tmpDir, err := os.MkdirTemp("", "k8tc-edit-")
		if err != nil {
			return editFetchedMsg{err: err}
		}
		tmpFile := filepath.Join(tmpDir, es.name)
		if isRemote {
			err = xfer.Pull(context.Background(), srcFull, tmpDir, func(int64) {})
		} else {
			err = copyFile(srcFull, tmpFile)
		}
		if err == nil {
			es.origMod, err = modTime(tmpFile)
		}
		if err != nil {
			os.RemoveAll(tmpDir)
			return editFetchedMsg{err: err}
		}
		es.tmpDir, es.tmpFile = tmpDir, tmpFile
		return editFetchedMsg{state: es}
	}
}

// runEditorCmd launches $EDITOR on the working copy, releasing the terminal for
// the duration via tea.ExecProcess. $EDITOR may carry flags (e.g. "code
// --wait"), so it is split on whitespace; the temp file is appended last, so a
// filename with spaces is still passed as a single argument. An editor whose
// own path contains spaces is not supported.
func (m Model) runEditorCmd() tea.Cmd {
	fields := strings.Fields(m.edit.editor)
	args := append(fields[1:], m.edit.tmpFile)
	c := exec.Command(fields[0], args...)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return editorFinishedMsg{err: err}
	})
}

// afterEditor runs once $EDITOR exits. If the working copy's mtime advanced the
// edits are written back; an unchanged file is left untouched and the temp dir
// discarded. A successful write-back hands cleanup to afterEditSend.
func (m Model) afterEditor(msg editorFinishedMsg) (tea.Model, tea.Cmd) {
	es := m.edit
	if msg.err != nil {
		return m.discardEdit("Editor failed: " + msg.err.Error())
	}
	newMod, err := modTime(es.tmpFile)
	switch {
	case err != nil:
		return m.discardEdit("Could not read edited file: " + err.Error())
	case newMod.Equal(es.origMod):
		return m.discardEditOK(es.name + " unchanged.")
	}
	m.status = "Saving " + es.name + "…"
	m.statusErr = false
	return m, m.sendBackCmd(es)
}

// sendBackCmd writes the edited working copy back to where it came from — Push
// to the pod, or a plain copy locally — preserving the original name. The temp
// dir is removed once the write-back returns, whether or not it succeeded.
func (m Model) sendBackCmd(es editState) tea.Cmd {
	destFull := m.pathsFor(es.side).Join(es.dir, es.name)
	isRemote := es.side == focusRemote
	xfer := m.xfer
	return func() tea.Msg {
		var err error
		if isRemote {
			err = xfer.Push(context.Background(), es.tmpFile, es.dir, func(int64) {})
		} else {
			err = copyFile(es.tmpFile, destFull)
		}
		os.RemoveAll(es.tmpDir)
		return editSentMsg{err: err}
	}
}

// afterEditSend reports the write-back result and reloads the panel so the
// file's new size and mtime show, keeping the cursor where it was.
func (m Model) afterEditSend(msg editSentMsg) (tea.Model, tea.Cmd) {
	es := m.edit
	m.mode = modeBrowse
	m.edit = editState{}
	if msg.err != nil {
		m.status = "Save failed: " + msg.err.Error()
		m.statusErr = true
		return m, nil
	}
	m.status = "Saved " + es.name
	m.statusErr = false
	return m, m.loadPanel(es.side, es.dir, cursorKeep, "")
}

// discardEdit tears down an edit that won't write anything back: it removes the
// temp dir, returns to browsing and reports status as an error. discardEditOK is
// the non-error variant, for the benign "unchanged" case.
func (m Model) discardEdit(status string) (tea.Model, tea.Cmd)   { return m.endEdit(status, true) }
func (m Model) discardEditOK(status string) (tea.Model, tea.Cmd) { return m.endEdit(status, false) }

func (m Model) endEdit(status string, isErr bool) (tea.Model, tea.Cmd) {
	os.RemoveAll(m.edit.tmpDir)
	m.mode = modeBrowse
	m.edit = editState{}
	m.status = status
	m.statusErr = isErr
	return m, nil
}

// copyFile copies src to dst, truncating dst if it exists. A freshly created
// dst takes src's permission bits; an existing dst keeps its own mode, owner and
// inode, so writing an edit back to a local file leaves that metadata intact.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// modTime returns p's modification time — the signal k8tc uses to decide whether
// an edit actually changed the file.
func modTime(p string) (time.Time, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return time.Time{}, err
	}
	return fi.ModTime(), nil
}
