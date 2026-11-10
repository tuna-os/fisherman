package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tuna-os/fisherman/tui/internal/backend"
	"github.com/tuna-os/fisherman/tui/internal/catalog"
	"github.com/tuna-os/fisherman/tui/internal/config"
)

type step int

// The wizard's pages. There is no network page and no SSH key page: the
// recipe has no field for either, so what they collected was dropped
// (#417, #361). Do not add a page for input the backend cannot receive.
const (
	stepWelcome step = iota
	stepImage
	stepDisk
	stepFilesystem
	stepEncryption
	stepUser
	stepConfirm
	stepProgress
	stepRecoveryKey
	stepDone
)

// stepNames is every page that exists, for titles and for tests.
var stepNames = map[step]string{
	stepWelcome:     "Welcome",
	stepImage:       "OS Image",
	stepDisk:        "Disk Selection",
	stepFilesystem:  "Filesystem & Hostname",
	stepEncryption:  "Disk Encryption",
	stepUser:        "User Account",
	stepConfirm:     "Confirm Installation",
	stepProgress:    "Installing",
	stepRecoveryKey: "Recovery Key",
	stepDone:        "Done",
}

// nextStep is the page that follows cur for this configuration. The user
// page is skipped for an image that creates its account at first boot, and
// the recovery key page appears only when fisherman emitted a key.
func nextStep(cur step, cfg *config.InstallConfig) step {
	switch cur {
	case stepWelcome:
		return stepImage
	case stepImage:
		return stepDisk
	case stepDisk:
		return stepFilesystem
	case stepFilesystem:
		return stepEncryption
	case stepEncryption:
		if cfg.Image.NeedsUserCreation {
			return stepUser
		}
		return stepConfirm
	case stepUser:
		return stepConfirm
	case stepConfirm:
		return stepProgress
	case stepProgress:
		if cfg.RecoveryKey != "" {
			return stepRecoveryKey
		}
		return stepDone
	default:
		return stepDone
	}
}

// prevStep is the page "Go back" on the confirm page returns to.
func prevStep(cfg *config.InstallConfig) step {
	if cfg.Image.NeedsUserCreation {
		return stepUser
	}
	return stepEncryption
}

// wizardSteps are the numbered pages before the install, in order.
func wizardSteps(cfg *config.InstallConfig) []step {
	steps := []step{stepImage, stepDisk, stepFilesystem, stepEncryption}
	if cfg.Image.NeedsUserCreation {
		steps = append(steps, stepUser)
	}
	return append(steps, stepConfirm)
}

// stepTitle is the heading of a numbered page: "Step 2 of 6: Disk Selection".
func stepTitle(cfg *config.InstallConfig, s step) string {
	steps := wizardSteps(cfg)
	for i, w := range steps {
		if w == s {
			return fmt.Sprintf("  Step %d of %d: %s", i+1, len(steps), stepNames[s])
		}
	}
	return "  " + stepNames[s]
}

// validateFields asks fisherman about recipe fields. Tests replace it.
var validateFields = backend.ValidatePartial

// checkFields sends the named top-level recipe keys to
// `fisherman validate --json --partial -` and returns "" when they are
// valid, or the text to show on the page. No verdict is not a pass: the
// page stays where it is.
func checkFields(cfg *config.InstallConfig, keys ...string) string {
	fields, err := cfg.Fields(keys...)
	if err != nil {
		return err.Error()
	}
	problems, err := validateFields(fields)
	if err != nil {
		return "Cannot check these settings: " + err.Error()
	}
	var msgs []string
	for _, p := range problems {
		msgs = append(msgs, p.Message)
	}
	return strings.Join(msgs, "\n  ")
}

// stepMsg advances to the next step.
type stepMsg struct{ next step }

// App is the top-level bubbletea model.
type App struct {
	cfg     *config.InstallConfig
	catalog *catalog.Catalog
	current step
	models  map[step]tea.Model
	width   int
	height  int
	dryRun  bool
}

func NewApp(dryRun bool) *App {
	a := &App{
		cfg:     &config.InstallConfig{Hostname: "localhost"},
		catalog: catalog.LoadDefault(),
		current: stepWelcome,
		models:  make(map[step]tea.Model),
		dryRun:  dryRun,
	}
	a.models[stepWelcome] = newWelcomeModel()
	return a
}

// newModel builds the page for a step, from the configuration as it is now.
func (a *App) newModel(s step) tea.Model {
	switch s {
	case stepWelcome:
		return newWelcomeModel()
	case stepImage:
		return newImageModel(a.cfg, a.catalog)
	case stepDisk:
		return newDiskModel(a.cfg)
	case stepFilesystem:
		return newFilesystemModel(a.cfg)
	case stepEncryption:
		return newEncryptionModel(a.cfg)
	case stepUser:
		return newUserModel(a.cfg)
	case stepConfirm:
		return newConfirmModel(a.cfg, a.dryRun)
	case stepProgress:
		return newProgressModel(a.cfg, a.dryRun)
	case stepRecoveryKey:
		return newRecoveryKeyModel(a.cfg)
	case stepDone:
		return newDoneModel(a.cfg, a.dryRun)
	}
	return nil
}

func (a *App) Init() tea.Cmd {
	if m, ok := a.models[a.current]; ok {
		return m.Init()
	}
	return nil
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height

	case stepMsg:
		next := msg.next
		// The install never starts from a configuration that must not
		// reach the backend, whichever page asked for it.
		if next == stepProgress && a.cfg.Check() != nil {
			next = stepConfirm
		}
		m := a.newModel(next)
		if m == nil {
			return a, nil
		}
		a.current = next
		a.models[next] = m
		cmds := []tea.Cmd{m.Init()}
		if a.width > 0 {
			size := tea.WindowSizeMsg{Width: a.width, Height: a.height}
			cmds = append(cmds, func() tea.Msg { return size })
		}
		return a, tea.Batch(cmds...)
	}

	if m, ok := a.models[a.current]; ok {
		updated, cmd := m.Update(msg)
		a.models[a.current] = updated
		return a, cmd
	}
	return a, nil
}

func (a *App) View() string {
	if m, ok := a.models[a.current]; ok {
		return m.View()
	}
	return ""
}
