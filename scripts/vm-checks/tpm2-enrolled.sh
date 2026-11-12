# shellcheck shell=bash
# Guest check: the first-boot TPM2 enrolment ran (fisherman
# internal/luks/firstboot.go). Sourced by scripts/boot-verify.sh
# (BOOTCREW_GUEST_CHECKS) in a subshell, with ssh_root set.
#
# After the first boot: the LUKS header has a systemd-tpm2 token, the
# single-use keyslot 31 is gone, and so is its key file. After a second boot
# with no passphrase typed, the same is still true, and SSH answering at all
# means the TPM unlocked the root.
set -u
fail=0
dev=$(ssh_root "lsblk -nrpo NAME,FSTYPE | awk '\$2 == \"crypto_LUKS\" { print \$1; exit }'" 2>/dev/null || true)
if [ -z "$dev" ]; then
  echo "❌ no LUKS device in the guest"; exit 1
fi
echo "  LUKS device: $dev"
# The unit runs early, but give it a minute: it retries while the TPM
# device appears. Wait for the unit to be done, not only for the token:
# the token appears the moment systemd-cryptenroll returns, and the
# single-use keyslot and key file go after that, in ExecStopPost (the
# first VM run read the header in between and saw both still there).
state=""
for _ in $(seq 1 "${TPM2_WAIT_ITERS:-30}"); do
  dump=$(ssh_root "cryptsetup luksDump $dev" 2>/dev/null || true)
  state=$(ssh_root "systemctl show -p ActiveState --value fisherman-tpm2-enroll.service" 2>/dev/null || true)
  if echo "$dump" | grep -q 'systemd-tpm2'; then
    case "$state" in inactive|failed) break ;; esac
  fi
  sleep 2
done
echo "  fisherman-tpm2-enroll.service: ${state:-unknown}"
ssh_root "journalctl -b -u fisherman-tpm2-enroll.service --no-pager" 2>/dev/null | tail -20 | sed 's/^/  /' || true
if echo "$dump" | grep -q 'systemd-tpm2'; then
  echo "✅ the LUKS header has a systemd-tpm2 token"
else
  echo "❌ no systemd-tpm2 token in the LUKS header"; fail=1
fi
if echo "$dump" | grep -Eq '^[[:space:]]*31: luks2'; then
  echo "❌ the single-use keyslot 31 is still there"; fail=1
else
  echo "✅ the single-use keyslot 31 is gone"
fi
if ssh_root 'test -e /etc/fisherman/tpm2-enroll.key' 2>/dev/null; then
  echo "❌ /etc/fisherman/tpm2-enroll.key is still there"; fail=1
else
  echo "✅ the single-use key file is gone"
fi
exit "$fail"
