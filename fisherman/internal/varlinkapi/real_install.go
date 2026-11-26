package varlinkapi

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tuna-os/fisherman/internal/crypt"
	"github.com/tuna-os/fisherman/internal/postfix"
	"github.com/tuna-os/fisherman/internal/probe"
	"github.com/tuna-os/fisherman/internal/repart"
)

// ESPType is the GPT partition type of the EFI system partition.
const espType = "c12a7328-f81f-11d2-ba4b-00a0c93ec93b"

// cleanupTimeout bounds the cleanup after a failure or a cancel. Cleanup
// runs on a context that survives the cancel, or every umount would fail at
// once.
const cleanupTimeout = 3 * time.Minute

// Install mirrors the live medium onto plan.Disk. The steps, in order:
//
//	validate_device  the target is a whole, unmounted disk that is not the
//	                 medium, and the medium's partitions fit
//	partition        systemd-repart writes the target GPT and copies every
//	                 medium partition with CopyBlocks= (this is where the
//	                 bytes move; the root lands in LUKS2)
//	luks             the user's passphrase replaces the empty keyslot
//	tpm2_enroll      the TPM2 token repart sealed to PCR 7 is verified
//	mount            the root is unlocked, every copied filesystem gets a
//	                 fresh UUID, then root, /boot and the ESP are mounted
//	install_os       the copy is checked: a deployment, a pristine machine-id
//	configure        ConfigureTarget: locale, keymap, time zone, hostname,
//	                 the user account (systemd-firstboot --root, useradd)
//	post_fixups      internal/postfix: references rewritten, live entry gone,
//	                 rd.luks.uuid and crypttab, the root grown
//	bootloader       the firmware boot entry (efibootmgr)
//	finalize         unmount, close the LUKS volume, drop the temporary state
//
// On a failure or a cancel the cleanup step unmounts and closes everything it
// opened, on a context that survives the cancel, and the original error is
// returned: an *InstallError, or ctx.Err() for a cancel. The disk is left as
// the failed step left it; no step undoes a partition write.
//
// Secrets: the passphrase travels on stdin or in a pipe and never reaches an
// argument list, a log or an error message.
func (r Real) Install(ctx context.Context, plan InstallPlan, rep Reporter) error {
	run := &installRun{r: r, plan: plan, rep: rep, uuidMap: map[string]string{}}
	err := run.execute(ctx)
	if err == nil {
		return nil
	}
	run.cleanup(rep)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ie *InstallError
	if errors.As(err, &ie) {
		return ie
	}
	return &InstallError{Step: run.step, Reason: ReasonInternal, Message: err.Error(), NextAction: ActionReportBug}
}

// installRun is the state of one Install.
type installRun struct {
	r    Real
	plan InstallPlan
	rep  Reporter
	step string

	work string // scratch tree, a tmpfs

	table       repart.Table
	defs        []repart.Definition
	mediumDisk  string
	mediumRoot  string
	tpm2        bool // the root is sealed to the TPM
	esp, boot   string
	rootPart    string // the LUKS container
	espPartNo   int
	luksUUID    string
	emptySlot   int64  // the slot of the transient key (transient_key.go)
	haveEmpty   bool   // the transient key slot exists
	keepEmpty   bool   // leave it (SysInstall Run: gnome-setup replaces it)
	wipeEmpty   bool   // wipe it after the work (TPM only, no passphrase)
	tkey        []byte // the transient key repart encrypted with
	tkeyPath    string // its file on the daemon's tmpfs
	userPass    []byte // the enrolled passphrase; nil when none
	tokenTPM2   bool
	mapper      string
	rootDev     string
	rootFS      string
	mounts      []string // mount points, in mount order
	mapperOpen  bool
	deployments []string
	report      postfix.Report
	finalized   bool
	uuidMap     map[string]string // old filesystem UUID to new
}

func (c *installRun) cmd() Commander { return c.r.cmd() }

func (c *installRun) fail(reason, action, format string, a ...any) *InstallError {
	return &InstallError{Step: c.step, Reason: reason, Message: fmt.Sprintf(format, a...), NextAction: action}
}

// runCmd runs a program; a cancel wins over the program's own error.
func (c *installRun) runCmd(ctx context.Context, o CmdOpts, name string, args ...string) ([]byte, error) {
	out, err := c.cmd().Run(ctx, name, args, o)
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return out, err
}

// begin starts a step.
func (c *installRun) begin(step string) {
	c.step = step
	c.rep.Start(step)
}

func (c *installRun) end() { c.rep.Done(c.step) }

func (r Real) diskGone(path string) bool {
	if r.DiskExists != nil {
		return !r.DiskExists(path)
	}
	return !fileExists(path)
}

// stepFail maps an error of the current step to an InstallError. A target
// disk that vanished is device_lost whatever the step was.
func (c *installRun) stepFail(err error, reason, action, what string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	if c.r.diskGone(c.plan.Disk.Path) {
		return c.fail(ReasonDeviceLost, ActionChooseAnotherDisk, "%s disappeared while the install was running", c.plan.Disk.Path)
	}
	return c.fail(reason, action, "%s: %v", what, err)
}

func (c *installRun) execute(ctx context.Context) error {
	steps := []struct {
		id string
		fn func(context.Context) error
	}{
		{StepValidateDevice, c.validateDevice},
		{StepPartition, c.partition},
		{StepLUKS, c.enrollLUKS},
		{StepTPM2Enroll, c.verifyTPM2},
		{StepMount, c.mountTarget},
		{StepInstallOS, c.checkCopy},
		{StepConfigure, c.configure},
		{StepPostFixups, c.postFixups},
		{StepBootloader, c.bootloader},
		{StepFinalize, c.finalize},
	}
	planned := map[string]bool{}
	for _, s := range PlannedSteps(c.plan) {
		planned[s] = true
	}
	for _, s := range steps {
		if !planned[s.id] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		c.begin(s.id)
		if err := s.fn(ctx); err != nil {
			return err
		}
		c.end()
	}
	return nil
}

// ---- validate_device -----------------------------------------------------

func (c *installRun) validateDevice(ctx context.Context) error {
	enc := c.plan.Encryption
	switch enc.Type {
	case "luks_passphrase", "tpm2_luks", "tpm2_luks_passphrase":
	default:
		return c.fail(ReasonUnsupported, ActionChangeSettings,
			"the medium installer encrypts the root; an unencrypted install (%q) is not supported yet", enc.Type)
	}
	needsTPM := enc.Type == "tpm2_luks" || enc.Type == "tpm2_luks_passphrase"
	if needsTPM && !c.r.hasTPM2() {
		return c.fail(ReasonUnsupported, ActionChangeSettings, "this encryption type needs a TPM 2.0, and this machine has none")
	}
	c.tpm2 = needsTPM
	if enc.Type == "luks_passphrase" && enc.Passphrase == nil {
		return c.fail(ReasonUnsupported, ActionChangeSettings, "a passphrase is required for luks_passphrase")
	}
	if enc.Passphrase != nil && enc.Passphrase.Reveal() != "" {
		c.userPass = []byte(enc.Passphrase.Reveal())
	}
	switch {
	case enc.Passphrase != nil && enc.Passphrase.Reveal() == "":
		// io.systemd.SysInstall Run: the empty slot stays for gnome-setup.
		c.keepEmpty = true
	case enc.Passphrase == nil:
		c.wipeEmpty = true // TPM only
	}
	if enc.Type == "tpm2_luks_passphrase" && c.userPass == nil && !c.keepEmpty {
		return c.fail(ReasonUnsupported, ActionChangeSettings, "a passphrase is required for tpm2_luks_passphrase")
	}

	target := c.plan.Disk.Path
	if target == "" {
		return c.fail(ReasonDeviceLost, ActionChooseAnotherDisk, "no target disk")
	}
	tv, err := c.r.prober(ctx).Target(target)
	if err != nil {
		return c.fail(ReasonDeviceLost, ActionChooseAnotherDisk, "%s cannot be read: %v", target, err)
	}
	if !tv.Eligible() {
		return c.fail(ReasonInternal, ActionChooseAnotherDisk, "%s cannot be installed to: %s", target, strings.Join(tv.Reasons, ", "))
	}
	c.plan.Disk.Path = tv.Device

	mdisk, mroot, err := c.r.mediumRoot(ctx)
	if err != nil {
		return c.fail(ReasonInternal, ActionReportBug, "the live medium cannot be identified: %v", err)
	}
	if sameDevice(mdisk, tv.Device) {
		return c.fail(ReasonInternal, ActionChooseAnotherDisk, "%s is the live medium", target)
	}
	c.mediumDisk, c.mediumRoot = mdisk, mroot
	sfdisk := c.r.Sfdisk
	if sfdisk == nil {
		sfdisk = func(ctx context.Context, disk string) ([]byte, error) {
			return c.cmd().Run(ctx, "sfdisk", []string{"--json", disk}, CmdOpts{})
		}
	}
	raw, err := sfdisk(ctx, mdisk)
	if err != nil {
		return c.fail(ReasonInternal, ActionReportBug, "reading the medium partition table: %v", err)
	}
	if c.table, err = repart.ParseSfdisk(raw, mdisk); err != nil {
		return c.fail(ReasonInternal, ActionReportBug, "%v", err)
	}
	if c.defs, err = repart.Generate(c.table, mroot, c.tpm2); err != nil {
		return c.fail(ReasonInternal, ActionReportBug, "%v", err)
	}

	// The medium's partitions, plus room for the LUKS2 header and the GPT.
	var need int64 = 64 << 20
	for _, p := range c.table.Partitions {
		if !strings.EqualFold(p.Type, repart.BIOSType) {
			need += p.SizeBytes
		}
	}
	have := tv.SizeBytes
	if !c.plan.Erase {
		have = c.freeSpace(ctx, tv.Device, tv.SizeBytes)
	}
	if have < need {
		return c.fail(ReasonDiskTooSmall, ActionChooseAnotherDisk,
			"%s has %s %s, the installer needs %s", target, probe.SizeLabel(have),
			map[bool]string{true: "of room", false: "free"}[c.plan.Erase], probe.SizeLabel(need))
	}
	c.rep.Progress(StepValidateDevice, 100, "")
	return nil
}

// freeSpace is the free space of a disk the install shares.
func (c *installRun) freeSpace(ctx context.Context, disk string, size int64) int64 {
	out, err := c.cmd().Run(ctx, "sfdisk", []string{"--json", disk}, CmdOpts{})
	if err != nil {
		return size // no table at all
	}
	var t sfdiskTable
	if json.Unmarshal(out, &t) != nil {
		return 0
	}
	return freeBytes(t, size)
}

func sameDevice(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return ra == rb
}

// ---- partition ------------------------------------------------------------

// reparted is one partition systemd-repart reports.
type reparted struct {
	File     string `json:"file"`
	Node     string `json:"node"`
	PartNo   int    `json:"partno"`
	Activity string `json:"activity"`
}

func (c *installRun) partition(ctx context.Context) error {
	if err := os.MkdirAll(c.r.workDir(), 0o700); err != nil {
		return c.fail(ReasonInternal, ActionReportBug, "creating %s: %v", c.r.workDir(), err)
	}
	work, err := os.MkdirTemp(c.r.workDir(), "install-")
	if err != nil {
		return c.fail(ReasonInternal, ActionReportBug, "creating the scratch directory: %v", err)
	}
	c.work = work
	defsDir := filepath.Join(work, "repart.d")
	if err := repart.Write(defsDir, c.defs); err != nil {
		return c.fail(ReasonInternal, ActionReportBug, "writing the repart definitions: %v", err)
	}
	disk := c.plan.Disk.Path
	c.rep.Progress(StepPartition, 5, "Preparing "+disk)
	empty := "allow"
	if c.plan.Erase {
		// Old signatures and kernel partition devices of the previous table
		// would make repart's new partitions busy, and its own wipe leaves
		// the old filesystems in place.
		if _, err := c.runCmd(ctx, CmdOpts{}, "wipefs", "--all", "--force", disk); err != nil {
			return c.stepFail(err, ReasonRepartError, ActionRetry, "wiping "+disk)
		}
		c.settle(ctx)
		empty = "force"
	}
	// Never the empty key: see transient_key.go.
	if c.tkey, err = newTransientKey(); err != nil {
		return c.fail(ReasonInternal, ActionReportBug, "making the install key: %v", err)
	}
	if c.tkeyPath, err = c.r.writeTransientKey("pending-"+filepath.Base(work), c.tkey); err != nil {
		return c.fail(ReasonInternal, ActionReportBug, "storing the install key: %v", err)
	}
	args := []string{"--dry-run=no", "--definitions=" + defsDir, "--empty=" + empty,
		"--key-file=" + c.tkeyPath, "--pretty=no", "--json=short", "--no-pager"}
	if c.tpm2 {
		args = append(args, "--tpm2-device=auto")
	}
	args = append(args, disk)
	c.rep.Progress(StepPartition, 10, "Copying the installer image")
	out, err := c.runCmd(ctx, CmdOpts{}, "systemd-repart", args...)
	if err != nil {
		return c.stepFail(err, ReasonRepartError, ActionRetry, "systemd-repart failed")
	}
	var created []reparted
	if err := json.Unmarshal(out, &created); err != nil {
		return c.fail(ReasonRepartError, ActionRetry, "systemd-repart printed an unreadable layout: %v", err)
	}
	c.settle(ctx)
	if err := c.assignPartitions(disk, created); err != nil {
		return err
	}
	c.rep.Progress(StepPartition, 100, "")
	return nil
}

// assignPartitions maps the partitions repart created to their roles by the
// definition each came from, never by a filesystem guess: definition NN-copy
// mirrors medium partition NN.
func (c *installRun) assignPartitions(disk string, created []reparted) error {
	byFile := map[string]reparted{}
	for _, p := range created {
		if p.Activity == "create" {
			byFile[filepath.Base(p.File)] = p
		}
	}
	for i, def := range c.defs {
		p, ok := byFile[def.FileName]
		if !ok {
			return c.fail(ReasonRepartError, ActionRetry, "systemd-repart did not create the partition for %s", def.FileName)
		}
		node := p.Node
		if node == "" {
			node = partNode(disk, p.PartNo+1)
		}
		med := c.partitionOf(def)
		switch {
		case med.Node == c.mediumRoot:
			c.rootPart = node
		case strings.EqualFold(med.Type, espType):
			c.esp, c.espPartNo = node, p.PartNo+1
		case c.boot == "":
			c.boot = node
		default:
			c.rep.Warn(StepPartition, fmt.Sprintf("partition %d of the medium has no known role and is copied unchanged", i+1))
		}
	}
	if c.esp == "" || c.rootPart == "" {
		return c.fail(ReasonRepartError, ActionRetry, "the copied layout has no EFI system partition or no root")
	}
	return nil
}

// partitionOf returns the medium partition a definition mirrors.
func (c *installRun) partitionOf(def repart.Definition) repart.Partition {
	for _, p := range c.table.Partitions {
		if fmt.Sprintf("%02d-copy.conf", p.Index) == def.FileName {
			return p
		}
	}
	return repart.Partition{}
}

// partNode is the kernel's node name of partition n of disk: nvme0n1 and
// loop0 get a p, sda does not.
func partNode(disk string, n int) string {
	if disk != "" && disk[len(disk)-1] >= '0' && disk[len(disk)-1] <= '9' {
		return disk + "p" + strconv.Itoa(n)
	}
	return disk + strconv.Itoa(n)
}

func (c *installRun) settle(ctx context.Context) {
	_, _ = c.cmd().Run(ctx, "udevadm", []string{"settle"}, CmdOpts{})
}

// ---- luks ----------------------------------------------------------------

func (c *installRun) enrollLUKS(ctx context.Context) error {
	enr := c.r.enroller()
	out, err := c.runCmd(ctx, CmdOpts{}, "cryptsetup", "luksUUID", c.rootPart)
	if err != nil {
		return c.stepFail(err, ReasonLUKSError, ActionRetry, "reading the LUKS UUID")
	}
	c.luksUUID = strings.TrimSpace(string(out))
	// The key file takes the volume's name, so io.systemd.CryptEnroll finds
	// it from the node gnome-setup names.
	named, ok := c.r.transientKeyPath(c.luksUUID)
	if !ok {
		return c.fail(ReasonLUKSError, ActionRetry, "the new volume has an unexpected LUKS UUID")
	}
	if err := os.Rename(c.tkeyPath, named); err != nil {
		return c.fail(ReasonInternal, ActionReportBug, "naming the install key: %v", err)
	}
	c.tkeyPath = named
	slots, err := enr.ListSlots(ctx, c.rootPart)
	if err != nil {
		return c.stepFail(err, ReasonLUKSError, ActionRetry, "listing keyslots")
	}
	for _, s := range slots {
		if s.Type == crypt.MechPassword {
			c.emptySlot, c.haveEmpty = s.Number, true
			break
		}
	}
	if !c.haveEmpty {
		return c.fail(ReasonLUKSError, ActionRetry, "repart left no keyslot to replace")
	}
	if c.userPass != nil {
		// One call: the empty slot is wiped, then the passphrase lands.
		_, err = enr.Enroll(ctx, crypt.EnrollOpts{
			Node: c.rootPart, Mechanism: crypt.MechPassword,
			NewPassword: c.userPass, Unlock: crypt.Unlock{Secret: c.tkey},
			WipeSlots: []int64{c.emptySlot},
		})
		if err != nil {
			return c.stepFail(err, ReasonLUKSError, ActionRetry, "enrolling the passphrase")
		}
		c.haveEmpty = false
		c.dropTransientKey()
	}
	if c.keepEmpty {
		c.rep.Warn(StepLUKS, "until a recovery key replaces it, the disk opens only with a key that this live session holds")
	}
	c.rep.Progress(StepLUKS, 100, "")
	return nil
}

// ---- tpm2_enroll --------------------------------------------------------

// verifyTPM2 checks the token repart sealed. The enrolment itself happens in
// the partition step (Encrypt=key-file+tpm2, TPM2PCRs=7); a missing token
// here means the install would boot asking for a passphrase nobody set.
func (c *installRun) verifyTPM2(ctx context.Context) error {
	slots, err := c.r.enroller().ListSlots(ctx, c.rootPart)
	if err != nil {
		return c.stepFail(err, ReasonLUKSError, ActionRetry, "listing keyslots")
	}
	for _, s := range slots {
		if s.Type == "tpm2" {
			c.tokenTPM2 = true
		}
	}
	if !c.tokenTPM2 {
		return c.fail(ReasonLUKSError, ActionChangeSettings, "the disk was not sealed to the TPM")
	}
	c.rep.Progress(StepTPM2Enroll, 100, "")
	return nil
}

// ---- mount ---------------------------------------------------------------

func (c *installRun) mountTarget(ctx context.Context) error {
	var id [4]byte
	_, _ = rand.Read(id[:])
	c.mapper = "fishermand-" + hex.EncodeToString(id[:])
	key := c.userPass
	if key == nil {
		// Only the transient key slot is left (TPM only, or SysInstall Run).
		key = c.tkey
	}
	if _, err := c.runCmd(ctx, CmdOpts{Stdin: key, Quiet: true},
		"cryptsetup", "open", "--type", "luks2", "--key-file=-", c.rootPart, c.mapper); err != nil {
		return c.stepFail(err, ReasonLUKSError, ActionRetry, "unlocking the root")
	}
	c.mapperOpen = true
	c.rootDev = "/dev/mapper/" + c.mapper
	c.rep.Progress(StepMount, 20, "")

	// New identities, while the filesystems are not mounted.
	devs := []string{c.esp}
	if c.boot != "" {
		devs = append(devs, c.boot)
	}
	devs = append(devs, c.rootDev)
	for _, d := range devs {
		if err := c.newIdentity(ctx, d); err != nil {
			return err
		}
	}
	c.rep.Progress(StepMount, 60, "")

	mnt := filepath.Join(c.work, "mnt")
	for _, m := range []struct{ dev, dir string }{
		{c.rootDev, "root"}, {c.boot, "boot"}, {c.esp, "efi"},
	} {
		if m.dev == "" {
			continue
		}
		dir := filepath.Join(mnt, m.dir)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return c.fail(ReasonInternal, ActionReportBug, "creating a mount point: %v", err)
		}
		if _, err := c.runCmd(ctx, CmdOpts{}, "mount", m.dev, dir); err != nil {
			return c.stepFail(err, ReasonInternal, ActionRetry, "mounting "+m.dir)
		}
		c.mounts = append(c.mounts, dir)
	}
	c.rep.Progress(StepMount, 100, "")
	return nil
}

func (c *installRun) mnt(name string) string { return filepath.Join(c.work, "mnt", name) }

// newIdentity gives a copied filesystem a fresh UUID (a FAT serial) and
// records old to new, so the postfix rewrite can fix every reference. The
// device must not be mounted.
func (c *installRun) newIdentity(ctx context.Context, dev string) error {
	fsOut, err := c.runCmd(ctx, CmdOpts{}, "blkid", "-p", "-s", "TYPE", "-o", "value", dev)
	if err != nil {
		return c.stepFail(err, ReasonInternal, ActionRetry, "reading the filesystem type")
	}
	fstype := strings.TrimSpace(string(fsOut))
	uuidOut, err := c.runCmd(ctx, CmdOpts{}, "blkid", "-p", "-s", "UUID", "-o", "value", dev)
	if err != nil {
		return c.stepFail(err, ReasonInternal, ActionRetry, "reading the filesystem UUID")
	}
	old := strings.TrimSpace(string(uuidOut))
	if dev == c.rootDev {
		c.rootFS = fstype
	}
	switch fstype {
	case "vfat":
		var b [4]byte
		_, _ = rand.Read(b[:])
		if err := c.r.setFATSerial(dev, binary.LittleEndian.Uint32(b[:])); err != nil {
			return c.fail(ReasonInternal, ActionRetry, "changing the FAT volume ID: %v", err)
		}
	case "ext4":
		// A copy of a mounted filesystem may need a check before tune2fs
		// accepts it; exit status 1 means "errors corrected".
		if _, err := c.runCmd(ctx, CmdOpts{}, "e2fsck", "-fy", dev); err != nil && !exitStatusAtMost(err, 1) {
			return c.stepFail(err, ReasonInternal, ActionRetry, "checking "+fstype)
		}
		if _, err := c.runCmd(ctx, CmdOpts{}, "tune2fs", "-U", "random", dev); err != nil {
			return c.stepFail(err, ReasonInternal, ActionRetry, "changing the UUID")
		}
	case "btrfs":
		// The medium root may be a seed (btrfstune -S): a seed mounts
		// read-only, and the copy must be a normal filesystem.
		// btrfstune exits 1 ("seeding flag is not set") for a filesystem
		// that is no seed, so the flag is read first: the medium of the
		// tunaOS pipeline is not a seed, and each btrfs install stopped in
		// the mount step (tuna-os/tunaOS run 45624212413).
		super, err := c.runCmd(ctx, CmdOpts{}, "btrfs", "inspect-internal", "dump-super", dev)
		if err != nil {
			return c.stepFail(err, ReasonInternal, ActionRetry, "reading the btrfs superblock")
		}
		if strings.Contains(string(super), "SEEDING") {
			if _, err := c.runCmd(ctx, CmdOpts{}, "btrfstune", "-f", "-S", "0", dev); err != nil {
				return c.stepFail(err, ReasonInternal, ActionRetry, "clearing the btrfs seed flag")
			}
		}
		if _, err := c.runCmd(ctx, CmdOpts{}, "btrfstune", "-f", "-u", dev); err != nil {
			return c.stepFail(err, ReasonInternal, ActionRetry, "changing the UUID")
		}
	case "xfs":
		if _, err := c.runCmd(ctx, CmdOpts{}, "xfs_admin", "-U", "generate", dev); err != nil {
			return c.stepFail(err, ReasonInternal, ActionRetry, "changing the UUID")
		}
	default:
		return c.fail(ReasonInternal, ActionReportBug, "unsupported filesystem %q on a copied partition", fstype)
	}
	c.settle(ctx)
	nowOut, err := c.runCmd(ctx, CmdOpts{}, "blkid", "-p", "-s", "UUID", "-o", "value", dev)
	if err != nil {
		return c.stepFail(err, ReasonInternal, ActionRetry, "reading the new UUID")
	}
	now := strings.TrimSpace(string(nowOut))
	if old == "" || now == "" || old == now {
		return c.fail(ReasonInternal, ActionRetry, "the new UUID of a %s partition did not take", fstype)
	}
	c.uuidMap[old] = now
	return nil
}

// exitStatusAtMost reports whether err is a command exit status <= n.
func exitStatusAtMost(err error, n int) bool {
	var ee interface{ ExitCode() int }
	if errors.As(err, &ee) {
		return ee.ExitCode() >= 0 && ee.ExitCode() <= n
	}
	return false
}

// setFATSerial writes a new volume serial into a FAT boot sector (and its
// FAT32 backup), like Utah's python helper. It opens the block device.
func (r Real) setFATSerial(dev string, serial uint32) error {
	if r.SetFATSerial != nil {
		return r.SetFATSerial(dev, serial)
	}
	f, err := os.OpenFile(dev, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	bs := make([]byte, 512)
	if _, err := f.ReadAt(bs, 0); err != nil {
		return err
	}
	fat32 := string(bs[82:90]) == "FAT32   "
	off := int64(0x27)
	if fat32 {
		off = 0x43
	}
	var sb [4]byte
	binary.LittleEndian.PutUint32(sb[:], serial)
	if _, err := f.WriteAt(sb[:], off); err != nil {
		return err
	}
	if fat32 {
		bytesPerSector := int64(binary.LittleEndian.Uint16(bs[11:13]))
		backup := int64(binary.LittleEndian.Uint16(bs[50:52]))
		if backup != 0 {
			if _, err := f.WriteAt(sb[:], backup*bytesPerSector+off); err != nil {
				return err
			}
		}
	}
	return f.Sync()
}

// ---- install_os -----------------------------------------------------------

// checkCopy verifies what the block copy brought: a deployment, and a
// machine-id that is still pristine (every copy of a baked-in id would
// share it).
func (c *installRun) checkCopy(ctx context.Context) error {
	deps, err := postfix.FindDeployments(c.mnt("root"))
	if err != nil {
		return c.fail(ReasonInternal, ActionReportBug, "%v", err)
	}
	for _, d := range deps {
		if err := postfix.CheckMachineID(d); err != nil {
			return c.fail(ReasonInternal, ActionReportBug, "%v", err)
		}
	}
	c.deployments = deps
	c.rep.Progress(StepInstallOS, 50, "")
	if _, err := c.runCmd(ctx, CmdOpts{}, "sync"); err != nil {
		return c.stepFail(err, ReasonInternal, ActionRetry, "syncing the copy")
	}
	c.rep.Progress(StepInstallOS, 100, "")
	return nil
}

// ---- configure ------------------------------------------------------------

func (c *installRun) configure(ctx context.Context) error {
	warns, err := c.r.configureTarget(ctx, c.mnt("root"), c.deployments, c.plan.Target)
	for _, w := range warns {
		c.rep.Warn(StepConfigure, w)
	}
	if err != nil {
		return c.stepFail(err, ReasonInternal, ActionRetry, "configuring the target")
	}
	c.rep.Progress(StepConfigure, 100, "")
	return nil
}

// ---- post_fixups ----------------------------------------------------------

func (c *installRun) postFixups(ctx context.Context) error {
	bootDir := ""
	switch {
	case c.boot != "":
		bootDir = c.mnt("boot")
	case isDir(filepath.Join(c.mnt("root"), "boot", "loader")):
		bootDir = filepath.Join(c.mnt("root"), "boot")
	}
	opts := postfix.Options{
		Target:    postfix.Target{Root: c.mnt("root"), Boot: bootDir, EFI: c.mnt("efi")},
		UUIDMap:   c.uuidMap,
		LUKSUUID:  c.luksUUID,
		TPM2:      c.tokenTPM2,
		Grow:      true,
		RootFS:    c.rootFS,
		RootDev:   c.rootDev,
		LiveEntry: postfix.MediumLiveEntry,
		LiveKarg:  postfix.MediumLiveKarg,
		// Disk stays empty: the firmware entry is the bootloader step.
	}
	run := func(ctx context.Context, name string, args ...string) error {
		_, err := c.runCmd(ctx, CmdOpts{}, name, args...)
		return err
	}
	rep, err := postfix.Apply(ctx, opts, run)
	if err != nil {
		return c.stepFail(err, ReasonInternal, ActionRetry, "post-install fixups")
	}
	c.report = rep
	c.rep.Progress(StepPostFixups, 100, "")
	return nil
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// ---- bootloader -----------------------------------------------------------

func (c *installRun) bootloader(ctx context.Context) error {
	switch {
	case c.report.Loader == "":
		c.rep.Progress(StepBootloader, 100, "no shim on the ESP: the loader boots from the removable path")
		return nil
	case c.r.NoFirmwareEntry || !fileExists(c.r.efiVars()):
		c.rep.Progress(StepBootloader, 100, "no firmware boot entry written")
		return nil
	}
	_, err := c.runCmd(ctx, CmdOpts{}, "efibootmgr", "--create", "--disk", c.plan.Disk.Path,
		"--part", strconv.Itoa(c.espPartNo), "--label", bootLabel(c.deployments), "--loader", c.report.Loader)
	if err != nil {
		// The disk still boots from the removable path; do not fail a
		// finished install for NVRAM trouble.
		c.rep.Warn(StepBootloader, "the firmware boot entry could not be written: "+err.Error())
	}
	c.rep.Progress(StepBootloader, 100, "")
	return nil
}

// defaultBootLabel names the firmware entry when no deployment says its name.
const defaultBootLabel = "TunaOS"

// bootLabel is the firmware entry label: NAME of the installed image's
// os-release (etc first, then usr/lib, as systemd reads them), so a Bonito
// install boots an entry called "Bonito". The image's branding check
// (tunaos-branding-contract boot.efi) fails on any other label.
func bootLabel(deployments []string) string {
	for _, d := range deployments {
		for _, rel := range []string{"etc/os-release", "usr/lib/os-release"} {
			data, err := os.ReadFile(filepath.Join(d, rel))
			if err != nil {
				continue
			}
			if name := osReleaseValue(string(data), "NAME"); name != "" {
				return name
			}
		}
	}
	return defaultBootLabel
}

// osReleaseValue is one key of an os-release file, unquoted; "" when absent.
func osReleaseValue(text, key string) string {
	for _, line := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || k != key {
			continue
		}
		if unq, err := strconv.Unquote(v); err == nil && strings.HasPrefix(v, "\"") {
			v = unq
		} else {
			v = strings.Trim(v, "'")
		}
		// A label is one printable line; efibootmgr takes it as an argument.
		v = strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return -1
			}
			return r
		}, v)
		return strings.TrimSpace(v)
	}
	return ""
}

// ---- finalize -------------------------------------------------------------

func (c *installRun) finalize(ctx context.Context) error {
	if _, err := c.runCmd(ctx, CmdOpts{}, "sync"); err != nil {
		return c.stepFail(err, ReasonInternal, ActionRetry, "syncing")
	}
	if err := c.closeAll(ctx); err != nil {
		return c.stepFail(err, ReasonInternal, ActionRetry, "unmounting the target")
	}
	c.rep.Progress(StepFinalize, 60, "")
	if c.wipeEmpty && c.haveEmpty {
		// TPM only: nothing but the TPM unlocks the disk now.
		if err := c.r.enroller().WipeSlots(ctx, c.rootPart, crypt.Unlock{Secret: c.tkey}, c.emptySlot); err != nil {
			return c.stepFail(err, ReasonLUKSError, ActionRetry, "removing the install key")
		}
		c.haveEmpty = false
		c.dropTransientKey()
		c.rep.Warn(StepFinalize, "only the TPM unlocks this disk: add a recovery key before changing firmware settings")
	}
	// Fail safe: an empty passphrase must never open the installed disk.
	// When cryptsetup cannot tell, the install fails with its exit status.
	opens, err := c.r.opensWith(ctx, c.rootPart, nil)
	if err != nil {
		return c.stepFail(err, ReasonLUKSError, ActionRetry, "testing the installed disk for an empty passphrase")
	}
	if opens {
		return c.fail(ReasonLUKSError, ActionRetry, "the installed disk opens with an empty passphrase")
	}
	c.finalized = true
	_ = os.RemoveAll(c.work)
	c.rep.Progress(StepFinalize, 100, "")
	return nil
}

// closeAll unmounts in reverse order and closes the LUKS volume.
func (c *installRun) closeAll(ctx context.Context) error {
	var first error
	for i := len(c.mounts) - 1; i >= 0; i-- {
		if _, err := c.runCmd(ctx, CmdOpts{}, "umount", "-R", c.mounts[i]); err != nil && first == nil {
			first = err
		}
	}
	if first == nil {
		c.mounts = nil
	}
	if c.mapperOpen && first == nil {
		if _, err := c.runCmd(ctx, CmdOpts{}, "cryptsetup", "close", c.mapper); err != nil {
			first = err
		} else {
			c.mapperOpen = false
		}
	}
	return first
}

// ---- cleanup ---------------------------------------------------------------

// cleanup unmounts and closes what the failed install opened. It runs on a
// context that outlives a cancel. It reports as the cleanup step and only
// warns on trouble: the original failure is what the caller returns.
func (c *installRun) cleanup(rep Reporter) {
	if c.finalized || (len(c.mounts) == 0 && !c.mapperOpen && c.work == "") {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), cleanupTimeout)
	defer cancel()
	rep.Start(StepCleanup)
	if err := c.closeAll(ctx); err != nil {
		// A busy mount: detach lazily, then close.
		for i := len(c.mounts) - 1; i >= 0; i-- {
			_, _ = c.cmd().Run(ctx, "umount", []string{"-R", "-l", c.mounts[i]}, CmdOpts{})
		}
		c.mounts = nil
		if c.mapperOpen {
			if _, err := c.cmd().Run(ctx, "cryptsetup", []string{"close", c.mapper}, CmdOpts{}); err != nil {
				rep.Warn(StepCleanup, "the LUKS volume "+c.mapper+" is still open: "+err.Error())
			} else {
				c.mapperOpen = false
			}
		}
	}
	if c.work != "" && !c.mapperOpen {
		_ = os.RemoveAll(c.work)
	}
	// A failed install leaves nothing to unlock later.
	c.dropTransientKey()
	rep.Progress(StepCleanup, 100, "")
	rep.Done(StepCleanup)
}

// dropTransientKey forgets the transient key: its slot is gone, or the
// install failed.
func (c *installRun) dropTransientKey() {
	if c.tkeyPath != "" {
		_ = os.Remove(c.tkeyPath)
		_ = os.Remove(filepath.Dir(c.tkeyPath)) // only when empty
		c.tkeyPath = ""
	}
	c.tkey = nil
}
