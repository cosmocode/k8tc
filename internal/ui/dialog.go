package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// dialog is a modal box: a bold title, a blank line, body lines, and a dim
// footer holding the key legend. box() renders just the bordered box; the model
// composites it over the panel view (see View), so the panels stay visible
// behind it.
type dialog struct {
	title  string
	body   []string
	footer string
}

// box renders the dialog as a bordered box sized to its content, capped at
// maxWidth display cells (including border and padding) so it can't overflow a
// narrow terminal.
func (d dialog) box(maxWidth int) string {
	contentW := lipgloss.Width(d.title)
	for _, l := range d.body {
		if w := lipgloss.Width(l); w > contentW {
			contentW = w
		}
	}
	if w := lipgloss.Width(d.footer); w > contentW {
		contentW = w
	}
	// Leave room for the border (2) and the horizontal padding (2).
	if cap := maxWidth - 4; cap > 0 && contentW > cap {
		contentW = cap
	}
	if contentW < 1 {
		contentW = 1
	}

	lines := make([]string, 0, len(d.body)+3)
	lines = append(lines, padRight(dialogTitleStyle.Render(truncate(d.title, contentW)), contentW))
	lines = append(lines, strings.Repeat(" ", contentW))
	for _, l := range d.body {
		lines = append(lines, padRight(truncate(l, contentW), contentW))
	}
	if d.footer != "" {
		lines = append(lines, strings.Repeat(" ", contentW))
		lines = append(lines, padRight(dialogButtonStyle.Render(truncate(d.footer, contentW)), contentW))
	}

	return dialogStyle.Render(strings.Join(lines, "\n"))
}

// plural returns "s" unless n is 1, for simple "N item(s)" phrasing.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// countPhrase renders a "2 files, 1 directory" style summary, omitting either
// half when its count is zero.
func countPhrase(files, dirs int) string {
	var parts []string
	if dirs > 0 {
		word := "directories"
		if dirs == 1 {
			word = "directory"
		}
		parts = append(parts, fmt.Sprintf("%d %s", dirs, word))
	}
	if files > 0 {
		parts = append(parts, fmt.Sprintf("%d file%s", files, plural(files)))
	}
	return strings.Join(parts, ", ")
}
