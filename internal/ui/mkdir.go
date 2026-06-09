package ui

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// mkdirState is the new-folder prompt. Unlike copy and delete it is not a batch:
// there is no item queue, no sequential loop and nothing to abort — just the
// panel and directory the folder lands in and a text input collecting its name.
type mkdirState struct {
	side  focus  // panel the folder is created in
	dir   string // directory it lands in (side panel's cwd)
	input textinput.Model
}

// mkdirDoneMsg reports the outcome of a directory creation. It echoes back the
// side and dir so the handler can reload the right panel and land the cursor on
// the new entry — the prompt state is already gone by the time it arrives.
type mkdirDoneMsg struct {
	err  error
	name string
	side focus
	dir  string
}

// mkdirCmd creates one directory on the given side, reporting the outcome. mkdir
// is a one-shot, so there is no cancelable context to thread through; side and
// dir are echoed back for the panel reload.
func mkdirCmd(fs panelFS, full, name string, side focus, dir string) tea.Cmd {
	return func() tea.Msg {
		err := fs.Mkdir(context.Background(), full)
		return mkdirDoneMsg{err: err, name: name, side: side, dir: dir}
	}
}

// handleMkdir opens the new-folder prompt for the focused panel's current
// directory. Nothing is created until the user types a name and confirms.
func (m Model) handleMkdir() (tea.Model, tea.Cmd) {
	p := m.focusedPanel()
	ti := textinput.New()
	ti.Placeholder = "name"
	ti.CharLimit = 255
	ti.Width = 32
	cmd := ti.Focus()
	m.mkdir = mkdirState{side: m.focus, dir: p.cwd, input: ti}
	m.mode = modeMkdir
	return m, cmd
}

// handleMkdirKey drives the new-folder prompt: Enter creates the directory and
// returns to browsing (mkdir is a one-shot, so there is no progress dialog),
// Esc backs out, Ctrl+C still quits. Every other key edits the name field — so
// unlike the batch handlers this must not treat the Quit binding as cancel,
// otherwise "q" could never appear in a folder name.
func (m Model) handleMkdirKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Type == tea.KeyCtrlC:
		m.quitting = true
		return m, tea.Quit
	case key.Matches(msg, m.keys.Cancel):
		m.mode = modeBrowse
		m.mkdir = mkdirState{}
		m.status = "New folder cancelled."
		m.statusErr = false
		return m, nil
	case key.Matches(msg, m.keys.Enter):
		name := strings.TrimSpace(m.mkdir.input.Value())
		if name == "" {
			return m, nil // nothing to create; keep the prompt open
		}
		side, dir := m.mkdir.side, m.mkdir.dir
		full := m.pathsFor(side).Join(dir, name)
		m.mode = modeBrowse
		m.mkdir = mkdirState{}
		m.status = "Creating " + name + "…"
		m.statusErr = false
		return m, mkdirCmd(m.fsFor(side), full, name, side, dir)
	}
	var cmd tea.Cmd
	m.mkdir.input, cmd = m.mkdir.input.Update(msg)
	return m, cmd
}

// mkdirDialog is the new-folder prompt: the target directory and the text input
// for the name, rendered inside the same bordered box the other dialogs use.
func (m Model) mkdirDialog() dialog {
	return dialog{
		title: "New folder",
		body: []string{
			"in " + m.panelPtr(m.mkdir.side).label + ": " + m.mkdir.dir,
			m.mkdir.input.View(),
		},
		footer: "[ Enter ] Create    [ Esc ] Cancel",
	}
}
