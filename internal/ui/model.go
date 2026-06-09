package ui

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
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
)

// item is one entry queued for a batch operation. size is the source size, used
// to report copy progress; deletion ignores it.
type item struct {
	name  string
	isDir bool
	size  int64
}

// batch is the shared state of an abortable, sequential batch operation (copy
// or delete): the queue and the bookkeeping for working through it one item at
// a time. ctx/cancel let the in-flight item be aborted along with the rest of
// the queue, and aborted records that the user asked to stop. index is the item
// currently being processed (0-based); failed counts items that errored (an
// abort is not a failure) and lastErr is the most recent error, for the summary.
type batch struct {
	items []item

	index   int
	failed  int
	lastErr error

	ctx     context.Context
	cancel  context.CancelFunc
	aborted bool
}

// begin readies a confirmed batch to run: a fresh cancelable context and zeroed
// counters.
func (b *batch) begin() {
	b.ctx, b.cancel = context.WithCancel(context.Background())
	b.index = 0
	b.failed = 0
	b.lastErr = nil
}

// requestAbort marks the batch aborted and cancels its context, killing the
// in-flight item (e.g. a remote tar/rm) so the queue can tear down. It is a
// no-op once already aborted.
func (b *batch) requestAbort() {
	if !b.aborted && b.cancel != nil {
		b.aborted = true
		b.cancel()
	}
}

// recordResult tallies the item that just finished and advances the index,
// returning true when the batch is complete. An aborted batch is complete at
// once and the aborting error is not counted as a failure.
func (b *batch) recordResult(err error) (done bool) {
	if b.aborted {
		return true
	}
	if err != nil {
		b.failed++
		b.lastErr = err
	}
	b.index++
	return b.index >= len(b.items)
}

// copyJob is a batch of items copied from one panel's directory into the
// other's. Items are transferred one at a time; the dialog reports progress and
// canceling ctx aborts the whole batch.
type copyJob struct {
	batch
	src     focus  // panel the items come from
	dest    focus  // panel they go to
	srcDir  string // source directory (src panel's cwd)
	destDir string // destination directory (dest panel's cwd)

	curBytes   int64      // bytes streamed for the current item
	doneBytes  int64      // bytes streamed for already-completed items
	progressCh chan int64 // progress channel for the current item
}

// deleteJob is a batch of entries being removed from one panel's directory.
// Items are deleted one at a time; the dialog reports progress and canceling
// ctx aborts the remaining items. Already-deleted entries are gone for good —
// an abort stops the queue, it does not roll back.
type deleteJob struct {
	batch
	side focus  // panel the items are deleted from
	dir  string // directory they live in (side panel's cwd)
}

// mkdirState is the new-folder prompt. Unlike copy and delete it is not a batch:
// there is no item queue, no sequential loop and nothing to abort — just the
// panel and directory the folder lands in and a text input collecting its name.
type mkdirState struct {
	side  focus  // panel the folder is created in
	dir   string // directory it lands in (side panel's cwd)
	input textinput.Model
}

// Model is the Bubble Tea root model.
type Model struct {
	localFS  panelFS
	remoteFS panelFS
	xfer     transferer

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

	keys     keyMap
	quitting bool
}

// New builds the initial model. localFS/remoteFS load and delete on each panel
// and xfer performs copies between them; remoteLabel is the pod-side panel
// title and localPath/remotePath are the starting dirs.
func New(localFS, remoteFS panelFS, xfer transferer, remoteLabel, remotePath, localPath string) Model {
	return Model{
		localFS:  localFS,
		remoteFS: remoteFS,
		xfer:     xfer,
		focus:    focusLocal,
		keys:     defaultKeys(),
		status:   "Space to mark, F5 to copy, F8 to delete.",
		local:    Panel{label: "LOCAL", cwd: localPath},
		remote:   Panel{label: remoteLabel, cwd: remotePath, isRemote: true},
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

// deleteDoneMsg reports the outcome of one item's deletion.
type deleteDoneMsg struct {
	err  error
	name string
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

// --- commands ---

func (m Model) loadPanel(which focus, p string, mode cursorMode, selName string) tea.Cmd {
	fs := m.fsFor(which)
	return func() tea.Msg {
		files, err := fs.List(p)
		return panelLoadedMsg{which: which, path: p, files: files, err: err, mode: mode, selName: selName}
	}
}

// deleteCmd removes one entry on the given side, reporting the outcome. The
// context lets an in-flight remote delete be aborted along with the rest of the
// batch.
func deleteCmd(ctx context.Context, fs panelFS, full, name string) tea.Cmd {
	return func() tea.Msg {
		err := fs.Delete(ctx, full)
		return deleteDoneMsg{err: err, name: name}
	}
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

// advanceJob records the outcome of the item that just finished and either
// starts the next one or finishes the batch.
func (m Model) advanceJob(msg transferDoneMsg) (tea.Model, tea.Cmd) {
	done := m.job.recordResult(msg.err)
	if !m.job.aborted {
		m.job.doneBytes += m.job.curBytes
		m.job.curBytes = 0
	}
	if done {
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
	case modeConfirmDelete:
		return m.handleConfirmDeleteKey(msg)
	case modeDeleting:
		return m.handleDeletingKey(msg)
	case modeMkdir:
		return m.handleMkdirKey(msg)
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
	}
	return m, nil
}

// handleConfirmKey drives the confirm-before-copy dialog: Enter starts the
// batch, Esc (or quit) backs out.
func (m Model) handleConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Enter):
		m.job.begin()
		m.job.curBytes = 0
		m.job.doneBytes = 0
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
		m.job.requestAbort()
		return m, nil
	}
	return m, nil
}

// handleConfirmDeleteKey drives the confirm-before-delete dialog: Enter starts
// the batch, Esc (or quit) backs out without deleting anything.
func (m Model) handleConfirmDeleteKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Enter):
		m.del.begin()
		m.mode = modeDeleting
		cmd := m.startDelete()
		return m, cmd
	case key.Matches(msg, m.keys.Cancel), key.Matches(msg, m.keys.Quit):
		m.mode = modeBrowse
		m.del = deleteJob{}
		m.status = "Delete cancelled."
		m.statusErr = false
		return m, nil
	}
	return m, nil
}

// handleDeletingKey drives the in-progress delete dialog: Esc (or quit) aborts.
// The abort cancels the context (killing an in-flight remote rm) and stops the
// queue before the next item; entries already removed stay removed.
func (m Model) handleDeletingKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Cancel), key.Matches(msg, m.keys.Quit):
		m.del.requestAbort()
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

// gatherBatch collects the entries a batch operation should act on: the panel's
// marked entries, or its highlighted one if nothing is marked. It returns nil
// when there is nothing actionable (an empty panel, or only ".." selected).
func gatherBatch(p *Panel) []item {
	var items []item
	for _, f := range p.markedInfos() {
		items = append(items, item{name: f.Name, isDir: f.IsDir, size: f.Size})
	}
	if len(items) == 0 {
		sel := p.selected()
		if sel == nil || sel.Name == ".." {
			return nil
		}
		items = []item{{name: sel.Name, isDir: sel.IsDir, size: sel.Size}}
	}
	return items
}

// handleCopy assembles a copy batch from the focused panel — the marked
// entries, or the highlighted one if nothing is marked — and opens the confirm
// dialog. No transfer starts until the user confirms.
func (m Model) handleCopy() (tea.Model, tea.Cmd) {
	src := m.focusedPanel()
	items := gatherBatch(src)
	if items == nil {
		return m, nil
	}

	dest := focusRemote
	if m.focus == focusRemote {
		dest = focusLocal
	}
	m.job = copyJob{
		batch:   batch{items: items},
		src:     m.focus,
		dest:    dest,
		srcDir:  src.cwd,
		destDir: m.panelPtr(dest).cwd,
	}
	m.mode = modeConfirm
	return m, nil
}

// handleDelete assembles a delete batch from the focused panel — the marked
// entries, or the highlighted one if nothing is marked — and opens the confirm
// dialog. No entry is removed until the user confirms.
func (m Model) handleDelete() (tea.Model, tea.Cmd) {
	src := m.focusedPanel()
	items := gatherBatch(src)
	if items == nil {
		return m, nil
	}

	m.del = deleteJob{
		batch: batch{items: items},
		side:  m.focus,
		dir:   src.cwd,
	}
	m.mode = modeConfirmDelete
	return m, nil
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
		full := joinPath(side, dir, name)
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

// startDelete launches the deletion of the current job item. It reads
// m.del.index, so the surrounding handler must have it set.
func (m *Model) startDelete() tea.Cmd {
	it := m.del.items[m.del.index]
	full := joinPath(m.del.side, m.del.dir, it.name)
	return deleteCmd(m.del.ctx, m.fsFor(m.del.side), full, it.name)
}

// advanceDelete records the outcome of the item that just finished and either
// starts the next one or finishes the batch.
func (m Model) advanceDelete(msg deleteDoneMsg) (tea.Model, tea.Cmd) {
	if m.del.recordResult(msg.err) {
		return m.finishDelete()
	}
	return m, m.startDelete()
}

// finishDelete ends the batch: it cancels the context, clears the panel's
// marks, reports a summary, reloads the panel and returns to browsing.
func (m Model) finishDelete() (tea.Model, tea.Cmd) {
	done := m.del.index // items fully completed
	total := len(m.del.items)
	aborted := m.del.aborted
	failed := m.del.failed
	lastErr := m.del.lastErr
	side := m.del.side
	dir := m.del.dir

	if m.del.cancel != nil {
		m.del.cancel()
	}
	m.panelPtr(side).clearMarks()

	m.mode = modeBrowse
	m.del = deleteJob{}

	switch {
	case aborted:
		m.status = fmt.Sprintf("Aborted — %d of %d deleted", done, total)
		m.statusErr = true
	case failed > 0:
		m.status = fmt.Sprintf("Deleted %d of %d, %d failed: %v", total-failed, total, failed, lastErr)
		m.statusErr = true
	default:
		m.status = fmt.Sprintf("Deleted %d item%s", total, plural(total))
		m.statusErr = false
	}
	return m, m.loadPanel(side, dir, cursorKeep, "")
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

// whatPhrase describes a batch for a confirm dialog: the lone entry's name
// (directories get a trailing slash) when there's just one, otherwise a
// "2 files, 1 directory" summary.
func whatPhrase(items []item) string {
	if len(items) == 1 {
		what := items[0].name
		if items[0].isDir {
			what += "/"
		}
		return what
	}
	files, dirs := 0, 0
	for _, it := range items {
		if it.isDir {
			dirs++
		} else {
			files++
		}
	}
	return countPhrase(files, dirs)
}

// confirmDialog is the "copy N items?" prompt shown before a batch starts.
func (m Model) confirmDialog() dialog {
	n := len(m.job.items)
	return dialog{
		title: fmt.Sprintf("Copy %d item%s?", n, plural(n)),
		body: []string{
			whatPhrase(m.job.items),
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

// confirmDeleteDialog is the "delete N items?" prompt shown before a batch
// starts. It is styled as a destructive action and spells out that deletes are
// recursive and irreversible.
func (m Model) confirmDeleteDialog() dialog {
	n := len(m.del.items)
	return dialog{
		title: fmt.Sprintf("Delete %d item%s?", n, plural(n)),
		body: []string{
			whatPhrase(m.del.items),
			"from " + m.panelPtr(m.del.side).label + ": " + m.del.dir,
			"Directories are removed recursively. This cannot be undone.",
		},
		footer: "[ Enter ] Delete    [ Esc ] Cancel",
		danger: true,
	}
}

// deletingDialog reports the running delete batch and offers the abort key.
func (m Model) deletingDialog() dialog {
	n := len(m.del.items)
	it := m.del.items[m.del.index]
	name := it.name
	if it.isDir {
		name += "/"
	}

	title := "Deleting…"
	if m.del.aborted {
		title = "Aborting…"
	}

	return dialog{
		title: title,
		body: []string{
			fmt.Sprintf("Item %d of %d: %s", m.del.index+1, n, name),
			"from " + m.panelPtr(m.del.side).label + ": " + m.del.dir,
		},
		footer: "[ Esc ] Abort",
		danger: true,
	}
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

func (m Model) footer() string {
	help := helpStyle.Render("Tab switch  ↑↓ move  ⏎ open  Space mark  F5 copy  F7 mkdir  F8 del  r refresh  q quit")
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
