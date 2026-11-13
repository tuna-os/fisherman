package tui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tuna-os/fisherman/tui/internal/backend"
	"github.com/tuna-os/fisherman/tui/internal/config"
)

// fishermanEvent is one line of fisherman's progress stream
// (docs/JSON-PROGRESS-FORMAT.md).
type fishermanEvent struct {
	Type          string   `json:"type"`
	Step          int      `json:"step"`
	TotalSteps    int      `json:"total_steps"`
	StepName      string   `json:"step_name"`
	CumulativePct int      `json:"cumulative_pct"`
	OverallPct    *float64 `json:"overall_pct"` // the progress bar, 0-100; absent on some events
	Message       string   `json:"message"`
	Key           string   `json:"key"` // recovery_key
	BootID        string   `json:"boot_id"`
}

// The three things that happen to a running install, each its own message.
// They come from different goroutines, so they can arrive in any order.
type (
	progressLineMsg string              // one line of output
	progressEOFMsg  struct{}            // the output was read to its end
	progressExitMsg struct{ err error } // the process exited
	// progressStartedMsg carries the running backend, or why it did not start.
	progressStartedMsg struct {
		run *backendRun
		err error
	}
)

// backendRun is a started backend: its output, its exit status, and a way
// to ask it to stop.
type backendRun struct {
	lines  <-chan string // closed when stdout and stderr are both at EOF
	exit   <-chan error  // one value: the result of Wait
	cancel func()        // ask the backend to stop (SIGTERM); may be nil
}

// runState is what is known about the install. The protocol's terminal
// event and the process exit are tracked apart, because neither implies the
// other and they arrive in either order (#359):
//
//   - `complete` is fisherman's claim that the install worked;
//   - the exit status is the kernel's word on how the process ended;
//   - EOF means no further event can still be in the pipe.
//
// The install succeeded only when all three agree.
type runState struct {
	terminal   string // "", "complete" or "error": the last word of the protocol
	errMessage string // the message of the `error` event
	exited     bool
	exitErr    error
	drained    bool
	cancelled  bool // the user asked for the cancel
	startErr   error
	settled    bool // the outcome was acted on; set once
}

type runOutcome int

const (
	outcomePending runOutcome = iota
	outcomeSucceeded
	outcomeFailed
)

// outcome reconciles the state. It is pending until the process has exited
// and its output is drained: an `error` event may still be in the pipe
// after a zero exit, and the exit status is unknown after `complete`.
func (s runState) outcome() (runOutcome, string) {
	if s.startErr != nil {
		return outcomeFailed, "could not start fisherman: " + s.startErr.Error()
	}
	if !s.exited || !s.drained {
		return outcomePending, ""
	}
	switch {
	case s.terminal == "complete" && s.exitErr == nil:
		// Also when a cancel came too late to stop anything.
		return outcomeSucceeded, ""
	case s.cancelled:
		return outcomeFailed, "Installation cancelled"
	case s.terminal == "error":
		return outcomeFailed, "Installation failed: " + s.errMessage
	case s.exitErr != nil && s.terminal == "complete":
		return outcomeFailed, "fisherman reported completion but exited with an error: " + s.exitErr.Error()
	case s.exitErr != nil:
		return outcomeFailed, "Installation failed: fisherman " + s.exitErr.Error()
	}
	return outcomeFailed, "Installation failed: fisherman ended without reporting completion"
}

type progressModel struct {
	cfg      *config.InstallConfig
	bar      progress.Model
	viewport viewport.Model
	logs     []string
	stepName string
	pct      float64
	state    runState
	failure  string // why the install failed; "" while it runs or after success
	run      *backendRun
	width    int
	height   int
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
		dryRun:   dryRun,
	}
}

// startBackend starts `fisherman -` with the recipe on its stdin. Tests
// replace it.
var startBackend = func(recipe []byte) (*backendRun, error) {
	cmd, err := backend.InstallCommand(recipe)
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	lines := make(chan string, 200)
	exit := make(chan error, 1)
	var readers sync.WaitGroup
	scan := func(r io.Reader, prefix string) {
		defer readers.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			lines <- prefix + sc.Text()
		}
	}
	readers.Add(2)
	go scan(stdout, "")
	go scan(stderr, "[stderr] ")
	go func() {
		// Wait closes the pipes, so both readers finish first.
		readers.Wait()
		close(lines)
		exit <- cmd.Wait()
	}()
	return &backendRun{
		lines: lines,
		exit:  exit,
		// SIGTERM is fisherman's cancel: it unmounts and closes what it
		// opened and exits.
		cancel: func() { _ = cmd.Process.Signal(syscall.SIGTERM) },
	}, nil
}

var dryRunSteps = []struct {
	name    string
	substep string
	pct     int
}{
	{"Partitioning disk", "Creating GPT partition table", 5},
	{"Partitioning disk", "Creating EFI partition", 10},
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
	{"Finalizing", "Syncing filesystems", 98},
}

// dryRunDelay is the pause between simulated steps. Tests set it to zero.
var dryRunDelay = 350 * time.Millisecond

// startDryRun simulates an install through the same channels a real one
// uses. It starts no process and writes no file.
func startDryRun(filesystem string) *backendRun {
	lines := make(chan string, 200)
	exit := make(chan error, 1)
	go func() {
		total := len(dryRunSteps)
		for i, s := range dryRunSteps {
			stepEvent, _ := json.Marshal(map[string]any{
				"type": "step", "step": i + 1, "total_steps": total, "step_name": s.name,
				"cumulative_pct": s.pct, "overall_pct": s.pct,
			})
			lines <- string(stepEvent)
			subEvent, _ := json.Marshal(map[string]any{
				"type": "substep", "message": strings.ReplaceAll(s.substep, "{{fs}}", filesystem), "overall_pct": s.pct,
			})
			lines <- string(subEvent)
			time.Sleep(dryRunDelay)
		}
		completeEvent, _ := json.Marshal(map[string]any{"type": "complete", "overall_pct": 100})
		lines <- string(completeEvent)
		close(lines)
		exit <- nil
	}()
	return &backendRun{lines: lines, exit: exit}
}

func (m *progressModel) start() tea.Cmd {
	return func() tea.Msg {
		if m.dryRun {
			return progressStartedMsg{run: startDryRun(m.cfg.Filesystem)}
		}
		recipe, err := m.cfg.Recipe()
		if err != nil {
			return progressStartedMsg{err: err}
		}
		run, err := startBackend(recipe)
		return progressStartedMsg{run: run, err: err}
	}
}

func listenForLines(run *backendRun) tea.Cmd {
	return func() tea.Msg {
		line, ok := <-run.lines
		if !ok {
			return progressEOFMsg{}
		}
		return progressLineMsg(line)
	}
}

func waitForExit(run *backendRun) tea.Cmd {
	return func() tea.Msg { return progressExitMsg{err: <-run.exit} }
}

func (m *progressModel) Init() tea.Cmd {
	return m.start()
}

// logLine is what the log shows for a line of output. The recovery key is
// kept out of the scrollback; it has a page of its own.
func logLine(line string, ev fishermanEvent) string {
	if ev.Type == "recovery_key" {
		return `{"type":"recovery_key","key":"` + config.Redacted + `"} (shown after the install)`
	}
	return line
}

func (m *progressModel) parseLine(line string) {
	var ev fishermanEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		m.logs = append(m.logs, line)
		return
	}
	m.logs = append(m.logs, logLine(line, ev))

	// The backend computes the bar; cumulative_pct is the fallback for a
	// `step` event of a backend that predates overall_pct.
	if ev.OverallPct != nil {
		m.pct = *ev.OverallPct / 100.0
	} else if ev.Type == "step" {
		m.pct = float64(ev.CumulativePct) / 100.0
	}

	switch ev.Type {
	case "step":
		m.stepName = fmt.Sprintf("Step %d/%d: %s", ev.Step, ev.TotalSteps, ev.StepName)
	case "substep", "info":
		m.stepName = ev.Message
	case "recovery_key":
		m.cfg.RecoveryKey = ev.Key
		m.stepName = "Recovery key received. It is shown when the install ends."
	case "complete":
		// A later `error` still wins; an earlier one is not undone.
		if m.state.terminal == "" {
			m.state.terminal = "complete"
			m.pct = 1.0
			m.stepName = "Finishing..."
		}
	case "error":
		if m.state.terminal != "error" {
			m.state.terminal = "error"
			m.state.errMessage = ev.Message
		}
		m.stepName = "Error: " + ev.Message
	}
}

// reconcile acts on the outcome, once. It is called after every message
// that changes the state, so the order the messages arrive in does not
// matter.
func (m *progressModel) reconcile() tea.Cmd {
	if m.state.settled {
		return nil
	}
	outcome, why := m.state.outcome()
	switch outcome {
	case outcomePending:
		return nil
	case outcomeSucceeded:
		m.state.settled = true
		m.stepName = "Installation complete!"
		next := nextStep(stepProgress, m.cfg)
		return func() tea.Msg { return stepMsg{next: next} }
	default:
		m.state.settled = true
		m.failure = why
		m.stepName = why
		if m.state.cancelled {
			return tea.Quit
		}
		return nil
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
		switch msg.String() {
		case "ctrl+c":
			// A running install is asked to stop and is waited for: the
			// backend unmounts and closes what it opened. Quitting at once
			// would leave it running with no one to read its output.
			if m.run != nil && !m.state.exited {
				if !m.state.cancelled {
					m.state.cancelled = true
					m.stepName = "Cancelling: waiting for fisherman to clean up..."
					if m.run.cancel != nil {
						m.run.cancel()
					}
				}
				return m, nil
			}
			return m, tea.Quit
		case "enter", "q":
			if m.failure != "" {
				return m, tea.Quit
			}
		}

	case progressStartedMsg:
		if msg.err != nil {
			m.state.startErr = msg.err
			cmds = append(cmds, m.reconcile())
			break
		}
		m.run = msg.run
		cmds = append(cmds, listenForLines(m.run), waitForExit(m.run))

	case progressLineMsg:
		m.parseLine(string(msg))
		m.viewport.SetContent(strings.Join(m.logs, "\n"))
		m.viewport.GotoBottom()
		// SetPercent's command keeps a pointer to the model it was called
		// on and reads it from another goroutine. Give it a copy, so later
		// updates of m.bar do not race with it.
		bar := m.bar
		cmds = append(cmds, bar.SetPercent(m.pct))
		m.bar = bar
		// Keep reading to EOF, whatever the line said: an `error` can
		// follow, and a full pipe would block the backend.
		if m.run != nil {
			cmds = append(cmds, listenForLines(m.run))
		}

	case progressEOFMsg:
		m.state.drained = true
		cmds = append(cmds, m.reconcile())

	case progressExitMsg:
		if !m.state.exited {
			m.state.exited = true
			m.state.exitErr = msg.err
		}
		cmds = append(cmds, m.reconcile())

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
	if m.failure != "" {
		sb.WriteString(errorStyle.Render(m.failure))
	} else {
		sb.WriteString(accentStyle.Render(m.stepName))
	}
	sb.WriteString("\n\n")

	logStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorSecondary).
		Padding(0, 1)
	sb.WriteString(logStyle.Render(m.viewport.View()))
	sb.WriteString("\n")
	help := "  ↑/↓ scroll log • Ctrl+C cancel"
	if m.failure != "" {
		help = "  ↑/↓ scroll log • Enter quit"
	}
	sb.WriteString(helpStyle.Render(help))
	return sb.String()
}
