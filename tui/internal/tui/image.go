package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/tuna-os/fisherman/tui/internal/catalog"
	"github.com/tuna-os/fisherman/tui/internal/config"
)

// The boot stack choices for an image the catalog does not know. Nothing is
// assumed for a custom reference: GRUB with ostree is wrong for a
// composefs-native image and the other way round, and either mistake shows
// only after the disk is wiped (#367).
const (
	stackUnset    = ""
	stackGrub     = "grub-ostree"
	stackComposeF = "systemd-composefs"
)

const customImage = "custom"

type imageModel struct {
	cfg      *config.InstallConfig
	catalog  *catalog.Catalog
	images   []catalog.Image
	form     *huh.Form
	selected string
	custom   string
	stack    string
}

func newImageModel(cfg *config.InstallConfig, cat *catalog.Catalog) *imageModel {
	m := &imageModel{cfg: cfg, catalog: cat, images: cat.Images()}
	m.selected = m.images[0].Ref
	if cfg.Image.Ref != "" {
		m.selected = customImage
		m.custom = cfg.Image.Ref
		for _, img := range m.images {
			if img.Ref == cfg.Image.Ref {
				m.selected, m.custom = img.Ref, ""
			}
		}
	}

	opts := make([]huh.Option[string], 0, len(m.images)+1)
	for _, img := range m.images {
		opts = append(opts, huh.NewOption(img.Label, img.Ref))
	}
	opts = append(opts, huh.NewOption("Custom URL...", customImage))

	m.form = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("OS Image").
				Description("Select the bootc image to install, or choose Custom to enter a URL.").
				Options(opts...).
				Value(&m.selected),
		),
		huh.NewGroup(
			huh.NewInput().
				Title("Custom image URL").
				Placeholder("registry.example.com/image:tag").
				Value(&m.custom).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return fmt.Errorf("image URL is required")
					}
					return nil
				}),
			huh.NewSelect[string]().
				Title("Boot stack of this image").
				Description("The installer cannot tell from a URL. A wrong choice fails after the disk is erased.").
				Options(
					huh.NewOption("Choose...", stackUnset),
					huh.NewOption("GRUB with ostree (Fedora, CentOS and Universal Blue images)", stackGrub),
					huh.NewOption("systemd-boot with composefs (Dakota, bootcrew Arch, Debian and Ubuntu images)", stackComposeF),
				).
				Value(&m.stack).
				Validate(func(s string) error {
					if s == stackUnset {
						return fmt.Errorf("choose the boot stack of the image")
					}
					return nil
				}),
		).WithHideFunc(func() bool { return m.selected != customImage }),
	)
	return m
}

// choice returns the image the form describes.
func (m *imageModel) choice() catalog.Image {
	return resolveImage(m.catalog, m.selected, m.custom, m.stack)
}

// resolveImage turns the form's answers into an Image. A listed image, and
// a custom reference the catalog knows, take the catalog's settings; any
// other reference takes the boot stack the user chose.
func resolveImage(cat *catalog.Catalog, selected, custom, stack string) catalog.Image {
	ref := selected
	if selected == customImage {
		ref = strings.TrimSpace(custom)
	}
	if img, ok := cat.Resolve(ref); ok {
		return img
	}
	if stack == stackComposeF {
		return catalog.Custom(ref, true, "systemd")
	}
	return catalog.Custom(ref, false, "")
}

func (m *imageModel) Init() tea.Cmd {
	return m.form.Init()
}

func (m *imageModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		m.cfg.Image = m.choice()
		// A filesystem chosen for another image may not fit this one.
		if !m.cfg.Image.SupportsFilesystem(m.cfg.Filesystem) {
			m.cfg.Filesystem = m.cfg.Image.Filesystem
		}
		return m, func() tea.Msg { return stepMsg{next: nextStep(stepImage, m.cfg)} }
	}
	return m, cmd
}

func (m *imageModel) View() string {
	var sb strings.Builder
	sb.WriteString("\n")
	sb.WriteString(titleStyle.Render(stepTitle(m.cfg, stepImage)))
	sb.WriteString("\n\n")
	sb.WriteString(m.form.View())
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render("  ↑/↓ navigate • Enter select • Ctrl+C quit"))
	return sb.String()
}
