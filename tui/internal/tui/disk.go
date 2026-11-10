package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/tuna-os/fisherman/tui/internal/backend"
	"github.com/tuna-os/fisherman/tui/internal/config"
)

// probeDisks asks fisherman for the disks. Tests replace it.
var probeDisks = backend.Probe

type diskModel struct {
	cfg      *config.InstallConfig
	form     *huh.Form // nil when there is no disk to choose
	selected string
	err      string
	excluded []backend.Disk
	labels   map[string]string
}

// newDiskModel lists the disks `fisherman probe --json` calls eligible: the
// same verdict the install gives, so a disk offered here is not refused
// later, and the live medium is never offered. When the probe fails or no
// disk is eligible, the page says so and offers nothing. It does not invent
// a disk (#417).
func newDiskModel(cfg *config.InstallConfig) *diskModel {
	m := &diskModel{cfg: cfg, labels: map[string]string{}}

	all, err := probeDisks()
	if err != nil {
		m.err = "Cannot list disks: " + err.Error()
		return m
	}
	eligible := backend.Eligible(all)
	for _, d := range all {
		if !d.Eligible {
			m.excluded = append(m.excluded, d)
		}
	}
	if len(eligible) == 0 {
		m.err = "No disk can be used as an install target."
		return m
	}

	var opts []huh.Option[string]
	for _, d := range eligible {
		m.labels[d.Path] = d.Label()
		opts = append(opts, huh.NewOption(d.Label(), d.Path))
	}
	m.selected = eligible[0].Path
	if _, ok := m.labels[cfg.DiskDevice]; ok {
		m.selected = cfg.DiskDevice
	}

	m.form = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Target disk").
				Description("⚠️  WARNING: The selected disk will be COMPLETELY ERASED.\nAll existing data will be lost!").
				Options(opts...).
				Value(&m.selected),
		),
	)
	return m
}

func (m *diskModel) Init() tea.Cmd {
	if m.form == nil {
		return nil
	}
	return m.form.Init()
}

func (m *diskModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.form == nil {
			switch msg.String() {
			case "r":
				// Probe again: a disk may have been plugged in.
				return m, func() tea.Msg { return stepMsg{next: stepDisk} }
			case "b", "esc":
				return m, func() tea.Msg { return stepMsg{next: stepImage} }
			case "q":
				return m, tea.Quit
			}
		}
	}
	if m.form == nil {
		return m, nil
	}
	form, cmd := m.form.Update(msg)
	if f, ok := form.(*huh.Form); ok {
		m.form = f
	}
	if m.form.State == huh.StateCompleted {
		m.cfg.DiskDevice = m.selected
		m.cfg.DiskLabel = m.labels[m.selected]
		return m, func() tea.Msg { return stepMsg{next: nextStep(stepDisk, m.cfg)} }
	}
	return m, cmd
}

func (m *diskModel) View() string {
	var sb strings.Builder
	sb.WriteString("\n")
	sb.WriteString(titleStyle.Render(stepTitle(m.cfg, stepDisk)))
	sb.WriteString("\n\n")
	if m.err != "" {
		sb.WriteString(warningStyle.Render("  ⚠ " + m.err))
		sb.WriteString("\n\n")
	}
	if len(m.excluded) > 0 {
		sb.WriteString(mutedStyle.Render("  Not offered:"))
		sb.WriteString("\n")
		for _, d := range m.excluded {
			sb.WriteString(mutedStyle.Render(fmt.Sprintf("    %s  (%s)", d.Label(), d.ExcludedReason)))
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}
	if m.form == nil {
		sb.WriteString(helpStyle.Render("  r scan again • b back • q quit"))
		return sb.String()
	}
	sb.WriteString(m.form.View())
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render("  ↑/↓ navigate • Enter select • Ctrl+C quit"))
	return sb.String()
}
