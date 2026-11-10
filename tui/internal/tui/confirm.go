package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/tuna-os/fisherman/tui/internal/config"
)

type confirmModel struct {
	cfg     *config.InstallConfig
	form    *huh.Form
	proceed bool
	dryRun  bool
	blocked string // why the install must not start; "" when it may
}

// confirmBlocker says why this configuration must not be installed, or "".
// It runs the TUI's own rule and then asks fisherman about the whole recipe
// (`validate --json --partial -`, which reads no disk), so a combination
// the backend refuses is refused here, before the user confirms.
func confirmBlocker(cfg *config.InstallConfig) string {
	if err := cfg.Check(); err != nil {
		return err.Error()
	}
	keys := []string{"filesystem", "composeFsBackend", "bootloader", "hostname", "encryption"}
	if cfg.Image.NeedsUserCreation {
		keys = append(keys, "user")
	}
	return checkFields(cfg, keys...)
}

func newConfirmModel(cfg *config.InstallConfig, dryRun bool) *confirmModel {
	m := &confirmModel{cfg: cfg, dryRun: dryRun, blocked: confirmBlocker(cfg)}

	if m.blocked != "" {
		m.form = huh.NewForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title("These settings cannot be installed").
					Description("Go back and change them.").
					Affirmative("Go back").
					Negative("Go back").
					Value(&m.proceed),
			),
		)
		return m
	}

	desc := "Review the summary above. This will ERASE the target disk."
	affirmative := "Install now"
	if dryRun {
		desc = "Dry-run mode: no changes will be made. The recipe JSON will be shown at the end, with its secrets masked."
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
		next := m.next()
		return m, func() tea.Msg { return stepMsg{next: next} }
	}
	return m, cmd
}

// next is the page the completed form leads to. A blocked configuration
// has one way out, back, whatever the answer was.
func (m *confirmModel) next() step {
	switch {
	case m.blocked != "":
		return stepFilesystem
	case m.proceed:
		return nextStep(stepConfirm, m.cfg)
	default:
		return prevStep(m.cfg)
	}
}

func (m *confirmModel) View() string {
	var sb strings.Builder
	sb.WriteString("\n")
	sb.WriteString(titleStyle.Render(stepTitle(m.cfg, stepConfirm)))
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
	boot := "GRUB, ostree"
	switch {
	case m.cfg.Image.ComposeFsBackend && m.cfg.Image.Bootloader == "systemd":
		boot = "systemd-boot, composefs"
	case m.cfg.Image.ComposeFsBackend:
		boot = "GRUB, composefs"
	case m.cfg.Image.Bootloader == "systemd":
		boot = "systemd-boot, ostree"
	}
	disk := m.cfg.DiskLabel
	if disk == "" {
		disk = m.cfg.DiskDevice
	}

	rows := row("Image", m.cfg.Image.Ref) +
		row("Boot stack", boot) +
		row("Disk", disk) +
		row("Filesystem", m.cfg.Filesystem) +
		row("Hostname", m.cfg.Hostname) +
		row("Encryption", encStr)
	if m.cfg.Image.NeedsUserCreation {
		rows += row("Username", m.cfg.Username) + row("Full name", m.cfg.FullName)
	} else {
		rows += row("Account", "created at first boot by the image")
	}
	sb.WriteString(boxStyle.Render(titleStyle.Render("Installation Summary") + "\n\n" + rows))

	sb.WriteString("\n\n")
	switch {
	case m.blocked != "":
		sb.WriteString(errorStyle.Render("  ✗ " + m.blocked))
	case m.dryRun:
		sb.WriteString(accentStyle.Render("  ⓘ  DRY RUN — no changes will be made to any disk"))
	default:
		sb.WriteString(warningStyle.Render("  ⚠  ALL DATA ON " + m.cfg.DiskDevice + " WILL BE DESTROYED"))
	}
	sb.WriteString("\n\n")
	sb.WriteString(m.form.View())
	return sb.String()
}
