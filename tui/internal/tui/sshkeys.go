package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/tuna-os/fisherman/tui/internal/config"
	"github.com/tuna-os/fisherman/tui/internal/sshkeys"
)

type sshKeysModel struct {
	cfg        *config.InstallConfig
	form       *huh.Form
	method     string
	ghUsername string
	glUsername string
	manualKeys string
	statusMsg  string
	importing  bool
}

func newSSHKeysModel(cfg *config.InstallConfig) *sshKeysModel {
	m := &sshKeysModel{cfg: cfg}
	m.method = "skip"

	m.form = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Import SSH keys from").
				Description("Import SSH public keys for passwordless remote login.\nKeys are optional — you can always add them later.").
				Options(
					huh.NewOption("Skip — no SSH keys", "skip"),
					huh.NewOption("GitHub username", "github"),
					huh.NewOption("GitLab username", "gitlab"),
					huh.NewOption("Paste manually", "manual"),
				).
				Value(&m.method),
		),
		huh.NewGroup(
			huh.NewInput().
				Title("GitHub username").
				Placeholder("octocat").
				Value(&m.ghUsername),
		).WithHideFunc(func() bool { return m.method != "github" }),
		huh.NewGroup(
			huh.NewInput().
				Title("GitLab username").
				Placeholder("gitlabber").
				Value(&m.glUsername),
		).WithHideFunc(func() bool { return m.method != "gitlab" }),
		huh.NewGroup(
			huh.NewInput().
				Title("Paste SSH public keys").
				Description("One key per line (ssh-ed25519 AAAA..., ssh-rsa AAAA..., etc.)").
				Value(&m.manualKeys),
		).WithHideFunc(func() bool { return m.method != "manual" }),
	)
	return m
}

func (m *sshKeysModel) Init() tea.Cmd {
	return m.form.Init()
}

type sshImportDoneMsg struct {
	keys []string
	err  error
}

func (m *sshKeysModel) importKeys() tea.Cmd {
	return func() tea.Msg {
		switch m.method {
		case "github":
			keys, err := sshkeys.FetchGitHub(m.ghUsername)
			return sshImportDoneMsg{keys: keys, err: err}
		case "gitlab":
			keys, err := sshkeys.FetchGitLab(m.glUsername)
			return sshImportDoneMsg{keys: keys, err: err}
		case "manual":
			var keys []string
			for _, line := range strings.Split(m.manualKeys, "\n") {
				line = strings.TrimSpace(line)
				if line != "" && (strings.HasPrefix(line, "ssh-") || strings.HasPrefix(line, "ecdsa-")) {
					keys = append(keys, line)
				}
			}
			return sshImportDoneMsg{keys: keys, err: nil}
		default:
			return sshImportDoneMsg{}
		}
	}
}

func (m *sshKeysModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	case sshImportDoneMsg:
		m.importing = false
		if msg.err != nil {
			m.statusMsg = fmt.Sprintf("⚠ Import failed: %v (continuing without keys)", msg.err)
		} else if len(msg.keys) > 0 {
			m.cfg.SSHKeys = msg.keys
			m.statusMsg = fmt.Sprintf("✓ Imported %d SSH key(s)", len(msg.keys))
		} else {
			m.statusMsg = "No keys found"
		}
		return m, func() tea.Msg { return stepMsg{next: stepConfirm} }
	}

	form, cmd := m.form.Update(msg)
	if f, ok := form.(*huh.Form); ok {
		m.form = f
	}
	if m.form.State == huh.StateCompleted {
		if m.method == "skip" {
			return m, func() tea.Msg { return stepMsg{next: stepConfirm} }
		}
		m.importing = true
		return m, m.importKeys()
	}
	return m, cmd
}

func (m *sshKeysModel) View() string {
	var sb strings.Builder
	sb.WriteString("\n")
	sb.WriteString(titleStyle.Render("  Step 7 of 9: SSH Keys"))
	sb.WriteString("\n\n")
	if m.importing {
		sb.WriteString(accentStyle.Render("  Importing SSH keys..."))
		sb.WriteString("\n\n")
		return sb.String()
	}
	sb.WriteString(m.form.View())
	if m.statusMsg != "" {
		sb.WriteString("\n")
		sb.WriteString(warningStyle.Render("  " + m.statusMsg))
	}
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render("  ↑/↓ navigate • Enter next • Ctrl+C quit"))
	return sb.String()
}
