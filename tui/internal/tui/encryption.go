package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/projectbluefin/fisherman/tui/internal/config"
)

type encryptionModel struct {
	cfg        *config.InstallConfig
	form       *huh.Form
	enabled    bool
	passphrase string
	confirm    string
}

func newEncryptionModel(cfg *config.InstallConfig) *encryptionModel {
	m := &encryptionModel{cfg: cfg}
	m.enabled = cfg.EncryptionEnabled

	m.form = huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Enable disk encryption?").
				Description("Enable LUKS full-disk encryption to protect your data at rest.\nYou will need to enter the passphrase at every boot.").
				Value(&m.enabled),
		),
		huh.NewGroup(
			huh.NewInput().
				Title("Passphrase").
				Placeholder("Enter passphrase").
				EchoMode(huh.EchoModePassword).
				Value(&m.passphrase).
				Validate(func(s string) error {
					if s == "" {
						return fmt.Errorf("passphrase is required when encryption is enabled")
					}
					if len(s) < 8 {
						return fmt.Errorf("passphrase must be at least 8 characters")
					}
					return nil
				}),
			huh.NewInput().
				Title("Confirm passphrase").
				Placeholder("Re-enter passphrase").
				EchoMode(huh.EchoModePassword).
				Value(&m.confirm).
				Validate(func(s string) error {
					if s != m.passphrase {
						return fmt.Errorf("passphrases do not match")
					}
					return nil
				}),
		).WithHideFunc(func() bool { return !m.enabled }),
	)
	return m
}

func (m *encryptionModel) Init() tea.Cmd {
	return m.form.Init()
}

func (m *encryptionModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		m.cfg.EncryptionEnabled = m.enabled
		if m.enabled {
			m.cfg.EncryptionType = "luks-passphrase"
			m.cfg.Passphrase = m.passphrase
		} else {
			m.cfg.EncryptionType = "none"
			m.cfg.Passphrase = ""
		}
		return m, func() tea.Msg { return stepMsg{next: stepUser} }
	}
	return m, cmd
}

func (m *encryptionModel) View() string {
	var sb strings.Builder
	sb.WriteString("\n")
	sb.WriteString(titleStyle.Render("  Step 5 of 9: Disk Encryption"))
	sb.WriteString("\n\n")
	sb.WriteString(m.form.View())
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render("  ↑/↓ navigate • Enter select/next • Ctrl+C quit"))
	return sb.String()
}
