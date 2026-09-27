package tui

import (
	"fmt"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/tuna-os/fisherman/tui/internal/config"
)

type userModel struct {
	cfg      *config.InstallConfig
	form     *huh.Form
	fullname string
	username string
	password string
	confirm  string
}

func newUserModel(cfg *config.InstallConfig) *userModel {
	m := &userModel{cfg: cfg}
	m.fullname = cfg.FullName
	m.username = cfg.Username

	usernameRe := regexp.MustCompile(`^[a-z][a-z0-9_-]{0,30}$`)

	m.form = huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Full name").
				Placeholder("Jane Doe").
				Value(&m.fullname),
			huh.NewInput().
				Title("Username").
				Placeholder("jane").
				Value(&m.username).
				Validate(func(s string) error {
					if s == "" {
						return fmt.Errorf("username is required")
					}
					if !usernameRe.MatchString(s) {
						return fmt.Errorf("username must be lowercase, start with a letter, no spaces")
					}
					return nil
				}),
			huh.NewInput().
				Title("Password").
				Placeholder("Enter password").
				EchoMode(huh.EchoModePassword).
				Value(&m.password).
				Validate(func(s string) error {
					if len(s) < 8 {
						return fmt.Errorf("password must be at least 8 characters")
					}
					return nil
				}),
			huh.NewInput().
				Title("Confirm password").
				Placeholder("Re-enter password").
				EchoMode(huh.EchoModePassword).
				Value(&m.confirm).
				Validate(func(s string) error {
					if s != m.password {
						return fmt.Errorf("passwords do not match")
					}
					return nil
				}),
		),
	)
	return m
}

func (m *userModel) Init() tea.Cmd {
	return m.form.Init()
}

func (m *userModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		m.cfg.FullName = m.fullname
		m.cfg.Username = m.username
		m.cfg.Password = m.password
		return m, func() tea.Msg { return stepMsg{next: stepSSHKeys} }
	}
	return m, cmd
}

func (m *userModel) View() string {
	var sb strings.Builder
	sb.WriteString("\n")
	sb.WriteString(titleStyle.Render("  Step 6 of 9: User Account"))
	sb.WriteString("\n\n")
	sb.WriteString(m.form.View())
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render("  Tab/Shift+Tab navigate • Enter next • Ctrl+C quit"))
	return sb.String()
}
