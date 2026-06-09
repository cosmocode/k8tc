package ui

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cosmocode/k8tc/internal/file"
	overlay "github.com/rmhubbert/bubbletea-overlay"
)

// lister reads a directory on one side of the view (local disk or a pod). Both
// internal/local and internal/remote satisfy it, which is what lets a panel
// load itself without the model knowing which side it is.
type lister interface {
	List(path string) ([]file.Info, error)
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
	modeBrowse   uiMode = iota // normal two-panel browsing
	modeConfirm                // confirm-before-copy dialog
	modeProgress               // copy-in-progress dialog (with abort)
)

// copyItem is one entry queued for copying.
type copyItem struct {
	name  string
	isDir bool
	size  int64
}

// copyJob is a batch of items copied from one panel's directory into the
// other's. Items are transferred one at a time; the dialog reports progress and
// canceling ctx aborts the whole batch.
type copyJob struct {
	src     focus  // panel the items come from
	dest    focus  // panel they go to
	srcDir  string // source directory (src panel's cwd)
	destDir string // destination directory (dest panel's cwd)
	items   []copyItem

	index     int   // item currently transferring (0-based)
	curBytes  int64 // bytes streamed for the current item
	doneBytes int64 // bytes streamed for already-completed items
	failed    int   // items that errored (abort excluded)
	lastErr   error // most recent item error, for the summary

	ctx        context.Context
	cancel     context.CancelFunc
	aborted    bool
	progressCh chan int64 // progress channel for the current item
}

// Model is the Bubble Tea root model.
type Model struct {
	localList  lister
	remoteList lister
	xfer       transferer

	local  Panel
	remote Panel
	focus  focus

	width, height int

	status    string
	statusErr bool

	mode uiMode
	job  copyJob

	keys     keyMap
	quitting bool
}

// New builds the initial model. localList/remoteList load each panel and xfer
// performs copies between them; remoteLabel is the pod-side panel title and
// localPath/remotePath are the starting dirs.
func New(localList, remoteList lister, xfer transferer, remoteLabel, remotePath, localPath string) Model {
	return Model{
		localList:  localList,
		remoteList: remoteList,
		xfer:       xfer,
		focus:      focusLocal,
		keys:       defaultKeys(),
		status:     "Space to mark, F5 to copy to the other panel.",
		local:      Panel{label: "LOCAL", cwd: localPath},
		remote:     Panel{label: remoteLabel, cwd: remotePath, isRemote: true},
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
	files   []file.Info
	err     error
	mode    cursorMode
	selName string
}

// transferProgressMsg reports the running byte total for one item. It carries
// its channel so a stray update from a just-finished item (whose channel has
// been replaced) can be told apart from the current item's and ignored.
type transferProgressMsg struct {
	n  int64
	ch chan int64
}
type transferDoneMsg struct {
	err  error
	name string
}
type progressClosedMsg struct{}

// --- commands ---

func (m Model) loadPanel(which focus, p string, mode cursorMode, selName string) tea.Cmd {
	ls := m.listerFor(which)
	return func() tea.Msg {
		files, err := ls.List(p)
		return panelLoadedMsg{which: which, path: p, files: files, err: err, mode: mode, selName: selName}
	}
}

func pushCmd(ctx context.Context, x transferer, localFull, remoteDest, name string, ch chan int64) tea.Cmd {
	return func() tea.Msg {
		err := x.Push(ctx, localFull, remoteDest, func(n int64) {
			select {
			case ch <- n:
			default:
			}
		})
		close(ch)
		return transferDoneMsg{err: err, name: name}
	}
}

func pullCmd(ctx context.Context, x transferer, remoteFull, localDest, name string, ch chan int64) tea.Cmd {
	return func() tea.Msg {
		err := x.Pull(ctx, remoteFull, localDest, func(n int64) {
			select {
			case ch <- n:
			default:
			}
		})
		close(ch)
		return transferDoneMsg{err: err, name: name}
	}
}

func listenProgress(ch chan int64) tea.Cmd {
	return func() tea.Msg {
		n, ok := <-ch
		if !ok {
			return progressClosedMsg{}
		}
		return transferProgressMsg{n: n, ch: ch}
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

	case progressClosedMsg:
		return m, nil
	}
	return m, nil
}

// advanceJob records the outcome of the item that just finished and either
// starts the next one or finishes the batch.
func (m Model) advanceJob(msg transferDoneMsg) (tea.Model, tea.Cmd) {
	if m.job.aborted {
		return m.finishJob()
	}
	if msg.err != nil {
		m.job.failed++
		m.job.lastErr = msg.err
	}
	m.job.doneBytes += m.job.curBytes
	m.job.curBytes = 0
	m.job.index++
	if m.job.index >= len(m.job.items) {
		return m.finishJob()
	}
	cmd := m.startItem()
	return m, cmd
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeConfirm:
		return m.handleConfirmKey(msg)
	case modeProgress:
		return m.handleProgressKey(msg)
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
	}
	return m, nil
}

// handleConfirmKey drives the confirm-before-copy dialog: Enter starts the
// batch, Esc (or quit) backs out.
func (m Model) handleConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Enter):
		ctx, cancel := context.WithCancel(context.Background())
		m.job.ctx = ctx
		m.job.cancel = cancel
		m.job.index = 0
		m.job.curBytes = 0
		m.job.doneBytes = 0
		m.job.failed = 0
		m.job.lastErr = nil
		m.mode = modeProgress
		cmd := m.startItem()
		return m, cmd
	case key.Matches(msg, m.keys.Cancel), key.Matches(msg, m.keys.Quit):
		m.mode = modeBrowse
		m.job = copyJob{}
		m.status = "Copy cancelled."
		m.statusErr = false
		return m, nil
	}
	return m, nil
}

// handleProgressKey drives the in-progress dialog: Esc (or quit) aborts. The
// abort cancels the context; the in-flight item then returns an error and
// advanceJob tears the batch down.
func (m Model) handleProgressKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Cancel), key.Matches(msg, m.keys.Quit):
		if !m.job.aborted && m.job.cancel != nil {
			m.job.aborted = true
			m.job.cancel()
		}
		return m, nil
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

// handleCopy assembles a copy batch from the focused panel — the marked
// entries, or the highlighted one if nothing is marked — and opens the confirm
// dialog. No transfer starts until the user confirms.
func (m Model) handleCopy() (tea.Model, tea.Cmd) {
	src := m.focusedPanel()

	var items []copyItem
	for _, f := range src.markedInfos() {
		items = append(items, copyItem{name: f.Name, isDir: f.IsDir, size: f.Size})
	}
	if len(items) == 0 {
		sel := src.selected()
		if sel == nil || sel.Name == ".." {
			return m, nil
		}
		items = []copyItem{{name: sel.Name, isDir: sel.IsDir, size: sel.Size}}
	}

	dest := focusRemote
	if m.focus == focusRemote {
		dest = focusLocal
	}
	m.job = copyJob{
		src:     m.focus,
		dest:    dest,
		srcDir:  src.cwd,
		destDir: m.panelPtr(dest).cwd,
		items:   items,
	}
	m.mode = modeConfirm
	return m, nil
}

// startItem launches the transfer for the current job item and the listener
// that pumps its progress. It must run before the surrounding handler returns
// the model, since it records the item's progress channel on m.job.
func (m *Model) startItem() tea.Cmd {
	it := m.job.items[m.job.index]
	ch := make(chan int64, 1)
	m.job.progressCh = ch

	if m.job.dest == focusRemote {
		localFull := filepath.Join(m.job.srcDir, it.name)
		return tea.Batch(
			pushCmd(m.job.ctx, m.xfer, localFull, m.job.destDir, it.name, ch),
			listenProgress(ch),
		)
	}
	remoteFull := path.Join(m.job.srcDir, it.name)
	return tea.Batch(
		pullCmd(m.job.ctx, m.xfer, remoteFull, m.job.destDir, it.name, ch),
		listenProgress(ch),
	)
}

// finishJob ends the batch: it cancels the context, clears the source marks,
// reports a summary, reloads the destination panel and returns to browsing.
func (m Model) finishJob() (tea.Model, tea.Cmd) {
	done := m.job.index // items fully completed
	total := len(m.job.items)
	aborted := m.job.aborted
	failed := m.job.failed
	lastErr := m.job.lastErr
	dest := m.job.dest
	destDir := m.job.destDir

	if m.job.cancel != nil {
		m.job.cancel()
	}
	m.panelPtr(m.job.src).clearMarks()

	m.mode = modeBrowse
	m.job = copyJob{}

	switch {
	case aborted:
		m.status = fmt.Sprintf("Aborted — %d of %d copied", done, total)
		m.statusErr = true
	case failed > 0:
		m.status = fmt.Sprintf("Copied %d of %d, %d failed: %v", total-failed, total, failed, lastErr)
		m.statusErr = true
	default:
		m.status = fmt.Sprintf("Copied %d item%s", total, plural(total))
		m.statusErr = false
	}
	return m, m.loadPanel(dest, destDir, cursorKeep, "")
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

// confirmDialog is the "copy N items?" prompt shown before a batch starts.
func (m Model) confirmDialog() dialog {
	n := len(m.job.items)
	files, dirs := 0, 0
	for _, it := range m.job.items {
		if it.isDir {
			dirs++
		} else {
			files++
		}
	}

	var what string
	if n == 1 {
		what = m.job.items[0].name
		if m.job.items[0].isDir {
			what += "/"
		}
	} else {
		what = countPhrase(files, dirs)
	}

	return dialog{
		title: fmt.Sprintf("Copy %d item%s?", n, plural(n)),
		body: []string{
			what,
			"→ " + m.panelPtr(m.job.dest).label + ": " + m.job.destDir,
		},
		footer: "[ Enter ] Copy    [ Esc ] Cancel",
	}
}

// progressDialog reports the running batch and offers the abort key.
func (m Model) progressDialog() dialog {
	n := len(m.job.items)
	it := m.job.items[m.job.index]
	name := it.name
	if it.isDir {
		name += "/"
	}

	title := "Copying…"
	if m.job.aborted {
		title = "Aborting…"
	}

	return dialog{
		title: title,
		body: []string{
			fmt.Sprintf("Item %d of %d: %s", m.job.index+1, n, name),
			humanSize(m.job.doneBytes+m.job.curBytes) + " transferred",
			"→ " + m.panelPtr(m.job.dest).label + ": " + m.job.destDir,
		},
		footer: "[ Esc ] Abort",
	}
}

func (m Model) footer() string {
	help := helpStyle.Render("Tab switch  ↑↓ move  ⏎ open  Space mark  F5 copy  r refresh  q quit")
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

func (m Model) listerFor(which focus) lister {
	if which == focusLocal {
		return m.localList
	}
	return m.remoteList
}

func indexOf(files []file.Info, name string) int {
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
