package ui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// editingModel returns a model already in modeEdit with a real working copy on
// disk: a local file fetched into a temp directory, its baseline mtime recorded.
// dir is where the original lives — and where a write-back lands.
func editingModel(t *testing.T) (m Model, dir string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "conf.yaml"), []byte("original"), 0o640); err != nil {
		t.Fatal(err)
	}
	b := fakeBackend{}
	m = New(b, b, b, b, b, "POD x", "/", dir)
	es := editState{side: focusLocal, dir: dir, name: "conf.yaml", editor: "true"}
	msg, ok := m.fetchForEditCmd(es)().(editFetchedMsg)
	if !ok || msg.err != nil {
		t.Fatalf("setup fetch failed: ok=%v err=%v", ok, msg.err)
	}
	m.mode = modeEdit
	m.edit = msg.state
	return m, dir
}

func TestEditNonFileIsNoop(t *testing.T) {
	t.Setenv("EDITOR", "true")
	m := sampleModel(t, 80, 24)
	// cursor 0 == "..", cursor 1 == "assets" (a directory): neither is editable.
	for _, c := range []int{0, 1} {
		m.local.cursor = c
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyF4})
		mm := next.(Model)
		if mm.mode != modeBrowse {
			t.Errorf("cursor %d: F4 left mode %v, want browse", c, mm.mode)
		}
		if cmd != nil {
			t.Errorf("cursor %d: F4 on a non-file produced a command", c)
		}
	}
}

func TestEditWithoutEditorReports(t *testing.T) {
	t.Setenv("EDITOR", "")
	m := sampleModel(t, 80, 24)
	m.local.cursor = 2 // index.html

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyF4})
	m = next.(Model)
	if m.mode != modeBrowse {
		t.Fatalf("mode = %v, want browse", m.mode)
	}
	if cmd != nil {
		t.Errorf("F4 with no $EDITOR produced a command")
	}
	if !m.statusErr || !strings.Contains(m.status, "$EDITOR") {
		t.Errorf("status = %q (err=%v), want an $EDITOR-unset message", m.status, m.statusErr)
	}
}

func TestEditEntersFetchOnFile(t *testing.T) {
	t.Setenv("EDITOR", "true")
	m := sampleModel(t, 80, 24)
	m.local.cursor = 2 // index.html

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyF4})
	m = next.(Model)
	if m.mode != modeEdit {
		t.Fatalf("mode = %v, want edit", m.mode)
	}
	if cmd == nil {
		t.Fatalf("F4 on a file produced no fetch command")
	}
	if m.edit.side != focusLocal || m.edit.name != "index.html" {
		t.Errorf("edit target = %v %q, want local index.html", m.edit.side, m.edit.name)
	}
}

func TestEditSwallowsKeysWhileInFlight(t *testing.T) {
	t.Setenv("EDITOR", "true")
	m := sampleModel(t, 80, 24)
	m.local.cursor = 2
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyF4})
	m = next.(Model)

	// A key arriving during the fetch/write-back window must do nothing.
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyF8})
	m = next.(Model)
	if m.mode != modeEdit {
		t.Errorf("key during edit changed mode to %v", m.mode)
	}
	if cmd != nil {
		t.Errorf("key during edit produced a command")
	}
}

func TestEditCtrlCStillQuits(t *testing.T) {
	t.Setenv("EDITOR", "true")
	m := sampleModel(t, 80, 24)
	m.local.cursor = 2
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyF4})
	m = next.(Model)

	// Ctrl+C must remain an escape hatch even while an edit is in flight.
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(Model)
	if !m.quitting || cmd == nil {
		t.Errorf("Ctrl+C during edit did not quit: quitting=%v cmd=%v", m.quitting, cmd)
	}
}

func TestEditFetchFailureReports(t *testing.T) {
	t.Setenv("EDITOR", "true")
	m := sampleModel(t, 80, 24)
	m.local.cursor = 2
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyF4})
	m = next.(Model)

	next, cmd := m.Update(editFetchedMsg{err: errors.New("boom")})
	m = next.(Model)
	if m.mode != modeBrowse {
		t.Fatalf("mode = %v after fetch failure, want browse", m.mode)
	}
	if cmd != nil {
		t.Errorf("fetch failure produced a command")
	}
	if !m.statusErr || !strings.Contains(m.status, "Fetch failed") {
		t.Errorf("status = %q (err=%v), want a fetch-failure summary", m.status, m.statusErr)
	}
}

func TestFetchForEditCopiesLocalFile(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "conf.yaml"), []byte("hello"), 0o640); err != nil {
		t.Fatal(err)
	}
	b := fakeBackend{}
	m := New(b, b, b, b, b, "POD x", "/", src)

	es := editState{side: focusLocal, dir: src, name: "conf.yaml", editor: "true"}
	msg, ok := m.fetchForEditCmd(es)().(editFetchedMsg)
	if !ok {
		t.Fatal("fetch did not return editFetchedMsg")
	}
	if msg.err != nil {
		t.Fatalf("fetch error: %v", msg.err)
	}
	defer os.RemoveAll(msg.state.tmpDir)

	if got, err := os.ReadFile(msg.state.tmpFile); err != nil || string(got) != "hello" {
		t.Fatalf("working copy = %q, %v; want hello", got, err)
	}
	if msg.state.origMod.IsZero() {
		t.Errorf("baseline mtime not recorded")
	}
}

func TestEditUnchangedDiscards(t *testing.T) {
	m, _ := editingModel(t)
	tmpDir, name := m.edit.tmpDir, m.edit.name

	// Editor exited without touching the file: baseline mtime still matches.
	next, cmd := m.Update(editorFinishedMsg{})
	m = next.(Model)
	if m.mode != modeBrowse {
		t.Fatalf("mode = %v, want browse", m.mode)
	}
	if cmd != nil {
		t.Errorf("unchanged edit produced a command; nothing should be written back")
	}
	if m.statusErr || !strings.Contains(m.status, name+" unchanged") {
		t.Errorf("status = %q (err=%v), want '<name> unchanged'", m.status, m.statusErr)
	}
	if _, err := os.Stat(tmpDir); !os.IsNotExist(err) {
		t.Errorf("temp dir not cleaned up: %v", err)
	}
}

func TestEditChangedWritesBackAndSaves(t *testing.T) {
	m, dir := editingModel(t)
	name, tmpDir := m.edit.name, m.edit.tmpDir

	// Simulate the editor saving a change: new content, with the mtime forced
	// past the baseline so the change is detected regardless of filesystem mtime
	// granularity.
	if err := os.WriteFile(m.edit.tmpFile, []byte("edited"), 0o640); err != nil {
		t.Fatal(err)
	}
	newer := m.edit.origMod.Add(time.Second)
	if err := os.Chtimes(m.edit.tmpFile, newer, newer); err != nil {
		t.Fatal(err)
	}

	next, cmd := m.Update(editorFinishedMsg{})
	m = next.(Model)
	if m.mode != modeEdit {
		t.Fatalf("mode = %v during save, want still edit", m.mode)
	}
	if cmd == nil {
		t.Fatalf("changed edit produced no write-back command")
	}
	if !strings.Contains(m.status, "Saving "+name) {
		t.Errorf("status = %q, want a 'Saving' message", m.status)
	}

	// Run the write-back and feed its result back in.
	sent, ok := cmd().(editSentMsg)
	if !ok || sent.err != nil {
		t.Fatalf("write-back: ok=%v err=%v", ok, sent.err)
	}
	next, cmd = m.Update(sent)
	m = next.(Model)
	if m.mode != modeBrowse {
		t.Fatalf("mode = %v after save, want browse", m.mode)
	}
	if m.statusErr || !strings.Contains(m.status, "Saved "+name) {
		t.Errorf("status = %q (err=%v), want 'Saved <name>'", m.status, m.statusErr)
	}
	if cmd == nil {
		t.Errorf("save did not reload the panel")
	}
	if _, err := os.Stat(tmpDir); !os.IsNotExist(err) {
		t.Errorf("temp dir not cleaned up after save: %v", err)
	}
	// The edit reached the original on disk.
	if got, _ := os.ReadFile(filepath.Join(dir, name)); string(got) != "edited" {
		t.Errorf("original content = %q, want edited", got)
	}
}

func TestCopyFilePreservesExistingMode(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Chmod explicitly so the assertion is independent of the test's umask.
	if err := os.Chmod(dst, 0o640); err != nil {
		t.Fatal(err)
	}

	if err := copyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "new" {
		t.Errorf("dst content = %q, want new", got)
	}
	if fi, _ := os.Stat(dst); fi.Mode().Perm() != 0o640 {
		t.Errorf("existing dst mode = %o, want preserved 0640", fi.Mode().Perm())
	}
}
