package ui

import (
	"context"
	"fmt"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

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

// startItem launches the transfer for the current job item and the listener
// that pumps its progress. It must run before the surrounding handler returns
// the model, since it records the item's progress channel on m.job.
func (m *Model) startItem() tea.Cmd {
	it := m.job.items[m.job.index]
	ch := make(chan int64, 1)
	m.job.progressCh = ch

	srcFull := m.pathsFor(m.job.src).Join(m.job.srcDir, it.name)
	if m.job.dest == focusRemote {
		return tea.Batch(
			pushCmd(m.job.ctx, m.xfer, srcFull, m.job.destDir, it.name, ch),
			listenProgress(ch),
		)
	}
	return tea.Batch(
		pullCmd(m.job.ctx, m.xfer, srcFull, m.job.destDir, it.name, ch),
		listenProgress(ch),
	)
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
