package ui

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cosmocode/k8tc/internal/local"
	"github.com/cosmocode/k8tc/internal/transfer"
)

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

// Model is the Bubble Tea root model.
type Model struct {
	transfer  transfer.Transfer
	pod       string
	container string

	local  Panel
	remote Panel
	focus  focus

	width, height int

	status    string
	statusErr bool

	// In-flight transfer state.
	transferring  bool
	transferName  string
	transferTotal int64
	progressCh    chan int64

	keys     keyMap
	quitting bool
}

// New builds the initial model. localPath/remotePath are the starting dirs.
func New(t transfer.Transfer, pod, container, remotePath, localPath string) Model {
	remoteLabel := "POD " + pod
	if container != "" {
		remoteLabel += " [" + container + "]"
	}
	return Model{
		transfer:  t,
		pod:       pod,
		container: container,
		focus:     focusLocal,
		keys:      defaultKeys(),
		status:    "F5 to copy the highlighted entry to the other panel.",
		local:     Panel{label: "LOCAL", cwd: localPath},
		remote:    Panel{label: remoteLabel, cwd: remotePath, isRemote: true},
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.loadPanel(focusLocal, m.local.cwd, cursorReset, ""),
		m.loadPanel(focusRemote, m.remote.cwd, cursorReset, ""),
	)
}

// --- messages ---

type panelLoadedMsg struct {
	which   focus
	path    string
	files   []transfer.FileInfo
	err     error
	mode    cursorMode
	selName string
}

type transferProgressMsg struct{ n int64 }
type transferDoneMsg struct {
	err  error
	dest focus
	name string
}
type progressClosedMsg struct{}

// --- commands ---

func (m Model) loadPanel(which focus, p string, mode cursorMode, selName string) tea.Cmd {
	t, pod, container := m.transfer, m.pod, m.container
	return func() tea.Msg {
		var files []transfer.FileInfo
		var err error
		if which == focusLocal {
			files, err = local.List(p)
		} else {
			files, err = t.List(pod, container, p)
		}
		return panelLoadedMsg{which: which, path: p, files: files, err: err, mode: mode, selName: selName}
	}
}

func pushCmd(t transfer.Transfer, pod, container, localFull, remoteDest string, ch chan int64) tea.Cmd {
	return func() tea.Msg {
		err := t.Push(pod, container, localFull, remoteDest, func(n int64) {
			select {
			case ch <- n:
			default:
			}
		})
		close(ch)
		return transferDoneMsg{err: err, dest: focusRemote, name: filepath.Base(localFull)}
	}
}

func pullCmd(t transfer.Transfer, pod, container, remoteFull, localDest string, ch chan int64) tea.Cmd {
	return func() tea.Msg {
		err := t.Pull(pod, container, remoteFull, localDest, func(n int64) {
			select {
			case ch <- n:
			default:
			}
		})
		close(ch)
		return transferDoneMsg{err: err, dest: focusLocal, name: path.Base(remoteFull)}
	}
}

func listenProgress(ch chan int64) tea.Cmd {
	return func() tea.Msg {
		n, ok := <-ch
		if !ok {
			return progressClosedMsg{}
		}
		return transferProgressMsg{n: n}
	}
}

// --- update ---

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
		case cursorSelect:
			p.cursor = indexOf(msg.files, msg.selName)
		case cursorKeep:
			// leave cursor where it is; clampScroll fixes it up
		}
		p.clampScroll()
		return m, nil

	case transferProgressMsg:
		if !m.transferring {
			return m, nil
		}
		m.transferTotal = msg.n
		m.status = fmt.Sprintf("Copying %s… %s", m.transferName, humanSize(msg.n))
		return m, listenProgress(m.progressCh)

	case transferDoneMsg:
		m.transferring = false
		if msg.err != nil {
			m.status = "Error: " + msg.err.Error()
			m.statusErr = true
			return m, nil
		}
		m.status = fmt.Sprintf("Copied %s (%s)", msg.name, humanSize(m.transferTotal))
		m.statusErr = false
		dst := m.panelPtr(msg.dest)
		return m, m.loadPanel(msg.dest, dst.cwd, cursorKeep, "")

	case progressClosedMsg:
		return m, nil
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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

	case key.Matches(msg, m.keys.Enter):
		return m.handleEnter()

	case key.Matches(msg, m.keys.Refresh):
		p := m.focusedPanel()
		return m, m.loadPanel(m.focus, p.cwd, cursorKeep, "")

	case key.Matches(msg, m.keys.Copy):
		return m.handleCopy()
	}
	return m, nil
}

func (m Model) handleEnter() (tea.Model, tea.Cmd) {
	p := m.focusedPanel()
	sel := p.selected()
	if sel == nil || !sel.IsDir {
		return m, nil // files are a no-op in v1
	}
	if sel.Name == ".." {
		parent := parentPath(m.focus, p.cwd)
		if parent == p.cwd {
			return m, nil // already at root
		}
		return m, m.loadPanel(m.focus, parent, cursorSelect, baseName(m.focus, p.cwd))
	}
	child := joinPath(m.focus, p.cwd, sel.Name)
	return m, m.loadPanel(m.focus, child, cursorReset, "")
}

func (m Model) handleCopy() (tea.Model, tea.Cmd) {
	if m.transferring {
		return m, nil
	}
	src := m.focusedPanel()
	sel := src.selected()
	if sel == nil || sel.Name == ".." {
		return m, nil
	}

	m.transferring = true
	m.transferTotal = 0
	m.transferName = sel.Name
	m.statusErr = false
	m.progressCh = make(chan int64, 1)
	m.status = "Copying " + sel.Name + "…"

	var cmd tea.Cmd
	if m.focus == focusLocal {
		localFull := filepath.Join(m.local.cwd, sel.Name)
		cmd = pushCmd(m.transfer, m.pod, m.container, localFull, m.remote.cwd, m.progressCh)
	} else {
		remoteFull := path.Join(m.remote.cwd, sel.Name)
		cmd = pullCmd(m.transfer, m.pod, m.container, remoteFull, m.local.cwd, m.progressCh)
	}
	return m, tea.Batch(cmd, listenProgress(m.progressCh))
}

// --- view ---

func (m Model) View() string {
	if m.quitting {
		return ""
	}
	if m.width == 0 || m.height == 0 {
		return "Loading…"
	}
	panels := lipgloss.JoinHorizontal(lipgloss.Top,
		m.local.render(m.focus == focusLocal),
		m.remote.render(m.focus == focusRemote),
	)
	return lipgloss.JoinVertical(lipgloss.Left, panels, m.footer())
}

func (m Model) footer() string {
	help := helpStyle.Render("Tab switch  ↑↓ move  ⏎ open  F5 copy  r refresh  q quit")
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

func indexOf(files []transfer.FileInfo, name string) int {
	for i, f := range files {
		if f.Name == name {
			return i
		}
	}
	return 0
}

func joinPath(which focus, dir, name string) string {
	if which == focusLocal {
		return filepath.Join(dir, name)
	}
	return path.Join(dir, name)
}

func parentPath(which focus, dir string) string {
	if which == focusLocal {
		return filepath.Dir(dir)
	}
	return path.Dir(dir)
}

func baseName(which focus, dir string) string {
	if which == focusLocal {
		return filepath.Base(dir)
	}
	return path.Base(dir)
}
