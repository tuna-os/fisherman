package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/tuna-os/fisherman/tui/internal/config"
)

// recoveryKeyModel shows the LUKS recovery key fisherman emitted (a
// `recovery_key` event) and does not let the user pass until they say they
// have it. For a TPM-only install the key is the only way into the disk
// when the TPM does not unlock, and the installer is the only place it is
// ever shown (#417).
type recoveryKeyModel struct {
	cfg   *config.InstallConfig
	form  *huh.Form
	saved bool
}

func newRecoveryKeyModel(cfg *config.InstallConfig) *recoveryKeyModel {
	m := &recoveryKeyModel{cfg: cfg}
	m.form = huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Have you written the recovery key down?").
				Description("It is not shown again and it is not stored anywhere else.").
				Affirmative("Yes, I have it").
				Negative("Not yet").
				Value(&m.saved),
		),
	)
	return m
}

func (m *recoveryKeyModel) Init() tea.Cmd { return m.form.Init() }

func (m *recoveryKeyModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// No Ctrl+C here: leaving this page is the acknowledgement.
	form, cmd := m.form.Update(msg)
	if f, ok := form.(*huh.Form); ok {
		m.form = f
	}
	if m.form.State == huh.StateCompleted {
		return m.acknowledge()
	}
	return m, cmd
}

// acknowledge moves on only when the user confirmed; otherwise the page
// asks again.
func (m *recoveryKeyModel) acknowledge() (tea.Model, tea.Cmd) {
	if !m.saved {
		again := newRecoveryKeyModel(m.cfg)
		return again, again.Init()
	}
	return m, func() tea.Msg { return stepMsg{next: stepDone} }
}

func (m *recoveryKeyModel) View() string {
	var sb strings.Builder
	sb.WriteString("\n")
	sb.WriteString(titleStyle.Render("  " + stepNames[stepRecoveryKey]))
	sb.WriteString("\n\n")
	sb.WriteString(warningStyle.Render("  ⚠  This key unlocks the disk when the TPM cannot. Write it down now."))
	sb.WriteString("\n\n")
	sb.WriteString(boxStyle.Render(m.cfg.RecoveryKey))
	sb.WriteString("\n\n")
	sb.WriteString(m.form.View())
	return sb.String()
}
