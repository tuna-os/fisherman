package recipe

import (
	"fmt"
	"strings"
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
