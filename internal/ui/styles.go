package ui

import "github.com/charmbracelet/lipgloss"

// Color palette (ANSI 256). Kept in one place so the look is easy to tweak.
var (
	colorFocus     = lipgloss.Color("63")  // bright purple — focused border
	colorBlur      = lipgloss.Color("240") // dim gray — unfocused border
	colorDir       = lipgloss.Color("39")  // blue — directories
	colorTitle     = lipgloss.Color("230") // near-white — panel titles
	colorHelp      = lipgloss.Color("241") // gray — footer help
	colorStatus    = lipgloss.Color("252") // light gray — status text
	colorError     = lipgloss.Color("203") // red — errors
	colorCurBg     = lipgloss.Color("63")  // focused cursor row background
	colorCurFg     = lipgloss.Color("230")
	colorCurBlurBg = lipgloss.Color("238") // unfocused cursor row background
	colorCurBlurFg = lipgloss.Color("252")
	colorMark      = lipgloss.Color("220") // yellow — marked entries
)

var (
	borderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBlur)
	borderFocusStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorFocus)

	titleStyle = lipgloss.NewStyle().Foreground(colorTitle).Bold(true)
	dirStyle   = lipgloss.NewStyle().Foreground(colorDir).Bold(true)
	fileStyle  = lipgloss.NewStyle()

	cursorStyle     = lipgloss.NewStyle().Background(colorCurBg).Foreground(colorCurFg)
	cursorBlurStyle = lipgloss.NewStyle().Background(colorCurBlurBg).Foreground(colorCurBlurFg)
	markedStyle     = lipgloss.NewStyle().Foreground(colorMark).Bold(true)

	helpStyle   = lipgloss.NewStyle().Foreground(colorHelp)
	statusStyle = lipgloss.NewStyle().Foreground(colorStatus)
	errorStyle  = lipgloss.NewStyle().Foreground(colorError).Bold(true)

	// Dialog (centered modal) styling.
	dialogStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorFocus).
			Padding(0, 1)
	// dialogDangerStyle marks a destructive confirmation (delete) with a red
	// border so it reads differently from the ordinary copy dialog.
	dialogDangerStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorError).
				Padding(0, 1)
	dialogTitleStyle  = lipgloss.NewStyle().Foreground(colorTitle).Bold(true)
	dialogButtonStyle = lipgloss.NewStyle().Foreground(colorHelp)
)
