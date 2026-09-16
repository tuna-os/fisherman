package tui

import "github.com/charmbracelet/lipgloss"

var (
	// Colors
	colorPrimary   = lipgloss.Color("#00AFFF") // bright blue
	colorSecondary = lipgloss.Color("#005F87") // dark blue
	colorAccent    = lipgloss.Color("#00D7AF") // teal
	colorMuted     = lipgloss.Color("#626262")
	colorSuccess   = lipgloss.Color("#00AF5F")
	colorWarning   = lipgloss.Color("#FFAF00")
	colorError     = lipgloss.Color("#FF5F5F")
	colorText      = lipgloss.Color("#E4E4E4")

	// Styles
	titleStyle = lipgloss.NewStyle().
			Foreground(colorPrimary).
			Bold(true)

	accentStyle = lipgloss.NewStyle().
			Foreground(colorAccent)

	mutedStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	successStyle = lipgloss.NewStyle().
			Foreground(colorSuccess).
			Bold(true)

	warningStyle = lipgloss.NewStyle().
			Foreground(colorWarning)

	errorStyle = lipgloss.NewStyle().
			Foreground(colorError).
			Bold(true)

	boxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorSecondary).
			Padding(1, 2)

	helpStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Italic(true)
)
