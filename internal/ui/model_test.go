package ui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cosmocode/k8tc/internal/transfer"
)

type fakeTransfer struct{}

func (fakeTransfer) List(_, _, _ string) ([]transfer.FileInfo, error) { return nil, nil }
func (fakeTransfer) Pull(_, _, _, _ string, _ func(int64)) error      { return nil }
func (fakeTransfer) Push(_, _, _, _ string, _ func(int64)) error      { return nil }

func sampleModel(t *testing.T, w, h int) Model {
	t.Helper()
	m := New(fakeTransfer{}, "nginx-abc", "", "/var/www", "/home/user")
	next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m = next.(Model)
	files := []transfer.FileInfo{
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
	if m.transferring {
		t.Errorf("copying '..' started a transfer")
	}
	if cmd != nil {
		t.Errorf("copying '..' produced a command")
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
