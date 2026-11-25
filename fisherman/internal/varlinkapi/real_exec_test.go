package varlinkapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/tuna-os/fisherman/internal/crypt"
)

// fakeCmd is the Commander of the unit tests: it records every command line
// (stdin shows as its length, never its bytes) and answers the way the host
// tools would, enough for the whole install to run against a fixture tree.
type fakeCmd struct {
	t    *testing.T
	root string // fixture root: the probe root and the scratch tree parent

	mu    sync.Mutex
	lines []string
	// ctxErr records ctx.Err() at each command, to prove cleanup runs on a
	// context that survives a cancel.
	ctxErr map[string]error

	// hooks: return handled=true to take over a command.
	hook func(ctx context.Context, name string, args []string, o CmdOpts) (out []byte, err error, handled bool)

	uuidCalls map[string]int
	mounted   []string
	// the fixture gets populated on mount.
	oldUUID map[string]string
}

func newFakeCmd(t *testing.T, root string) *fakeCmd {
	return &fakeCmd{t: t, root: root, ctxErr: map[string]error{}, uuidCalls: map[string]int{},
		oldUUID: map[string]string{
			"/dev/fake1p1": "AAAA-0001",
			"/dev/fake1p2": "0b0b0b0b-0000-4000-8000-000000000002",
		}}
}

func (f *fakeCmd) record(name string, args []string, o CmdOpts) string {
	line := name + " " + strings.Join(args, " ")
	if o.Stdin != nil {
		line += fmt.Sprintf(" <stdin %d bytes>", len(o.Stdin))
	}
	f.mu.Lock()
	f.lines = append(f.lines, strings.TrimSpace(line))
	f.mu.Unlock()
	return line
}

var mapperRe = regexp.MustCompile(`fishermand-[0-9a-f]{8}`)
var installRe = regexp.MustCompile(`install-[0-9]+`)

// normalized returns the command lines with the random parts fixed.
func (f *fakeCmd) normalized() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var b strings.Builder
	for _, l := range f.lines {
		l = strings.ReplaceAll(l, f.root, "$ROOT")
		l = mapperRe.ReplaceAllString(l, "$$MAPPER")
		l = installRe.ReplaceAllString(l, "install-$$N")
		b.WriteString(l + "\n")
	}
	return b.String()
}

func (f *fakeCmd) Run(ctx context.Context, name string, args []string, o CmdOpts) ([]byte, error) {
	f.record(name, args, o)
	key := name + " " + strings.Join(args, " ")
	f.mu.Lock()
	f.ctxErr[key] = ctx.Err()
	f.mu.Unlock()
	if f.hook != nil {
		if out, err, ok := f.hook(ctx, name, args, o); ok {
			return out, err
		}
	}
	switch name {
	case "lsblk":
		return f.lsblk(args)
	case "sfdisk":
		return f.sfdisk(args[len(args)-1])
	case "systemd-repart":
		return f.repart(args)
	case "cryptsetup":
		if args[0] == "luksUUID" {
			return []byte("99999999-aaaa-bbbb-cccc-dddddddddddd\n"), nil
		}
	case "blkid":
		return f.blkid(args)
	case "btrfs": // inspect-internal dump-super: a seed, like btrfstune -S 1 leaves it
		return []byte("flags\t\t\t0x100000001\n\t\t\t( WRITTEN |\n\t\t\t  SEEDING )\n"), nil
	case "mount":
		return nil, f.mount(args[0], args[1])
	case "useradd": // what useradd --root writes
		var dep string
		for i, x := range args {
			if x == "--root" {
				dep = args[i+1]
			}
		}
		user := args[len(args)-1]
		if err := os.MkdirAll(filepath.Join(dep, "etc"), 0o755); err != nil {
			return nil, err
		}
		return nil, os.WriteFile(filepath.Join(dep, "etc/shadow"), []byte("root:*:1:::::\n"+user+":!:19000:0:99999:7:::\n"), 0o640)
	}
	return nil, nil
}

func (f *fakeCmd) mediumNode(n int) string {
	return filepath.Join(f.root, "dev", fmt.Sprintf("fakemed%d", n))
}

func (f *fakeCmd) lsblk(args []string) ([]byte, error) {
	if len(args) > 0 && args[0] == "-rno" { // PKNAME of the medium root
		return []byte("fakemed\n"), nil
	}
	cols := args[len(args)-1]
	if strings.Contains(cols, "KNAME") { // probe.Prober: Disks and Target
		return []byte(`{"blockdevices":[
 {"name":"/dev/fake1","kname":"/dev/fake1","type":"disk","size":68719476736,"model":"Fake Disk","vendor":"Acme","tran":"nvme","rm":false,"hotplug":false,"ro":false,"serial":"S1","wwn":null,"mountpoint":null,"mountpoints":[null]},
 {"name":"/dev/fakemed","kname":"/dev/fakemed","type":"disk","size":3221225472,"model":"Medium","vendor":"Acme","tran":"usb","rm":true,"hotplug":true,"ro":false,"serial":null,"wwn":null,"mountpoint":null,"mountpoints":["/sysroot"]}
]}`), nil
	}
	return []byte(`{"blockdevices":[
 {"name":"/dev/fake1","type":"disk","size":68719476736,"fstype":null,"label":null,"children":[
   {"name":"/dev/fake1p1","type":"part","size":104857600,"fstype":"ntfs","label":"Windows"}]},
 {"name":"/dev/fakemed","type":"disk","size":3221225472,"fstype":null,"label":null}
]}`), nil
}

func (f *fakeCmd) sfdisk(disk string) ([]byte, error) {
	if strings.HasSuffix(disk, "fakemed") || strings.HasSuffix(disk, "/dev/fakemed") {
		return []byte(fmt.Sprintf(`{"partitiontable":{"label":"gpt","sectorsize":512,"firstlba":2048,"lastlba":6291422,"partitions":[
 {"node":%q,"start":2048,"size":204800,"type":"C12A7328-F81F-11D2-BA4B-00A0C93EC93B","uuid":"AAAAAAAA-0000-4000-8000-000000000001","name":"EFI-SYSTEM"},
 {"node":%q,"start":206848,"size":524288,"type":"BC13C2FF-59E6-4262-A352-B275FD6F7172","uuid":"AAAAAAAA-0000-4000-8000-000000000002","name":"boot"},
 {"node":%q,"start":731136,"size":5560000,"type":"4F68BCE3-E8CD-4DB1-96E7-FBCAF984B709","uuid":"AAAAAAAA-0000-4000-8000-000000000003","name":"root"}]}}`,
			f.mediumNode(1), f.mediumNode(2), f.mediumNode(3))), nil
	}
	// the target: a 64 GiB GPT disk holding one 100 MiB partition
	return []byte(`{"partitiontable":{"label":"gpt","sectorsize":512,"firstlba":34,"lastlba":134217694,"partitions":[
 {"node":"/dev/fake1p1","start":2048,"size":204800,"type":"EBD0A0A2-B9E5-4433-87C0-68B6B72699C7"}]}}`), nil
}

func (f *fakeCmd) repart(args []string) ([]byte, error) {
	var defs string
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "--definitions="); ok {
			defs = v
		}
	}
	// the three definitions must exist when repart runs
	for _, n := range []string{"01-copy.conf", "02-copy.conf", "03-copy.conf"} {
		if _, err := os.Stat(filepath.Join(defs, n)); err != nil {
			return nil, fmt.Errorf("systemd-repart: %w", err)
		}
	}
	return []byte(fmt.Sprintf(`[
{"type":"esp","label":"EFI-SYSTEM","uuid":"u1","partno":1,"file":"%[1]s/01-copy.conf","node":"/dev/fake1p2","activity":"create"},
{"type":"xbootldr","label":"boot","uuid":"u2","partno":2,"file":"%[1]s/02-copy.conf","node":"/dev/fake1p3","activity":"create"},
{"type":"root-x86-64","label":"root","uuid":"u3","partno":3,"file":"%[1]s/03-copy.conf","node":"/dev/fake1p4","activity":"create"}]`, defs)), nil
}

// the target partitions of the fixture, by role
const (
	fESP  = "/dev/fake1p2"
	fBoot = "/dev/fake1p3"
	fRoot = "/dev/fake1p4"
)

func (f *fakeCmd) blkid(args []string) ([]byte, error) {
	dev := args[len(args)-1]
	want := args[2] // TYPE or UUID
	if want == "TYPE" {
		switch dev {
		case fESP:
			return []byte("vfat\n"), nil
		case fBoot:
			return []byte("ext4\n"), nil
		default:
			return []byte("btrfs\n"), nil
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uuidCalls[dev]++
	suffix := map[bool]string{true: "OLD", false: "NEW"}[f.uuidCalls[dev]%2 == 1]
	base := map[string]string{fESP: "ABCD-", fBoot: "boot-", "other": "root-"}
	b, ok := base[dev]
	if !ok {
		b = base["other"]
	}
	return []byte(b + suffix + "\n"), nil
}

// mount fills the fixture tree the copied partitions would hold.
func (f *fakeCmd) mount(dev, dir string) error {
	f.mu.Lock()
	f.mounted = append(f.mounted, dir)
	f.mu.Unlock()
	w := func(rel, text string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			f.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
	switch dev {
	case fESP:
		w("EFI/fedora/shimx64.efi", "shim")
		w("EFI/BOOT/BOOTX64.EFI", "fallback")
		w("EFI/tunaos-live/vmlinuz", "live kernel")
		w("EFI/fedora/grub.cfg", "search --fs-uuid ABCD-OLD\nroot=UUID=root-OLD\n")
	case fBoot:
		w("loader/entries/ostree-1.conf", "title TunaOS\noptions root=UUID=root-OLD rw\n")
		w("loader/entries/medium-live.conf", "title TunaOS Live\noptions root=UUID=root-OLD ro tunaos.live\n")
		w("grub2/grubenv", "# GRUB Environment Block\nsaved_entry=medium-live\n"+strings.Repeat("#", 960))
	default: // the unlocked root
		w("ostree/deploy/default/deploy/abc.0/etc/machine-id", "uninitialized\n")
		w("ostree/deploy/default/deploy/abc.0/etc/fstab", "UUID=root-OLD / btrfs defaults 0 0\nUUID=boot-OLD /boot ext4 defaults 0 0\n")
		w("ostree/deploy/default/deploy/abc.0/etc/group", "root:x:0:\nwheel:x:10:\n")
		w("ostree/deploy/default/deploy/abc.0/usr/lib/systemd/system/gdm.service", "")
		w("ostree/deploy/default/var/lib/extensions/medium-sysinstall.raw", "sysext")
	}
	return nil
}

// fakeCrypt is the crypt.Runner under the Enroller: a LUKS volume with the
// empty slot 0 and, optionally, a TPM2 token on slot 1.
type fakeCrypt struct {
	mu    sync.Mutex
	lines []string
	slots []int
	tpm   bool
}

func (f *fakeCrypt) Run(_ context.Context, name string, args []string, stdin []byte, unlock crypt.Unlock) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	line := name + " " + strings.Join(args, " ")
	if stdin != nil {
		line += fmt.Sprintf(" <new secret %d bytes>", len(stdin))
	}
	switch {
	case unlock.Empty:
		line += " <unlock empty>"
	case unlock.Secret != nil:
		line += fmt.Sprintf(" <unlock secret %d bytes>", len(unlock.Secret))
	}
	f.lines = append(f.lines, "crypt: "+line)
	switch {
	case name == "cryptsetup" && args[0] == "luksUUID":
		return []byte("u\n"), nil
	case name == "cryptsetup" && args[0] == "luksDump":
		var b strings.Builder
		b.WriteString("LUKS header information\nVersion:       \t2\n\nKeyslots:\n")
		for _, s := range f.slots {
			fmt.Fprintf(&b, "  %d: luks2\n\tKey:       \t512 bits\n", s)
		}
		b.WriteString("Tokens:\n")
		if f.tpm {
			b.WriteString("  0: systemd-tpm2\n\tKeyslot:  1\n")
		}
		return []byte(b.String()), nil
	case name == "systemd-cryptenroll":
		for _, a := range args {
			if v, ok := strings.CutPrefix(a, "--wipe-slot="); ok {
				var keep []int
				for _, s := range f.slots {
					if fmt.Sprint(s) != v {
						keep = append(keep, s)
					}
				}
				f.slots = keep
			}
		}
		for _, a := range args {
			if a == "--password" {
				next := 0
				for contains(f.slots, next) {
					next++
				}
				f.slots = append(f.slots, next)
				sort.Ints(f.slots)
			}
		}
	}
	return nil, nil
}

func contains(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// recReporter records Install events.
type recReporter struct {
	mu     sync.Mutex
	events []string
}

func (r *recReporter) add(s string) {
	r.mu.Lock()
	r.events = append(r.events, s)
	r.mu.Unlock()
}
func (r *recReporter) Start(step string) { r.add("start " + step) }
func (r *recReporter) Progress(step string, pct int, msg string) {
	if pct == 100 || msg != "" {
		r.add(fmt.Sprintf("progress %s %d %s", step, pct, msg))
	}
}
func (r *recReporter) Warn(step, msg string) { r.add("warn " + step + ": " + msg) }
func (r *recReporter) Done(step string)      { r.add("done " + step) }

func (r *recReporter) started() []string {
	var out []string
	for _, e := range r.events {
		if s, ok := strings.CutPrefix(e, "start "); ok {
			out = append(out, s)
		}
	}
	return out
}

// installFixture builds a Real over fakes.
type installFixture struct {
	real   Real
	cmd    *fakeCmd
	crypt  *fakeCrypt
	root   string
	work   string
	fatSet []string
}

func newInstallFixture(t *testing.T, tpm bool) *installFixture {
	t.Helper()
	root := t.TempDir()
	fx := &installFixture{root: root, work: filepath.Join(root, "run", "fishermand")}
	for _, d := range []string{"dev/disk/by-uuid", "proc", "sys/class/tpm/tpm0", "sys/devices/system/cpu"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"fake1", "fakemed", "fakemed1", "fakemed2", "fakemed3"} {
		if err := os.WriteFile(filepath.Join(root, "dev", n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// root=UUID names the medium root, /dev/disk/by-uuid resolves it.
	if err := os.Symlink(filepath.Join(root, "dev", "fakemed3"), filepath.Join(root, "dev/disk/by-uuid/MEDIUM-ROOT")); err != nil {
		t.Fatal(err)
	}
	cmdline := filepath.Join(root, "proc", "cmdline")
	if err := os.WriteFile(cmdline, []byte("BOOT_IMAGE=/vmlinuz root=UUID=MEDIUM-ROOT ro tunaos.live\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if tpm {
		if err := os.WriteFile(filepath.Join(root, "sys/class/tpm/tpm0/tpm_version_major"), []byte("2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fx.cmd = newFakeCmd(t, root)
	fx.crypt = &fakeCrypt{slots: []int{0}, tpm: tpm}
	if tpm {
		fx.crypt.slots = []int{0, 1}
	}
	fx.real = Real{
		SysRoot: filepath.Join(root, "sys"), DevDir: filepath.Join(root, "dev"),
		CmdlinePath: cmdline, Root: root, Cmd: fx.cmd, WorkDir: fx.work,
		Enroller:        crypt.NewEnroller(fx.crypt),
		NoFirmwareEntry: false,
		EFIVars:         filepath.Join(root, "efivars"),
		DiskExists:      func(string) bool { return true },
		SetFATSerial: func(dev string, serial uint32) error {
			fx.fatSet = append(fx.fatSet, dev)
			return nil
		},
	}
	if err := os.MkdirAll(fx.real.EFIVars, 0o755); err != nil {
		t.Fatal(err)
	}
	return fx
}

func fixturePlan(enc Encryption) InstallPlan {
	full := "Ada Lovelace"
	return InstallPlan{
		Disk:       Disk{Path: "/dev/fake1"},
		Erase:      true,
		Encryption: enc,
		Target: TargetConfig{
			User:     &User{Username: "ada", FullName: &full, Password: ptr(Secret("user pw 123"))},
			Hostname: "ada-box", Locale: "en_GB.UTF-8", Keymap: "gb", TimeZone: "Europe/London",
			Autologin: true, Privacy: &Privacy{LocationServices: false},
		},
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRealInstallPassphrase(t *testing.T) {
	fx := newInstallFixture(t, false)
	rep := &recReporter{}
	plan := fixturePlan(Encryption{Type: "luks_passphrase", Passphrase: ptr(Secret("correct horse battery"))})
	if err := fx.real.Install(context.Background(), plan, rep); err != nil {
		t.Fatalf("Install: %v\n%s", err, fx.cmd.normalized())
	}
	if got, want := rep.started(), PlannedSteps(plan); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("steps = %v, want %v", got, want)
	}
	cmds := fx.cmd.normalized() + strings.Join(fx.crypt.lines, "\n") + "\n"
	golden(t, "real_install_passphrase.txt", cmds)

	// no secret in any command line, anywhere
	for _, secret := range []string{"correct horse battery", "user pw 123"} {
		if strings.Contains(cmds, secret) {
			t.Errorf("the secret %q reached a command line", secret)
		}
	}
	for _, e := range rep.events {
		if strings.Contains(e, "correct horse") || strings.Contains(e, "user pw") {
			t.Errorf("a secret reached an event: %s", e)
		}
	}
	// the empty keyslot is gone, the passphrase is in
	if len(fx.crypt.slots) != 1 || fx.crypt.slots[0] != 0 {
		t.Errorf("slots = %v, want only the new passphrase slot", fx.crypt.slots)
	}
	if len(fx.fatSet) != 1 {
		t.Errorf("FAT serial set on %v, want only the ESP", fx.fatSet)
	}
	// the scratch tree is gone
	if _, err := os.Stat(fx.work); err == nil {
		entries, _ := os.ReadDir(fx.work)
		if len(entries) != 0 {
			t.Errorf("scratch tree left behind: %v", entries)
		}
	}
}

// TestRealInstallFixups runs the whole install and keeps the fixture's
// mounted tree by cancelling the final removal, then reads what the
// fixups wrote.
func TestRealInstallFixups(t *testing.T) {
	fx := newInstallFixture(t, true)
	keep := map[string]string{}
	var once sync.Once
	fx.cmd.hook = func(ctx context.Context, name string, args []string, o CmdOpts) ([]byte, error, bool) {
		if name == "umount" {
			once.Do(func() {
				base := filepath.Dir(args[len(args)-1])
				read := func(rel string) {
					if b, err := os.ReadFile(filepath.Join(base, rel)); err == nil {
						keep[rel] = string(b)
					}
				}
				dep := "root/ostree/deploy/default/deploy/abc.0/"
				read(dep + "etc/crypttab")
				read(dep + "etc/fstab")
				read(dep + "etc/tmpfiles.d/tunaos-home-ada.conf")
				read(dep + "etc/gdm/custom.conf")
				read(dep + PrivacyConf)
				read("boot/loader/entries/ostree-1.conf")
				read("efi/EFI/fedora/grub.cfg")
				for _, rel := range []string{"boot/loader/entries/medium-live.conf", "efi/EFI/tunaos-live/vmlinuz",
					"root/ostree/deploy/default/var/lib/extensions/medium-sysinstall.raw"} {
					if _, err := os.Stat(filepath.Join(base, rel)); err == nil {
						keep["EXISTS "+rel] = "yes"
					}
				}
				read("boot/grub2/grubenv")
			})
		}
		return nil, nil, false
	}
	rep := &recReporter{}
	plan := fixturePlan(Encryption{Type: "tpm2_luks_passphrase", Passphrase: ptr(Secret("pass phrase 1"))})
	if err := fx.real.Install(context.Background(), plan, rep); err != nil {
		t.Fatalf("Install: %v\n%s", err, fx.cmd.normalized())
	}
	dep := "root/ostree/deploy/default/deploy/abc.0/"
	if got := keep[dep+"etc/crypttab"]; !strings.Contains(got, "UUID=99999999-aaaa-bbbb-cccc-dddddddddddd") || !strings.Contains(got, "tpm2-device=auto") {
		t.Errorf("crypttab = %q", got)
	}
	if got := keep["boot/loader/entries/ostree-1.conf"]; !strings.Contains(got, "rd.luks.uuid=99999999") || !strings.Contains(got, "root=UUID=root-NEW") {
		t.Errorf("entry = %q: want the new root UUID and rd.luks.uuid", got)
	}
	if got := keep[dep+"etc/fstab"]; strings.Contains(got, "OLD") || !strings.Contains(got, "boot-NEW") {
		t.Errorf("fstab = %q", got)
	}
	if got := keep["efi/EFI/fedora/grub.cfg"]; strings.Contains(got, "OLD") {
		t.Errorf("grub.cfg still names an old UUID: %q", got)
	}
	for k := range keep {
		if strings.HasPrefix(k, "EXISTS ") {
			t.Errorf("%s survived the fixups", strings.TrimPrefix(k, "EXISTS "))
		}
	}
	if got := keep[dep+"etc/tmpfiles.d/tunaos-home-ada.conf"]; !strings.Contains(got, "C /home/ada 0700 ada ada - /etc/skel") {
		t.Errorf("home tmpfiles = %q", got)
	}
	if got := keep[dep+"etc/gdm/custom.conf"]; !strings.Contains(got, "AutomaticLogin=ada") {
		t.Errorf("gdm = %q", got)
	}
	if got := keep[dep+PrivacyConf]; !strings.Contains(got, "LocationServices=no") {
		t.Errorf("privacy = %q", got)
	}
	if got := keep["boot/grub2/grubenv"]; strings.Contains(got, "medium-live") {
		t.Error("grubenv still defaults to the live entry")
	}
	cmds := fx.cmd.normalized()
	if !strings.Contains(cmds, "efibootmgr --create --disk /dev/fake1 --part 2 --label TunaOS --loader \\EFI\\fedora\\shimx64.efi") {
		t.Errorf("no firmware entry in:\n%s", cmds)
	}
	if !strings.Contains(cmds, "--tpm2-device=auto") {
		t.Error("repart was not asked for the TPM2 seal")
	}
}

func TestRealInstallRepartFailure(t *testing.T) {
	fx := newInstallFixture(t, false)
	fx.cmd.hook = func(ctx context.Context, name string, args []string, o CmdOpts) ([]byte, error, bool) {
		if name == "systemd-repart" {
			return nil, &CmdError{Name: name, Err: errors.New("exit status 1"), Stderr: "Failed to create new partition"}, true
		}
		return nil, nil, false
	}
	rep := &recReporter{}
	err := fx.real.Install(context.Background(), fixturePlan(Encryption{Type: "luks_passphrase", Passphrase: ptr(Secret("x pass phrase"))}), rep)
	var ie *InstallError
	if !errors.As(err, &ie) {
		t.Fatalf("err = %v, want *InstallError", err)
	}
	if ie.Reason != ReasonRepartError || ie.NextAction != ActionRetry || ie.Step != StepPartition {
		t.Errorf("error = %+v", ie)
	}
	if !strings.Contains(ie.Message, "Failed to create new partition") {
		t.Errorf("message %q lost repart's reason", ie.Message)
	}
	if got := rep.started(); got[len(got)-1] != StepCleanup {
		t.Errorf("last step = %v, want cleanup", got)
	}
}

func TestRealInstallDeviceLost(t *testing.T) {
	fx := newInstallFixture(t, false)
	fx.real.DiskExists = func(p string) bool { return false }
	fx.cmd.hook = func(ctx context.Context, name string, args []string, o CmdOpts) ([]byte, error, bool) {
		if name == "systemd-repart" {
			return nil, errors.New("gone"), true
		}
		return nil, nil, false
	}
	err := fx.real.Install(context.Background(), fixturePlan(Encryption{Type: "luks_passphrase", Passphrase: ptr(Secret("x pass phrase"))}), &recReporter{})
	var ie *InstallError
	if !errors.As(err, &ie) || ie.Reason != ReasonDeviceLost || ie.NextAction != ActionChooseAnotherDisk {
		t.Fatalf("err = %v, want device_lost", err)
	}
}

// TestRealInstallMountFailureCleansUp fails the ESP mount: the root is
// already mounted and unlocked, so cleanup must unmount and close.
func TestRealInstallMountFailureCleansUp(t *testing.T) {
	fx := newInstallFixture(t, false)
	fx.cmd.hook = func(ctx context.Context, name string, args []string, o CmdOpts) ([]byte, error, bool) {
		if name == "mount" && args[0] == fESP {
			return nil, &CmdError{Name: "mount", Err: errors.New("exit status 32")}, true
		}
		return nil, nil, false
	}
	rep := &recReporter{}
	err := fx.real.Install(context.Background(), fixturePlan(Encryption{Type: "luks_passphrase", Passphrase: ptr(Secret("x pass phrase"))}), rep)
	var ie *InstallError
	if !errors.As(err, &ie) || ie.Step != StepMount {
		t.Fatalf("err = %v, want a mount failure", err)
	}
	cmds := fx.cmd.normalized()
	i := strings.Index(cmds, "umount -R $ROOT")
	j := strings.Index(cmds, "cryptsetup close $MAPPER")
	if i < 0 || j < i {
		t.Errorf("cleanup did not unmount before it closed:\n%s", cmds)
	}
	if got := strings.Count(cmds, "umount -R "); got != 2 {
		t.Errorf("unmounted %d mounts, want the root and /boot", got)
	}
	if _, err := os.Stat(fx.work); err == nil {
		if entries, _ := os.ReadDir(fx.work); len(entries) != 0 {
			t.Errorf("scratch tree left behind: %v", entries)
		}
	}
}

// TestRealInstallCancel cancels during the copy. The cleanup must run on a
// context that is not cancelled, and Install returns the cancellation.
func TestRealInstallCancel(t *testing.T) {
	fx := newInstallFixture(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	fx.cmd.hook = func(c context.Context, name string, args []string, o CmdOpts) ([]byte, error, bool) {
		if name == "mount" && args[0] == fBoot {
			cancel()
			return nil, context.Canceled, true
		}
		return nil, nil, false
	}
	rep := &recReporter{}
	err := fx.real.Install(ctx, fixturePlan(Encryption{Type: "luks_passphrase", Passphrase: ptr(Secret("x pass phrase"))}), rep)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	var ie *InstallError
	if errors.As(err, &ie) {
		t.Errorf("a cancel must not be a failure: %+v", ie)
	}
	for key, cerr := range fx.cmd.ctxErr {
		if (strings.HasPrefix(key, "umount") || strings.HasPrefix(key, "cryptsetup close")) && cerr != nil {
			t.Errorf("%q ran on a cancelled context: %v", key, cerr)
		}
	}
	if !strings.Contains(fx.cmd.normalized(), "cryptsetup close $MAPPER") {
		t.Error("the LUKS volume was not closed after the cancel")
	}
}

func TestRealInstallRefusesUnencrypted(t *testing.T) {
	fx := newInstallFixture(t, false)
	err := fx.real.Install(context.Background(), fixturePlan(Encryption{Type: "none"}), &recReporter{})
	var ie *InstallError
	if !errors.As(err, &ie) || ie.Reason != ReasonUnsupported || ie.NextAction != ActionChangeSettings || ie.Step != StepValidateDevice {
		t.Fatalf("err = %v, want unsupported_config at validate_device", err)
	}
	if strings.Contains(fx.cmd.normalized(), "systemd-repart") {
		t.Error("repart ran for an unsupported config")
	}
}

func TestRealInstallRefusesTPMWithoutTPM(t *testing.T) {
	fx := newInstallFixture(t, false)
	err := fx.real.Install(context.Background(), fixturePlan(Encryption{Type: "tpm2_luks"}), &recReporter{})
	var ie *InstallError
	if !errors.As(err, &ie) || ie.Reason != ReasonUnsupported {
		t.Fatalf("err = %v", err)
	}
}

func TestRealInstallRefusesMedium(t *testing.T) {
	fx := newInstallFixture(t, false)
	plan := fixturePlan(Encryption{Type: "luks_passphrase", Passphrase: ptr(Secret("x pass phrase"))})
	plan.Disk.Path = "/dev/fakemed"
	err := fx.real.Install(context.Background(), plan, &recReporter{})
	var ie *InstallError
	if !errors.As(err, &ie) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(fx.cmd.normalized(), "systemd-repart") || strings.Contains(fx.cmd.normalized(), "wipefs") {
		t.Error("a destructive command ran against the medium")
	}
}

// TestRealInstallSysInstallRun is the gnome-setup path: an empty passphrase.
// The empty slot stays for gnome-setup to replace, and the install says so.
func TestRealInstallSysInstallRun(t *testing.T) {
	fx := newInstallFixture(t, true)
	rep := &recReporter{}
	plan := fixturePlan(Encryption{Type: "tpm2_luks", Passphrase: ptr(Secret(""))})
	if err := fx.real.Install(context.Background(), plan, rep); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !contains(fx.crypt.slots, 0) {
		t.Errorf("slots = %v: the empty slot must stay", fx.crypt.slots)
	}
	if !strings.Contains(strings.Join(rep.events, "\n"), "empty passphrase until a recovery key") {
		t.Errorf("no warning about the empty slot:\n%v", rep.events)
	}
	if !strings.Contains(fx.cmd.normalized(), "systemd-cryptsetup attach") {
		t.Error("the volume was not opened with the empty passphrase")
	}
}

// TestRealInstallTPMOnly: tpm2_luks without a passphrase wipes the empty
// slot at the end, so only the TPM unlocks the disk.
func TestRealInstallTPMOnly(t *testing.T) {
	fx := newInstallFixture(t, true)
	rep := &recReporter{}
	plan := fixturePlan(Encryption{Type: "tpm2_luks"})
	if err := fx.real.Install(context.Background(), plan, rep); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if contains(fx.crypt.slots, 0) {
		t.Errorf("slots = %v: the empty slot must be wiped", fx.crypt.slots)
	}
	if !strings.Contains(strings.Join(rep.events, "\n"), "only the TPM unlocks") {
		t.Errorf("no warning about TPM-only:\n%v", rep.events)
	}
}

func TestRealInstallDiskTooSmall(t *testing.T) {
	fx := newInstallFixture(t, false)
	fx.cmd.hook = func(ctx context.Context, name string, args []string, o CmdOpts) ([]byte, error, bool) {
		if name == "lsblk" && strings.Contains(args[len(args)-1], "KNAME") {
			return []byte(`{"blockdevices":[{"name":"/dev/fake1","kname":"/dev/fake1","type":"disk","size":1073741824,"model":"Tiny","vendor":"Acme","tran":"nvme","rm":false,"hotplug":false,"ro":false,"serial":null,"wwn":null,"mountpoint":null,"mountpoints":[null]}]}`), nil, true
		}
		return nil, nil, false
	}
	err := fx.real.Install(context.Background(), fixturePlan(Encryption{Type: "luks_passphrase", Passphrase: ptr(Secret("x pass phrase"))}), &recReporter{})
	var ie *InstallError
	if !errors.As(err, &ie) {
		t.Fatalf("err = %v", err)
	}
	// a 1 GiB target is "too small" for probe.MinDiskBytes (not a loop device)
	if ie.NextAction != ActionChooseAnotherDisk {
		t.Errorf("error = %+v", ie)
	}
}

// A medium that is no btrfs seed: btrfstune exits 1 for "-S 0" on it, so the
// installer must not call it (tuna-os/tunaOS run 45624212413: every btrfs
// install stopped in the mount step).
func TestRealInstallBtrfsWithoutSeedFlag(t *testing.T) {
	fx := newInstallFixture(t, false)
	fx.cmd.hook = func(ctx context.Context, name string, args []string, o CmdOpts) ([]byte, error, bool) {
		line := name + " " + strings.Join(args, " ")
		switch {
		case strings.HasPrefix(line, "btrfs inspect-internal dump-super "):
			return []byte("flags\t\t\t0x1\n\t\t\t( WRITTEN )\n"), nil, true
		case strings.HasPrefix(line, "btrfstune -f -S 0 "):
			return nil, errors.New("exit status 1: WARNING: seeding flag is not set"), true
		}
		return nil, nil, false
	}
	plan := fixturePlan(Encryption{Type: "luks_passphrase", Passphrase: ptr(Secret("correct horse battery"))})
	if err := fx.real.Install(context.Background(), plan, &recReporter{}); err != nil {
		t.Fatalf("Install: %v\n%s", err, fx.cmd.normalized())
	}
}
