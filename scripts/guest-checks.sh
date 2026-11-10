#!/bin/bash
# Pure decision logic for the guest assertions in boot-verify.sh.
#
# boot-verify.sh sources this file and feeds these functions the text it read
# from the booted guest over SSH. tests/check-validation.sh sources it too and
# feeds them fixtures, so each check is shown to fail on the bug it exists for
# without a disk or a VM. Keep them free of ssh, sudo and global state.

# classify_shadow_hash FIELD
#   FIELD is the second field of the account's /etc/shadow line.
#   Prints one of:
#     ok       a crypt(3) hash ("$id$..."); a password can match it
#     empty    no password at all: anyone signs in without one
#     locked   "!" or "*" prefix: no password can ever match
#     plain    anything else, e.g. a plaintext password written verbatim by
#              `chpasswd -e` (fisherman#396): no password matches it either
classify_shadow_hash() {
  local field="$1"
  # shellcheck disable=SC2016  # literal "$" characters, not expansions
  case "$field" in
    "")       echo "empty" ;;
    '!'*|'*'*) echo "locked" ;;
    '$'?*'$'?*) echo "ok" ;;
    *)        echo "plain" ;;
  esac
}

# verify_sha512crypt HASH PASSWORD
#   Recomputes a SHA-512 crypt hash ("$6$salt$digest", default rounds) with
#   openssl on this host and compares. Returns:
#     0  PASSWORD produces HASH
#     1  it does not
#     2  HASH is not a default-rounds $6$ hash, or openssl is missing; the
#        caller must not treat this as a pass
verify_sha512crypt() {
  local hash="$1" password="$2" salt want
  # shellcheck disable=SC2016  # literal "$6$" prefixes, not expansions
  case "$hash" in
    '$6$rounds='*) return 2 ;;
    '$6$'*'$'*) ;;
    *) return 2 ;;
  esac
  command -v openssl >/dev/null 2>&1 || return 2
  salt="${hash#\$6\$}"
  salt="${salt%%\$*}"
  want=$(openssl passwd -6 -salt "$salt" "$password" 2>/dev/null) || return 2
  [ "$want" = "$hash" ]
}

# check_var_on_own_disk VAR_SOURCE VAR_FSTYPE VAR_LABEL ROOT_SOURCE
#   The four values come from `findmnt` in the guest. Returns 0 when /var is
#   its own filesystem on a different device than the root, formatted the way
#   fisherman's varDisk step formats it (xfs, label "var"). Returns 1 with a
#   reason on stderr otherwise.
#
#   Without a var disk an ostree system reports
#   "/dev/sda3[/ostree/deploy/default/var]" for /var: the same device as the
#   root with a subdirectory in brackets. That is the failure this catches.
check_var_on_own_disk() {
  local var_src="$1" var_fstype="$2" var_label="$3" root_src="$4"
  if [ -z "$var_src" ]; then
    echo "/var is not a mount point" >&2
    return 1
  fi
  case "$var_src" in
    *"["*)
      echo "/var is a bind mount of $var_src, not its own filesystem" >&2
      return 1
      ;;
  esac
  if [ "${var_src%%\[*}" = "${root_src%%\[*}" ]; then
    echo "/var is on the root device $var_src" >&2
    return 1
  fi
  if [ "$var_label" != "var" ]; then
    echo "/var filesystem label is '$var_label', want 'var'" >&2
    return 1
  fi
  if [ "$var_fstype" != "xfs" ]; then
    echo "/var filesystem type is '$var_fstype', want 'xfs'" >&2
    return 1
  fi
  return 0
}

# has_tpm2_token LUKS_DUMP
#   LUKS_DUMP is the output of `cryptsetup luksDump <device>` in the guest.
#   Returns 0 when a systemd-tpm2 token is enrolled.
has_tpm2_token() {
  printf '%s\n' "$1" | grep -qE '^[[:space:]]*[0-9]+: systemd-tpm2'
}
