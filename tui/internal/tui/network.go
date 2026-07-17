package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/projectbluefin/fisherman/tui/internal/config"
	"github.com/projectbluefin/fisherman/tui/internal/network"
)

type networkModel struct {
	cfg    *config.InstallConfig
	form   *huh.Form
	choice string
}

func newNetworkModel(cfg *config.InstallConfig) *networkModel {
	m := &networkModel{cfg: cfg}

	ifaces, _ := network.List()
	var ifaceOptions []huh.Option[string]
	ifaceOptions = append(ifaceOptions, huh.NewOption("Skip (use DHCP automatically)", "skip"))
	for _, iface := range ifaces {
		status := "down"
		if iface.Up {
			status = "up"
		}
		label := fmt.Sprintf("%s (%s)", iface.Name, status)
		ifaceOptions = append(ifaceOptions, huh.NewOption(label, iface.Name))
	}

	m.choice = "skip"
	m.form = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Network interface").
				Description("Select a network interface or skip to use DHCP automatically.\nNetwork is used to pull the OS image during installation.").
				Options(ifaceOptions...).
				Value(&m.choice),
		),
	)
	return m
}

func (m *networkModel) Init() tea.Cmd {
	return m.form.Init()
}

func (m *networkModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		if m.choice != "skip" {
			m.cfg.NetworkInterface = m.choice
		}
		return m, func() tea.Msg { return stepMsg{next: stepImage} }
	}
	return m, cmd
}

func (m *networkModel) View() string {
	var sb strings.Builder
	sb.WriteString("\n")
	sb.WriteString(titleStyle.Render("  Step 1 of 9: Network Configuration"))
	sb.WriteString("\n\n")
	sb.WriteString(m.form.View())
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render("  ↑/↓ navigate • Enter select • Ctrl+C quit"))
	return sb.String()
}
