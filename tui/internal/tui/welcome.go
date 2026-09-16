package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const banner = `
 ██████╗  ██████╗  ██████╗ ████████╗ ██████╗
 ██╔══██╗██╔═══██╗██╔═══██╗╚══██╔══╝██╔════╝
 ██████╔╝██║   ██║██║   ██║   ██║   ██║
 ██╔══██╗██║   ██║██║   ██║   ██║   ██║
 ██████╔╝╚██████╔╝╚██████╔╝   ██║   ╚██████╗
 ╚═════╝  ╚═════╝  ╚═════╝    ╚═╝    ╚═════╝

  ██╗███╗   ██╗███████╗████████╗ █████╗ ██╗     ██╗     ███████╗██████╗
  ██║████╗  ██║██╔════╝╚══██╔══╝██╔══██╗██║     ██║     ██╔════╝██╔══██╗
  ██║██╔██╗ ██║███████╗   ██║   ███████║██║     ██║     █████╗  ██████╔╝
  ██║██║╚██╗██║╚════██║   ██║   ██╔══██║██║     ██║     ██╔══╝  ██╔══██╗
  ██║██║ ╚████║███████║   ██║   ██║  ██║███████╗███████╗███████╗██║  ██║
  ╚═╝╚═╝  ╚═══╝╚══════╝   ╚═╝   ╚═╝  ╚═╝╚══════╝╚══════╝╚══════╝╚═╝  ╚═╝
`

type welcomeModel struct{}

func newWelcomeModel() *welcomeModel {
	return &welcomeModel{}
}

func (m *welcomeModel) Init() tea.Cmd { return nil }

func (m *welcomeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "enter", " ":
			return m, func() tea.Msg { return stepMsg{next: stepNetwork} }
		}
	}
	return m, nil
}

func (m *welcomeModel) View() string {
	bannerStyle := lipgloss.NewStyle().
		Foreground(colorPrimary).
		Bold(true)

	subtitle := lipgloss.NewStyle().
		Foreground(colorAccent).
		Italic(true).
		Render("  bootc-based OS installer — powered by fisherman")

	hint := lipgloss.NewStyle().
		Foreground(colorMuted).
		Render("\n  Press Enter to begin  •  Ctrl+C to quit")

	return "\n" + bannerStyle.Render(banner) + "\n" + subtitle + "\n" + hint + "\n"
}
