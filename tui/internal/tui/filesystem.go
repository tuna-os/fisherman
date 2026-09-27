package tui

import (
	"fmt"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/tuna-os/fisherman/tui/internal/config"
)

type filesystemModel struct {
	cfg      *config.InstallConfig
	form     *huh.Form
	fsChoice string
	hostname string
}

func newFilesystemModel(cfg *config.InstallConfig) *filesystemModel {
	m := &filesystemModel{cfg: cfg}
	m.fsChoice = cfg.Filesystem
	if m.fsChoice == "" {
		m.fsChoice = "ext4"
	}
	m.hostname = cfg.Hostname
	if m.hostname == "" {
		m.hostname = "localhost"
	}

	hostnameRe := regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9\-]{0,62}$`)

	m.form = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Root filesystem").
				Description("Choose the root filesystem type and set the hostname for the installed system.").
				Options(
					huh.NewOption("ext4 — stable, widely supported", "ext4"),
					huh.NewOption("xfs  — high performance, recommended for servers", "xfs"),
					huh.NewOption("btrfs — copy-on-write, snapshots", "btrfs"),
				).
				Value(&m.fsChoice),
			huh.NewInput().
				Title("Hostname").
				Placeholder("localhost").
				Value(&m.hostname).
				Validate(func(s string) error {
					if s == "" {
						return fmt.Errorf("hostname is required")
					}
					if !hostnameRe.MatchString(s) {
						return fmt.Errorf("hostname must be alphanumeric/hyphens, start with letter/digit")
					}
					return nil
				}),
		),
	)
	return m
}

func (m *filesystemModel) Init() tea.Cmd {
	return m.form.Init()
}

func (m *filesystemModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		m.cfg.Filesystem = m.fsChoice
		m.cfg.Hostname = m.hostname
		return m, func() tea.Msg { return stepMsg{next: stepEncryption} }
	}
	return m, cmd
}

func (m *filesystemModel) View() string {
	var sb strings.Builder
	sb.WriteString("\n")
	sb.WriteString(titleStyle.Render("  Step 4 of 9: Filesystem & Hostname"))
	sb.WriteString("\n\n")
	sb.WriteString(m.form.View())
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render("  ↑/↓ navigate • Enter select/next • Ctrl+C quit"))
	return sb.String()
}
