package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/projectbluefin/fisherman/tui/internal/config"
)

type step int

const (
	stepWelcome step = iota
	stepNetwork
	stepImage
	stepDisk
	stepFilesystem
	stepEncryption
	stepUser
	stepSSHKeys
	stepConfirm
	stepProgress
	stepDone
	stepAbort
)

// stepMsg advances to the next step.
type stepMsg struct{ next step }

// abortMsg quits the installer.
type abortMsg struct{}

// App is the top-level bubbletea model.
type App struct {
	cfg     *config.InstallConfig
	current step
	models  map[step]tea.Model
	width   int
	height  int
	dryRun  bool
}

func NewApp(dryRun bool) *App {
	cfg := &config.InstallConfig{
		Hostname:   "localhost",
		Filesystem: "ext4",
	}
	a := &App{
		cfg:     cfg,
		current: stepWelcome,
		models:  make(map[step]tea.Model),
		dryRun:  dryRun,
	}
	a.initModels()
	return a
}

func (a *App) initModels() {
	a.models[stepWelcome] = newWelcomeModel()
	a.models[stepNetwork] = newNetworkModel(a.cfg)
	a.models[stepImage] = newImageModel(a.cfg)
	a.models[stepDisk] = newDiskModel(a.cfg)
	a.models[stepFilesystem] = newFilesystemModel(a.cfg)
	a.models[stepEncryption] = newEncryptionModel(a.cfg)
	a.models[stepUser] = newUserModel(a.cfg)
	a.models[stepSSHKeys] = newSSHKeysModel(a.cfg)
	a.models[stepConfirm] = newConfirmModel(a.cfg, a.dryRun)
	a.models[stepProgress] = newProgressModel(a.cfg, a.dryRun)
	a.models[stepDone] = newDoneModel(a.cfg, a.dryRun)
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
		if m, ok := a.models[a.current]; ok {
			updated, cmd := m.Update(msg)
			a.models[a.current] = updated
			return a, cmd
		}
		return a, nil

	case stepMsg:
		a.current = msg.next
		switch msg.next {
		case stepNetwork:
			a.models[stepNetwork] = newNetworkModel(a.cfg)
		case stepImage:
			a.models[stepImage] = newImageModel(a.cfg)
		case stepDisk:
			a.models[stepDisk] = newDiskModel(a.cfg)
		case stepFilesystem:
			a.models[stepFilesystem] = newFilesystemModel(a.cfg)
		case stepEncryption:
			a.models[stepEncryption] = newEncryptionModel(a.cfg)
		case stepUser:
			a.models[stepUser] = newUserModel(a.cfg)
		case stepSSHKeys:
			a.models[stepSSHKeys] = newSSHKeysModel(a.cfg)
		case stepConfirm:
			a.models[stepConfirm] = newConfirmModel(a.cfg, a.dryRun)
		case stepProgress:
			a.models[stepProgress] = newProgressModel(a.cfg, a.dryRun)
		case stepDone:
			a.models[stepDone] = newDoneModel(a.cfg, a.dryRun)
		}
		if m, ok := a.models[a.current]; ok {
			return a, m.Init()
		}
		return a, nil

	case abortMsg:
		return a, tea.Quit
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
