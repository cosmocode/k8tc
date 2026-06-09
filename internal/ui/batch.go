package ui

import "context"

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
