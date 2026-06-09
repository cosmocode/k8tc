package ui

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cosmocode/k8tc/internal/file"
)

// fakeBackend satisfies both the panelFS and transferer interfaces, standing in
// for the local FS, the remote lister, and the transfer manager at once.
type fakeBackend struct{}

func (fakeBackend) List(string) ([]file.Info, error)                        { return nil, nil }
func (fakeBackend) Delete(context.Context, string) error                    { return nil }
func (fakeBackend) Mkdir(context.Context, string) error                     { return nil }
func (fakeBackend) Pull(context.Context, string, string, func(int64)) error { return nil }
func (fakeBackend) Push(context.Context, string, string, func(int64)) error { return nil }

func sampleModel(t *testing.T, w, h int) Model {
	t.Helper()
	b := fakeBackend{}
	m := New(b, b, b, "POD nginx-abc", "/var/www", "/home/user")
	next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m = next.(Model)
	files := []file.Info{
		{Name: "..", IsDir: true},
		{Name: "assets", IsDir: true},
		{Name: "index.html", Size: 1234},
		{Name: "go.mod", Size: 56},
	}
	for _, which := range []focus{focusLocal, focusRemote} {
		next, _ = m.Update(panelLoadedMsg{which: which, path: m.panelPtr(which).cwd, files: files, mode: cursorReset})
		m = next.(Model)
	}
	return m
}

func TestViewLayoutIsRectangular(t *testing.T) {
	const w, h = 80, 24
	m := sampleModel(t, w, h)
	view := m.View()

	lines := strings.Split(view, "\n")
	if len(lines) != h {
		t.Fatalf("view has %d lines, want %d", len(lines), h)
	}
	for i, line := range lines {
		if got := lipgloss.Width(line); got != w {
			t.Errorf("line %d width = %d, want %d: %q", i, got, w, line)
		}
	}
	if !strings.Contains(view, "LOCAL") || !strings.Contains(view, "POD nginx-abc") {
		t.Errorf("view missing panel titles:\n%s", view)
	}
}

// TestSnapshot writes a rendered frame to disk when K8TC_SNAPSHOT is set, so a
// human (or screenshot) can eyeball the layout. It is a no-op otherwise.
func TestSnapshot(t *testing.T) {
	path := os.Getenv("K8TC_SNAPSHOT")
	if path == "" {
		t.Skip("set K8TC_SNAPSHOT=<file> to write a rendered frame")
	}
	m := sampleModel(t, 90, 22)
	// Put the cursor on a file in the (focused) local panel for a livelier shot.
	m.local.cursor = 2
	m.status = "Copied index.html (1.2K)"
	if err := os.WriteFile(path, []byte(m.View()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTabSwitchesFocus(t *testing.T) {
	m := sampleModel(t, 80, 24)
	if m.focus != focusLocal {
		t.Fatalf("initial focus = %v, want local", m.focus)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(Model)
	if m.focus != focusRemote {
		t.Errorf("after Tab focus = %v, want remote", m.focus)
	}
}

func TestCursorMovesAndStaysInBounds(t *testing.T) {
	m := sampleModel(t, 80, 24)
	// Down three times from a 4-entry list lands on the last entry.
	for i := 0; i < 3; i++ {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		m = next.(Model)
	}
	if m.local.cursor != 3 {
		t.Errorf("cursor = %d, want 3", m.local.cursor)
	}
	// One more must not run past the end.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	m = next.(Model)
	if m.local.cursor != 3 {
		t.Errorf("cursor overran to %d, want clamped at 3", m.local.cursor)
	}
}

func TestCopyOnDotDotIsNoop(t *testing.T) {
	m := sampleModel(t, 80, 24)
	// Cursor starts on ".." — copying it must do nothing.
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyF5})
	m = next.(Model)
	if m.mode != modeBrowse {
		t.Errorf("copying '..' left browse mode: %v", m.mode)
	}
	if cmd != nil {
		t.Errorf("copying '..' produced a command")
	}
}

// markLocal marks the two non-".." entries by raking Space down from the top.
// sampleModel's files are: .., assets, index.html, go.mod.
func markLocal(t *testing.T, m Model) Model {
	t.Helper()
	// Space on "..": no mark, cursor advances to "assets".
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = next.(Model)
	if len(m.local.marked) != 0 {
		t.Fatalf("'..' should not be markable, got %v", m.local.marked)
	}
	if m.local.cursor != 1 {
		t.Fatalf("cursor after marking '..' = %d, want 1", m.local.cursor)
	}
	// Space on "assets" then "index.html": both get marked.
	for i := 0; i < 2; i++ {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
		m = next.(Model)
	}
	if !m.local.isMarked("assets") || !m.local.isMarked("index.html") {
		t.Fatalf("expected assets+index.html marked, got %v", m.local.marked)
	}
	return m
}

func TestCopyMarkedConfirmsThenTransfers(t *testing.T) {
	m := markLocal(t, sampleModel(t, 80, 24))

	// F5 opens the confirm dialog with both marked items; nothing transfers yet.
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyF5})
	m = next.(Model)
	if m.mode != modeConfirm {
		t.Fatalf("mode = %v, want confirm", m.mode)
	}
	if len(m.job.items) != 2 {
		t.Fatalf("job has %d items, want 2", len(m.job.items))
	}
	if m.job.dest != focusRemote {
		t.Errorf("dest = %v, want remote", m.job.dest)
	}
	if cmd != nil {
		t.Errorf("confirm dialog should not start a transfer yet")
	}

	// Enter starts the batch.
	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if m.mode != modeProgress {
		t.Fatalf("mode = %v, want progress", m.mode)
	}
	if cmd == nil {
		t.Errorf("starting the batch produced no command")
	}

	// First item finishes → advance to the second, still in progress.
	next, _ = m.Update(transferDoneMsg{name: "assets"})
	m = next.(Model)
	if m.mode != modeProgress || m.job.index != 1 {
		t.Fatalf("after item 1: mode=%v index=%d, want progress/1", m.mode, m.job.index)
	}

	// Second item finishes → batch done, back to browsing, marks cleared.
	next, _ = m.Update(transferDoneMsg{name: "index.html"})
	m = next.(Model)
	if m.mode != modeBrowse {
		t.Fatalf("mode = %v, want browse", m.mode)
	}
	if len(m.local.marked) != 0 {
		t.Errorf("marks not cleared after copy: %v", m.local.marked)
	}
	if m.statusErr || !strings.Contains(m.status, "Copied 2") {
		t.Errorf("status = %q (err=%v), want a 'Copied 2' summary", m.status, m.statusErr)
	}
}

func TestMarksClearedOnNavigateAndPrunedOnRefresh(t *testing.T) {
	m := markLocal(t, sampleModel(t, 80, 24))

	// A refresh (cursorKeep) where "index.html" vanished prunes only that mark.
	survivors := []file.Info{
		{Name: "..", IsDir: true},
		{Name: "assets", IsDir: true},
		{Name: "go.mod", Size: 56},
	}
	next, _ := m.Update(panelLoadedMsg{which: focusLocal, path: m.local.cwd, files: survivors, mode: cursorKeep})
	m = next.(Model)
	if !m.local.isMarked("assets") || m.local.isMarked("index.html") {
		t.Errorf("refresh prune wrong: %v", m.local.marked)
	}

	// Navigating into a new directory (cursorReset) clears all marks.
	next, _ = m.Update(panelLoadedMsg{which: focusLocal, path: "/var/www/assets", files: survivors, mode: cursorReset})
	m = next.(Model)
	if len(m.local.marked) != 0 {
		t.Errorf("marks not cleared on navigate: %v", m.local.marked)
	}
}

func TestDialogOverlaysPanels(t *testing.T) {
	const w, h = 80, 24
	m := markLocal(t, sampleModel(t, w, h))
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyF5})
	m = next.(Model)
	if m.mode != modeConfirm {
		t.Fatalf("mode = %v, want confirm", m.mode)
	}

	view := m.View()
	lines := strings.Split(view, "\n")
	if len(lines) != h {
		t.Fatalf("overlaid view has %d lines, want %d", len(lines), h)
	}
	for i, line := range lines {
		if got := lipgloss.Width(line); got != w {
			t.Errorf("line %d width = %d, want %d", i, got, w)
		}
	}
	// The panels must still be visible behind the dialog (overlay, not replace)…
	if !strings.Contains(view, "LOCAL") || !strings.Contains(view, "POD nginx-abc") {
		t.Errorf("panels hidden behind dialog; want them composited underneath")
	}
	// …and the dialog itself must be on top.
	if !strings.Contains(view, "Copy 2 items?") {
		t.Errorf("dialog not visible in overlaid view")
	}
}

func TestCopyFallsBackToHighlightWhenNothingMarked(t *testing.T) {
	m := sampleModel(t, 80, 24)
	m.local.cursor = 2 // index.html, nothing marked

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyF5})
	m = next.(Model)
	if m.mode != modeConfirm {
		t.Fatalf("mode = %v, want confirm", m.mode)
	}
	if len(m.job.items) != 1 || m.job.items[0].name != "index.html" {
		t.Errorf("job items = %+v, want just index.html", m.job.items)
	}
}

func TestConfirmEscCancels(t *testing.T) {
	m := markLocal(t, sampleModel(t, 80, 24))
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyF5})
	m = next.(Model)

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if m.mode != modeBrowse {
		t.Errorf("Esc on confirm left mode = %v, want browse", m.mode)
	}
	if cmd != nil {
		t.Errorf("cancelling produced a command")
	}
}

func TestAbortStopsBatch(t *testing.T) {
	m := markLocal(t, sampleModel(t, 80, 24))
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyF5})
	m = next.(Model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)

	// Abort mid-transfer.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if !m.job.aborted {
		t.Fatalf("Esc during progress did not set aborted")
	}

	// The killed transfer reports an error; the batch tears down regardless.
	next, _ = m.Update(transferDoneMsg{err: context.Canceled, name: "assets"})
	m = next.(Model)
	if m.mode != modeBrowse {
		t.Fatalf("mode = %v after abort, want browse", m.mode)
	}
	if !m.statusErr || !strings.Contains(m.status, "Aborted") {
		t.Errorf("status = %q (err=%v), want an 'Aborted' summary", m.status, m.statusErr)
	}
}

func TestDeleteMarkedConfirmsThenDeletes(t *testing.T) {
	m := markLocal(t, sampleModel(t, 80, 24))

	// F8 opens the destructive confirm dialog with both marked items; nothing
	// is deleted yet.
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyF8})
	m = next.(Model)
	if m.mode != modeConfirmDelete {
		t.Fatalf("mode = %v, want confirmDelete", m.mode)
	}
	if len(m.del.items) != 2 {
		t.Fatalf("delete job has %d items, want 2", len(m.del.items))
	}
	if m.del.side != focusLocal {
		t.Errorf("side = %v, want local", m.del.side)
	}
	if cmd != nil {
		t.Errorf("confirm dialog should not start deleting yet")
	}

	// Enter starts the batch.
	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if m.mode != modeDeleting {
		t.Fatalf("mode = %v, want deleting", m.mode)
	}
	if cmd == nil {
		t.Errorf("starting the batch produced no command")
	}

	// First item finishes → advance to the second, still deleting.
	next, _ = m.Update(deleteDoneMsg{name: "assets"})
	m = next.(Model)
	if m.mode != modeDeleting || m.del.index != 1 {
		t.Fatalf("after item 1: mode=%v index=%d, want deleting/1", m.mode, m.del.index)
	}

	// Second item finishes → batch done, back to browsing, marks cleared.
	next, _ = m.Update(deleteDoneMsg{name: "index.html"})
	m = next.(Model)
	if m.mode != modeBrowse {
		t.Fatalf("mode = %v, want browse", m.mode)
	}
	if len(m.local.marked) != 0 {
		t.Errorf("marks not cleared after delete: %v", m.local.marked)
	}
	if m.statusErr || !strings.Contains(m.status, "Deleted 2") {
		t.Errorf("status = %q (err=%v), want a 'Deleted 2' summary", m.status, m.statusErr)
	}
}

func TestDeleteFallsBackToHighlightWhenNothingMarked(t *testing.T) {
	m := sampleModel(t, 80, 24)
	m.local.cursor = 2 // index.html, nothing marked

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyF8})
	m = next.(Model)
	if m.mode != modeConfirmDelete {
		t.Fatalf("mode = %v, want confirmDelete", m.mode)
	}
	if len(m.del.items) != 1 || m.del.items[0].name != "index.html" {
		t.Errorf("delete items = %+v, want just index.html", m.del.items)
	}
}

func TestDeleteOnDotDotIsNoop(t *testing.T) {
	m := sampleModel(t, 80, 24)
	// Cursor starts on ".." — deleting it must do nothing.
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyF8})
	m = next.(Model)
	if m.mode != modeBrowse {
		t.Errorf("deleting '..' left browse mode: %v", m.mode)
	}
	if cmd != nil {
		t.Errorf("deleting '..' produced a command")
	}
}

func TestConfirmDeleteEscCancels(t *testing.T) {
	m := markLocal(t, sampleModel(t, 80, 24))
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyF8})
	m = next.(Model)
	if m.mode != modeConfirmDelete {
		t.Fatalf("mode = %v, want confirmDelete", m.mode)
	}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if m.mode != modeBrowse {
		t.Errorf("Esc on confirmDelete left mode = %v, want browse", m.mode)
	}
	if cmd != nil {
		t.Errorf("cancelling produced a command")
	}
	// Esc must not have deleted anything; marks stay put.
	if !m.local.isMarked("assets") || !m.local.isMarked("index.html") {
		t.Errorf("cancelling cleared marks: %v", m.local.marked)
	}
}

func TestAbortStopsDelete(t *testing.T) {
	m := markLocal(t, sampleModel(t, 80, 24))
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyF8})
	m = next.(Model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)

	// Abort mid-batch.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if !m.del.aborted {
		t.Fatalf("Esc during delete did not set aborted")
	}

	// The in-flight item reports back; the batch tears down regardless.
	next, _ = m.Update(deleteDoneMsg{err: context.Canceled, name: "assets"})
	m = next.(Model)
	if m.mode != modeBrowse {
		t.Fatalf("mode = %v after abort, want browse", m.mode)
	}
	if !m.statusErr || !strings.Contains(m.status, "Aborted") {
		t.Errorf("status = %q (err=%v), want an 'Aborted' summary", m.status, m.statusErr)
	}
}

func TestDeleteConfirmDialogIsDanger(t *testing.T) {
	m := markLocal(t, sampleModel(t, 80, 24))
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyF8})
	m = next.(Model)
	if d := m.confirmDeleteDialog(); !d.danger {
		t.Errorf("delete confirm dialog should be flagged danger")
	}
	// The dialog must surface the destructive count and be visible over the panels.
	view := m.View()
	if !strings.Contains(view, "Delete 2 items?") {
		t.Errorf("delete dialog not visible in overlaid view")
	}
	if !strings.Contains(view, "LOCAL") {
		t.Errorf("panels hidden behind delete dialog; want them composited underneath")
	}
}

func TestMkdirPromptOpensAndCreates(t *testing.T) {
	m := sampleModel(t, 80, 24)

	// F7 opens the new-folder prompt, targeting the focused panel's cwd.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyF7})
	m = next.(Model)
	if m.mode != modeMkdir {
		t.Fatalf("mode = %v, want mkdir", m.mode)
	}
	if m.mkdir.side != focusLocal || m.mkdir.dir != "/home/user" {
		t.Fatalf("mkdir target = %v %q, want local /home/user", m.mkdir.side, m.mkdir.dir)
	}

	// Typing edits the name field; nothing is created yet.
	for _, r := range "newdir" {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(Model)
	}
	if got := m.mkdir.input.Value(); got != "newdir" {
		t.Fatalf("input value = %q, want newdir", got)
	}

	// Enter fires the create command and returns to browsing immediately —
	// mkdir has no progress dialog.
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if m.mode != modeBrowse {
		t.Fatalf("after Enter mode = %v, want browse", m.mode)
	}
	if cmd == nil {
		t.Fatalf("Enter produced no mkdir command")
	}

	// The create reports success → status reflects it.
	next, _ = m.Update(mkdirDoneMsg{name: "newdir", side: focusLocal, dir: "/home/user"})
	m = next.(Model)
	if m.statusErr || !strings.Contains(m.status, "Created newdir") {
		t.Errorf("status = %q (err=%v), want a 'Created newdir' summary", m.status, m.statusErr)
	}
}

func TestMkdirEscCancels(t *testing.T) {
	m := sampleModel(t, 80, 24)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyF7})
	m = next.(Model)

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if m.mode != modeBrowse {
		t.Errorf("Esc left mode = %v, want browse", m.mode)
	}
	if cmd != nil {
		t.Errorf("cancelling produced a command")
	}
}

func TestMkdirEmptyNameStaysOpen(t *testing.T) {
	m := sampleModel(t, 80, 24)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyF7})
	m = next.(Model)

	// Enter with an empty field must not create anything or close the prompt.
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if m.mode != modeMkdir {
		t.Errorf("Enter on empty name left mode = %v, want still mkdir", m.mode)
	}
	if cmd != nil {
		t.Errorf("empty-name Enter produced a command")
	}
}

func TestMkdirReportsFailure(t *testing.T) {
	m := sampleModel(t, 80, 24)
	next, _ := m.Update(mkdirDoneMsg{err: errors.New("File exists"), name: "newdir", side: focusLocal, dir: "/home/user"})
	m = next.(Model)
	if !m.statusErr || !strings.Contains(m.status, "mkdir failed") {
		t.Errorf("status = %q (err=%v), want a failure summary", m.status, m.statusErr)
	}
}

func TestMkdirDialogOverlaysPanels(t *testing.T) {
	const w, h = 80, 24
	m := sampleModel(t, w, h)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyF7})
	m = next.(Model)

	// The text input embedded in the dialog must not break the frame's geometry.
	view := m.View()
	lines := strings.Split(view, "\n")
	if len(lines) != h {
		t.Fatalf("overlaid view has %d lines, want %d", len(lines), h)
	}
	for i, line := range lines {
		if got := lipgloss.Width(line); got != w {
			t.Errorf("line %d width = %d, want %d", i, got, w)
		}
	}
	if !strings.Contains(view, "New folder") {
		t.Errorf("mkdir dialog not visible in overlaid view")
	}
	if !strings.Contains(view, "LOCAL") {
		t.Errorf("panels hidden behind mkdir dialog; want them composited underneath")
	}
}

func TestEnterFileIsNoop(t *testing.T) {
	m := sampleModel(t, 80, 24)
	// Move to a regular file (index 2: index.html) and press Enter.
	m.local.cursor = 2
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if cmd != nil {
		t.Errorf("Enter on a file should be a no-op, got a command")
	}
	if m.local.cwd != "/home/user" {
		t.Errorf("cwd changed on file Enter: %q", m.local.cwd)
	}
}
