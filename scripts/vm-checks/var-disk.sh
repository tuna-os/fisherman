# shellcheck shell=bash
# Guest check: /var is the recipe's varDisk. fisherman formats it XFS with
# the label "var" and writes an fstab line (docs/VALIDATE.md, "Mounts after
# the install"). Sourced by scripts/boot-verify.sh (BOOTCREW_GUEST_CHECKS)
# in a subshell, with ssh_root set; boot-verify attaches the disk as the
# VM's second drive (BOOTCREW_EXTRA_DRIVE).
set -u
fail=0
info=$(ssh_root 'src=$(findmnt -no SOURCE /var); echo "$src $(findmnt -no FSTYPE /var) $(lsblk -no LABEL "$src" 2>/dev/null)"; findmnt -no TARGET -T /var/home; grep -E "[[:space:]]/var[[:space:]]" /etc/fstab' 2>/dev/null || true)
echo "$info" | sed 's/^/  /'
read -r src fstype label <<<"$(echo "$info" | sed -n 1p)"
if [ "${fstype:-}" = "xfs" ] && [ "${label:-}" = "var" ]; then
  echo "✅ /var is the varDisk ($src, xfs, label var)"
else
  echo "❌ /var is not the varDisk (source ${src:-?}, type ${fstype:-?}, label ${label:-?})"; fail=1
fi
if [ "$(echo "$info" | sed -n 2p)" = "/var" ]; then
  echo "✅ /var/home is on it"
else
  echo "❌ /var/home is not on the varDisk"; fail=1
fi
if echo "$info" | sed -n '3,$p' | grep -q 'nofail'; then
  echo "✅ fstab has the nofail /var line"
else
  echo "❌ no fisherman /var line in /etc/fstab"; fail=1
fi
exit "$fail"
