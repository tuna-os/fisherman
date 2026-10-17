# Troubleshooting Guide

If a fisherman installation fails, this guide will help you diagnose the problem and recover.

## Before Installation: Validation

The first step is to validate your recipe before running the install.

### Validate the recipe

```bash
fisherman validate <recipe.json>
```

This gives a human-readable summary of any problems. If there are issues, fix them before proceeding.

For more detailed diagnostics, use:

```bash
fisherman validate --json <recipe.json>
```

This returns structured errors with stable error codes. See `docs/VALIDATE.md` for the complete reference and fixes for each code.

### Check system readiness

Before installing, probe your system to verify:

```bash
fisherman probe --json
```

This outputs a JSON object with:

| Field | Meaning |
|---|---|
| `disk` | Available block devices and their sizes |
| `tpm` | TPM 2.0 availability (needed for TPM-based encryption) |
| `system.ram` | Memory available (fisherman requires at least 2 GiB) |
| `system.cpu` | Number of CPUs |
| `system.uefi` | Whether the system booted in UEFI mode |
| `live.is_live` | Whether running from live media (allows empty `image` field) |
| `offline.stores` | Paths to offline image stores (if using air-gapped installs) |

**Example validation workflow:**

```bash
# 1. Check system
fisherman probe --json | jq .

# 2. Verify disk choice
fisherman probe --json | jq '.disk[] | select(.path == "/dev/sda")'

# 3. Validate recipe
fisherman validate --json recipe.json

# 4. If validation passes, run install
sudo fisherman recipe.json
```

## During Installation: Reading Progress Output

fisherman emits newline-delimited JSON on stdout describing each installation step.

```bash
sudo fisherman recipe.json 2>&1 | jq .
```

Each line is a JSON object with:

```json
{
  "step": 1,
  "status": "running" | "done" | "error",
  "action": "partition",
  "message": "Partitioning /dev/sda"
}
```

The 9 steps are:

1. **partition** — Create partition layout
2. **format-efi** — Format EFI partition as FAT32
3. **format-boot** — Format /boot partition (GRUB images only)
4. **luks-setup** — Set up LUKS encryption (if requested)
5. **format-root** — Format root filesystem
6. **mount** — Mount all filesystems
7. **bootc-install** — Run bootc install to-filesystem
8. **flatpaks** — Copy Flatpaks from host to target
9. **finalize** — Write configuration and finalize

**If installation hangs:** Most hangs occur during step 7 (bootc-install) if the container image is large or the network is slow. Wait at least 5 minutes before interrupting.

## Installation Failed: Diagnosis and Recovery

### Read the error message

fisherman exits with status 1 on failure. The last JSON object on stdout describes the failure:

```json
{
  "step": 7,
  "status": "error",
  "action": "bootc-install",
  "message": "podman run failed: image not found"
}
```

Common error messages and fixes:

| Error | Cause | Fix |
|---|---|---|
| `recipe validation failed` | Recipe has invalid fields | Run `fisherman validate --json recipe.json` to see the specific error |
| `image not found` | Container image pull failed | Check the `image` field, network connectivity, and registry credentials |
| `disk /dev/X not found` | Block device doesn't exist | Verify the disk path with `lsblk` |
| `not enough space on disk` | Disk is too small | Use a larger disk, or resize partitions in the recipe |
| `permission denied` | fisherman was not run as root | Use `sudo fisherman ...` |
| `LUKS passphrase failed` | Passphrase is incorrect | Check the `encryption.passphrase` field |
| `mount failed` | Partition could not be mounted | The filesystem may be corrupted; try recovery steps below |

### Check system state after failure

```bash
# List block devices and partitions
lsblk /dev/sdX

# Check for active LUKS mappings
sudo dmsetup ls

# Look for mounted filesystems
mount | grep fisherman

# Check system logs for errors
sudo journalctl -xe
```

### Cleanup after failure

If fisherman exited with an error, it should have cleaned up automatically. However, you can verify and manually clean if needed:

#### Unmount all fisherman targets

```bash
sudo umount -R /mnt/fisherman-target 2>/dev/null || true
```

#### Close LUKS mappings

```bash
sudo dmsetup remove fisherman-root 2>/dev/null || true
```

#### Remove scratch directories

```bash
sudo rm -rf /var/fisherman-tmp/.fisherman-scratch
sudo rm -rf /.fisherman-scratch
```

#### Verify cleanup

```bash
# Check no fisherman mounts remain
mount | grep -i fisherman
# Should return empty

# Check no fisherman LUKS mappings
sudo dmsetup ls | grep fisherman
# Should return empty
```

## Data Recovery After Failure

### Unencrypted disks

If the install failed before step 7 (bootc-install), the target disk partitions are formatted but empty. Recover the disk by:

1. Back up any data you need from other partitions
2. Run a new fisherman install with the corrected recipe
3. The install will overwrite the empty partitions

### Encrypted disks (LUKS)

If installation failed with LUKS encryption active:

1. **Check if the LUKS volume is open:**
   ```bash
   sudo dmsetup ls
   ```
   If `fisherman-root` is listed, it is still open.

2. **If it's open, close it:**
   ```bash
   sudo dmsetup remove fisherman-root
   ```

3. **If you have the LUKS passphrase and want to recover the partition:**
   ```bash
   # Verify the partition exists
   sudo lsblk /dev/sdX
   
   # Open the LUKS volume manually
   sudo cryptsetup luksOpen /dev/sdX3 fisherman-root
   # (Partition number may vary; use the root partition from lsblk)
   
   # Mount it
   sudo mount /dev/mapper/fisherman-root /mnt
   
   # Recover files
   cp -r /mnt/important-files /backup/
   
   # Unmount and close
   sudo umount /mnt
   sudo cryptsetup luksClose fisherman-root
   ```

4. **To reuse the disk,** run a new fisherman install. It will overwrite the LUKS volume.

## Common Scenarios

### "Image not found" during step 7

This usually means:
- The image URL is incorrect or inaccessible
- Network is not available
- Container registry credentials are missing

**Fix:**
```bash
# Verify network
ping registry.example.com

# Test image pull manually
podman pull <image-url>

# If credentials are needed, log in first
podman login <registry>

# Then retry fisherman
```

### Installation interrupted (Ctrl+C)

If you interrupted fisherman during installation:

1. **The disk is left in an intermediate state.** Do not reboot yet.
2. **Run manual cleanup:**
   ```bash
   sudo umount -R /mnt/fisherman-target 2>/dev/null || true
   sudo dmsetup remove fisherman-root 2>/dev/null || true
   ```
3. **Verify the disk is unmounted:**
   ```bash
   lsblk /dev/sdX
   ```
   Partitions should show no mounts in the `MOUNTPOINTS` column.
4. **Run fisherman again** with the same or different recipe.

### Partition table is corrupted or unrecognizable

If `lsblk` shows strange partition names or no partitions:

1. **Back up any data** from other disks to a safe location
2. **Wipe the disk** (this will destroy all data):
   ```bash
   sudo dd if=/dev/zero of=/dev/sdX bs=1M count=1000
   ```
3. **Run a new fisherman install**

### Running out of disk space during step 7

If `bootc install` fails due to insufficient space:

1. The recipe's `filesystem` choice may be too large, or
2. The disk is genuinely too small for the image

**Fix:**
- Use a larger disk, or
- Disable Flatpaks in the recipe (step 8 will not run)
- Use a smaller base image

### TPM not detected but recipe requires TPM

If you requested `tpm2-luks` encryption and get `encryption_tpm_unavailable`:

1. **Verify TPM is present:**
   ```bash
   sudo /usr/sbin/tpm2_getcap handles-persistent
   ```
   If this fails, TPM is not available.

2. **If TPM is missing:**
   - Enable TPM in system BIOS/UEFI firmware, or
   - Change recipe to `encryption.type: luks-passphrase` instead

3. **If TPM is present but not detected,** it may need enrollment:
   ```bash
   sudo systemd-boot-check
   ```

## Post-Install Verification

After a successful install (exit 0), verify the system:

### Check the boot environment

```bash
# For systemd-boot
bootctl status

# For GRUB
grub-editenv list
```

### Verify deployed image

```bash
# After first boot into the installed system:
bootc status
```

### Test LUKS unlock (encrypted installs only)

Reboot the system and verify:
1. Plymouth prompt appears asking for passphrase
2. Entering the correct passphrase unlocks the disk
3. System boots normally

### Check storage space

```bash
df -h /
```

Verify `/` has the expected size and is not 100% full.

## Integrator Troubleshooting

If you are integrating fisherman into an installer or CI/CD system:

### Log JSON events for debugging

```bash
fisherman recipe.json 2>&1 | tee install.log | jq .
```

### Extract the failed step

```bash
jq 'select(.status == "error") | {step, action, message}' install.log
```

### Validate recipes programmatically

```bash
fisherman validate --json recipe.json | jq '.problems[] | .code' | sort | uniq
```

### Run fisherman with verbose logging

Fisherman logs to stderr; capture it:

```bash
fisherman recipe.json 2>&1 | grep -E 'error|failed|ERROR'
```

## Getting Help

If you cannot resolve the problem:

1. **Collect diagnostic information:**
   ```bash
   fisherman probe --json > probe.json
   fisherman validate --json recipe.json > validation.json
   sudo journalctl -xe > journal.log
   lsblk > lsblk.txt
   ```

2. **Create a minimal reproducible recipe** (change sensitive values)

3. **File an issue on GitHub** with:
   - The error message from fisherman
   - The output of `fisherman probe --json`
   - The (sanitized) recipe
   - System information (distro, kernel version)
   - Steps to reproduce

---

For more details on specific components:
- **Recipe validation:** See `docs/VALIDATE.md` and the error code reference
- **JSON progress format:** See `docs/JSON-PROGRESS-FORMAT.md`
- **System probing:** See `docs/PROBE.md`
