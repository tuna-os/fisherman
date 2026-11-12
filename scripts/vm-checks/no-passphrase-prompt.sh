# shellcheck shell=bash
# Guest check: this boot asked for no LUKS passphrase. Run on the second
# boot of a tpm2-* entry (#418, #421), which types nothing: SSH answering
# already shows the root was unlocked, and this check shows the TPM2 token
# unlocked it, not a prompt that something answered. Sourced by
# scripts/boot-verify.sh (BOOTCREW_GUEST_CHECKS) in a subshell; reads the
# boot's serial console log, SERIAL_LOG, which boot-verify.sh sets.
# systemd-cryptsetup writes its prompt there ("Please enter passphrase for
# disk ..."; luks-unlock.py waits for the same text).
set -u
log="${SERIAL_LOG:-}"
if [ -z "$log" ] || [ ! -r "$log" ]; then
  echo "❌ no serial log to read (SERIAL_LOG='${log}')"; exit 1
fi
if grep -aiq 'enter passphrase for' "$log"; then
  echo "❌ this boot asked for a LUKS passphrase:"
  grep -ai 'enter passphrase for' "$log" | head -3 | sed 's/^/  /'
  exit 1
fi
echo "✅ no passphrase prompt on this boot: the TPM2 token unlocked the root"
