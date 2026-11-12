# shellcheck shell=bash
# Guest check: a `btrfsSubvolumes` install put the system in the @ subvolume
# and the booted system mounts it as the recipe says (README "btrfsSubvolumes",
# internal/disk/format.go SetupBtrfsSubvolumes, recipe.RootMountOptions).
# Sourced by scripts/boot-verify.sh (BOOTCREW_GUEST_CHECKS) in a subshell,
# with ssh_root set.
#
#   - /sysroot (the physical root on both backends; /var when /sysroot is
#     not a mount point) is btrfs, from [/@...], mounted with subvol=/@ and
#     compress=zstd:1;
#   - the kernel command line carries rootflags=subvol=@,compress=zstd:1;
#   - /var, and so /var/home, lies inside @;
#   - @ is the only subvolume at the top level: no @home, no @snapshots
#     (#416). Checked when the image ships the btrfs tool; nested
#     subvolumes that systemd-tmpfiles makes inside @ are allowed.
set -u
: "${BTRFS_ROOTFLAGS:=subvol=@,compress=zstd:1}"
fail=0
info=$(ssh_root 'm=/sysroot; findmnt -n /sysroot >/dev/null 2>&1 || m=/var
  echo "root: $m $(findmnt -no SOURCE,FSTYPE,OPTIONS "$m" | head -n 1)"
  echo "var: $(findmnt -no SOURCE /var | head -n 1)"
  echo "cmdline: $(cat /proc/cmdline)"
  if command -v btrfs >/dev/null 2>&1; then
    btrfs subvolume list "$m" | sed "s/^/subvol: /"; echo "listed: yes"
  fi' 2>/dev/null || true)
echo "$info" | sed 's/^/  /'

read -r mnt src fstype opts <<<"$(echo "$info" | sed -n 's/^root: //p')"
if [ "${fstype:-}" = btrfs ] && [[ "${src:-}" =~ \[/@(/[^]]*)?\]$ ]] \
  && echo ",${opts:-}," | grep -q ',subvol=/@,' && echo ",${opts:-}," | grep -q ',compress=zstd:1,'; then
  echo "✅ $mnt is on the @ subvolume ($src, subvol=/@, compress=zstd:1)"
else
  echo "❌ ${mnt:-/sysroot} is not on the @ subvolume (source ${src:-?}, type ${fstype:-?}, options ${opts:-?})"; fail=1
fi

if echo "$info" | sed -n 's/^cmdline: //p' | tr ' ' '\n' | grep -qxF "rootflags=$BTRFS_ROOTFLAGS"; then
  echo "✅ the kernel command line has rootflags=$BTRFS_ROOTFLAGS"
else
  echo "❌ no rootflags=$BTRFS_ROOTFLAGS on the kernel command line"; fail=1
fi

case "$(echo "$info" | sed -n 's/^var: //p')" in
  *"[/@/"*) echo "✅ /var lies inside @" ;;
  *) echo "❌ /var is not inside @: $(echo "$info" | sed -n 's/^var: //p')"; fail=1 ;;
esac

if echo "$info" | grep -qx 'listed: yes'; then
  # "ID 256 gen 9 top level 5 path @": the subvolumes right under the top.
  top=$(echo "$info" | sed -n 's/^subvol: .* top level 5 path \(.*\)$/\1/p' | sort | tr '\n' ' ')
  if [ "$top" = "@ " ]; then
    echo "✅ @ is the only top-level subvolume"
  else
    echo "❌ top-level subvolumes: '${top% }', want only @"; fail=1
  fi
else
  echo "ℹ️  the image has no btrfs tool; the subvolume list is not checked"
fi
exit "$fail"
