//go:build integration && linux

package varlinkapi

// An end-to-end run of Real.Install against loop devices. It needs root and
// the host tools (systemd-repart, cryptsetup, sfdisk, losetup, mkfs.vfat,
// mkfs.ext4, mkfs.btrfs, btrfstune, e2fsck, tune2fs, useradd, chpasswd,
// systemd-firstboot).
//
//	go test -c -tags integration -o /path/it.test ./internal/varlinkapi
//	sudo env PATH="$PATH" FISHERMAND_IT_DIR=$HOME/tunaos-fishermand/it \
//	    /path/it.test -test.run Integration -test.v
//
// The test builds a tiny fake medium (GPT: ESP, /boot, a btrfs seed root
// with an ostree deployment) in a file on a loop device, installs it onto a
// second loop device, and checks the result with independent tools. It
// refuses to touch anything that is not a loop device it attached itself,
// never writes a firmware boot entry (NoFirmwareEntry), and takes the medium
// from a command line file it writes, never from /proc/cmdline.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const itPassphrase = "integration passphrase 1"

type itEnv struct {
	t       *testing.T
	dir     string
	medium  string // loop device of the medium
	target  string // loop device of the target
	cmdline string
	work    string
	// loops this test attached, to detach at the end
	loops []string
}

// sh runs a host program and fails the test on error.
func (e *itEnv) sh(name string, args ...string) string {
	e.t.Helper()
	out, err := e.try(nil, name, args...)
	if err != nil {
		e.t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(out)
}

func (e *itEnv) try(stdin []byte, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

func requireLoop(t *testing.T, dev string) {
	t.Helper()
	if !regexp.MustCompile(`^/dev/loop[0-9]+$`).MatchString(dev) {
		t.Fatalf("refusing to use %q: not a loop device", dev)
	}
}

func (e *itEnv) waitFor(what string, ok func() bool) {
	e.t.Helper()
	for i := 0; i < 100; i++ {
		_, _ = e.try(nil, "udevadm", "settle")
		if ok() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	e.t.Fatalf("timed out waiting for %s", what)
}

func newITEnv(t *testing.T) *itEnv {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	base := os.Getenv("FISHERMAND_IT_DIR")
	if base == "" {
		t.Skip("FISHERMAND_IT_DIR is not set")
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "run-")
	if err != nil {
		t.Fatal(err)
	}
	e := &itEnv{t: t, dir: dir, work: filepath.Join(dir, "work"), cmdline: filepath.Join(dir, "cmdline")}
	t.Cleanup(e.teardown)
	return e
}

// teardown unmounts, closes and detaches only what this test made.
func (e *itEnv) teardown() {
	// anything mounted under our directory, deepest first
	if out, err := e.try(nil, "findmnt", "-rn", "-o", "TARGET"); err == nil {
		var ours []string
		for _, m := range strings.Fields(out) {
			if strings.HasPrefix(m, e.dir+"/") {
				ours = append(ours, m)
			}
		}
		for i := len(ours) - 1; i >= 0; i-- {
			_, _ = e.try(nil, "umount", "-l", ours[i])
		}
	}
	// crypt mappings stacked on our loop devices
	for _, l := range e.loops {
		if out, err := e.try(nil, "lsblk", "-rno", "NAME,TYPE", l); err == nil {
			for _, line := range strings.Split(out, "\n") {
				f := strings.Fields(line)
				if len(f) == 2 && f[1] == "crypt" {
					_, _ = e.try(nil, "cryptsetup", "close", f[0])
				}
			}
		}
	}
	for _, l := range e.loops {
		_, _ = e.try(nil, "losetup", "-d", l)
	}
	_ = os.RemoveAll(e.dir)
}

func (e *itEnv) attach(file string) string {
	e.t.Helper()
	dev := e.sh("losetup", "--partscan", "--find", "--show", file)
	requireLoop(e.t, dev)
	e.loops = append(e.loops, dev)
	return dev
}

func (e *itEnv) write(root, rel, text string) {
	e.t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// buildMedium makes the fake medium and the target.
func (e *itEnv) buildMedium() {
	mimg, timg := filepath.Join(e.dir, "medium.img"), filepath.Join(e.dir, "target.img")
	e.sh("truncate", "-s", "1500M", mimg)
	e.sh("truncate", "-s", "6G", timg)
	if _, err := e.try([]byte("label: gpt\nsize=64M, type=C12A7328-F81F-11D2-BA4B-00A0C93EC93B, name=EFI-SYSTEM\n"+
		"size=128M, type=BC13C2FF-59E6-4262-A352-B275FD6F7172, name=boot\n"+
		"type=4F68BCE3-E8CD-4DB1-96E7-FBCAF984B709, name=root\n"), "sfdisk", mimg); err != nil {
		e.t.Fatal(err)
	}
	e.medium = e.attach(mimg)
	e.waitFor("medium partitions", func() bool {
		for _, n := range []string{"p1", "p2", "p3"} {
			if !fileExists(e.medium + n) {
				return false
			}
		}
		return true
	})
	e.sh("mkfs.vfat", "-F", "32", e.medium+"p1")
	e.sh("mkfs.ext4", "-q", e.medium+"p2")
	e.sh("mkfs.btrfs", "-q", e.medium+"p3")

	mnt := filepath.Join(e.dir, "medium-mnt")
	for _, d := range []string{"root", "boot", "efi"} {
		if err := os.MkdirAll(filepath.Join(mnt, d), 0o755); err != nil {
			e.t.Fatal(err)
		}
	}
	e.sh("mount", e.medium+"p3", filepath.Join(mnt, "root"))
	e.sh("mount", e.medium+"p2", filepath.Join(mnt, "boot"))
	e.sh("mount", e.medium+"p1", filepath.Join(mnt, "efi"))
	rootUUID := e.sh("blkid", "-p", "-s", "UUID", "-o", "value", e.medium+"p3")
	bootUUID := e.sh("blkid", "-p", "-s", "UUID", "-o", "value", e.medium+"p2")
	espUUID := e.sh("blkid", "-p", "-s", "UUID", "-o", "value", e.medium+"p1")

	r, b, f := filepath.Join(mnt, "root"), filepath.Join(mnt, "boot"), filepath.Join(mnt, "efi")
	dep := "ostree/deploy/default/deploy/abc.0/"
	e.write(r, dep+"etc/machine-id", "uninitialized\n")
	e.write(r, dep+"etc/fstab", fmt.Sprintf("UUID=%s / btrfs defaults 0 0\nUUID=%s /boot ext4 defaults 0 0\nUUID=%s /boot/efi vfat defaults 0 0\n", rootUUID, bootUUID, espUUID))
	e.write(r, dep+"etc/passwd", "root:x:0:0:root:/root:/bin/bash\n")
	e.write(r, dep+"etc/shadow", "root:*:19000:0:99999:7:::\n")
	e.write(r, dep+"etc/group", "root:x:0:\nwheel:x:10:\n")
	e.write(r, dep+"etc/gshadow", "root:::\nwheel:::\n")
	e.write(r, dep+"etc/login.defs", "UID_MIN 1000\nUID_MAX 60000\nGID_MIN 1000\nGID_MAX 60000\nSYS_UID_MIN 201\nSYS_UID_MAX 999\nSYS_GID_MIN 201\nSYS_GID_MAX 999\nCREATE_HOME no\nENCRYPT_METHOD SHA512\n")
	e.write(r, dep+"usr/lib/systemd/system/gdm.service", "[Unit]\nDescription=GDM\n")
	e.write(r, "ostree/deploy/default/var/lib/extensions/medium-sysinstall.raw", "sysext")
	e.write(r, "payload.bin", strings.Repeat("tunaos fake medium payload\n", 1<<16)) // 1.8 MB so the copy is real
	e.write(b, "loader/entries/ostree-1.conf", fmt.Sprintf("title TunaOS\nversion 1\nlinux /vmlinuz\noptions root=UUID=%s rw\n", rootUUID))
	e.write(b, "loader/entries/medium-live.conf", fmt.Sprintf("title TunaOS Live\nlinux /vmlinuz\noptions root=UUID=%s ro tunaos.live\n", rootUUID))
	e.write(b, "grub2/grubenv", "# GRUB Environment Block\nsaved_entry=medium-live\n"+strings.Repeat("#", 960))
	e.write(f, "EFI/fedora/shimx64.efi", "shim")
	e.write(f, "EFI/BOOT/BOOTX64.EFI", "fallback")
	e.write(f, "EFI/tunaos-live/vmlinuz", "live kernel")
	e.write(f, "EFI/fedora/grub.cfg", fmt.Sprintf("search --no-floppy --fs-uuid --set=root %s\n", bootUUID))
	e.sh("sync")
	for _, d := range []string{"efi", "boot", "root"} {
		e.sh("umount", filepath.Join(mnt, d))
	}
	// the medium root is a btrfs seed, like the tunaOS media (btrfstune -S)
	e.sh("btrfstune", "-S", "1", e.medium+"p3")

	e.waitFor("by-uuid link of the medium root", func() bool { return fileExists("/dev/disk/by-uuid/" + rootUUID) })
	for _, n := range []string{"p1", "p2", "p3"} {
		pu := e.sh("blkid", "-p", "-s", "PART_ENTRY_UUID", "-o", "value", e.medium+n)
		e.waitFor("by-partuuid link of "+n, func() bool { return fileExists("/dev/disk/by-partuuid/" + pu) })
	}
	e.write(e.dir, "cmdline", fmt.Sprintf("BOOT_IMAGE=/vmlinuz root=UUID=%s ro tunaos.live\n", rootUUID))
	e.target = e.attach(timg)
}

func (e *itEnv) real() Real {
	return Real{
		CmdlinePath: e.cmdline,
		WorkDir:     e.work,
		// the host has real NVRAM: never write a boot entry
		NoFirmwareEntry: true,
		EFIVars:         filepath.Join(e.dir, "no-efivars"),
	}
}

func (e *itEnv) plan() InstallPlan {
	full := "Ada Lovelace"
	return InstallPlan{
		Disk:       Disk{Path: e.target},
		Erase:      true,
		Encryption: Encryption{Type: "luks_passphrase", Passphrase: ptr(Secret(itPassphrase))},
		Target: TargetConfig{
			User:     &User{Username: "ada", FullName: &full, Password: ptr(Secret("ada login pw 1"))},
			Hostname: "ada-box", Locale: "en_GB.UTF-8", Keymap: "gb", TimeZone: "Europe/London",
			Autologin: true, Privacy: &Privacy{LocationServices: false},
		},
	}
}

// leftovers reports what an install must not leave behind: mounts under the
// scratch tree and crypt mappings on the target.
func (e *itEnv) leftovers() []string {
	var left []string
	if out, err := e.try(nil, "findmnt", "-rn", "-o", "TARGET"); err == nil {
		for _, m := range strings.Fields(out) {
			if strings.HasPrefix(m, e.work) {
				left = append(left, "mount "+m)
			}
		}
	}
	if out, err := e.try(nil, "lsblk", "-rno", "NAME,TYPE", e.target); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if f := strings.Fields(line); len(f) == 2 && f[1] == "crypt" {
				left = append(left, "crypt "+f[0])
			}
		}
	}
	if entries, err := os.ReadDir(e.work); err == nil && len(entries) > 0 {
		left = append(left, fmt.Sprintf("scratch entries %d", len(entries)))
	}
	return left
}

func TestIntegrationInstallEndToEnd(t *testing.T) {
	e := newITEnv(t)
	e.buildMedium()
	requireLoop(t, e.medium)
	requireLoop(t, e.target)
	loopsBefore := e.sh("losetup", "-a")
	t.Logf("medium %s, target %s", e.medium, e.target)

	rep := &recReporter{}
	r := e.real()
	plan := e.plan()
	start := time.Now()
	if err := r.Install(context.Background(), plan, rep); err != nil {
		t.Fatalf("Install: %v\nevents:\n%s", err, strings.Join(rep.events, "\n"))
	}
	t.Logf("Install took %s", time.Since(start).Round(time.Millisecond))
	t.Logf("events:\n%s", strings.Join(rep.events, "\n"))
	if got, want := strings.Join(rep.started(), ","), strings.Join(PlannedSteps(plan), ","); got != want {
		t.Errorf("steps = %s, want %s", got, want)
	}
	if left := e.leftovers(); len(left) > 0 {
		t.Errorf("left behind: %v", left)
	}

	e.settleTarget()
	luks := e.target + "p3"
	// the keyslots: exactly the passphrase, and the empty passphrase is gone
	dump := e.sh("cryptsetup", "luksDump", luks)
	if n := len(regexp.MustCompile(`(?m)^  \d+: luks2`).FindAllString(dump, -1)); n != 1 {
		t.Errorf("%d keyslots, want 1:\n%s", n, dump)
	}
	if _, err := e.try([]byte(itPassphrase), "cryptsetup", "open", "--test-passphrase", "--key-file=-", luks); err != nil {
		t.Errorf("the passphrase does not unlock the root: %v", err)
	}
	if _, err := e.try([]byte("wrong passphrase"), "cryptsetup", "open", "--test-passphrase", "--key-file=-", luks); err == nil {
		t.Error("a wrong passphrase unlocks the root")
	}
	if out, err := e.try(nil, systemdCryptsetup(), "attach", "it-empty-"+filepath.Base(e.dir), luks, "-", "try-empty-password=yes,headless=yes"); err == nil {
		_, _ = e.try(nil, "cryptsetup", "close", "it-empty-"+filepath.Base(e.dir))
		t.Errorf("the empty passphrase still unlocks the root:\n%s", out)
	}

	// look at the result with independent tools
	mapper := "it-verify-" + filepath.Base(e.dir)
	if _, err := e.try([]byte(itPassphrase), "cryptsetup", "open", "--type", "luks2", "--key-file=-", luks, mapper); err != nil {
		t.Fatalf("opening the result: %v", err)
	}
	mnt := filepath.Join(e.dir, "verify")
	for _, d := range []string{"root", "boot", "efi"} {
		if err := os.MkdirAll(filepath.Join(mnt, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e.sh("mount", "-o", "ro", "/dev/mapper/"+mapper, filepath.Join(mnt, "root"))
	e.sh("mount", "-o", "ro", e.target+"p2", filepath.Join(mnt, "boot"))
	e.sh("mount", "-o", "ro", e.target+"p1", filepath.Join(mnt, "efi"))
	defer func() {
		for _, d := range []string{"efi", "boot", "root"} {
			_, _ = e.try(nil, "umount", filepath.Join(mnt, d))
		}
		_, _ = e.try(nil, "cryptsetup", "close", mapper)
	}()
	rd := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(mnt, rel))
		if err != nil {
			t.Errorf("reading %s: %v", rel, err)
		}
		return string(b)
	}
	exists := func(rel string) bool { _, err := os.Stat(filepath.Join(mnt, rel)); return err == nil }

	// new identities
	oldRoot := e.sh("blkid", "-p", "-s", "UUID", "-o", "value", e.medium+"p3")
	oldBoot := e.sh("blkid", "-p", "-s", "UUID", "-o", "value", e.medium+"p2")
	newRoot := e.sh("blkid", "-p", "-s", "UUID", "-o", "value", "/dev/mapper/"+mapper)
	newBoot := e.sh("blkid", "-p", "-s", "UUID", "-o", "value", e.target+"p2")
	newESP := e.sh("blkid", "-p", "-s", "UUID", "-o", "value", e.target+"p1")
	oldESP := e.sh("blkid", "-p", "-s", "UUID", "-o", "value", e.medium+"p1")
	for what, p := range map[string][2]string{"root": {oldRoot, newRoot}, "boot": {oldBoot, newBoot}, "esp": {oldESP, newESP}} {
		if p[0] == p[1] {
			t.Errorf("the %s kept its UUID %s", what, p[0])
		}
	}
	luksUUID := e.sh("cryptsetup", "luksUUID", luks)

	dep := "root/ostree/deploy/default/deploy/abc.0/"
	entry := rd("boot/loader/entries/ostree-1.conf")
	if !strings.Contains(entry, "root=UUID="+newRoot) || strings.Contains(entry, oldRoot) ||
		!strings.Contains(entry, "rd.luks.uuid="+luksUUID) || !strings.Contains(entry, "rd.luks.options="+luksUUID+"=discard") {
		t.Errorf("boot entry:\n%s", entry)
	}
	if exists("boot/loader/entries/medium-live.conf") {
		t.Error("the live entry survived")
	}
	if strings.Contains(rd("boot/grub2/grubenv"), "medium-live") {
		t.Error("grubenv still defaults to the live entry")
	}
	if fstab := rd(dep + "etc/fstab"); strings.Contains(fstab, oldRoot) || strings.Contains(fstab, oldBoot) || !strings.Contains(fstab, newRoot) || !strings.Contains(fstab, newBoot) {
		t.Errorf("fstab:\n%s", fstab)
	}
	if grub := rd("efi/EFI/fedora/grub.cfg"); strings.Contains(grub, oldBoot) || !strings.Contains(grub, newBoot) {
		t.Errorf("grub.cfg:\n%s", grub)
	}
	if got, want := strings.TrimSpace(rd(dep+"etc/crypttab")), fmt.Sprintf("luks-%s UUID=%s none discard", luksUUID, luksUUID); got != want {
		t.Errorf("crypttab = %q, want %q", got, want)
	}
	if exists("efi/EFI/tunaos-live") {
		t.Error("the live kernel copy survived on the ESP")
	}
	if exists("root/ostree/deploy/default/var/lib/extensions/medium-sysinstall.raw") {
		t.Error("the medium sysext survived")
	}
	if !exists("efi/EFI/fedora/shimx64.efi") || !exists("root/payload.bin") {
		t.Error("the copy lost files that belong to the system")
	}
	if got := strings.TrimSpace(rd(dep + "etc/machine-id")); got != "uninitialized" {
		t.Errorf("machine-id = %q", got)
	}

	// the filesystem grew with the partition
	fsSize := e.sh("btrfs", "filesystem", "show", "--raw", filepath.Join(mnt, "root"))
	m := regexp.MustCompile(`devid\s+1 size (\d+)`).FindStringSubmatch(fsSize)
	if m == nil {
		t.Fatalf("no devid size in:\n%s", fsSize)
	}
	var size int64
	fmt.Sscan(m[1], &size)
	if size < 3<<30 {
		t.Errorf("root filesystem is %d bytes, want it grown past 3 GiB (the medium root is 1.3 GiB)", size)
	}

	// ConfigureTarget
	if !strings.Contains(rd(dep+"etc/passwd"), "ada:x:") || !strings.Contains(rd(dep+"etc/passwd"), "/home/ada") {
		t.Errorf("passwd:\n%s", rd(dep+"etc/passwd"))
	}
	if sh := rd(dep + "etc/shadow"); !regexp.MustCompile(`(?m)^ada:\$6\$`).MatchString(sh) || strings.Contains(sh, "ada login pw") {
		t.Errorf("shadow has no SHA512 hash for ada (or leaks the password)")
	}
	if !strings.Contains(rd(dep+"etc/group"), "wheel:x:10:ada") {
		t.Errorf("ada is not in wheel:\n%s", rd(dep+"etc/group"))
	}
	if got := strings.TrimSpace(rd(dep + "etc/hostname")); got != "ada-box" {
		t.Errorf("hostname = %q", got)
	}
	if !strings.Contains(rd(dep+"etc/locale.conf"), "LANG=en_GB.UTF-8") || !strings.Contains(rd(dep+"etc/vconsole.conf"), "KEYMAP=gb") {
		t.Error("locale or keymap missing")
	}
	if tz, err := os.Readlink(filepath.Join(mnt, dep+"etc/localtime")); err != nil || !strings.HasSuffix(tz, "Europe/London") {
		t.Errorf("localtime = %q, %v", tz, err)
	}
	if !strings.Contains(rd(dep+"etc/gdm/custom.conf"), "AutomaticLogin=ada") {
		t.Error("no GDM autologin")
	}
	if !strings.Contains(rd(dep+PrivacyConf), "LocationServices=no") {
		t.Error("no privacy record")
	}

	// nothing of ours is left on the host
	if after := e.sh("losetup", "-a"); strings.Count(after, "\n") != strings.Count(loopsBefore, "\n") {
		t.Errorf("loop devices changed:\nbefore:\n%s\nafter:\n%s", loopsBefore, after)
	}
}

// settleTarget waits for the kernel to publish the target's partitions.
func (e *itEnv) settleTarget() {
	e.waitFor("target partitions", func() bool {
		return fileExists(e.target+"p1") && fileExists(e.target+"p2") && fileExists(e.target+"p3")
	})
}

// TestIntegrationInstallCancelCleansUp cancels once the root is unlocked and
// mounted: the cleanup must unmount and close, and the cancel must not look
// like a failure.
func TestIntegrationInstallCancelCleansUp(t *testing.T) {
	e := newITEnv(t)
	e.buildMedium()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rep := &cancelReporter{recReporter: &recReporter{}, at: StepInstallOS, cancel: cancel}
	err := e.real().Install(ctx, e.plan(), rep)
	if err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled\n%s", err, strings.Join(rep.events, "\n"))
	}
	t.Logf("events:\n%s", strings.Join(rep.events, "\n"))
	if got := rep.started(); got[len(got)-1] != StepCleanup {
		t.Errorf("steps = %v, want cleanup last", got)
	}
	if left := e.leftovers(); len(left) > 0 {
		t.Errorf("left behind after the cancel: %v", left)
	}
}

type cancelReporter struct {
	*recReporter
	at     string
	cancel context.CancelFunc
}

func (c *cancelReporter) Start(step string) {
	c.recReporter.Start(step)
	if step == c.at {
		c.cancel()
	}
}

// TestIntegrationInstallRefusesMedium points the install at the medium's
// own loop device: validate_device must refuse before anything is written.
func TestIntegrationInstallRefusesMedium(t *testing.T) {
	e := newITEnv(t)
	e.buildMedium()
	before := e.sh("sha256sum", filepath.Join(e.dir, "medium.img"))
	plan := e.plan()
	plan.Disk.Path = e.medium
	err := e.real().Install(context.Background(), plan, &recReporter{})
	var ie *InstallError
	if ie2, ok := err.(*InstallError); !ok || ie2.Step != StepValidateDevice {
		t.Fatalf("err = %v, want a validate_device failure", err)
	} else {
		ie = ie2
	}
	t.Logf("refused: %s", ie.Message)
	if after := e.sh("sha256sum", filepath.Join(e.dir, "medium.img")); after != before {
		t.Error("the medium image changed")
	}
}

// TestIntegrationProbeReadOnly runs the real Probe and Requirements on the
// host: lsblk and sfdisk --json only, nothing is written.
func TestIntegrationProbeReadOnly(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	r := Real{}
	req, err := r.Requirements(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range req.Gates {
		t.Logf("gate %s ok=%v actual=%s", g.ID, g.OK, *g.Actual)
	}
	res, err := r.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range res.Disks {
		reason := ""
		if d.ExcludedReason != nil {
			reason = *d.ExcludedReason
		}
		t.Logf("disk %s %s eligible=%v excluded=%q free=%s tpm=%v partitions=%d", d.Path, d.SizeLabel, d.Eligible,
			reason, sizeLabel(d.FreeBytes), res.TPM.Usable, len(d.ExistingData.Partitions))
	}
	t.Logf("tpm=%+v rst=%+v live=%+v handoff=%+v", res.TPM, res.IntelRST, res.Live, res.Handoff)
}

// TestIntegrationTPM2RecoveryKey installs a TPM-only plan (tpm2_luks, no
// passphrase) and then enrols the recovery key the way a frontend does:
// CryptEnroll.Enroll with the whole-disk path, an empty unlock password and
// the empty slot in wipeSlots. It needs a TPM 2.0 that systemd-repart finds
// with --tpm2-device=auto: a software TPM from swtpm that is visible as
// /dev/tpmrm0 and /sys/class/tpmrm (swtpm_cuse, or a VM with a swtpm
// device). Start it and set FISHERMAND_IT_TPM2=1:
//
//	sudo env PATH="$PATH" FISHERMAND_IT_DIR=$HOME/tunaos-fishermand/it \
//	    FISHERMAND_IT_TPM2=1 /path/it.test -test.run IntegrationTPM2 -test.v
func TestIntegrationTPM2RecoveryKey(t *testing.T) {
	if os.Getenv("FISHERMAND_IT_TPM2") != "1" {
		t.Skip("FISHERMAND_IT_TPM2=1 not set: needs a TPM 2.0 (swtpm) visible to systemd-repart")
	}
	e := newITEnv(t)
	e.buildMedium()
	requireLoop(t, e.medium)
	requireLoop(t, e.target)

	r := e.real()
	plan := e.plan()
	plan.Encryption = Encryption{Type: "tpm2_luks"}
	rep := &recReporter{}
	if err := r.Install(context.Background(), plan, rep); err != nil {
		t.Fatalf("Install: %v\nevents:\n%s", err, strings.Join(rep.events, "\n"))
	}
	if got, want := strings.Join(rep.started(), ","), strings.Join(PlannedSteps(plan), ","); got != want {
		t.Errorf("steps = %s, want %s", got, want)
	}
	e.settleTarget()
	luks := e.target + "p3"

	dump := e.sh("cryptsetup", "luksDump", luks)
	if !strings.Contains(dump, "systemd-tpm2") {
		t.Fatalf("no TPM2 token on the root:\n%s", dump)
	}
	empty := func(name string) bool {
		_, err := e.try(nil, systemdCryptsetup(), "attach", name, luks, "-", "try-empty-password=yes,headless=yes")
		if err == nil {
			_, _ = e.try(nil, "cryptsetup", "close", name)
		}
		return err == nil
	}
	if !empty("it-empty1-" + filepath.Base(e.dir)) {
		t.Fatal("the empty passphrase must still unlock the root before the recovery key lands")
	}

	key, err := r.MakeRecoveryKey(context.Background())
	if err != nil {
		t.Fatalf("MakeRecoveryKey: %v", err)
	}
	slots, err := r.ListSlots(context.Background(), e.target) // whole-disk path
	if err != nil {
		t.Fatalf("ListSlots(%s): %v", e.target, err)
	}
	var emptySlot int64 = -1
	for _, s := range slots {
		if s.Type == "password" {
			emptySlot = s.Slot
		}
	}
	if emptySlot < 0 {
		t.Fatalf("no password slot in %+v", slots)
	}
	if _, err := r.Enroll(context.Background(), EnrollParams{
		Node: e.target, Mechanism: "recovery", RecoveryKey: &key,
		UnlockPassword: ptr(Secret("")), WipeSlots: []int64{emptySlot},
	}); err != nil {
		t.Fatalf("Enroll(%s): %v", e.target, err)
	}

	if empty("it-empty2-" + filepath.Base(e.dir)) {
		t.Error("the empty passphrase still unlocks the root after the recovery key")
	}
	if _, err := e.try([]byte(key.Reveal()), "cryptsetup", "open", "--test-passphrase", "--key-file=-", luks); err != nil {
		t.Errorf("the recovery key does not unlock the root: %v", err)
	}
	if after := e.sh("cryptsetup", "luksDump", luks); !strings.Contains(after, "systemd-tpm2") {
		t.Errorf("the TPM2 token is gone after the recovery key:\n%s", after)
	}
	if left := e.leftovers(); len(left) > 0 {
		t.Errorf("left behind: %v", left)
	}
}
