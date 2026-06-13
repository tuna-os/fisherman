package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/projectbluefin/fisherman/tui/internal/config"
)

type confirmModel struct {
	cfg     *config.InstallConfig
	form    *huh.Form
	proceed bool
	dryRun  bool
}

func newConfirmModel(cfg *config.InstallConfig, dryRun bool) *confirmModel {
	m := &confirmModel{cfg: cfg, dryRun: dryRun}
	m.proceed = false

	desc := "Review the summary above. This will ERASE the target disk."
	affirmative := "Install now"
	if dryRun {
		desc = "Dry-run mode: no changes will be made. The recipe JSON will be shown at the end."
		affirmative = "Run dry-run"
	}

	m.form = huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Start installation?").
				Description(desc).
				Affirmative(affirmative).
				Negative("Go back").
				Value(&m.proceed),
		),
	)
	return m
}

func (m *confirmModel) Init() tea.Cmd {
	return m.form.Init()
}

func (m *confirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		if m.proceed {
			return m, func() tea.Msg { return stepMsg{next: stepProgress} }
		}
		return m, func() tea.Msg { return stepMsg{next: stepUser} }
	}
	return m, cmd
}

func (m *confirmModel) View() string {
	var sb strings.Builder
	sb.WriteString("\n")
	sb.WriteString(titleStyle.Render("  Step 8 of 9: Confirm Installation"))
	sb.WriteString("\n\n")

	labelStyle := lipgloss.NewStyle().Foreground(colorMuted).Width(18)
	valueStyle := lipgloss.NewStyle().Foreground(colorText).Bold(true)

	row := func(label, value string) string {
		return "  " + labelStyle.Render(label+":") + " " + valueStyle.Render(value) + "\n"
	}

	encStr := "Disabled"
	if m.cfg.EncryptionEnabled {
		encStr = "LUKS (passphrase)"
	}

	keysStr := "None"
	if len(m.cfg.SSHKeys) > 0 {
		keysStr = fmt.Sprintf("%d key(s) imported", len(m.cfg.SSHKeys))
	}

	sb.WriteString(boxStyle.Render(
		titleStyle.Render("Installation Summary") + "\n\n" +
			row("Image", m.cfg.Image) +
			row("Disk", m.cfg.DiskDevice) +
			row("Filesystem", m.cfg.Filesystem) +
			row("Hostname", m.cfg.Hostname) +
			row("Encryption", encStr) +
			row("Username", m.cfg.Username) +
			row("Full name", m.cfg.FullName) +
			row("SSH keys", keysStr),
	))

	sb.WriteString("\n\n")
	if m.dryRun {
		sb.WriteString(accentStyle.Render("  ⓘ  DRY RUN — no changes will be made to any disk"))
	} else {
		sb.WriteString(warningStyle.Render("  ⚠  ALL DATA ON " + m.cfg.DiskDevice + " WILL BE DESTROYED"))
	}
	sb.WriteString("\n\n")
	sb.WriteString(m.form.View())
	return sb.String()
}
