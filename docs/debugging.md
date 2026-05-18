# Debugging fisherman installs

This guide covers how to diagnose failures at each stage of the fisherman
install pipeline, with particular focus on LUKS installs and post-install boot
problems that don't surface until the installed disk is first booted.

---

## Validate a recipe without installing

Before running a full install, check that your recipe parses and all referenced
tools are available:

```bash
sudo fisherman validate recipe.json
```

This exits 0 if the recipe is valid. It does **not** touch the disk.

---

## Capture full output

fisherman writes progress to stdout and errors to stderr. Redirect both to a
file for post-mortem analysis:

```bash
sudo fisherman recipe.json 2>&1 | tee /tmp/fisherman-install.log
```

The progress lines look like:

```
[fisherman] version: 0.x.y
[step 1/9] Partitioning disk ...
[step 6/9] Installing OS ...
  Injected rd.luks.name into 1 boot entry
  Added Plymouth boot args to 1 loader entry
[step 9/9] Finalizing ...
```

---

## Log locations

| Log | Where | What's in it |
|---|---|---|
| fisherman output | stdout/stderr (redirect it) | Step progress, error messages |
| bootc installer | `/var/log/bootc-installer.log` | Output from `bootc install to-filesystem` run inside the podman container |
| system journal | `journalctl -b` | Everything else — podman, udev, cryptsetup |
| scratch directory | `/var/fisherman-tmp/` (or `/mnt/fisherman-target/.fisherman-scratch/` on live ISOs) | Temporary container layers and pull cache |

To grab all three at once after a failure:

```bash
sudo cat /tmp/fisherman-install.log
sudo cat /var/log/bootc-installer.log
sudo journalctl -b --no-pager -n 200
```

---

## Inspecting BLS boot entries after install

fisherman injects `rd.luks.name=` and Plymouth args into the BLS entries at
step 9. If the installed system fails to boot, read the entries to verify:

```bash
# 3-partition layout (GRUB): entries are on the ext4 /boot partition
sudo mount /dev/sda2 /mnt
cat /mnt/loader/entries/*.conf

# 2-partition layout (systemd-boot / composefs): entries are on the EFI partition
sudo mount /dev/sda1 /mnt
cat /mnt/loader/entries/*.conf
# or
cat /mnt/EFI/loader/entries/*.conf
```

A correct LUKS entry `options` line looks like:

```
options root=UUID=<root-fs-uuid> rd.luks.name=<luks-uuid>=root rhgb quiet
```

**Important:** fisherman injects `rd.luks.name=<UUID>=root` (not
`rd.luks.uuid=`). The `=root` suffix maps the LUKS container to
`/dev/mapper/root`, which is required for `systemd-gpt-auto-generator` to
locate the root filesystem. Using `rd.luks.uuid` maps to
`/dev/mapper/luks-<UUID>` instead — the generator can't find that, causing a
~90 s hang before dropping to an emergency shell.

---

## Debugging boot failures in QEMU

When you need to inspect a freshly-installed disk without access to physical
serial hardware, boot it in QEMU with the serial console redirected to a file.

### Step 1 — Add `console=ttyS0` to the BLS entry

fisherman doesn't inject `console=ttyS0` because it's a VM/debugging artifact
that most production hardware doesn't need. Add it manually after install:

```bash
# Mount the correct partition (see above)
sudo mount -t vfat /dev/sda1 /mnt         # EFI / systemd-boot layout
# or
sudo mount /dev/sda2 /mnt                 # GRUB / 3-partition layout

sudo sed -i 's|^options .*|& console=tty0 console=ttyS0|' \
    /mnt/loader/entries/*.conf

sudo cat /mnt/loader/entries/*.conf       # verify
sudo umount /mnt
```

Using **both** `console=tty0 console=ttyS0` means output goes to both the
framebuffer (tty0) and the serial port (ttyS0). Plymouth shows the LUKS
passphrase prompt on whichever console is active.

### Step 2 — Boot in QEMU

```bash
DISK=/path/to/installed.img   # or /dev/loop0, etc.
SERIAL_LOG=/tmp/installed-serial.log

sudo qemu-system-x86_64 \
    -machine q35 -m 4096 -smp 2 \
    -accel kvm -cpu host \
    -drive if=pflash,format=raw,readonly=on,\
file=/usr/share/edk2/ovmf/OVMF_CODE.fd \
    -drive if=pflash,format=raw,\
file=/path/to/OVMF_VARS.fd \
    -drive if=none,id=disk,file=${DISK},format=qcow2 \
    -device virtio-blk-pci,drive=disk \
    -serial "file:${SERIAL_LOG}" \
    -monitor unix:/tmp/qemu-monitor.sock,server,nowait \
    -display none &

# Watch the serial log live
tail -f "$SERIAL_LOG"
```

With `console=ttyS0` in the cmdline you will see the full kernel log, the
dracut initqueue progress, and — for LUKS installs — the passphrase prompt:

```
Please enter current passphrase for disk /dev/vda2 (root):
```

### Step 3 — Send the LUKS passphrase via the QEMU monitor

Plymouth intercepts keyboard input, so you can't type the passphrase into the
serial terminal directly. Use the QEMU HMP monitor to inject keystrokes:

```bash
# Open the monitor
socat - UNIX-CONNECT:/tmp/qemu-monitor.sock

# Inside the monitor, type:
sendkey p
sendkey a
sendkey s
sendkey s
sendkey w
sendkey o
sendkey r
sendkey d
sendkey ret
```

Or in a script:

```bash
send_key() {
    echo "sendkey $1" | socat - UNIX-CONNECT:/tmp/qemu-monitor.sock
}

for char in p a s s w o r d; do send_key "$char"; done
send_key ret
```

---

## Common failure modes

| Symptom | Likely cause | Where to look |
|---|---|---|
| Boot hangs ~90 s then drops to emergency shell | `rd.luks.uuid=` used instead of `rd.luks.name=` | BLS `options` line — check for `=root` suffix |
| LUKS passphrase prompt never appears | `console=ttyS0` missing; Plymouth only renders on framebuffer | Add `console=tty0 console=ttyS0` to BLS entry |
| "Failed to find root device" in dracut | LUKS arg missing entirely | Verify `rd.luks.name=` is present; check fisherman log for "Injected rd.luks.name" |
| `bootc install` fails inside the container | Image pull / OCI store issue | Check `/var/log/bootc-installer.log` |
| Scratch dir full during pull | `/var` too small on live ISO | fisherman auto-relocates scratch to the target disk; check journalctl for "space-constrained" |
| BLS entry has 0 entries patched | Entries not found at expected paths | Check both `boot/loader/entries/` and `boot/efi/loader/entries/` |
| Boot tries wrong device, never reaches disk | UEFI boot order wrong | Add `-boot order=d` to QEMU, or check OVMF VARS file is a fresh copy |
