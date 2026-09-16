#!/usr/bin/env python3
"""
Automate LUKS passphrase entry for a VM booting with Plymouth.

Plymouth renders the passphrase prompt on the EFI framebuffer (not the serial
console by default), so we use two detection strategies:

  Primary:  serial log contains "Please enter passphrase for disk"
            (requires console=ttyS0 in kernel cmdline — added by bootcrew-ci-test)
  Fallback: QEMU screendump MD5 hash stabilises after the framebuffer shows
            any non-zero content (Plymouth passphrase prompt is a static screen)

Passphrase keystrokes are injected via QEMU HMP sendkey over the monitor socket.

Usage:
  luks-unlock.py qemu <monitor-sock> <passphrase> <serial-log>

Exit codes:
  0 — passphrase sent and boot succeeded (SSH will become available shortly)
  1 — Plymouth prompt never appeared (timeout)
  2 — passphrase sent but boot produced an emergency shell
"""

import hashlib
import os
import subprocess
import sys
import time


POLL_INTERVAL   = 3    # seconds between screendump polls
PLYMOUTH_WAIT   = 10   # seconds to wait after detecting prompt before sending keys
PROMPT_DEADLINE = 300  # seconds to wait for Plymouth to appear
BOOT_DEADLINE   = 300  # seconds to wait for boot to succeed after passphrase


# ── QEMU monitor helpers ──────────────────────────────────────────────────────

def qemu_screendump(sock: str, path: str) -> tuple:
    """Return (brightness, md5) for the screendump, or (-1, '') on error.

    brightness — average sampled pixel value (0–255).
      Uninitialized framebuffer: 0.0
      OVMF/bootloader:           ~0.5–5
      Plymouth prompt:           ~0.5–2, STABLE
      GDM/GNOME:                 higher (~2.4+)

    md5 — hash of the full PPM file; used to detect display stability.
    """
    subprocess.run(
        ["socat", "-", f"UNIX-CONNECT:{sock}"],
        input=f"screendump {path}\n".encode(),
        capture_output=True,
        timeout=5,
    )
    time.sleep(0.5)
    try:
        data = open(path, "rb").read()
    except OSError:
        return -1, ""
    md5 = hashlib.md5(data).hexdigest()
    try:
        header_end = data.index(b"255\n") + 4
    except ValueError:
        return -1, ""
    pixel_data = data[header_end:]
    if not pixel_data:
        return -1, ""
    sampled = pixel_data[::100]
    return sum(sampled) / len(sampled), md5


def qemu_send_passphrase(sock: str, passphrase: str):
    key_map = {c: c for c in "abcdefghijklmnopqrstuvwxyz0123456789"}
    key_map["-"] = "minus"
    key_map["_"] = "shift-minus"
    key_map[" "] = "spc"

    def _sendkey(key: str):
        subprocess.run(
            ["socat", "-", f"UNIX-CONNECT:{sock}"],
            input=f"sendkey {key}\n".encode(),
            capture_output=True,
            timeout=5,
        )

    for ch in passphrase:
        key = key_map.get(ch)
        if key is None:
            print(f"[luks-unlock] WARNING: no key mapping for {ch!r}", file=sys.stderr)
            continue
        _sendkey(key)
        time.sleep(0.1)
    _sendkey("ret")


def check_serial(serial_log: str) -> str:
    """Return 'plymouth', 'gdm', 'emergency', or '' if no marker yet."""
    import re
    try:
        raw = open(serial_log).read()
    except OSError:
        return ""
    content = re.sub(r'\x1b\[[0-9;]*[A-Za-z]', '', raw)
    content_flat = ' '.join(content.split())
    if "emergency mode" in content or "emergency shell" in content:
        return "emergency"
    if "Started gnome-initial-setup" in content_flat:
        return "gnome-initial-setup"
    if "Started gdm.service" in content_flat or "Started GNOME Display Manager" in content_flat:
        return "gdm"
    if "Please enter passphrase for disk" in raw:
        return "plymouth"
    # systemd-boot images reaching multi-user.target via serial
    if "Reached target" in content_flat and ("multi-user" in content_flat or "graphical" in content_flat):
        return "gdm"
    if "login:" in raw:
        return "gdm"
    return ""


# ── Main QEMU mode ────────────────────────────────────────────────────────────

def run_qemu(monitor_sock: str, passphrase: str, serial_log: str):
    snap = "/tmp/luks-unlock-snap.ppm"

    CONTENT_THRESHOLD = 0.5
    STABLE_POLLS      = 2

    print(f"[luks-unlock] watching monitor {monitor_sock} and serial {serial_log}...",
          flush=True)
    deadline = time.time() + PROMPT_DEADLINE

    had_content  = False
    stable_count = 0
    prev_hash    = ""

    while time.time() < deadline:
        # Primary: serial log detection (reliable when console=ttyS0 is set).
        serial_result = check_serial(serial_log)
        if serial_result == "plymouth":
            print("[luks-unlock] Plymouth prompt detected via serial log", flush=True)
            _save_snap(monitor_sock, snap, "/tmp/luks-screenshot-plymouth.ppm")
            print(f"[luks-unlock] waiting {PLYMOUTH_WAIT}s for Plymouth to settle...",
                  flush=True)
            time.sleep(PLYMOUTH_WAIT)
            print("[luks-unlock] sending passphrase via QEMU monitor sendkey...", flush=True)
            qemu_send_passphrase(monitor_sock, passphrase)
            print("[luks-unlock] passphrase sent", flush=True)
            break

        # Fallback: framebuffer stability.
        brightness, md5 = qemu_screendump(monitor_sock, snap)
        print(f"[luks-unlock] screendump brightness={brightness:.2f} hash={md5[:8]}",
              flush=True)

        if brightness < 0:
            stable_count = 0
            time.sleep(POLL_INTERVAL)
            continue

        if not had_content and brightness > CONTENT_THRESHOLD:
            had_content = True
            print(f"[luks-unlock] VM is rendering (brightness {brightness:.2f})", flush=True)

        if had_content:
            if md5 == prev_hash:
                stable_count += 1
            else:
                stable_count = 0
            prev_hash = md5

        if had_content and stable_count >= STABLE_POLLS:
            print(
                f"[luks-unlock] framebuffer stable {stable_count} polls"
                f" (brightness={brightness:.2f}) — treating as Plymouth prompt",
                flush=True,
            )
            _save_snap(monitor_sock, snap, "/tmp/luks-screenshot-plymouth.ppm")
            print(f"[luks-unlock] waiting {PLYMOUTH_WAIT}s for Plymouth to settle...",
                  flush=True)
            time.sleep(PLYMOUTH_WAIT)
            print("[luks-unlock] sending passphrase via QEMU monitor sendkey...", flush=True)
            qemu_send_passphrase(monitor_sock, passphrase)
            print("[luks-unlock] passphrase sent", flush=True)
            break

        time.sleep(POLL_INTERVAL)
    else:
        print("[luks-unlock] ERROR: Plymouth prompt never appeared within timeout",
              file=sys.stderr)
        sys.exit(1)

    # Watch for boot success.
    deadline     = time.time() + BOOT_DEADLINE
    passphrase_h = prev_hash
    screen_changed   = False
    gnome_stable_cnt = 0

    while time.time() < deadline:
        result = check_serial(serial_log)
        if result == "emergency":
            print("[luks-unlock] RESULT: emergency shell — LUKS passphrase rejected or boot failed",
                  flush=True)
            _save_snap(monitor_sock, snap, "/tmp/luks-screenshot-final.ppm")
            sys.exit(2)

        brightness, md5 = qemu_screendump(monitor_sock, snap)
        print(f"[luks-unlock] post-passphrase brightness={brightness:.2f} hash={md5[:8]}",
              flush=True)

        if md5 != passphrase_h and not screen_changed:
            screen_changed = True
            print("[luks-unlock] screen changed — LUKS accepted, boot proceeding", flush=True)

        if result in ("gnome-initial-setup", "gdm"):
            print(f"[luks-unlock] RESULT: boot succeeded ({result} via serial)", flush=True)
            _save_snap(monitor_sock, snap, "/tmp/luks-screenshot-final.ppm")
            sys.exit(0)

        # Framebuffer fallback boot-success check.
        GNOME_THRESHOLD    = 1.8
        GNOME_STABLE_POLLS = 1
        if md5 == prev_hash:
            gnome_stable_cnt += 1
        else:
            gnome_stable_cnt = 0

        if screen_changed and gnome_stable_cnt >= GNOME_STABLE_POLLS:
            success = brightness > GNOME_THRESHOLD
            print(
                f"[luks-unlock] RESULT: {'boot succeeded' if success else 'emergency shell suspected'}"
                f" (framebuffer brightness={brightness:.2f})",
                flush=True,
            )
            _save_snap(monitor_sock, snap, "/tmp/luks-screenshot-final.ppm")
            sys.exit(0 if success else 2)

        prev_hash = md5
        time.sleep(5)

    print("[luks-unlock] WARNING: passphrase sent but boot did not complete within timeout",
          file=sys.stderr)
    sys.exit(2)


def _save_snap(sock: str, src: str, dst: str):
    try:
        import shutil
        shutil.copy2(src, dst)
    except OSError:
        pass


def main():
    if len(sys.argv) < 5 or sys.argv[1] != "qemu":
        print(__doc__, file=sys.stderr)
        sys.exit(1)
    run_qemu(sys.argv[2], sys.argv[3], sys.argv[4])


if __name__ == "__main__":
    main()
