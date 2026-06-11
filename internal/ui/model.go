package ui

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cosmocode/k8tc/internal/file"
	overlay "github.com/rmhubbert/bubbletea-overlay"
)

// panelFS is one side of the view (local disk or a pod) as the model sees it:
// something it can list, delete entries on, and create directories in. Both
// internal/local and internal/remote satisfy it, which is what lets a panel
// load and mutate itself without the model knowing which side it is.
type panelFS interface {
	List(path string) ([]file.Info, error)
	Delete(ctx context.Context, path string) error
	Mkdir(ctx context.Context, path string) error
}

// pathFlavor is one side's path-string semantics: how it joins a directory and
// an entry name, and finds a path's parent and base. The pod side is POSIX, the
// local side OS-native; the model picks the right one per panel via pathsFor.
// internal/local.Paths and internal/remote.Paths satisfy it. It is pure string
// math with no I/O, which is why it is its own small interface rather than part
// of panelFS.
type pathFlavor interface {
	Join(dir, name string) string
	Dir(p string) string
	Base(p string) string
}

// transferer moves a file or directory tree between local disk and the pod.
// internal/transfer.Manager satisfies it. The context lets an in-flight copy be
// aborted: canceling it kills the underlying tar processes.
type transferer interface {
	Push(ctx context.Context, localPath, remotePath string, progress func(int64)) error
	Pull(ctx context.Context, remotePath, localPath string, progress func(int64)) error
}

type focus int

const (
	focusLocal focus = iota
	focusRemote
)

// cursorMode controls where the cursor lands after a panel reload.
type cursorMode int

const (
	cursorReset  cursorMode = iota // top (descending into a directory)
	cursorKeep                     // same index, clamped (refresh)
	cursorSelect                   // on the named entry (ascending via "..")
)

// uiMode is the top-level interaction state. In a dialog mode the panels are
// hidden and keys drive the dialog rather than the file browser.
type uiMode int

const (
	modeBrowse        uiMode = iota // normal two-panel browsing
	modeConfirm                     // confirm-before-copy dialog
	modeProgress                    // copy-in-progress dialog (with abort)
	modeConfirmDelete               // confirm-before-delete dialog
	modeDeleting                    // delete-in-progress dialog (with abort)
	modeMkdir                       // new-folder text prompt
	modeEdit                        // file fetched/edited in $EDITOR/written back
)

// Model is the Bubble Tea root model.
type Model struct {
	localFS  panelFS
	remoteFS panelFS

	localPaths  pathFlavor
	remotePaths pathFlavor

	xfer transferer

	local  Panel
	remote Panel
	focus  focus

	width, height int

	status    string
	statusErr bool

	mode  uiMode
	job   copyJob
	del   deleteJob
	mkdir mkdirState
	edit  editState

	keys     keyMap
	quitting bool
}

// New builds the initial model. localFS/remoteFS load and delete on each panel
// and xfer performs copies between them; localPaths/remotePaths give each side
// its path semantics. remoteLabel is the pod-side panel title and
// localPath/remotePath are the starting dirs.
func New(localFS panelFS, localPaths pathFlavor, remoteFS panelFS, remotePaths pathFlavor, xfer transferer, remoteLabel, remotePath, localPath string) Model {
	return Model{
		localFS:     localFS,
		remoteFS:    remoteFS,
		localPaths:  localPaths,
		remotePaths: remotePaths,
		xfer:        xfer,
		focus:       focusLocal,
		keys:        defaultKeys(),
		status:      "Space to mark, F5 to copy, F8 to delete.",
		local:       Panel{label: "LOCAL", cwd: localPath},
		remote:      Panel{label: remoteLabel, cwd: remotePath, isRemote: true},
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.loadPanel(focusLocal, m.local.cwd, cursorReset, ""),
		m.loadPanel(focusRemote, m.remote.cwd, cursorReset, ""),
	)
}

// panelLoadedMsg carries the result of a directory listing back to the model.
type panelLoadedMsg struct {
	which   focus
	path    string
	files   []file.Info
	err     error
	mode    cursorMode
	selName string
}

// loadPanel lists a directory on the given side and reports it back as a
// panelLoadedMsg, landing the cursor according to mode.
func (m Model) loadPanel(which focus, p string, mode cursorMode, selName string) tea.Cmd {
	fs := m.fsFor(which)
	return func() tea.Msg {
		files, err := fs.List(p)
		return panelLoadedMsg{which: which, path: p, files: files, err: err, mode: mode, selName: selName}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case panelLoadedMsg:
		p := m.panelPtr(msg.which)
		if msg.err != nil {
			m.status = "list error: " + msg.err.Error()
			m.statusErr = true
			return m, nil
		}
		p.cwd = msg.path
		p.files = msg.files
		switch msg.mode {
		case cursorReset:
			p.cursor, p.offset = 0, 0
			p.clearMarks() // moved into a new directory
		case cursorSelect:
			p.cursor = indexOf(msg.files, msg.selName)
			p.clearMarks() // moved into a new directory
		case cursorKeep:
			p.pruneMarks() // refresh/post-copy reload: drop vanished names
		}
		p.clampScroll()
		return m, nil

	case transferProgressMsg:
		if m.mode != modeProgress || msg.ch != m.job.progressCh {
			return m, nil
		}
		m.job.curBytes = msg.n
		return m, listenProgress(m.job.progressCh)

	case transferDoneMsg:
		if m.mode != modeProgress {
			return m, nil
		}
		return m.advanceJob(msg)

	case deleteDoneMsg:
		if m.mode != modeDeleting {
			return m, nil
		}
		return m.advanceDelete(msg)

	case mkdirDoneMsg:
		if msg.err != nil {
			m.status = "mkdir failed: " + msg.err.Error()
			m.statusErr = true
			return m, nil
		}
		m.status = "Created " + msg.name + "/"
		m.statusErr = false
		// Reload the panel and land the cursor on the new directory.
		return m, m.loadPanel(msg.side, msg.dir, cursorSelect, msg.name)

	case editFetchedMsg:
		if m.mode != modeEdit {
			return m, nil
		}
		if msg.err != nil {
			return m.discardEdit("Fetch failed: " + msg.err.Error())
		}
		m.edit = msg.state
		m.status = "Editing " + m.edit.name + "…"
		return m, m.runEditorCmd()

	case editorFinishedMsg:
		if m.mode != modeEdit {
			return m, nil
		}
		return m.afterEditor(msg)

	case editSentMsg:
		if m.mode != modeEdit {
			return m, nil
		}
		return m.afterEditSend(msg)

	case progressClosedMsg:
		return m, nil
	}
	// While the new-folder prompt is open, hand anything we don't handle here
	// (notably the text input's own cursor-blink ticks) to the input.
	if m.mode == modeMkdir {
		var cmd tea.Cmd
		m.mkdir.input, cmd = m.mkdir.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeConfirm:
		return m.handleConfirmKey(msg)
	case modeProgress:
		return m.handleProgressKey(msg)
	case modeConfirmDelete:
		return m.handleConfirmDeleteKey(msg)
	case modeDeleting:
		return m.handleDeletingKey(msg)
	case modeMkdir:
		return m.handleMkdirKey(msg)
	case modeEdit:
		return m.handleEditKey(msg)
	}

	switch {
	case key.Matches(msg, m.keys.Quit):
		m.quitting = true
		return m, tea.Quit

	case key.Matches(msg, m.keys.Tab):
		if m.focus == focusLocal {
			m.focus = focusRemote
		} else {
			m.focus = focusLocal
		}
		return m, nil

	case key.Matches(msg, m.keys.Up):
		m.focusedPanel().moveCursor(-1)
		return m, nil
	case key.Matches(msg, m.keys.Down):
		m.focusedPanel().moveCursor(1)
		return m, nil
	case key.Matches(msg, m.keys.PgUp):
		m.focusedPanel().moveCursor(-m.focusedPanel().innerRows)
		return m, nil
	case key.Matches(msg, m.keys.PgDn):
		m.focusedPanel().moveCursor(m.focusedPanel().innerRows)
		return m, nil

	case key.Matches(msg, m.keys.Mark):
		p := m.focusedPanel()
		p.toggleMark()
		p.moveCursor(1) // rake down the list, MC-style
		return m, nil

	case key.Matches(msg, m.keys.Enter):
		return m.handleEnter()

	case key.Matches(msg, m.keys.Refresh):
		p := m.focusedPanel()
		return m, m.loadPanel(m.focus, p.cwd, cursorKeep, "")

	case key.Matches(msg, m.keys.Copy):
		return m.handleCopy()

	case key.Matches(msg, m.keys.Delete):
		return m.handleDelete()

	case key.Matches(msg, m.keys.Mkdir):
		return m.handleMkdir()

	case key.Matches(msg, m.keys.Edit):
		return m.handleEdit()
	}
	return m, nil
}

func (m Model) handleEnter() (tea.Model, tea.Cmd) {
	p := m.focusedPanel()
	sel := p.selected()
	if sel == nil || !sel.IsDir {
		return m, nil // files are a no-op in v1
	}
	paths := m.pathsFor(m.focus)
	if sel.Name == ".." {
		parent := paths.Dir(p.cwd)
		if parent == p.cwd {
			return m, nil // already at root
		}
		return m, m.loadPanel(m.focus, parent, cursorSelect, paths.Base(p.cwd))
	}
	child := paths.Join(p.cwd, sel.Name)
	return m, m.loadPanel(m.focus, child, cursorReset, "")
}

// --- view ---

func (m Model) View() string {
	if m.quitting {
		return ""
	}
	if m.width == 0 || m.height == 0 {
		return "Loading…"
	}

	bg := m.browseView()
	switch m.mode {
	case modeConfirm:
		return overlay.Composite(m.confirmDialog().box(m.width), bg, overlay.Center, overlay.Center, 0, 0)
	case modeProgress:
		return overlay.Composite(m.progressDialog().box(m.width), bg, overlay.Center, overlay.Center, 0, 0)
	case modeConfirmDelete:
		return overlay.Composite(m.confirmDeleteDialog().box(m.width), bg, overlay.Center, overlay.Center, 0, 0)
	case modeDeleting:
		return overlay.Composite(m.deletingDialog().box(m.width), bg, overlay.Center, overlay.Center, 0, 0)
	case modeMkdir:
		return overlay.Composite(m.mkdirDialog().box(m.width), bg, overlay.Center, overlay.Center, 0, 0)
	case modeEdit:
		// No dialog: the editor owns the screen while it runs, and the status
		// line covers the brief fetch/write-back moments the panels show.
		return bg
	}
	return bg
}

// browseView renders the two panels and the footer — the normal screen, and the
// background a dialog is composited over.
func (m Model) browseView() string {
	panels := lipgloss.JoinHorizontal(lipgloss.Top,
		m.local.render(m.focus == focusLocal),
		m.remote.render(m.focus == focusRemote),
	)
	return lipgloss.JoinVertical(lipgloss.Left, panels, m.footer())
}

func (m Model) footer() string {
	help := helpStyle.Render("Tab switch  ↑↓ move  ⏎ open  Space mark  F4 edit  F5 copy  F7 mkdir  F8 del  r refresh  q quit")
	st := statusStyle
	if m.statusErr {
		st = errorStyle
	}
	status := st.Render(m.status)

	gap := m.width - lipgloss.Width(help) - lipgloss.Width(status)
	if gap < 1 {
		// Not enough room for both; status wins.
		return truncate(status, m.width)
	}
	return help + strings.Repeat(" ", gap) + status
}

// layout recomputes panel geometry for the current terminal size. One footer
// line is reserved; the remaining height is split between the two bordered
// panels and the width is split evenly (left panel takes any odd column).
func (m *Model) layout() {
	const footerH = 1
	outerH := m.height - footerH
	innerH := outerH - 2 // borders
	if innerH < 1 {
		innerH = 1
	}

	leftOuter := m.width / 2
	rightOuter := m.width - leftOuter

	setGeom := func(p *Panel, outerW int) {
		w := outerW - 2 // borders
		if w < 1 {
			w = 1
		}
		p.width = w
		p.height = innerH
		p.innerRows = innerH - 1 // first inner line is the title
		if p.innerRows < 0 {
			p.innerRows = 0
		}
		p.clampScroll()
	}
	setGeom(&m.local, leftOuter)
	setGeom(&m.remote, rightOuter)
}

// --- helpers ---

func (m *Model) panelPtr(which focus) *Panel {
	if which == focusLocal {
		return &m.local
	}
	return &m.remote
}

func (m *Model) focusedPanel() *Panel { return m.panelPtr(m.focus) }

func (m Model) fsFor(which focus) panelFS {
	if which == focusLocal {
		return m.localFS
	}
	return m.remoteFS
}

// pathsFor returns the path semantics of the given side: OS-native for the
// local panel, POSIX for the pod. It is the path-math counterpart to fsFor.
func (m Model) pathsFor(which focus) pathFlavor {
	if which == focusLocal {
		return m.localPaths
	}
	return m.remotePaths
}

func indexOf(files []file.Info, name string) int {
	for i, f := range files {
		if f.Name == name {
			return i
		}
	}
	return 0
}
