# shellcheck shell=bash
# Guest check (#422): the install enabled no unit the image did not enable.
# Sourced by scripts/boot-verify.sh (BOOTCREW_GUEST_CHECKS) in a subshell,
# with ssh_root set. The matrix field `enabled_units: true` selects it.
#
# Two lists, both read in the guest by the script below (ENABLED_UNITS_GUEST):
#
#   installed  what the booted system enables: `systemctl list-unit-files
#              --state=enabled` (system and --global user scope), every link
#              in a *.wants/, *.requires/ or *.upholds/ directory under
#              /etc/systemd/{system,user}, and every `enable` line of a
#              preset file under /etc/systemd/{system,user}-preset.
#   image      the same three, read from the image's own /etc (ostree keeps
#              it unchanged at /usr/etc) and /usr/lib/systemd, copied to a
#              scratch root. Before the listing, `systemctl --root
#              preset-all --preset-mode=enable-only` applies the image's own
#              presets to the copy: systemd does the same on the first boot
#              (empty machine-id), so a unit the image's presets enable is
#              the image's choice, not fisherman's.
#
# A unit in `installed` and not in `image` fails the check, unless it is on
# one of the two allowlists below. On a composefs target the image's /etc is
# not at /usr/etc: the check fails and says so, so only select it for an
# ostree entry.
set -u

# Units fisherman itself is meant to enable, as scope:pattern (a shell
# glob), each with where it is written. Nothing else. Print services
# (post.PrintServices) and vendor services (internal/post/oem.go) are not
# here: fisherman enables them only when the recipe or the hardware asks,
# and the matrix entry asks for neither, so one that shows up is the bug
# #412 fixed.
ENABLED_UNITS_FISHERMAN="
system:fisherman-boot-receipt.service
system:fisherman-tpm2-enroll.service
user:*-oem-setup.service
"
#   fisherman-boot-receipt.service  internal/receipt (receipt.UnitName): the
#       first-boot receipt collector, a wants link in the deployment's /etc.
#   fisherman-tpm2-enroll.service   internal/luks/firstboot.go
#       (EnrollUnitName): tpm2-* only; it removes its own link when it stops.
#   <distroID>-oem-setup.service    internal/post/oem.go: a user preset,
#       only on a known OEM machine.

# Units the test harness enables, not fisherman: scripts/enable-ssh-installed.sh
# runs `systemctl enable ssh`/`sshd` in the installed root so the VM answers.
ENABLED_UNITS_HARNESS="
system:sshd.service
system:ssh.service
"

# The guest side. `sh -s installed` or `sh -s image`; ENABLED_UNITS_ROOT
# (the second argument) is a root prefix, empty in the guest. One
# scope:unit per line on stdout.
# shellcheck disable=SC2016 # it runs in the guest; nothing expands here
ENABLED_UNITS_GUEST='
set -u
mode=$1
R=${2:-}
units() {  # units ROOT ETC: scope:unit lines for ROOT (empty: the running system)
  ro=""
  [ -n "$1" ] && ro="--root=$1"
  # shellcheck disable=SC2086
  systemctl $ro list-unit-files --state=enabled --no-legend --no-pager 2>/dev/null | awk "{ print \"system:\" \$1 }"
  # shellcheck disable=SC2086
  systemctl $ro --global list-unit-files --state=enabled --no-legend --no-pager 2>/dev/null | awk "{ print \"user:\" \$1 }"
  for scope in system user; do
    d="$2/systemd/$scope"
    if [ -d "$d" ]; then
      find "$d" -mindepth 2 -maxdepth 2 \( -path "*.wants/*" -o -path "*.requires/*" -o -path "*.upholds/*" \) -printf "$scope:%f\n"
    fi
    for p in "$2/systemd/$scope-preset"/*.preset; do
      [ -f "$p" ] && awk -v s="$scope" "\$1 == \"enable\" { print s \":\" \$2 }" "$p"
    done
  done
  return 0
}
case "$mode" in
  installed)
    units "$R" "$R/etc"
    ;;
  image)
    if [ ! -d "$R/usr/etc/systemd" ]; then
      echo "no $R/usr/etc/systemd: the image /etc is not readable here (not an ostree deployment?)" >&2
      exit 2
    fi
    img=$(mktemp -d /var/tmp/enabled-units.XXXXXX) || exit 2
    mkdir -p "$img/usr" "$img/etc"
    cp -a "$R/usr/etc/." "$img/etc/" || exit 2
    # The image /usr itself, read-only: unit links into other /usr paths
    # (dracut) resolve as on the booted system. Only remove the scratch root
    # once /usr is unmounted from it. Without a mount, copy the unit and
    # preset directories.
    # shellcheck disable=SC2064 # $img is known now
    if mount --bind -o ro "$R/usr" "$img/usr" 2>/dev/null; then
      trap "umount \"$img/usr\" && rm -rf --one-file-system \"$img\"" EXIT
    else
      # shellcheck disable=SC2064
      trap "rm -rf --one-file-system \"$img\"" EXIT
      mkdir -p "$img/usr/lib/systemd"
      for d in system user system-preset user-preset; do
        [ -d "$R/usr/lib/systemd/$d" ] && cp -a "$R/usr/lib/systemd/$d" "$img/usr/lib/systemd/"
      done
    fi
    # The first boot of the image: an empty machine-id, then its presets.
    rm -f "$img/etc/machine-id"
    systemctl --root="$img" preset-all --preset-mode=enable-only >/dev/null 2>&1 || true
    systemctl --root="$img" --global preset-all --preset-mode=enable-only >/dev/null 2>&1 || true
    units "$img" "$img/etc"
    ;;
  *) echo "usage: sh -s installed|image [ROOT]" >&2; exit 2 ;;
esac
'

# enabled_units_allowed SCOPE:UNIT: true when an allowlist names it.
enabled_units_allowed() {
  local entry
  for entry in $ENABLED_UNITS_FISHERMAN $ENABLED_UNITS_HARNESS; do
    # shellcheck disable=SC2254
    case "$1" in $entry) return 0 ;; esac
  done
  return 1
}

fail=0
installed=$(printf '%s' "$ENABLED_UNITS_GUEST" | ssh_root "sh -s installed ${ENABLED_UNITS_ROOT:-}" 2>/dev/null) || installed=""
image=$(printf '%s' "$ENABLED_UNITS_GUEST" | ssh_root "sh -s image ${ENABLED_UNITS_ROOT:-}" 2>/dev/null) || image=""
if [ -z "$installed" ]; then
  echo "❌ could not list the enabled units of the installed system"; exit 1
fi
if [ -z "$image" ]; then
  echo "❌ could not list the units the image enables (its /etc at /usr/etc, its presets)"; exit 1
fi
installed=$(echo "$installed" | sort -u)
image=$(echo "$image" | sort -u)
echo "  the installed system enables $(echo "$installed" | wc -l) unit(s), the image $(echo "$image" | wc -l)"
extra=$(comm -23 <(echo "$installed") <(echo "$image"))
bad=""
while IFS= read -r unit; do
  [ -n "$unit" ] || continue
  if enabled_units_allowed "$unit"; then
    echo "  allowed: $unit (on the allowlist)"
  else
    bad="$bad $unit"
  fi
done <<<"$extra"
if [ -n "$bad" ]; then
  for unit in $bad; do echo "❌ enabled, and the image did not enable it: $unit"; done
  fail=1
else
  echo "✅ every enabled unit is the image's own, or on the allowlist"
fi
exit "$fail"
