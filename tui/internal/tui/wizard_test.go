package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tuna-os/fisherman/tui/internal/config"
)

// driver runs an App the way bubbletea does, without a terminal: commands
// run in goroutines and their messages come back to Update on one goroutine.
type driver struct {
	t    *testing.T
	app  *App
	msgs chan tea.Msg
	quit bool
}

func newDriver(t *testing.T, app *App) *driver {
	d := &driver{t: t, app: app, msgs: make(chan tea.Msg, 256)}
	d.spawn(app.Init())
	d.send(tea.WindowSizeMsg{Width: 100, Height: 40})
	return d
}

func (d *driver) spawn(cmd tea.Cmd) {
	if cmd != nil {
		go func() { d.msgs <- cmd() }()
	}
}

func (d *driver) send(msg tea.Msg) {
	switch msg := msg.(type) {
	case nil:
		return
	case tea.BatchMsg:
		for _, c := range msg {
			d.spawn(c)
		}
		return
	case tea.QuitMsg:
		d.quit = true
		return
	default:
		// A blinking cursor re-arms itself for ever; it changes nothing.
		if strings.Contains(strings.ToLower(fmt.Sprintf("%T", msg)), "blink") {
			return
		}
		_, cmd := d.app.Update(msg)
		d.spawn(cmd)
	}
}

// until processes messages until cond holds.
func (d *driver) until(what string, cond func() bool) {
	d.t.Helper()
	deadline := time.After(20 * time.Second)
	for !cond() {
		select {
		case msg := <-d.msgs:
			d.send(msg)
		case <-deadline:
			d.t.Fatalf("timed out waiting for %s; the wizard is on step %d (%s):\n%s",
				what, d.app.current, stepNames[d.app.current], d.app.View())
		}
	}
}

// settle processes messages until none arrives for a moment: a form turns
// one key press into several internal messages (next field, next group).
func (d *driver) settle() {
	for {
		select {
		case msg := <-d.msgs:
			d.send(msg)
		case <-time.After(80 * time.Millisecond):
			return
		}
	}
}

// press sends a key and lets the form finish reacting to it.
func (d *driver) press(k tea.KeyMsg) {
	d.send(k)
	d.settle()
}

func (d *driver) typeText(text string) {
	for _, r := range text {
		d.send(keyRune(r))
	}
	d.settle()
}

func (d *driver) key(k tea.KeyMsg, wantStep step) {
	d.t.Helper()
	d.settle()
	d.send(k)
	d.until(stepNames[wantStep], func() bool { return d.app.current == wantStep })
}

var (
	keyEnter = tea.KeyMsg{Type: tea.KeyEnter}
	keyDown  = tea.KeyMsg{Type: tea.KeyDown}
)

func keyRune(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

// TestWizard_DryRunEndToEnd walks the real pages with key presses, from the
// welcome banner to the done page, for the default image (Dakota). It pins
// the page order, that no SSH key or network page appears on the way, that
// Dakota's boot stack reaches the recipe, and that the recipe shown at the
// end hides nothing but the secrets.
func TestWizard_DryRunEndToEnd(t *testing.T) {
	acceptAllFields(t)
	stubDisks(t, oneDisk, nil)
	t.Setenv("FISHERMAN_IMAGES_PATH", "../../../data/images.json")
	origDelay := dryRunDelay
	dryRunDelay = 0
	t.Cleanup(func() { dryRunDelay = origDelay })

	app := NewApp(true)
	d := newDriver(t, app)
	var visited []step
	record := func() { visited = append(visited, app.current) }

	record()
	d.key(keyEnter, stepImage)
	record()
	d.key(keyEnter, stepDisk) // the first image in the list
	record()
	d.key(keyEnter, stepFilesystem)
	record()
	d.press(keyEnter) // the filesystem select; the hostname input is next
	d.key(keyEnter, stepEncryption)
	record()
	d.press(keyRune('y')) // enable encryption
	d.typeText("correct horse")
	d.press(keyEnter)
	d.typeText("correct horse")
	// Dakota creates its account at first boot: the confirm page is next.
	d.key(keyEnter, stepConfirm)
	record()
	d.settle()
	d.send(keyRune('y')) // start
	d.until("the done page", func() bool { return app.current == stepDone })
	record()

	want := []step{stepWelcome, stepImage, stepDisk, stepFilesystem, stepEncryption, stepConfirm, stepDone}
	if fmt.Sprint(visited) != fmt.Sprint(want) {
		t.Fatalf("pages visited = %v, want %v", visited, want)
	}

	cfg := app.cfg
	if cfg.Image.Ref != "ghcr.io/projectbluefin/dakota:latest" || !cfg.Image.ComposeFsBackend || cfg.Image.Bootloader != "systemd" {
		t.Errorf("image = %+v, want Dakota with composefs and systemd-boot", cfg.Image)
	}
	if cfg.DiskDevice != "/dev/nvme0n1" || cfg.Filesystem != "btrfs" || cfg.Hostname != "localhost" {
		t.Errorf("disk=%q filesystem=%q hostname=%q", cfg.DiskDevice, cfg.Filesystem, cfg.Hostname)
	}
	if !cfg.EncryptionEnabled || cfg.Passphrase != "correct horse" {
		t.Errorf("encryption: enabled=%v passphrase=%q", cfg.EncryptionEnabled, cfg.Passphrase)
	}

	view := app.View()
	for _, wantLine := range []string{`"composeFsBackend": true`, `"bootloader": "systemd"`, `"filesystem": "btrfs"`, `"disk": "/dev/nvme0n1"`, config.Redacted} {
		if !strings.Contains(view, wantLine) {
			t.Errorf("the done page lacks %s:\n%s", wantLine, view)
		}
	}
	if strings.Contains(view, "correct horse") {
		t.Errorf("the done page shows the passphrase:\n%s", view)
	}
	if strings.Contains(view, `"user"`) {
		t.Errorf("Dakota's recipe has a user block:\n%s", view)
	}

	d.settle()
	d.send(keyEnter)
	d.until("the quit", func() bool { return d.quit })
}

// The user page appears for an image that needs an account, and its answers
// reach the recipe.
func TestWizard_UserPageForImagesThatNeedAnAccount(t *testing.T) {
	acceptAllFields(t)
	stubDisks(t, oneDisk, nil)
	t.Setenv("FISHERMAN_IMAGES_PATH", "../../../data/images.json")

	app := NewApp(true)
	d := newDriver(t, app)
	d.key(keyEnter, stepImage)
	d.settle()
	d.press(keyDown) // the second image: Aurora
	d.key(keyEnter, stepDisk)
	d.key(keyEnter, stepFilesystem)
	d.settle()
	d.press(keyEnter)
	d.key(keyEnter, stepEncryption)
	d.key(keyRune('n'), stepUser)
	if app.cfg.Image.Ref != "ghcr.io/ublue-os/aurora:latest" || app.cfg.Image.ComposeFsBackend {
		t.Fatalf("image = %+v, want Aurora on the ostree backend", app.cfg.Image)
	}
	for _, field := range []string{"Alice", "alice", "hunter2-password", "hunter2-password"} {
		d.settle()
		d.typeText(field)
		d.press(keyEnter)
	}
	d.until("the confirm page", func() bool { return app.current == stepConfirm })
	if app.cfg.Username != "alice" || app.cfg.Password != "hunter2-password" {
		t.Errorf("username=%q password set=%v", app.cfg.Username, app.cfg.Password != "")
	}
	view := app.View()
	if !strings.Contains(view, "alice") || !strings.Contains(view, "GRUB, ostree") || strings.Contains(view, "hunter2-password") {
		t.Errorf("the summary is wrong:\n%s", view)
	}
	// "Go back" from the confirm page returns to the user page.
	d.key(keyRune('n'), stepUser)
}
