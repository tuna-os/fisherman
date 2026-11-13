package recipe

import (
	"fmt"
	"os"
)

// Recipe describes a fisherman installation.
type Recipe struct {
	Disk            string     `json:"disk"`            // block device, e.g. "/dev/sda" (auto-partition)
	Filesystem      string     `json:"filesystem"`      // "xfs", "ext4", "btrfs", or "zfs"
	BtrfsSubvolumes bool       `json:"btrfsSubvolumes"` // put the root in an @ subvolume (rootflags=subvol=@)
	Encryption      Encryption `json:"encryption"`
	Image           string     `json:"image"`        // source OCI image reference
	TargetImgref    string     `json:"targetImgref"` // update-tracking ref (optional; required with sourceReuse)
	// SourceReuse takes the source from an image the live host's bootc
	// already keeps (#384); see SourceReuse in reuse.go. Absent: the
	// ordinary source paths. Never a default.
	SourceReuse     *SourceReuse `json:"sourceReuse,omitempty"`
	SelinuxDisabled bool         `json:"selinuxDisabled"`
	UnifiedStorage  bool         `json:"unifiedStorage"` // true is refused (CheckUnifiedStorage): the flag is not passed
	// ComposeFsBackend passes --composefs-backend to bootc install to-filesystem.
	// Required for composefs-native images (e.g. ghcr.io/bootcrew/*).
	// Independent of UnifiedStorage — these are different bootc features.
	// Requires a filesystem that supports fs-verity (ext4, btrfs, f2fs).
	// XFS does NOT support fs-verity and is rejected in Validate().
	// Automatically forced to true when Filesystem is "zfs".
	ComposeFsBackend bool `json:"composeFsBackend"`
	// GenericImage passes --generic-image to bootc install to-filesystem, which
	// skips the bootupd presence check (and host-specific EFI NVRAM writes).
	// Set for ostree-based images that ship no bootupd (e.g. non-Fedora/EL bootc
	// images like Arch/Debian): bootc otherwise aborts with "bootupd is required
	// for ostree-based installs". Safe for wootc because Phase-2 boots via the
	// signed shim+grub chain wootc stages on the ESP, not a bootc-installed
	// bootloader. Left false for images that DO ship bootupd (bluefin, EL, Fedora)
	// so their proven install path is unchanged.
	GenericImage bool `json:"genericImage,omitempty"`
	// ZFSPoolName is the name of the ZFS pool to create (default: "rpool").
	// Only used when Filesystem is "zfs".
	ZFSPoolName string `json:"zfsPoolName,omitempty"`
	// Bootloader selects the bootloader: "grub2" (default) or "systemd".
	// Use "systemd" for images that ship systemd-boot (e.g. Project Bluefin/Dakota).
	Bootloader string `json:"bootloader"`
	// ImageType selects the install backend: "bootc" (default) or "ostree".
	// "ostree" support is not yet implemented and will be rejected by Validate().
	ImageType string `json:"imageType"`
	// FlatpakVarPath is the path relative to the install target root where the
	// writable /var for flatpaks lives. Defaults to "" which means fisherman
	// auto-detects based on whether the system is ostree or composefs-native.
	// Override in images.json for non-standard layouts, e.g. GnomeOS/Dakota:
	//   "flatpak_var_path": "state/os/default/var"
	FlatpakVarPath string `json:"flatpakVarPath,omitempty"`
	Hostname       string `json:"hostname"`
	// Flatpaks decides whether the host's system Flatpaks (/var/lib/flatpak)
	// are copied to the installed system. Absent (null): copy them. []: copy
	// nothing. A non-empty list also copies them, all of them: it is not a
	// filter and nothing is downloaded; fisherman only reports each listed
	// app the host does not have. The nil/empty distinction is the
	// contract, so keep the field a plain slice decoded by encoding/json.
	Flatpaks []string `json:"flatpaks"`
	User     UserSpec `json:"user"` // optional user account to create
	// CustomMounts is set for manual partitioning. When non-empty, Disk/Filesystem/
	// BtrfsSubvolumes and the auto-partition steps are skipped; fisherman formats and
	// mounts the listed partitions directly.
	CustomMounts []CustomMount `json:"customMounts,omitempty"`
	// VarDisk optionally describes a separate disk to mount at /var.
	// When set, fisherman formats (or mounts as-is) this disk before running
	// bootc, then adds a /var entry to the installed system's fstab.
	VarDisk *VarDiskSpec `json:"varDisk,omitempty"`
	// DiskIdentity binds the install to the device the frontend planned on:
	// the serial, WWN and size `fisherman plan --json` reported for `disk`.
	// Absent: no binding. When set, validate and the install refuse the
	// recipe (disk_identity_mismatch) if the device `disk` names now
	// reports another value for any field that is set. See
	// docs/VALIDATE.md, "Disk identity".
	DiskIdentity *DiskIdentity `json:"diskIdentity,omitempty"`
	// PlanBinding binds the install to the plan the user confirmed: the
	// plan.binding `fisherman plan --json` printed. Absent: no binding.
	// When set, the full validate and the install plan the recipe again
	// and refuse it (plan_stale) if the target, the partitions or a
	// capacity verdict changed. See docs/PLAN.md, "Binding the install to
	// the confirmed plan".
	PlanBinding *PlanBinding `json:"planBinding,omitempty"`
	// AdditionalImageStores lists host paths to be exposed to the bootc
	// container as containers/storage additionalimagestores. Each path is
	// bind-mounted read-only into the container at the same location and added
	// to a fisherman-generated storage.conf passed as CONTAINERS_STORAGE_CONF.
	//
	// Use this for live-media offline image stores (e.g. a squashfs of an OCI
	// store baked into an installer ISO). When the caller provides their own
	// CONTAINERS_STORAGE_CONF env var, that takes priority and this field is
	// ignored.
	//
	// Absent (or null): fisherman finds the offline stores on the host itself,
	// with the same discovery as `fisherman probe` (see docs/PROBE.md). A
	// list, including an empty one, is used exactly as given: [] opts out of
	// discovery. The nil/empty distinction is the contract, so keep the
	// field a plain slice decoded by encoding/json.
	AdditionalImageStores []string `json:"additionalImageStores,omitempty"`
	// TargetMount overrides the host path where fisherman assembles the
	// target filesystem hierarchy. Defaults to "/mnt/fisherman-target".
	// Use it when the default path is taken on the host. It does not make
	// two installs safe side by side: they share the scratch directory, and
	// a second install on one host is refused by the install lock
	// (cmd/fisherman/lock.go).
	TargetMount string `json:"targetMount,omitempty"`
	// LuksMapperName overrides the cryptsetup mapper name used for the
	// LUKS root device. Defaults to "fisherman-root". Like TargetMount, it
	// is for a host where the default name is taken, not for parallel
	// installs. Production installs should leave it at the default since
	// the same name is hard-coded into installed system kernel cmdlines
	// (rd.luks.name).
	LuksMapperName string `json:"luksMapperName,omitempty"`
	// DistroID is a short lowercase identifier for the distribution, used
	// to name OEM setup paths and service files written to the target
	// (e.g. /etc/<distroID>/oem/, <distroID>-oem-setup.service).
	// Defaults to "bootc" when empty.
	DistroID string `json:"distroID,omitempty"`
	// BrewTap is an optional Homebrew tap to add before installing OEM
	// packages (e.g. "ublue-os/tap"). When empty, no tap is added.
	BrewTap string `json:"brewTap,omitempty"`
	// Override lists the target checks the caller accepts failing: the
	// values of Overrides ("too_small", "uefi"). They are the two checks
	// about hardware policy rather than safety; the checks that protect
	// data (the boot disk, a read-only disk, a path that is not a whole
	// disk) cannot be overridden. See docs/VALIDATE.md, "Target eligibility".
	Override []string `json:"override,omitempty"`
	// CopyNetworkProfiles says whether the host's NetworkManager connection
	// profiles (/etc/NetworkManager/system-connections/*.nmconnection: Wi-Fi
	// passwords, 802.1X credentials) are copied to the installed system.
	// Absent (null): copy only when the host booted from live media, where
	// the profiles are the ones the user just typed into the installer.
	// true copies on any host; false never copies.
	CopyNetworkProfiles *bool `json:"copyNetworkProfiles,omitempty"`
	// CopyBluetoothPairings is the same choice for the host's Bluetooth
	// pairings (/var/lib/bluetooth, which holds the link keys).
	CopyBluetoothPairings *bool `json:"copyBluetoothPairings,omitempty"`
	// Extras turns on the desktop and product steps of the post-install
	// stage. Each is off unless asked for. Absent (null): fisherman looks
	// the image up in its catalog (data/images.json, key "extras") and uses
	// that entry's values; an image the catalog does not know gets none.
	// An object, {} included, is used exactly as given.
	Extras *Extras `json:"extras,omitempty"`
	// Experimental lists the experimental features the caller accepts: the
	// values of Experimentals ("zfs"). A feature is experimental when no VM
	// test covers it; without its name here the recipe is refused.
	Experimental []string `json:"experimental,omitempty"`
	// SetBootNext says whether fisherman sets the firmware's BootNext
	// variable to the installed system's boot entry when the install ends,
	// so the next boot starts it whatever the boot order says. It changes
	// the firmware of the machine fisherman runs on. Absent (null): only
	// when that machine booted from live media. true: always. false: never.
	SetBootNext *bool `json:"setBootNext,omitempty"`
	// Locale is the system locale written to the installed system's
	// /etc/locale.conf as LANG, e.g. "de_DE.UTF-8". Empty: the image's.
	Locale string `json:"locale,omitempty"`
	// Keyboard is the keyboard layout written to the installed system's
	// /etc/vconsole.conf and X11 keyboard configuration. For an encrypted
	// install its console keymap also reaches the initrd
	// (rd.vconsole.keymap), so the passphrase prompt uses the layout the
	// passphrase was typed with. Absent: the image's.
	Keyboard *KeyboardSpec `json:"keyboard,omitempty"`
	// Timezone is the IANA time zone the installed system's /etc/localtime
	// points to, e.g. "Europe/Berlin". Empty: the image's.
	Timezone string `json:"timezone,omitempty"`
}

// KeyboardSpec is an XKB keyboard configuration, with the names of
// /usr/share/X11/xkb/rules/base.lst (what `localectl list-x11-keymap-*`
// prints). Layout is required when the object is present.
type KeyboardSpec struct {
	Layout  string `json:"layout"`            // e.g. "de", "us", "us,ru"
	Variant string `json:"variant,omitempty"` // e.g. "nodeadkeys"
	Model   string `json:"model,omitempty"`   // e.g. "pc105"
}

// Experimental features (Recipe.Experimental).
const (
	// ExperimentalZFS: filesystem "zfs". No VM matrix entry installs and
	// boots it (tuna-os/fisherman#413).
	ExperimentalZFS = "zfs"
)

// Experimentals lists every value Recipe.Experimental accepts.
// ExperimentalBootcStoreReuse is in reuse.go.
var Experimentals = []string{ExperimentalZFS, ExperimentalBootcStoreReuse}

// AcceptsExperimental reports whether the recipe opts in to feature (one
// of Experimentals).
func (r *Recipe) AcceptsExperimental(feature string) bool {
	return contains(r.Experimental, feature)
}

// Extras are the post-install steps that suit a desktop product image and
// are wrong for an image fisherman knows nothing about (a server image, a
// base image). See the README, "Desktop extras".
type Extras struct {
	// PrintServices enables cups-browsed, avahi-daemon and ipp-usb, each
	// only when the installed image has the unit.
	PrintServices bool `json:"printServices,omitempty"`
	// OEMSetup writes the first-login unit that installs vendor packages
	// with Homebrew (ublue-os tap), enables the vendor's system service
	// when the image has it, and sets a vendor menu icon.
	OEMSetup bool `json:"oemSetup,omitempty"`
	// AudioNames writes WirePlumber rules with friendly device names into
	// the installed system, and applies them to the running session when
	// the host is live media.
	AudioNames bool `json:"audioNames,omitempty"`
	// BootSplash adds `rhgb quiet` to every boot entry.
	BootSplash bool `json:"bootSplash,omitempty"`
}

// Names lists the extras that are on, in a fixed order, for a log line.
func (e Extras) Names() []string {
	var on []string
	for _, x := range []struct {
		name string
		on   bool
	}{
		{"printServices", e.PrintServices},
		{"oemSetup", e.OEMSetup},
		{"audioNames", e.AudioNames},
		{"bootSplash", e.BootSplash},
	} {
		if x.on {
			on = append(on, x.name)
		}
	}
	return on
}

// FollowLive resolves an optional yes/no recipe field whose default follows
// live media: an explicit value wins; absent means "only on live media".
func FollowLive(field *bool, isLive bool) bool {
	if field != nil {
		return *field
	}
	return isLive
}

// Overrides are the values Recipe.Override accepts. They are probe's own
// identifiers: disks[].excluded_reason "too_small" and system.unmet "uefi".
const (
	OverrideTooSmall = "too_small"
	OverrideUEFI     = "uefi"
)

// Overrides lists every value Recipe.Override accepts.
var Overrides = []string{OverrideTooSmall, OverrideUEFI}

// Overridden reports whether the recipe accepts check (one of Overrides)
// failing.
func (r *Recipe) Overridden(check string) bool {
	return contains(r.Override, check)
}

// RootMountOptions returns the filesystem mount options that should be applied
// when mounting root during installation and recorded as persistent rootflags
// for post-reboot boots.
func (r *Recipe) RootMountOptions() string {
	if r.IsManualLayout() {
		for _, cm := range r.CustomMounts {
			if cm.Target == "/" {
				if cm.Fstype == "btrfs" {
					return "compress=zstd:1"
				}
				return ""
			}
		}
		return ""
	}

	if r.Filesystem != "btrfs" {
		return ""
	}
	if r.BtrfsSubvolumes {
		return "subvol=@,compress=zstd:1"
	}
	return "compress=zstd:1"
}

// UserSpec describes a user account to create during installation.
// If Username is empty the user creation step is skipped.
type UserSpec struct {
	Username string   `json:"username"`
	Fullname string   `json:"fullname"`
	Password string   `json:"password"`
	Groups   []string `json:"groups"`
}

// DiskIdentity is the identity a recipe expects `disk` to have. An empty
// string or a zero size is not checked; at least one field must be set.
type DiskIdentity struct {
	Serial    string `json:"serial,omitempty"`    // lsblk SERIAL, as plan reports it
	WWN       string `json:"wwn,omitempty"`       // lsblk WWN; compared without case
	SizeBytes int64  `json:"sizeBytes,omitempty"` // the size in bytes
}

// PlanBinding is the plan a recipe expects: plan.binding of `fisherman plan
// --json`, with read_image spelt readImage.
type PlanBinding struct {
	// Digest is plan.binding.digest: sha256 of the parts of the plan that
	// must not change (docs/PLAN.md).
	Digest string `json:"digest"`
	// ReadImage: the plan was made with --read-image, so the check reads
	// the image's metadata too (never a layer).
	ReadImage bool `json:"readImage,omitempty"`
}

// VarDiskSpec describes an optional separate disk to mount at /var.
type VarDiskSpec struct {
	Disk         string `json:"disk"`         // block device, e.g. "/dev/sdb"
	KeepExisting bool   `json:"keepExisting"` // if true, mount as-is; if false, format XFS
}

// isSupportedMountFstype reports whether a customMount fstype is one
// disk.formatPartition() can actually act on. Keep in sync with that switch:
// the empty string and "unformatted" mean "mount, do not format", which is what
// a pre-populated partition such as an existing ESP requires.
func isSupportedMountFstype(fstype string) bool {
	switch fstype {
	case "", "unformatted", "swap", "fat32", "ext3", "ext4", "xfs", "btrfs":
		return true
	default:
		return false
	}
}

// CustomMount describes a single partition → mountpoint mapping for manual layouts.
type CustomMount struct {
	Partition string `json:"partition"` // e.g. "/dev/sda1"
	Target    string `json:"target"`    // mountpoint, e.g. "/" or "/boot/efi"
	Fstype    string `json:"fstype"`    // e.g. "xfs", "fat32", "ext4", "unformatted"
}

// Encryption describes the disk encryption configuration.
type Encryption struct {
	Type       string `json:"type"`       // "none", "luks-passphrase", "tpm2-luks", "tpm2-luks-passphrase"
	Passphrase string `json:"passphrase"` // required for luks-passphrase and tpm2-luks-passphrase
}

// Load reads and parses a recipe JSON file.
func Load(path string) (*Recipe, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading recipe: %w", err)
	}
	return Parse(data)
}

// Validate checks that the recipe fields are coherent and that the disk
// exists. It returns the first of Problems() (as a Problem), or nil.
func (r *Recipe) Validate() error {
	if ps := r.Problems(); len(ps) > 0 {
		return ps[0]
	}
	return nil
}

// Problems runs every recipe rule (rules.go) plus the existence checks for
// the disk, the customMounts partitions and the varDisk, and returns every
// problem found, in a stable order: the layout first, then unifiedStorage,
// sourceReuse, imageType, image and targetImgref, bootloader, encryption,
// varDisk, hostname, user, distroID, brewTap, targetMount,
// flatpakVarPath, zfsPoolName, luksMapperName, additionalImageStores,
// override, experimental, locale, keyboard and timezone (docs/VALIDATE.md
// lists the same order). It does not check the
// TPM or whether the disk is an eligible target, which need the machine; see
// CheckTPM and CheckTarget.
func (r *Recipe) Problems() []Problem {
	var ps []Problem
	if r.IsManualLayout() {
		// Manual layout: validate each mount spec instead of the auto-partition fields.
		hasRoot := false
		for i, cm := range r.CustomMounts {
			if cm.Partition == "" {
				ps = append(ps, Problem{fmt.Sprintf("customMounts[%d].partition", i), CodeCustomMountPartitionRequired,
					fmt.Sprintf("customMounts[%d]: partition is required", i)})
			} else if cm.Target != "swap" && cm.Target != "" {
				if _, err := os.Stat(cm.Partition); err != nil {
					ps = append(ps, Problem{fmt.Sprintf("customMounts[%d].partition", i), CodeCustomMountPartitionNotFound,
						fmt.Sprintf("customMounts[%d]: partition %s: %v", i, cm.Partition, err)})
				}
			}
			if cm.Target == "/" {
				hasRoot = true
			}
			ps = append(ps, CheckCustomMountTarget(i, cm.Target)...)
			// Validate the fstype here, where it is cheap and non-destructive.
			// disk.ApplyCustomLayout() only discovers an unsupported value once
			// it reaches formatPartition() — by which point the caller may
			// already have repartitioned a disk on the strength of this recipe
			// validating. A caller passing "vfat" (the obvious spelling, and
			// not one we accept) got exactly that: validation passed, the
			// install died mid-flight.
			if !isSupportedMountFstype(cm.Fstype) {
				ps = append(ps, Problem{fmt.Sprintf("customMounts[%d].fstype", i), CodeCustomMountFstypeUnsupported,
					fmt.Sprintf("customMounts[%d]: unsupported fstype %q "+
						"(supported: fat32, ext3, ext4, xfs, btrfs, swap, or "+
						"\"unformatted\"/\"\" to mount without formatting)", i, cm.Fstype)})
			}
		}
		if !hasRoot {
			ps = append(ps, Problem{"customMounts", CodeCustomMountsNoRoot,
				"customMounts: no root (/) partition specified"})
		}
		ps = append(ps, CheckCustomMountsLayout(r)...)
		ps = append(ps, checkCustomMountsPersistence(r, resolveSymlinks)...)
		// Encryption is NOT applied on the manual path: luksFormat/luksOpen run
		// only in the auto-partition branch below, and TPM enrolment needs an
		// activeRootPart that manual mode leaves empty. Accepting an encrypted
		// manual recipe therefore produces an install that completes
		// UNENCRYPTED while the caller believes otherwise — a security-boundary
		// failure, so fail closed here rather than silently downgrade.
		// (Same shape as the ZFS+LUKS rejection.)
		if r.HasEncryption() {
			ps = append(ps, Problem{"encryption.type", CodeEncryptionUnsupportedOnManual,
				fmt.Sprintf("encryption %q is not supported with customMounts: "+
					"manual layouts do not run luksFormat, so the install would complete "+
					"unencrypted", r.Encryption.Type)})
		}
	} else {
		if r.Disk == "" {
			ps = append(ps, Problem{"disk", CodeDiskRequired, "disk is required"})
		} else if _, err := os.Stat(r.Disk); err != nil {
			ps = append(ps, Problem{"disk", CodeDiskNotFound, fmt.Sprintf("disk %s: %v", r.Disk, err)})
		}
		ps = append(ps, CheckFilesystem(r.Filesystem)...)
		ps = append(ps, CheckLayoutCombos(r)...)
		ps = append(ps, CheckExperimentalFilesystem(r)...)
	}
	ps = append(ps, CheckUnifiedStorage(r.UnifiedStorage)...)
	ps = append(ps, CheckSourceReuse(r)...)
	ps = append(ps, CheckImageType(r.ImageType)...)
	ps = append(ps, CheckImageRefs(r)...)
	ps = append(ps, CheckBootloader(r.Bootloader)...)
	ps = append(ps, CheckEncryption(r.Encryption)...)
	ps = append(ps, CheckVarDiskEncryption(r)...)
	// image may be empty in live-ISO mode; bootc auto-detects the running
	// container. Whether this host IS live media is a host fact, so that
	// check is ValidateImage, called by the CLI with probe's answer.
	if r.VarDisk != nil {
		if r.VarDisk.Disk == "" {
			ps = append(ps, Problem{"varDisk.disk", CodeVarDiskRequired, "varDisk.disk is required"})
		} else {
			if _, err := os.Stat(r.VarDisk.Disk); err != nil {
				ps = append(ps, Problem{"varDisk.disk", CodeVarDiskNotFound,
					fmt.Sprintf("varDisk.disk %s: %v", r.VarDisk.Disk, err)})
			}
			if r.VarDisk.Disk == r.Disk {
				ps = append(ps, Problem{"varDisk.disk", CodeVarDiskSameAsDisk,
					"varDisk.disk must differ from the system disk"})
			}
		}
		ps = append(ps, CheckVarDiskBackend(r)...)
	}
	ps = append(ps, CheckHostname(r.Hostname)...)
	// The username used to be checked only by useradd in the configure step,
	// after the OS was already on disk. Checking it here makes the install
	// refuse it before partitioning.
	ps = append(ps, CheckUsername(r.User.Username)...)
	// distroID and brewTap end up in generated paths, unit names and a root
	// shell script, so they are refused before any disk step too.
	ps = append(ps, CheckDistroID(r.DistroID)...)
	ps = append(ps, CheckBrewTap(r.BrewTap)...)
	// Host paths root mounts onto or writes under; see CheckTargetMount.
	ps = append(ps, CheckTargetMount(r.TargetMount)...)
	ps = append(ps, CheckFlatpakVarPath(r.FlatpakVarPath)...)
	// Names and paths that reach zpool, cryptsetup and podman as arguments
	// after the disk is partitioned (#414).
	ps = append(ps, CheckZFSPoolName(r.ZFSPoolName)...)
	ps = append(ps, CheckLuksMapperName(r.LuksMapperName)...)
	ps = append(ps, CheckAdditionalImageStores(r.AdditionalImageStores)...)
	ps = append(ps, CheckOverride(r.Override)...)
	ps = append(ps, CheckDiskIdentity(r)...)
	ps = append(ps, CheckPlanBinding(r)...)
	ps = append(ps, CheckExperimental(r.Experimental)...)
	// Written into /etc files and, for the keymap, onto the kernel command
	// line: refused before any disk step (#405).
	ps = append(ps, CheckLocale(r.Locale)...)
	ps = append(ps, CheckKeyboard(r.Keyboard)...)
	ps = append(ps, CheckTimezone(r.Timezone)...)
	return ps
}
