package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/cosmocode/k8tc/internal/file"
)

// Panel is one side of the two-panel view. It owns its own cwd, file list,
// cursor and scroll offset. width/height/innerRows are the rendering geometry
// set by the model on resize.
type Panel struct {
	label    string // "LOCAL" or "POD nginx-abc"
	cwd      string
	files    []file.Info
	cursor   int
	offset   int
	isRemote bool

	width     int // inner content width (inside the border)
	height    int // inner content height (title + rows)
	innerRows int // file rows visible (height - 1)
}

func (p *Panel) selected() *file.Info {
	if p.cursor < 0 || p.cursor >= len(p.files) {
		return nil
	}
	return &p.files[p.cursor]
}

func (p *Panel) moveCursor(delta int) {
	p.cursor += delta
	p.clampScroll()
}

func (p *Panel) clampScroll() {
	if p.cursor < 0 {
		p.cursor = 0
	}
	if p.cursor >= len(p.files) {
		p.cursor = len(p.files) - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
	if p.innerRows <= 0 {
		return
	}
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+p.innerRows {
		p.offset = p.cursor - p.innerRows + 1
	}
	if p.offset < 0 {
		p.offset = 0
	}
}

// render draws the bordered panel. focused selects the border highlight and
// whether the cursor row is shown with the active or inactive style.
func (p *Panel) render(focused bool) string {
	if p.width < 1 || p.height < 1 {
		return ""
	}

	title := titleStyle.Render(padRight(truncate(p.label+": "+p.cwd, p.width), p.width))

	rows := make([]string, 0, p.innerRows)
	for i := 0; i < p.innerRows; i++ {
		idx := p.offset + i
		if idx >= len(p.files) {
			rows = append(rows, strings.Repeat(" ", p.width))
			continue
		}
		f := p.files[idx]
		row := formatRow(f, p.width)
		switch {
		case idx == p.cursor && focused:
			row = cursorStyle.Render(row)
		case idx == p.cursor:
			row = cursorBlurStyle.Render(row)
		case f.IsDir:
			row = dirStyle.Render(row)
		default:
			row = fileStyle.Render(row)
		}
		rows = append(rows, row)
	}

	body := title + "\n" + strings.Join(rows, "\n")
	style := borderStyle
	if focused {
		style = borderFocusStyle
	}
	return style.Width(p.width).Height(p.height).Render(body)
}

// formatRow lays out one entry: name on the left (dirs get a trailing "/"),
// human-readable size right-aligned for files. The result is exactly width
// display cells wide so row highlighting fills the panel.
func formatRow(f file.Info, width int) string {
	if width <= 2 {
		return strings.Repeat(" ", max(0, width))
	}
	inner := width - 2 // one space of padding on each side

	name := f.Name
	if f.IsDir && f.Name != ".." {
		name += "/"
	}
	size := ""
	if !f.IsDir {
		size = humanSize(f.Size)
	}

	var content string
	if size != "" && lipgloss.Width(size)+2 <= inner {
		nameW := inner - lipgloss.Width(size) - 1
		content = padRight(truncate(name, nameW), nameW) + " " + size
	} else {
		content = truncate(name, inner)
	}
	return " " + padRight(content, inner) + " "
}

// humanSize renders a byte count compactly (e.g. 1.2M).
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", float64(n)/float64(div), "KMGTPE"[exp])
}

// truncate shortens s to at most max display cells (not runes, so wide CJK
// glyphs count as two), adding an ellipsis if it had to cut.
func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= max {
		return s
	}
	return ansi.Truncate(s, max, "…")
}

// padRight pads s with spaces to w display cells (no-op if already wider).
func padRight(s string, w int) string {
	n := w - lipgloss.Width(s)
	if n <= 0 {
		return s
	}
	return s + strings.Repeat(" ", n)
}
