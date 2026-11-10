package tui

import (
	"fmt"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/tuna-os/fisherman/tui/internal/config"
)

type filesystemModel struct {
	cfg      *config.InstallConfig
	form     *huh.Form
	fsChoice string
	hostname string
	err      string
}

// fsLabels describe the filesystems the TUI can ask for.
var fsLabels = map[string]string{
	"ext4":  "ext4 — stable, widely supported",
	"xfs":   "xfs  — high performance, recommended for servers",
	"btrfs": "btrfs — copy-on-write, snapshots",
}

// filesystemOptions are the choices for the selected image: the catalog's
// list, and never XFS for an image that needs composefs.
func filesystemOptions(cfg *config.InstallConfig) []huh.Option[string] {
	var opts []huh.Option[string]
	for _, fs := range cfg.Image.Filesystems {
		if !cfg.Image.SupportsFilesystem(fs) {
			continue
		}
		label, ok := fsLabels[fs]
		if !ok {
			label = fs
		}
		opts = append(opts, huh.NewOption(label, fs))
	}
	return opts
}

func newFilesystemModel(cfg *config.InstallConfig) *filesystemModel {
	m := &filesystemModel{cfg: cfg}
	m.fsChoice = cfg.Filesystem
	if !cfg.Image.SupportsFilesystem(m.fsChoice) {
		m.fsChoice = cfg.Image.Filesystem
	}
	m.hostname = cfg.Hostname
	if m.hostname == "" {
		m.hostname = "localhost"
	}

	hostnameRe := regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9\-]{0,62}$`)

	m.form = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Root filesystem").
				Description("Choose the root filesystem type and set the hostname for the installed system.").
				Options(filesystemOptions(cfg)...).
				Value(&m.fsChoice),
			huh.NewInput().
				Title("Hostname").
				Placeholder("localhost").
				Value(&m.hostname).
				Validate(func(s string) error {
					if s == "" {
						return fmt.Errorf("hostname is required")
					}
					if !hostnameRe.MatchString(s) {
						return fmt.Errorf("hostname must be alphanumeric/hyphens, start with letter/digit")
					}
					return nil
				}),
		),
	)
	return m
}

func (m *filesystemModel) Init() tea.Cmd {
	return m.form.Init()
}

func (m *filesystemModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		m.cfg.Filesystem = m.fsChoice
		m.cfg.Hostname = m.hostname
		// The image's own rule first (it needs no backend), then
		// fisherman's verdict on the fields this page sets.
		msg := ""
		if err := m.cfg.Check(); err != nil {
			msg = err.Error()
		} else {
			msg = checkFields(m.cfg, "filesystem", "composeFsBackend", "bootloader", "hostname")
		}
		if msg != "" {
			again := newFilesystemModel(m.cfg)
			again.err = msg
			return again, again.Init()
		}
		return m, func() tea.Msg { return stepMsg{next: nextStep(stepFilesystem, m.cfg)} }
	}
	return m, cmd
}

func (m *filesystemModel) View() string {
	var sb strings.Builder
	sb.WriteString("\n")
	sb.WriteString(titleStyle.Render(stepTitle(m.cfg, stepFilesystem)))
	sb.WriteString("\n\n")
	if m.err != "" {
		sb.WriteString(errorStyle.Render("  " + m.err))
		sb.WriteString("\n\n")
	}
	sb.WriteString(m.form.View())
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render("  ↑/↓ navigate • Enter select/next • Ctrl+C quit"))
	return sb.String()
}
