package ui

import (
	"context"
	"fmt"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// deleteJob is a batch of entries being removed from one panel's directory.
// Items are deleted one at a time; the dialog reports progress and canceling
// ctx aborts the remaining items. Already-deleted entries are gone for good —
// an abort stops the queue, it does not roll back.
type deleteJob struct {
	batch
	side focus  // panel the items are deleted from
	dir  string // directory they live in (side panel's cwd)
}

// deleteDoneMsg reports the outcome of one item's deletion.
type deleteDoneMsg struct {
	err  error
	name string
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

// startDelete launches the deletion of the current job item. It reads
// m.del.index, so the surrounding handler must have it set.
func (m *Model) startDelete() tea.Cmd {
	it := m.del.items[m.del.index]
	full := m.pathsFor(m.del.side).Join(m.del.dir, it.name)
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
