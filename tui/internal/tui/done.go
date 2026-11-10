package tui

import (
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/tuna-os/fisherman/tui/internal/config"
)

type doneModel struct {
	cfg        *config.InstallConfig
	form       *huh.Form
	reboot     bool
	dryRun     bool
	recipeJSON string
}

func newDoneModel(cfg *config.InstallConfig, dryRun bool) *doneModel {
	m := &doneModel{cfg: cfg, dryRun: dryRun}

	if dryRun {
		// The recipe as fisherman would get it on stdin, with the
		// passphrase and the password masked. Nothing is written to disk.
		recipe, err := cfg.RedactedRecipe()
		if err != nil {
			recipe = "No recipe: " + err.Error()
		}
		m.recipeJSON = recipe
		m.form = huh.NewForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title("Dry run complete").
					Description("The recipe above is what fisherman would get on stdin (secrets masked).").
					Affirmative("Exit").
					Negative("Exit").
					Value(&m.reboot),
			),
		)
	} else {
		m.reboot = true
		m.form = huh.NewForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title("Reboot now?").
					Description("Remove the installation media before rebooting.").
					Affirmative("Reboot").
					Negative("Stay in installer").
					Value(&m.reboot),
			),
		)
	}
	return m
}

func (m *doneModel) Init() tea.Cmd {
	return m.form.Init()
}

func (m *doneModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	}
	form, cmd := m.form.Update(msg)
	if f, ok := form.(*huh.Form); ok {
		m.form = f
	}
	if m.form.State == huh.StateCompleted {
		if !m.dryRun && m.reboot {
			exec.Command("reboot").Run()
		}
		return m, tea.Quit
	}
	return m, cmd
}

func (m *doneModel) View() string {
	var sb strings.Builder
	sb.WriteString("\n\n")

	if m.dryRun {
		sb.WriteString(accentStyle.Render("  ⓘ  Dry Run Complete — recipe JSON"))
		sb.WriteString("\n\n")
		codeStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorSecondary).
			Padding(0, 1).
			Foreground(colorText)
		sb.WriteString(codeStyle.Render(m.recipeJSON))
		sb.WriteString("\n\n")
		sb.WriteString(mutedStyle.Render("  Recipe rendered successfully during dry run."))
	} else {
		sb.WriteString(successStyle.Render("  ✓ Installation Complete!"))
		sb.WriteString("\n\n")
		sb.WriteString(accentStyle.Render("  The system has been installed successfully."))
		sb.WriteString("\n")
		sb.WriteString(mutedStyle.Render("  You can now reboot into your new system."))
	}

	sb.WriteString("\n\n")
	sb.WriteString(m.form.View())
	return sb.String()
}
