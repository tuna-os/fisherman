package recipe

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// This file is the one place the recipe rules live. Validate(), the install
// path and `fisherman validate --json` all run them, so a frontend that asks
// fisherman before it writes a recipe gets exactly the answer the install
// would give, before any disk is touched. docs/VALIDATE.md is the contract.

// Problem is one rule a recipe breaks.
//
// Field is the JSON path of the offending recipe field ("hostname",
// "user.username", "encryption.passphrase", "customMounts[0].fstype"), or ""
// when the recipe as a whole could not be read. Code is stable: frontends key
// their copy on it, so a code is never renamed or reused for another meaning
// (add a new one instead). Message is English for logs and CLI users; it may
// change.
type Problem struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error makes a Problem usable as the error Validate returns.
func (p Problem) Error() string { return p.Message }

// Problem codes. See docs/VALIDATE.md for what each one means.
const (
	CodeRecipeUnreadable = "recipe_unreadable"
	CodeRecipeMalformed  = "recipe_malformed"
	// CodeRecipeUnknownField: a key the Recipe type does not have. See
	// UnknownFields.
	CodeRecipeUnknownField = "recipe_unknown_field"

	CodeDiskRequired = "disk_required"
	CodeDiskNotFound = "disk_not_found"

	CodeFilesystemUnsupported         = "filesystem_unsupported"
	CodeBtrfsSubvolumesRequireBtrfs   = "btrfs_subvolumes_require_btrfs"
	CodeComposeFsRequiresFsVerity     = "composefs_requires_fsverity"
	CodeCustomMountPartitionRequired  = "custom_mount_partition_required"
	CodeCustomMountPartitionNotFound  = "custom_mount_partition_not_found"
	CodeCustomMountFstypeUnsupported  = "custom_mount_fstype_unsupported"
	CodeCustomMountsNoRoot            = "custom_mounts_no_root"
	CodeVarDiskRequired               = "var_disk_required"
	CodeVarDiskNotFound               = "var_disk_not_found"
	CodeVarDiskSameAsDisk             = "var_disk_same_as_disk"
	CodeImageRequired                 = "image_required"
	CodeImageTypeUnsupported          = "image_type_unsupported"
	CodeBootloaderUnsupported         = "bootloader_unsupported"
	CodeEncryptionUnsupported         = "encryption_unsupported"
	CodeEncryptionUnsupportedOnZFS    = "encryption_unsupported_on_zfs"
	CodeEncryptionUnsupportedOnManual = "encryption_unsupported_on_manual_layout"
	CodeEncryptionPassphraseRequired  = "encryption_passphrase_required"
	CodeEncryptionTPMUnavailable      = "encryption_tpm_unavailable"
	CodeHostnameRequired              = "hostname_required"
	CodeHostnameTooLong               = "hostname_too_long"
	CodeHostnameLabelEmpty            = "hostname_label_empty"
	CodeHostnameLabelTooLong          = "hostname_label_too_long"
	CodeHostnameInvalidChar           = "hostname_invalid_char"
	CodeHostnameLabelHyphen           = "hostname_label_hyphen"
	CodeUsernameTooLong               = "username_too_long"
	CodeUsernameInvalidStart          = "username_invalid_start"
	CodeUsernameInvalidChar           = "username_invalid_char"
	CodeUsernameReserved              = "username_reserved"
	CodeSlurpUnsupported              = "slurp_unsupported"
	CodeDistroIDInvalid               = "distro_id_invalid"
	CodeBrewTapInvalid                = "brew_tap_invalid"
	CodeCustomMountTargetInvalid      = "custom_mount_target_invalid"
	CodeTargetMountInvalid            = "target_mount_invalid"
	CodeFlatpakVarPathInvalid         = "flatpak_var_path_invalid"

	// Target eligibility (CheckTarget). The reason part is probe's
	// excluded_reason / unmet identifier.
	CodeDiskUnverified      = "disk_unverified"
	CodeDiskNotDisk         = "disk_not_disk"
	CodeDiskBootDisk        = "disk_boot_disk"
	CodeDiskReadOnly        = "disk_read_only"
	CodeDiskTooSmall        = "disk_too_small"
	CodeFirmwareNotUEFI     = "firmware_not_uefi"
	CodeVarDiskUnverified   = "var_disk_unverified"
	CodeVarDiskNotDisk      = "var_disk_not_disk"
	CodeVarDiskBootDisk     = "var_disk_boot_disk"
	CodeVarDiskReadOnly     = "var_disk_read_only"
	CodeOverrideUnsupported = "override_unsupported"

	// CodeZFSExperimental: filesystem "zfs" without "zfs" in `experimental`.
	CodeZFSExperimental = "zfs_experimental"
	// CodeExperimentalUnsupported: an `experimental` entry fisherman does
	// not know.
	CodeExperimentalUnsupported = "experimental_unsupported"

	CodeAdditionalImageStoreInvalid  = "additional_image_store_invalid"
	CodeAdditionalImageStoreNotFound = "additional_image_store_not_found"
	CodeZFSPoolNameInvalid           = "zfs_pool_name_invalid"
	CodeLuksMapperNameInvalid        = "luks_mapper_name_invalid"

	CodeUnifiedStorageUnsupported = "unified_storage_unsupported"

	CodeCustomMountsNoESP  = "custom_mounts_no_esp"
	CodeCustomMountsNoBoot = "custom_mounts_no_boot"

	// Install-only: reported in the install's error event, not by validate,
	// which needs no root and does not look for host tools.
	CodeNotRoot               = "not_root"
	CodeHostToolMissing       = "host_tool_missing"
	CodeInstallAlreadyRunning = "install_already_running"
)

// Filesystems is the set of root filesystems the auto-partition path can
// create (disk.FormatRoot and the ZFS path), in the order frontends offer
// them. A manual layout uses customMounts[].fstype instead.
var Filesystems = []string{"xfs", "ext4", "btrfs", "zfs"}

// EncryptionChoice describes one encryption.type value. EncryptionChoices is
// the single table the encryption.type enum, the passphrase rule, the TPM
// rule and data/encryption-choices.json all come from.
type EncryptionChoice struct {
	// Type is the encryption.type value written into the recipe.
	Type string `json:"type"`
	// Encrypted: the root is a LUKS2 volume.
	Encrypted bool `json:"encrypted"`
	// NeedsPassphrase: encryption.passphrase must be non-empty.
	NeedsPassphrase bool `json:"needs_passphrase"`
	// NeedsTPM2: a usable TPM 2.0 is required (probe's tpm.usable); offer
	// the choice only when it is true.
	NeedsTPM2 bool `json:"needs_tpm2"`
	// CopyKey is the branding copy-key stem: the label is CopyKey+"_label",
	// the description CopyKey+"_description" (bootc-installer
	// shared/branding/README.md, "Encryption choices").
	CopyKey string `json:"copy_key"`
}

// EncryptionChoices lists every encryption.type fisherman accepts, in the
// order frontends offer them. An empty type means "none".
var EncryptionChoices = []EncryptionChoice{
	{Type: "none", CopyKey: "encryption_none"},
	{Type: "luks-passphrase", Encrypted: true, NeedsPassphrase: true, CopyKey: "encryption_luks_passphrase"},
	{Type: "tpm2-luks", Encrypted: true, NeedsTPM2: true, CopyKey: "encryption_tpm2_luks"},
	{Type: "tpm2-luks-passphrase", Encrypted: true, NeedsPassphrase: true, NeedsTPM2: true, CopyKey: "encryption_tpm2_luks_passphrase"},
}

// LookupEncryption returns the choice for an encryption.type ("" is "none").
func LookupEncryption(t string) (EncryptionChoice, bool) {
	if t == "" {
		t = "none"
	}
	for _, c := range EncryptionChoices {
		if c.Type == t {
			return c, true
		}
	}
	return EncryptionChoice{}, false
}

// IsEncrypted reports whether t is a known type that encrypts the root.
func IsEncrypted(t string) bool {
	c, ok := LookupEncryption(t)
	return ok && c.Encrypted
}

// NeedsTPM2 reports whether t is a known type that enrols a TPM 2.0.
func NeedsTPM2(t string) bool {
	c, ok := LookupEncryption(t)
	return ok && c.NeedsTPM2
}

// Hostname limits. The kernel holds at most HOST_NAME_MAX (64) bytes, and
// systemd's hostname_is_valid() applies the same bound to /etc/hostname;
// each dot-separated label is an RFC 1123 label of at most 63 characters.
const (
	HostnameMaxLen      = 64
	HostnameLabelMaxLen = 63
)

// CheckHostname applies the hostname rule: required; at most 64 characters;
// one or more dot-separated RFC 1123 labels, each 1-63 characters of ASCII
// letters, digits and hyphens that neither starts nor ends with a hyphen.
func CheckHostname(h string) []Problem {
	const f = "hostname"
	if h == "" {
		return []Problem{{f, CodeHostnameRequired, "hostname is required"}}
	}
	if len(h) > HostnameMaxLen {
		return []Problem{{f, CodeHostnameTooLong,
			fmt.Sprintf("hostname is %d characters; the maximum is %d", len(h), HostnameMaxLen)}}
	}
	for _, r := range h {
		if r != '.' && r != '-' && !isASCIIAlnum(r) {
			return []Problem{{f, CodeHostnameInvalidChar,
				fmt.Sprintf("hostname %q contains %q; use only letters, digits, hyphens and dots", h, r)}}
		}
	}
	for _, label := range strings.Split(h, ".") {
		switch {
		case label == "":
			return []Problem{{f, CodeHostnameLabelEmpty,
				fmt.Sprintf("hostname %q has an empty label (a leading, trailing or doubled dot)", h)}}
		case len(label) > HostnameLabelMaxLen:
			return []Problem{{f, CodeHostnameLabelTooLong,
				fmt.Sprintf("hostname label %q is %d characters; the maximum is %d", label, len(label), HostnameLabelMaxLen)}}
		case label[0] == '-' || label[len(label)-1] == '-':
			return []Problem{{f, CodeHostnameLabelHyphen,
				fmt.Sprintf("hostname label %q starts or ends with a hyphen", label)}}
		}
	}
	return nil
}

// UsernameMaxLen is shadow-utils' limit on Linux (the utmp ut_user field).
const UsernameMaxLen = 32

// ReservedUsernames are names useradd refuses on the images fisherman
// installs, because the target already has a user or group by that name
// (useradd also creates a same-named group, so a group name collides too).
// It covers the base passwd/group of Fedora, EL and Debian, the system
// accounts systemd and common desktop services create, and the groups
// frontends add the first user to. It cannot be exhaustive for every image;
// it exists so the common collisions fail before partitioning instead of in
// the configure step, after the OS is on disk. Names starting with
// "systemd-" are reserved as a prefix.
var ReservedUsernames = []string{
	// base passwd/group
	"root", "bin", "daemon", "adm", "lp", "sync", "shutdown", "halt", "mail",
	"operator", "games", "ftp", "nobody", "nogroup", "sys", "tty", "disk",
	"mem", "kmem", "wheel", "cdrom", "dialout", "floppy", "tape", "video",
	"audio", "input", "kvm", "render", "sgx", "utmp", "lock", "users",
	"man", "uucp", "news", "proxy", "backup", "list", "irc", "src", "shadow",
	"staff", "sudo", "plugdev", "netdev", "www-data", "ssh_keys",
	// service accounts on desktop images
	"dbus", "polkitd", "tss", "chrony", "avahi", "rtkit", "pipewire",
	"geoclue", "colord", "gdm", "sddm", "flatpak", "sshd", "rpc", "rpcuser",
	"dnsmasq", "unbound", "usbmuxd", "nm-openvpn", "nm-openconnect", "qemu",
	"saslauth", "setroubleshoot", "sssd", "gnome-initial-setup", "brlapi",
	"printadmin", "openvpn", "lightdm", "messagebus",
	// groups frontends add the first user to
	"docker", "incus-admin", "libvirt",
}

// CheckUsername applies the user.username rule. Empty is valid (it skips
// user creation). Otherwise it is what useradd accepts on every target
// image: 1-32 characters, a lowercase letter or underscore first, then
// lowercase letters, digits, underscores and hyphens, and not a name the
// image already has (ReservedUsernames, or the "systemd-" prefix).
func CheckUsername(u string) []Problem {
	const f = "user.username"
	if u == "" {
		return nil
	}
	if len(u) > UsernameMaxLen {
		return []Problem{{f, CodeUsernameTooLong,
			fmt.Sprintf("username is %d characters; the maximum is %d", len(u), UsernameMaxLen)}}
	}
	if c := rune(u[0]); !isLower(c) && c != '_' {
		return []Problem{{f, CodeUsernameInvalidStart,
			fmt.Sprintf("username %q must start with a lowercase letter or an underscore", u)}}
	}
	for _, r := range u {
		if !isLower(r) && !isDigit(r) && r != '_' && r != '-' {
			return []Problem{{f, CodeUsernameInvalidChar,
				fmt.Sprintf("username %q contains %q; use only lowercase letters, digits, underscores and hyphens", u, r)}}
		}
	}
	if strings.HasPrefix(u, "systemd-") || contains(ReservedUsernames, u) {
		return []Problem{{f, CodeUsernameReserved,
			fmt.Sprintf("username %q is already a system user or group on the target", u)}}
	}
	return nil
}

// distroIDPattern and brewTapPattern bound two recipe strings the install
// writes into generated files: distroID names /etc/<distroID>/oem/, the
// <distroID>-oem-setup.service unit, its preset and the GRUB fallback
// entry; brewTap is interpolated into the OEM setup shell script as
// `brew tap <brewTap>`. Anything outside these sets (a "/" or "..", a
// newline, a shell metacharacter, a leading "-") would let a recipe write
// outside those paths, inject unit directives or run commands as root.
var (
	distroIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	brewTapPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

// CheckDistroID applies the distroID rule. Empty is valid (the install uses
// "bootc"); otherwise 1-63 characters, a lowercase letter first, then
// lowercase letters, digits and hyphens.
func CheckDistroID(id string) []Problem {
	if id == "" || distroIDPattern.MatchString(id) {
		return nil
	}
	return []Problem{{"distroID", CodeDistroIDInvalid,
		fmt.Sprintf("distroID must start with a lowercase letter and contain only lowercase letters, digits and hyphens (at most 63 characters), got %q", id)}}
}

// CheckBrewTap applies the brewTap rule. Empty is valid (no tap is added);
// otherwise an owner/name pair, each part starting with a letter or digit
// and containing only letters, digits, ".", "_" and "-".
func CheckBrewTap(tap string) []Problem {
	if tap == "" || brewTapPattern.MatchString(tap) {
		return nil
	}
	return []Problem{{"brewTap", CodeBrewTapInvalid,
		fmt.Sprintf("brewTap must be an owner/name pair of letters, digits, \".\", \"_\" and \"-\", got %q", tap)}}
}

// CheckFilesystem applies the auto-layout filesystem rule.
func CheckFilesystem(fs string) []Problem {
	if contains(Filesystems, fs) {
		return nil
	}
	return []Problem{{"filesystem", CodeFilesystemUnsupported,
		fmt.Sprintf("filesystem must be \"xfs\", \"ext4\", \"btrfs\", or \"zfs\", got %q", fs)}}
}

// CheckEncryption applies the encryption.type enum and the passphrase rule.
func CheckEncryption(e Encryption) []Problem {
	c, ok := LookupEncryption(e.Type)
	if !ok {
		return []Problem{{"encryption.type", CodeEncryptionUnsupported,
			"encryption.type must be \"none\", \"luks-passphrase\", \"tpm2-luks\", or \"tpm2-luks-passphrase\""}}
	}
	if c.NeedsPassphrase && e.Passphrase == "" {
		return []Problem{{"encryption.passphrase", CodeEncryptionPassphraseRequired,
			fmt.Sprintf("encryption.passphrase required for %s", e.Type)}}
	}
	return nil
}

// CheckLayoutCombos applies the auto-layout rules that tie the filesystem to
// other fields: btrfsSubvolumes needs btrfs, composefs needs fs-verity (not
// XFS), and ZFS cannot be LUKS-encrypted.
func CheckLayoutCombos(r *Recipe) []Problem {
	var ps []Problem
	if r.BtrfsSubvolumes && r.Filesystem != "btrfs" {
		ps = append(ps, Problem{"btrfsSubvolumes", CodeBtrfsSubvolumesRequireBtrfs,
			"btrfsSubvolumes requires filesystem=btrfs"})
	}
	if r.ComposeFsBackend && r.Filesystem == "xfs" {
		ps = append(ps, Problem{"composeFsBackend", CodeComposeFsRequiresFsVerity,
			"composefs-backend requires fs-verity, which XFS does not support; use ext4 or btrfs instead"})
	}
	if r.Filesystem == "zfs" && IsEncrypted(r.Encryption.Type) {
		ps = append(ps, Problem{"encryption.type", CodeEncryptionUnsupportedOnZFS,
			"ZFS filesystem does not support LUKS encryption in this version"})
	}
	return ps
}

// CheckUnifiedStorage applies the unifiedStorage rule: an explicit true is
// refused. The field asks for bootc's --experimental-unified-storage, which
// fisherman does not pass: it runs bootc inside `podman run --privileged`,
// where bootc's unified storage does not resolve (install.Options has the
// detail). A recipe that set true used to install without what it asked
// for, and report success. false and absent are unchanged.
//
// It is not composeFsBackend, and it is not additionalImageStores; those
// are separate features and are supported. Do not turn this rule off
// because a bootc version or its help text changed: the flag needs a tested
// storage profile first.
func CheckUnifiedStorage(unified bool) []Problem {
	if !unified {
		return nil
	}
	return []Problem{{"unifiedStorage", CodeUnifiedStorageUnsupported,
		"unifiedStorage is not supported: fisherman does not pass bootc's --experimental-unified-storage, " +
			"so the install would complete without it; set it to false or leave it out"}}
}

// CheckImageType applies the imageType rule.
func CheckImageType(t string) []Problem {
	switch t {
	case "", "bootc":
		return nil
	case "ostree":
		return []Problem{{"imageType", CodeImageTypeUnsupported,
			"imageType \"ostree\" is not yet supported; only \"bootc\" is implemented"}}
	default:
		return []Problem{{"imageType", CodeImageTypeUnsupported,
			fmt.Sprintf("imageType must be \"bootc\" (or empty), got %q", t)}}
	}
}

// CheckBootloader applies the bootloader rule.
func CheckBootloader(b string) []Problem {
	switch b {
	case "", "grub2", "systemd":
		return nil
	default:
		return []Problem{{"bootloader", CodeBootloaderUnsupported,
			fmt.Sprintf("bootloader must be \"grub2\" or \"systemd\", got %q", b)}}
	}
}

// CheckSlurp applies the slurp rule: a requested user-data migration is
// refused. The extracted copy lives only in RAM (/run), an extraction error
// or a partial copy still lets the install reach partitioning, and an
// interruption loses the copy after the source is gone. Until there is a
// durable rescue store, an explicit data request must stop before any disk
// work. slurpWallpapers (best-effort wallpapers) is unaffected.
func CheckSlurp(s *SlurpSpec) []Problem {
	if s == nil {
		return nil
	}
	return []Problem{{"slurp", CodeSlurpUnsupported,
		"requested data migration requires a verified durable rescue store; refusing installation before disk work"}}
}

// CheckTPM applies the TPM rule: a tpm2-* encryption type needs a usable
// TPM 2.0 on this machine. tpm2Usable is the probe's tpm.usable. It is a
// separate check because it reads the machine, not the recipe; the install
// path and `validate` run it, `validate --partial` does not.
func (r *Recipe) CheckTPM(tpm2Usable bool) []Problem {
	if len(r.CustomMounts) > 0 || !NeedsTPM2(r.Encryption.Type) || tpm2Usable {
		return nil
	}
	return []Problem{{"encryption.type", CodeEncryptionTPMUnavailable,
		fmt.Sprintf("encryption %q needs a TPM 2.0, and this machine has none usable", r.Encryption.Type)}}
}

// The three path rules below exist because each path is used by root, before
// or during disk work, by joining it to a host directory (#333):
//
//   - customMounts[].target is appended to targetMount, so "/../../etc"
//     would make fisherman mkdir and mount a partition over the host's /etc,
//     and a relative "etc" would land beside the target ("/mnt/fisherman-targetetc").
//   - targetMount is where the root filesystem is mounted and every
//     post-install write goes, so "/" would mount it over the running host.
//   - flatpakVarPath is joined under the installed root for the flatpak
//     copy, so "../../etc" would send that copy outside the target.
//
// They reject ".." components rather than every non-canonical spelling, so a
// recipe that was valid before (a trailing slash, say) still is.

// hasDotDot reports whether any "/"-separated component of p is "..".
func hasDotDot(p string) bool {
	for _, c := range strings.Split(p, "/") {
		if c == ".." {
			return true
		}
	}
	return false
}

// CheckCustomMountTarget applies the customMounts[i].target rule: "" and
// "swap" are the documented special values; anything else is an absolute
// path inside the target with no ".." component.
func CheckCustomMountTarget(i int, target string) []Problem {
	if target == "" || target == "swap" {
		return nil
	}
	if strings.HasPrefix(target, "/") && !hasDotDot(target) {
		return nil
	}
	return []Problem{{fmt.Sprintf("customMounts[%d].target", i), CodeCustomMountTargetInvalid,
		fmt.Sprintf("customMounts[%d]: target must be \"swap\" or an absolute path without \"..\" components, got %q", i, target)}}
}

// CheckCustomMountsLayout applies the rules a manual layout must meet as a
// whole, beyond "one entry targets /" (which Problems reports itself):
//
//   - An entry targets /boot/efi. fisherman installs UEFI boot loaders only;
//     bootc writes the loader to the ESP mounted there, and the install
//     reads that entry to find the EFI boot entry afterwards. Without it
//     bootc fails after the root is formatted, or installs a disk that
//     cannot boot.
//   - For GRUB (bootloader "" or "grub2") with an XFS root, an entry targets
//     /boot and is not XFS itself. GRUB cannot read modern XFS features
//     (nrext64, exchange, rmapbt), which is why the auto layout always
//     makes an ext4 /boot (the AGENTS.md invariant). systemd-boot reads the
//     kernel from the ESP and needs no /boot.
//
// The fstype of a partition kept as it is ("unformatted" or "") is not
// known here, so only a declared "xfs" counts.
func CheckCustomMountsLayout(r *Recipe) []Problem {
	var root, boot *CustomMount
	hasESP := false
	for i := range r.CustomMounts {
		switch filepath.Clean(r.CustomMounts[i].Target) {
		case "/":
			if r.CustomMounts[i].Target != "" {
				root = &r.CustomMounts[i]
			}
		case "/boot":
			boot = &r.CustomMounts[i]
		case "/boot/efi":
			hasESP = true
		}
	}
	var ps []Problem
	if !hasESP {
		ps = append(ps, Problem{"customMounts", CodeCustomMountsNoESP,
			"customMounts: no EFI system partition (target /boot/efi) specified"})
	}
	grub := r.Bootloader == "" || r.Bootloader == "grub2"
	if grub && root != nil && root.Fstype == "xfs" && (boot == nil || boot.Fstype == "xfs") {
		ps = append(ps, Problem{"customMounts", CodeCustomMountsNoBoot,
			"customMounts: GRUB cannot read an XFS root; add a separate /boot partition that is not XFS (ext4), or use bootloader \"systemd\""})
	}
	return ps
}

// CheckTargetMount applies the targetMount rule. Empty is valid (the default,
// /mnt/fisherman-target); otherwise an absolute path with no ".." component
// that is not the host root.
func CheckTargetMount(p string) []Problem {
	if p == "" {
		return nil
	}
	if strings.HasPrefix(p, "/") && !hasDotDot(p) && filepath.Clean(p) != "/" {
		return nil
	}
	return []Problem{{"targetMount", CodeTargetMountInvalid,
		fmt.Sprintf("targetMount must be an absolute path other than / without \"..\" components, got %q", p)}}
}

// CheckFlatpakVarPath applies the flatpakVarPath rule. Empty is valid
// (auto-detect); otherwise a path under the installed root with no ".."
// component.
func CheckFlatpakVarPath(p string) []Problem {
	if p == "" || !hasDotDot(p) {
		return nil
	}
	return []Problem{{"flatpakVarPath", CodeFlatpakVarPathInvalid,
		fmt.Sprintf("flatpakVarPath must stay inside the installed root (no \"..\" components), got %q", p)}}
}

// CheckAdditionalImageStores applies the additionalImageStores syntax rule:
// every entry is an absolute path with no ".." component, no ":" or ","
// and no control character. Each store is bind-mounted into the bootc
// container as `-v <store>:<store>:ro` and listed in a generated
// storage.conf, so a relative path, or one with ":", fails `podman run`
// after the disk is partitioned. Whether the directory exists is a host
// fact: the CLI checks it (additional_image_store_not_found).
func CheckAdditionalImageStores(stores []string) []Problem {
	var ps []Problem
	for i, store := range stores {
		bad := !strings.HasPrefix(store, "/") || hasDotDot(store) || strings.ContainsAny(store, ":,")
		for _, r := range store {
			bad = bad || unicode.IsControl(r)
		}
		if bad {
			ps = append(ps, Problem{fmt.Sprintf("additionalImageStores[%d]", i), CodeAdditionalImageStoreInvalid,
				fmt.Sprintf("additionalImageStores[%d] must be an absolute path without \"..\", \":\", \",\" or control characters, got %q", i, store)})
		}
	}
	return ps
}

// zfsPoolNamePattern is the subset of zpool(8)'s naming rule fisherman
// accepts: a letter first, then letters, digits, "_", "-" and ".". zpool
// also allows ":" and " ", which are left out because the name goes into a
// kernel command line (root=zfs:<pool>/…) and dataset paths. The name is an
// argument of `zpool create`; "-f" would be read as a flag.
var zfsPoolNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,62}$`)

// zfsReservedPoolPrefixes are names zpool refuses: vdev type keywords, and
// "c[0-9]" (a Solaris disk name).
var zfsReservedPoolPrefixes = []string{"mirror", "raidz", "draid", "spare"}

// CheckZFSPoolName applies the zfsPoolName rule. Empty is valid (the install
// uses "rpool").
func CheckZFSPoolName(name string) []Problem {
	if name == "" {
		return nil
	}
	ok := zfsPoolNamePattern.MatchString(name) && name != "log"
	for _, prefix := range zfsReservedPoolPrefixes {
		ok = ok && !strings.HasPrefix(name, prefix)
	}
	if ok && len(name) >= 2 && name[0] == 'c' && isDigit(rune(name[1])) {
		ok = false
	}
	if ok {
		return nil
	}
	return []Problem{{"zfsPoolName", CodeZFSPoolNameInvalid,
		fmt.Sprintf("zfsPoolName must start with a letter, contain only letters, digits, \"_\", \"-\" and \".\" (at most 63 characters), "+
			"and not be a name zpool reserves (mirror*, raidz*, draid*, spare*, log, c0-c9…), got %q", name)}}
}

// luksMapperNamePattern bounds the device-mapper name: it becomes the node
// /dev/mapper/<name> and the rd.luks.name=<uuid>=<name> kernel argument, so
// it has no "/", no space and no leading "." or "-" ("../sda" would point
// the install at another device node).
var luksMapperNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.+-]{0,62}$`)

// CheckLuksMapperName applies the luksMapperName rule. Empty is valid (the
// install uses "fisherman-root"). "control" is the device-mapper control
// node.
func CheckLuksMapperName(name string) []Problem {
	if name == "" || (luksMapperNamePattern.MatchString(name) && name != "control") {
		return nil
	}
	return []Problem{{"luksMapperName", CodeLuksMapperNameInvalid,
		fmt.Sprintf("luksMapperName must start with a letter or digit and contain only letters, digits, \"_\", \".\", \"+\" and \"-\" "+
			"(at most 63 characters), got %q", name)}}
}

// CheckOverride applies the override rule: every entry is one of Overrides.
// An unknown entry is refused rather than ignored, for the reason unknown
// keys are: the caller believes it switched a check off.
func CheckOverride(override []string) []Problem {
	for i, o := range override {
		if !contains(Overrides, o) {
			return []Problem{{fmt.Sprintf("override[%d]", i), CodeOverrideUnsupported,
				fmt.Sprintf("override accepts only \"too_small\" and \"uefi\", got %q", o)}}
		}
	}
	return nil
}

// CheckExperimental applies the experimental rule: every entry is one of
// Experimentals. An unknown entry is refused, as an unknown override is: the
// caller believes it accepted something.
func CheckExperimental(experimental []string) []Problem {
	for i, e := range experimental {
		if !contains(Experimentals, e) {
			return []Problem{{fmt.Sprintf("experimental[%d]", i), CodeExperimentalUnsupported,
				fmt.Sprintf("experimental accepts only \"zfs\", got %q", e)}}
		}
	}
	return nil
}

// CheckExperimentalFilesystem refuses filesystem "zfs" unless the recipe
// opts in with "experimental": ["zfs"]. The ZFS path has no VM test: no
// matrix entry installs it and boots the result (tuna-os/fisherman#413).
// The opt-in is the caller's statement that it knows.
func CheckExperimentalFilesystem(r *Recipe) []Problem {
	if r.Filesystem == "zfs" && !r.AcceptsExperimental(ExperimentalZFS) {
		return []Problem{{"filesystem", CodeZFSExperimental,
			"filesystem \"zfs\" is experimental: no VM test installs and boots it. " +
				"Add \"experimental\": [\"zfs\"] to the recipe to accept that, or use xfs, ext4 or btrfs"}}
	}
	return nil
}

// TargetFacts is what the machine says about one path a recipe names as a
// disk: probe.Target's verdict, in the recipe package's own terms so the
// rules stay free of host reads.
type TargetFacts struct {
	// Err: the verdict could not be reached (lsblk failed).
	Err error
	// Device is the path with symlinks resolved.
	Device string
	// Loop: a whole loop device (an image for another machine).
	Loop      bool
	SizeBytes int64
	MinBytes  int64
	// NotDisk, BootDisk, ReadOnly, TooSmall are probe's excluded reasons.
	NotDisk, BootDisk, ReadOnly, TooSmall bool
}

// CheckTarget applies the target-eligibility rules to `disk` and
// `varDisk.disk`. The install and the full `validate` run it; it is separate
// from Problems because it reads the machine. A manual layout (customMounts)
// names partitions, not a disk, so only the firmware rule applies to it.
//
//   - not a whole-disk block device, the disk the running system (or the
//     live medium) is on, or read-only: always an error. An install there
//     either destroys the system that is running it or fails after the
//     partition table is gone.
//   - smaller than the minimum, or a machine that did not boot through
//     UEFI: an error unless the recipe lists it in `override`. Neither
//     applies to a loop device, which is an image for another machine.
//
// disk and varDisk are nil when the recipe names none, or the path does not
// exist (Problems has reported that).
func (r *Recipe) CheckTarget(disk, varDisk *TargetFacts, uefi bool) []Problem {
	var ps []Problem
	manual := len(r.CustomMounts) > 0
	loop := false
	if disk != nil && !manual {
		loop = disk.Loop
		switch {
		case disk.Err != nil:
			ps = append(ps, Problem{"disk", CodeDiskUnverified,
				fmt.Sprintf("disk %s: cannot tell whether it is safe to install to: %v", r.Disk, disk.Err)})
		case disk.NotDisk:
			ps = append(ps, Problem{"disk", CodeDiskNotDisk,
				fmt.Sprintf("disk %s is not a whole disk (a partition, a file or another kind of device); name the disk itself, for example /dev/sda", r.Disk)})
		default:
			if disk.BootDisk {
				ps = append(ps, Problem{"disk", CodeDiskBootDisk,
					fmt.Sprintf("disk %s holds the system that is running now (or the live medium); installing to it would destroy it", r.Disk)})
			}
			if disk.ReadOnly {
				ps = append(ps, Problem{"disk", CodeDiskReadOnly,
					fmt.Sprintf("disk %s is read-only", r.Disk)})
			}
			if disk.TooSmall && !r.Overridden(OverrideTooSmall) {
				ps = append(ps, Problem{"disk", CodeDiskTooSmall,
					fmt.Sprintf("disk %s is %d bytes; the minimum is %d (add \"too_small\" to \"override\" to install anyway)",
						r.Disk, disk.SizeBytes, disk.MinBytes)})
			}
		}
	}
	if varDisk != nil && r.VarDisk != nil {
		switch {
		case varDisk.Err != nil:
			ps = append(ps, Problem{"varDisk.disk", CodeVarDiskUnverified,
				fmt.Sprintf("varDisk.disk %s: cannot tell whether it is safe to use: %v", r.VarDisk.Disk, varDisk.Err)})
		case varDisk.NotDisk:
			ps = append(ps, Problem{"varDisk.disk", CodeVarDiskNotDisk,
				fmt.Sprintf("varDisk.disk %s is not a whole disk (a partition, a file or another kind of device)", r.VarDisk.Disk)})
		default:
			if disk != nil && disk.Err == nil && varDisk.Device == disk.Device && r.VarDisk.Disk != r.Disk {
				// Two spellings of one device; Problems compares the strings.
				ps = append(ps, Problem{"varDisk.disk", CodeVarDiskSameAsDisk,
					"varDisk.disk must differ from the system disk"})
			}
			if varDisk.BootDisk {
				ps = append(ps, Problem{"varDisk.disk", CodeVarDiskBootDisk,
					fmt.Sprintf("varDisk.disk %s holds the system that is running now (or the live medium)", r.VarDisk.Disk)})
			}
			if varDisk.ReadOnly {
				ps = append(ps, Problem{"varDisk.disk", CodeVarDiskReadOnly,
					fmt.Sprintf("varDisk.disk %s is read-only", r.VarDisk.Disk)})
			}
		}
	}
	if !uefi && !loop && !r.Overridden(OverrideUEFI) {
		ps = append(ps, Problem{"", CodeFirmwareNotUEFI,
			"this machine did not boot through UEFI, and fisherman installs UEFI boot loaders only " +
				"(add \"uefi\" to \"override\" to install anyway, for a disk that another machine will boot)"})
	}
	return ps
}

func isLower(r rune) bool { return r >= 'a' && r <= 'z' }
func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func isASCIIAlnum(r rune) bool {
	return isLower(r) || (r >= 'A' && r <= 'Z') || isDigit(r)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
