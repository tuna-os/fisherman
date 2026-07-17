package tui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/projectbluefin/fisherman/tui/internal/config"
)

const recipePath = "/tmp/bootc-installer-recipe.json"

type fishermanEvent struct {
	Type          string `json:"type"`
	Step          int    `json:"step"`
	TotalSteps    int    `json:"total_steps"`
	StepName      string `json:"step_name"`
	WeightPct     int    `json:"weight_pct"`
	CumulativePct int    `json:"cumulative_pct"`
	Message       string `json:"message"`
	BootID        string `json:"boot_id"`
}

type progressLineMsg string
type progressDoneMsg struct{ err error }

type progressModel struct {
	cfg      *config.InstallConfig
	bar      progress.Model
	viewport viewport.Model
	logs     []string
	stepName string
	pct      float64
	done     bool
	err      error
	width    int
	height   int
	sub      chan string
	dryRun   bool
}

func newProgressModel(cfg *config.InstallConfig, dryRun bool) *progressModel {
	p := progress.New(
		progress.WithDefaultGradient(),
		progress.WithWidth(60),
	)
	vp := viewport.New(80, 10)
	return &progressModel{
		cfg:      cfg,
		bar:      p,
		viewport: vp,
		stepName: "Starting installation...",
		sub:      make(chan string, 200),
		dryRun:   dryRun,
	}
}

func findFisherman() string {
	for _, candidate := range []string{"fisherman", "/usr/bin/fisherman", "./fisherman"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	return "fisherman"
}

func (m *progressModel) listenForLines() tea.Cmd {
	return func() tea.Msg {
		line, ok := <-m.sub
		if !ok {
			return progressDoneMsg{}
		}
		return progressLineMsg(line)
	}
}

var dryRunSteps = []struct {
	name    string
	substep string
	pct     int
}{
	{"Partitioning disk", "Creating GPT partition table", 5},
	{"Partitioning disk", "Creating EFI partition (512 MiB)", 10},
	{"Partitioning disk", "Creating root partition", 15},
	{"Formatting partitions", "Formatting EFI as FAT32", 20},
	{"Formatting partitions", "Formatting root as " + "{{fs}}", 25},
	{"Pulling container image", "Authenticating with registry", 30},
	{"Pulling container image", "Fetching manifest", 38},
	{"Pulling container image", "Downloading layers", 50},
	{"Installing system", "Running bootc install to-filesystem", 60},
	{"Installing system", "Generating initramfs", 72},
	{"Installing system", "Installing bootloader", 82},
	{"Configuring system", "Setting hostname", 87},
	{"Configuring system", "Creating user account", 91},
	{"Configuring system", "Writing authorized_keys", 95},
	{"Finalizing", "Syncing filesystems", 98},
}

func (m *progressModel) startDryRun() tea.Cmd {
	return func() tea.Msg {
		total := len(dryRunSteps)
		for i, s := range dryRunSteps {
			name := strings.ReplaceAll(s.substep, "{{fs}}", m.cfg.Filesystem)
			stepEvent, _ := json.Marshal(map[string]any{
				"type":           "step",
				"step":           i + 1,
				"total_steps":    total,
				"step_name":      s.name,
				"cumulative_pct": s.pct,
			})
			m.sub <- string(stepEvent)
			subEvent, _ := json.Marshal(map[string]any{
				"type":    "substep",
				"message": name,
			})
			m.sub <- string(subEvent)
			time.Sleep(350 * time.Millisecond)
		}
		completeEvent, _ := json.Marshal(map[string]any{
			"type": "complete",
		})
		m.sub <- string(completeEvent)
		close(m.sub)
		return progressDoneMsg{}
	}
}

func (m *progressModel) startInstall() tea.Cmd {
	if m.dryRun {
		return m.startDryRun()
	}
	return func() tea.Msg {
		if err := m.cfg.WriteRecipe(recipePath); err != nil {
			m.sub <- fmt.Sprintf("ERROR: writing recipe: %v", err)
			close(m.sub)
			return progressDoneMsg{err: err}
		}

		fishermanPath := findFisherman()
		cmd := exec.Command(fishermanPath, "install", "--recipe", recipePath)

		stdoutPipe, _ := cmd.StdoutPipe()
		stderrPipe, _ := cmd.StderrPipe()

		if err := cmd.Start(); err != nil {
			m.sub <- fmt.Sprintf("ERROR: %v", err)
			close(m.sub)
			return progressDoneMsg{err: err}
		}

		done := make(chan struct{}, 2)
		go func() {
			scanner := bufio.NewScanner(stdoutPipe)
			for scanner.Scan() {
				m.sub <- scanner.Text()
			}
			done <- struct{}{}
		}()
		go func() {
			scanner := bufio.NewScanner(stderrPipe)
			for scanner.Scan() {
				m.sub <- "[stderr] " + scanner.Text()
			}
			done <- struct{}{}
		}()

		<-done
		<-done
		err := cmd.Wait()
		close(m.sub)
		return progressDoneMsg{err: err}
	}
}

func (m *progressModel) Init() tea.Cmd {
	return tea.Batch(m.startInstall(), m.listenForLines())
}

func (m *progressModel) parseLine(line string) {
	m.logs = append(m.logs, line)

	var ev fishermanEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return
	}

	switch ev.Type {
	case "step":
		m.stepName = fmt.Sprintf("Step %d/%d: %s", ev.Step, ev.TotalSteps, ev.StepName)
		m.pct = float64(ev.CumulativePct) / 100.0
	case "substep", "info":
		m.stepName = ev.Message
	case "complete":
		m.pct = 1.0
		m.stepName = "Installation complete!"
		m.done = true
	case "error":
		m.stepName = "Error: " + ev.Message
	}
}

func (m *progressModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.bar.Width = msg.Width - 4
		m.viewport.Width = msg.Width - 4
		if msg.Height > 12 {
			m.viewport.Height = msg.Height - 12
		}

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}

	case progressLineMsg:
		m.parseLine(string(msg))
		m.viewport.SetContent(strings.Join(m.logs, "\n"))
		m.viewport.GotoBottom()
		barCmd := m.bar.SetPercent(m.pct)
		cmds = append(cmds, barCmd)
		if !m.done {
			cmds = append(cmds, m.listenForLines())
		}

	case progressDoneMsg:
		if msg.err != nil && !m.done {
			m.err = msg.err
			m.stepName = "Installation failed: " + msg.err.Error()
		}
		if m.done {
			cmds = append(cmds, func() tea.Msg { return stepMsg{next: stepDone} })
		}

	case progress.FrameMsg:
		barModel, cmd := m.bar.Update(msg)
		m.bar = barModel.(progress.Model)
		cmds = append(cmds, cmd)
	}

	var vpCmd tea.Cmd
	m.viewport, vpCmd = m.viewport.Update(msg)
	cmds = append(cmds, vpCmd)

	return m, tea.Batch(cmds...)
}

func (m *progressModel) View() string {
	var sb strings.Builder
	sb.WriteString("\n")
	title := "  Installing..."
	if m.dryRun {
		title = "  Dry Run — Simulating Installation..."
	}
	sb.WriteString(titleStyle.Render(title))
	sb.WriteString("\n\n")

	sb.WriteString("  ")
	sb.WriteString(m.bar.View())
	sb.WriteString("\n\n")

	sb.WriteString("  ")
	sb.WriteString(accentStyle.Render(m.stepName))
	sb.WriteString("\n\n")

	if m.err != nil {
		sb.WriteString(errorStyle.Render("  Error: " + m.err.Error()))
		sb.WriteString("\n")
	}

	logStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorSecondary).
		Padding(0, 1)
	sb.WriteString(logStyle.Render(m.viewport.View()))
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render("  ↑/↓ scroll log • Ctrl+C abort"))
	return sb.String()
}
