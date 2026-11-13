package tui

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tuna-os/fisherman/tui/internal/backend"
	"github.com/tuna-os/fisherman/tui/internal/catalog"
)

func stubDisks(t *testing.T, disks []backend.Disk, err error) {
	t.Helper()
	orig := probeDisks
	probeDisks = func() ([]backend.Disk, error) { return disks, err }
	t.Cleanup(func() { probeDisks = orig })
}

var oneDisk = []backend.Disk{{Path: "/dev/nvme0n1", SizeLabel: "512 GB", Model: "Samsung SSD 980", Eligible: true}}

// walk follows nextStep from the welcome page to the install.
func walk(needsUser bool) []step {
	cfg := testConfig()
	cfg.Image.NeedsUserCreation = needsUser
	var path []step
	for s := stepWelcome; s != stepProgress; s = nextStep(s, cfg) {
		path = append(path, s)
		if len(path) > 20 {
			break
		}
	}
	return path
}

// TestWizardFlow_NoSSHKeyOrNetworkPage: #361 and #417. Neither page can be
// reached, because neither exists: the recipe has no field for an SSH key or
// for network settings, and both pages reported success for input that was
// then dropped.
func TestWizardFlow_NoSSHKeyOrNetworkPage(t *testing.T) {
	want := []step{stepWelcome, stepImage, stepDisk, stepFilesystem, stepEncryption, stepUser, stepConfirm}
	if got := walk(true); !reflect.DeepEqual(got, want) {
		t.Fatalf("flow = %v, want %v", got, want)
	}
	// An image that creates its account at first boot skips the user page.
	want = []step{stepWelcome, stepImage, stepDisk, stepFilesystem, stepEncryption, stepConfirm}
	if got := walk(false); !reflect.DeepEqual(got, want) {
		t.Fatalf("flow without account creation = %v, want %v", got, want)
	}

	for s, name := range stepNames {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "ssh") || strings.Contains(lower, "network") {
			t.Errorf("step %d is the %q page, which must not exist", s, name)
		}
	}

	// Every step that exists has a page, and no other step can be opened.
	acceptAllFields(t)
	stubDisks(t, oneDisk, nil)
	app := NewApp(true)
	app.cfg = testConfig()
	for s := range stepNames {
		if app.newModel(s) == nil {
			t.Errorf("step %d (%s) has no page", s, stepNames[s])
		}
	}
	for _, s := range []step{-1, step(len(stepNames)), step(len(stepNames) + 1)} {
		if app.newModel(s) != nil {
			t.Errorf("step %d must not exist", s)
		}
		app.current = stepUser
		app.Update(stepMsg{next: s})
		if app.current != stepUser {
			t.Errorf("a stepMsg for the non-existent step %d moved the wizard to %d", s, app.current)
		}
	}

	// The user page leads to the confirm page, and the summary promises
	// nothing about SSH keys.
	if next := nextStep(stepUser, app.cfg); next != stepConfirm {
		t.Errorf("after the user page: step %d, want the confirm page", next)
	}
	if view := newConfirmModel(app.cfg, false).View(); strings.Contains(strings.ToLower(view), "ssh") {
		t.Errorf("the summary mentions SSH keys:\n%s", view)
	}
}

func TestStepTitle(t *testing.T) {
	cfg := testConfig()
	if got := stepTitle(cfg, stepConfirm); got != "  Step 6 of 6: Confirm Installation" {
		t.Errorf("stepTitle = %q", got)
	}
	cfg.Image.NeedsUserCreation = false
	if got := stepTitle(cfg, stepConfirm); got != "  Step 5 of 5: Confirm Installation" {
		t.Errorf("stepTitle without the user page = %q", got)
	}
	if got := stepTitle(cfg, stepImage); got != "  Step 1 of 5: OS Image" {
		t.Errorf("stepTitle = %q", got)
	}
}

// TestConfirm_RejectsXFSWithComposeFS: #367. The pair cannot be confirmed,
// and the install cannot be started around the confirm page.
func TestConfirm_RejectsXFSWithComposeFS(t *testing.T) {
	acceptAllFields(t) // the backend is not what stops it here
	stubDisks(t, oneDisk, nil)
	app := NewApp(false)
	app.cfg = testConfig()
	app.cfg.Image = testDakota
	app.cfg.Filesystem = "xfs"

	confirm := newConfirmModel(app.cfg, false)
	if !strings.Contains(confirm.blocked, "XFS") {
		t.Fatalf("blocked = %q, want the XFS with composefs reason", confirm.blocked)
	}
	for _, proceed := range []bool{true, false} {
		confirm.proceed = proceed
		if next := confirm.next(); next == stepProgress {
			t.Fatalf("the blocked confirm page leads to the install (proceed=%v)", proceed)
		}
	}
	if view := confirm.View(); strings.Contains(view, "Install now") || !strings.Contains(view, "cannot use XFS") {
		t.Errorf("the blocked confirm page offers the install:\n%s", view)
	}

	// The App refuses the install step itself.
	app.current = stepConfirm
	app.Update(stepMsg{next: stepProgress})
	if app.current == stepProgress {
		t.Fatal("the App started the install with XFS on a composefs image")
	}

	// The same image with ext4 goes through.
	app.cfg.Filesystem = "ext4"
	confirm = newConfirmModel(app.cfg, false)
	confirm.proceed = true
	if confirm.blocked != "" || confirm.next() != stepProgress {
		t.Fatalf("ext4 on Dakota: blocked=%q next=%d", confirm.blocked, confirm.next())
	}
}

// The filesystem page does not offer XFS for a composefs image.
func TestFilesystemOptions(t *testing.T) {
	keys := func(img catalog.Image) []string {
		cfg := testConfig()
		cfg.Image = img
		var out []string
		for _, o := range filesystemOptions(cfg) {
			out = append(out, o.Value)
		}
		return out
	}
	if got := keys(testGrub); !reflect.DeepEqual(got, []string{"ext4", "xfs", "btrfs"}) {
		t.Errorf("GRUB image: %v", got)
	}
	if got := keys(testDakota); !reflect.DeepEqual(got, []string{"btrfs", "ext4"}) {
		t.Errorf("Dakota: %v", got)
	}
	if got := keys(catalog.Custom("registry.example.com/os:1", true, "systemd")); !reflect.DeepEqual(got, []string{"ext4", "btrfs"}) {
		t.Errorf("custom composefs image: %v", got)
	}
	// An XFS choice left over from another image is replaced by the page.
	cfg := testConfig()
	cfg.Image, cfg.Filesystem = testDakota, "xfs"
	if m := newFilesystemModel(cfg); m.fsChoice != "btrfs" {
		t.Errorf("the page starts on %q, want the image's default btrfs", m.fsChoice)
	}
}

// A custom URL assumes no boot stack; a URL the catalog knows takes the
// catalog's.
func TestResolveImage(t *testing.T) {
	var cat *catalog.Catalog
	listed := resolveImage(cat, "ghcr.io/bootcrew/arch-bootc:latest", "", stackUnset)
	if !listed.ComposeFsBackend || listed.Bootloader != "systemd" || listed.Filesystem != "ext4" {
		t.Errorf("Arch = %+v", listed)
	}
	custom := resolveImage(cat, customImage, " registry.example.com/os:1 ", stackComposeF)
	if custom.Ref != "registry.example.com/os:1" || !custom.ComposeFsBackend || custom.Bootloader != "systemd" {
		t.Errorf("custom composefs = %+v", custom)
	}
	custom = resolveImage(cat, customImage, "registry.example.com/os:1", stackGrub)
	if custom.ComposeFsBackend || custom.Bootloader != "" || !custom.NeedsUserCreation {
		t.Errorf("custom GRUB = %+v", custom)
	}
	// Typing Dakota's URL by hand does not make it a GRUB image, whatever
	// the boot stack answer was.
	typed := resolveImage(cat, customImage, "ghcr.io/projectbluefin/dakota:latest", stackGrub)
	if !typed.ComposeFsBackend || typed.Bootloader != "systemd" {
		t.Errorf("Dakota typed as a custom URL = %+v", typed)
	}
}

// The disk page offers what fisherman calls eligible and nothing else. A
// failed probe offers nothing; the page used to invent /dev/sda (#417).
func TestDiskPage(t *testing.T) {
	t.Run("eligible disks only", func(t *testing.T) {
		stubDisks(t, []backend.Disk{
			{Path: "/dev/nvme0n1", SizeLabel: "512 GB", Model: "Samsung SSD 980", Eligible: true},
			{Path: "/dev/sda", SizeLabel: "62 GB", Model: "Ultra Fit", ExcludedReason: "boot_disk"},
		}, nil)
		m := newDiskModel(testConfig())
		if m.form == nil || m.selected != "/dev/nvme0n1" {
			t.Fatalf("selected = %q, want the eligible disk (the stale /dev/sda in the config is not offered)", m.selected)
		}
		if _, offered := m.labels["/dev/sda"]; offered {
			t.Error("the live medium is offered as a target")
		}
		if view := m.View(); !strings.Contains(view, "boot_disk") {
			t.Errorf("the page should say why /dev/sda is not offered:\n%s", view)
		}
	})
	for name, tc := range map[string]struct {
		disks []backend.Disk
		err   error
	}{
		"probe failed":     {nil, errors.New("fisherman probe --json: exit status 1: lsblk failed")},
		"no fisherman":     {nil, backend.ErrNotFound},
		"nothing eligible": {[]backend.Disk{{Path: "/dev/sdb", SizeLabel: "8 GB", ExcludedReason: "too_small"}}, nil},
		"no disk at all":   {nil, nil},
	} {
		t.Run(name, func(t *testing.T) {
			stubDisks(t, tc.disks, tc.err)
			cfg := testConfig()
			cfg.DiskDevice = ""
			m := newDiskModel(cfg)
			if m.form != nil || m.err == "" {
				t.Fatalf("want no form and an error, got form=%v err=%q", m.form != nil, m.err)
			}
			if view := m.View(); strings.Contains(view, "QEMU") || strings.Contains(view, "/dev/sda") {
				t.Errorf("the page shows a disk that does not exist:\n%s", view)
			}
			// Enter does not move on, and no disk reaches the config.
			_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			if len(steps(drain(cmd))) != 0 || cfg.DiskDevice != "" {
				t.Errorf("the page moved on without a disk (DiskDevice=%q)", cfg.DiskDevice)
			}
			// r probes again.
			_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
			if got := steps(drain(cmd)); !reflect.DeepEqual(got, []step{stepDisk}) {
				t.Errorf("r: transitions = %v, want the disk page again", got)
			}
		})
	}
}

// checkFields: problems are shown, and no verdict is not a pass.
func TestCheckFields(t *testing.T) {
	orig := validateFields
	t.Cleanup(func() { validateFields = orig })
	cfg := testConfig()

	var sent string
	validateFields = func(fields []byte) ([]backend.Problem, error) { sent = string(fields); return nil, nil }
	if msg := checkFields(cfg, "hostname"); msg != "" || sent != `{"hostname":"marlin"}` {
		t.Errorf("valid: msg=%q sent=%q", msg, sent)
	}
	validateFields = func([]byte) ([]backend.Problem, error) {
		return []backend.Problem{{Field: "hostname", Code: "hostname_label_hyphen", Message: "hostname label must not start with a hyphen"}}, nil
	}
	if msg := checkFields(cfg, "hostname"); !strings.Contains(msg, "must not start with a hyphen") {
		t.Errorf("invalid: msg=%q", msg)
	}
	validateFields = func([]byte) ([]backend.Problem, error) { return nil, backend.ErrNotFound }
	if msg := checkFields(cfg, "hostname"); !strings.Contains(msg, "Cannot check") {
		t.Errorf("no verdict must not pass: msg=%q", msg)
	}
	if blocked := confirmBlocker(cfg); blocked == "" {
		t.Error("the confirm page must not open the install when fisherman gives no verdict")
	}
}
