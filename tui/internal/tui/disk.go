package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/tuna-os/fisherman/tui/internal/config"
	"github.com/tuna-os/fisherman/tui/internal/disks"
)

type diskModel struct {
	cfg      *config.InstallConfig
	form     *huh.Form
	selected string
	err      string
}

func newDiskModel(cfg *config.InstallConfig) *diskModel {
	m := &diskModel{cfg: cfg}

	diskList, err := disks.List()
	if err != nil || len(diskList) == 0 {
		m.err = "No suitable disks found. Ensure disks are not mounted."
		diskList = []disks.Disk{{Name: "sda", Size: 0, Model: "Unknown"}}
	}

	var opts []huh.Option[string]
	for _, d := range diskList {
		label := fmt.Sprintf("/dev/%s  %s  %s", d.Name, disks.HumanSize(d.Size), d.Model)
		opts = append(opts, huh.NewOption(label, "/dev/"+d.Name))
	}

	if cfg.DiskDevice != "" {
		m.selected = cfg.DiskDevice
	} else if len(opts) > 0 {
		m.selected = "/dev/" + diskList[0].Name
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
	return m.form.Init()
}

func (m *diskModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		m.cfg.DiskDevice = m.selected
		m.cfg.DiskLabel = m.selected
		return m, func() tea.Msg { return stepMsg{next: stepFilesystem} }
	}
	return m, cmd
}

func (m *diskModel) View() string {
	var sb strings.Builder
	sb.WriteString("\n")
	sb.WriteString(titleStyle.Render("  Step 3 of 9: Disk Selection"))
	sb.WriteString("\n\n")
	if m.err != "" {
		sb.WriteString(warningStyle.Render("  ⚠ " + m.err))
		sb.WriteString("\n\n")
	}
	sb.WriteString(m.form.View())
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render("  ↑/↓ navigate • Enter select • Ctrl+C quit"))
	return sb.String()
}
