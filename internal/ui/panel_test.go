package ui

import (
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/cosmocode/k8tc/internal/file"
)

func TestHumanSize(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0B"},
		{512, "512B"},
		{1024, "1.0K"},
		{1536, "1.5K"},
		{1048576, "1.0M"},
		{1073741824, "1.0G"},
	}
	for _, c := range cases {
		if got := humanSize(c.n); got != c.want {
			t.Errorf("humanSize(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestFormatRowWidth(t *testing.T) {
	// A row must always render to exactly the panel width so highlighting and
	// borders line up, even with long names or unicode.
	for _, width := range []int{10, 20, 40} {
		for _, f := range []file.Info{
			{Name: "short.txt", Size: 12},
			{Name: "a-very-long-file-name-that-overflows-the-panel.tar.gz", Size: 999999},
			{Name: "dir", IsDir: true},
			{Name: "..", IsDir: true},
			{Name: "ünïcödé-名前.txt", Size: 3},
		} {
			row := formatRow(f, width)
			if got := lipgloss.Width(row); got != width {
				t.Errorf("formatRow(%q, %d) width = %d, want %d (%q)", f.Name, width, got, width, row)
			}
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("no-op truncate = %q", got)
	}
	if got := truncate("hello world", 5); utf8.RuneCountInString(got) != 5 {
		t.Errorf("truncate len = %d, want 5 (%q)", utf8.RuneCountInString(got), got)
	}
}
