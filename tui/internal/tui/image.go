package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/tuna-os/fisherman/tui/internal/config"
)

type imageModel struct {
	cfg      *config.InstallConfig
	form     *huh.Form
	selected string
	custom   string
}

func newImageModel(cfg *config.InstallConfig) *imageModel {
	m := &imageModel{cfg: cfg}
	m.selected = "ghcr.io/projectbluefin/dakota:latest"
	if cfg.Image != "" {
		m.selected = cfg.Image
	}

	m.form = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("OS Image").
				Description("Select the bootc image to install, or choose Custom to enter a URL.").
				Options(
					huh.NewOption("Bluefin (Fedora-based, GNOME)", "ghcr.io/projectbluefin/dakota:latest"),
					huh.NewOption("Aurora (Fedora-based, KDE)", "ghcr.io/ublue-os/aurora:latest"),
					huh.NewOption("CentOS Stream 10", "quay.io/centos-bootc/centos-bootc:stream10"),
					huh.NewOption("Debian", "ghcr.io/bootcrew/debian-bootc:latest"),
					huh.NewOption("Arch Linux", "ghcr.io/bootcrew/arch-bootc:latest"),
					huh.NewOption("Custom URL...", "custom"),
				).
				Value(&m.selected),
		),
		huh.NewGroup(
			huh.NewInput().
				Title("Custom image URL").
				Placeholder("registry.example.com/image:tag").
				Value(&m.custom).
				Validate(func(s string) error {
					if s == "" {
						return fmt.Errorf("image URL is required")
					}
					return nil
				}),
		).WithHideFunc(func() bool { return m.selected != "custom" }),
	)
	return m
}

func (m *imageModel) Init() tea.Cmd {
	return m.form.Init()
}

func (m *imageModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		if m.selected == "custom" {
			m.cfg.Image = m.custom
		} else {
			m.cfg.Image = m.selected
		}
		return m, func() tea.Msg { return stepMsg{next: stepDisk} }
	}
	return m, cmd
}

func (m *imageModel) View() string {
	var sb strings.Builder
	sb.WriteString("\n")
	sb.WriteString(titleStyle.Render("  Step 2 of 9: OS Image"))
	sb.WriteString("\n\n")
	sb.WriteString(m.form.View())
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render("  ↑/↓ navigate • Enter select • Ctrl+C quit"))
	return sb.String()
}
