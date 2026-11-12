package recipe

// Derived install state. The install path (cmd/fisherman) asks a recipe
// these questions instead of recomputing them from the raw fields, so each
// answer has one definition (#317). None of them reads the machine, and
// none of them validates: Problems refuses the combinations the install
// cannot do (an encrypted manual layout, an encrypted ZFS root, an unknown
// encryption type), and these methods assume a recipe that passed it.
//
// The invariants the methods rely on, each enforced by Problems and pinned
// by state_test.go:
//   - HasTPM2 implies HasEncryption (EncryptionChoices: every NeedsTPM2
//     type is Encrypted).
//   - A manual layout has no encryption (encryption_unsupported_on_manual_layout),
//     so it has no TPM2 enrolment either.
//   - ZFS has no encryption (encryption_unsupported_on_zfs).

// HasEncryption reports whether the recipe asks for an encrypted root: any
// encryption.type other than "" and "none". An unknown type counts as
// encrypted, so it is never silently downgraded; Problems refuses it.
func (r *Recipe) HasEncryption() bool {
	return r.Encryption.Type != "" && r.Encryption.Type != "none"
}

// HasTPM2 reports whether the encryption type enrols a TPM 2.0
// ("tpm2-luks", "tpm2-luks-passphrase").
func (r *Recipe) HasTPM2() bool {
	return NeedsTPM2(r.Encryption.Type)
}

// IsManualLayout reports whether the recipe names its own partitions
// (customMounts). A manual layout skips the auto-partition steps: disk,
// filesystem and btrfsSubvolumes do not apply.
func (r *Recipe) IsManualLayout() bool {
	return len(r.CustomMounts) > 0
}

// IsZFS reports whether the auto-partitioned root is a ZFS pool.
func (r *Recipe) IsZFS() bool {
	return r.Filesystem == "zfs"
}

// UsesSystemdBoot reports whether the install uses the systemd-boot
// layout: two partitions (ESP + root), the kernel on the ESP, no ext4
// /boot. bootloader "systemd" selects it, and ZFS always uses it.
func (r *Recipe) UsesSystemdBoot() bool {
	return r.Bootloader == "systemd" || r.IsZFS()
}

// UsesComposeFs reports whether bootc installs with the composefs backend:
// composeFsBackend, which ZFS forces on.
func (r *Recipe) UsesComposeFs() bool {
	return r.ComposeFsBackend || r.IsZFS()
}

// HasVarDisk reports whether a separate disk is mounted at /var.
func (r *Recipe) HasVarDisk() bool {
	return r.VarDisk != nil
}

// FormatsVarDisk reports whether the /var disk is wiped and formatted
// (varDisk without keepExisting).
func (r *Recipe) FormatsVarDisk() bool {
	return r.HasVarDisk() && !r.VarDisk.KeepExisting
}

// EncryptsRoot reports whether the install runs the LUKS step: an
// encrypted root on the auto-partition path. A manual layout never does.
func (r *Recipe) EncryptsRoot() bool {
	return r.HasEncryption() && !r.IsManualLayout()
}

// EnrollsTPM2 reports whether the install runs the TPM2 enrolment step. It
// needs the LUKS root of the auto-partition path.
func (r *Recipe) EnrollsTPM2() bool {
	return r.HasTPM2() && !r.IsManualLayout()
}
