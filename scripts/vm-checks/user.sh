# shellcheck shell=bash
# Guest check: the recipe's `user` account exists and can sign in.
# Sourced by scripts/boot-verify.sh (BOOTCREW_GUEST_CHECKS) in a subshell,
# with ssh_root, SSH_BIN, SSH_KEY and SSH_PORT set. Needs BOOTCREW_USER and
# BOOTCREW_USER_PASSWORD. enable-ssh-installed.sh installed this run's key
# for the user too, so the user signs in over SSH with it.
set -u
: "${BOOTCREW_USER:?}" "${BOOTCREW_USER_PASSWORD:?}"

ssh_user() {
  "$SSH_BIN" -i "$SSH_KEY" -o BatchMode=yes -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 -o LogLevel=ERROR \
    -p "$SSH_PORT" "$BOOTCREW_USER@127.0.0.1" "$@"
}

fail=0
# 1. The user signs in, and the session is the user's, in a home it owns.
who=$(ssh_user 'id -un; test -d "$HOME" && test -O "$HOME" && test -w "$HOME" && echo home-ok; echo "groups: $(id -Gn)"' 2>&1) || true
echo "$who" | sed 's/^/  /'
# A line of its own that is the user name: ssh may print a warning first.
if echo "$who" | grep -qxF "$BOOTCREW_USER"; then
  echo "✅ $BOOTCREW_USER signs in over SSH"
else
  echo "❌ $BOOTCREW_USER cannot sign in"; fail=1
fi
if echo "$who" | grep -qx home-ok; then
  echo "✅ the home directory exists and belongs to $BOOTCREW_USER"
else
  echo "❌ no home directory that $BOOTCREW_USER owns"; fail=1
fi
if ssh_root 'getent group wheel' >/dev/null 2>&1; then
  if echo "$who" | sed -n 's/^groups: //p' | tr ' ' '\n' | grep -qx wheel; then
    echo "✅ $BOOTCREW_USER is in wheel"
  else
    echo "❌ $BOOTCREW_USER is not in wheel"; fail=1
  fi
fi

# 2. The password the recipe gave is the one the account has: crypt(3)
#    the recipe's password with the stored hash as its setting, so any
#    scheme the image's chpasswd chose is checked (CentOS Stream 10 hashes
#    with yescrypt, $y$; fisherman writes $6$ itself on composefs). The
#    runner's libxcrypt does the hashing; the guest needs no tool.
crypt_matches() {  # crypt_matches PASSWORD HASH
  python3 - "$1" "$2" <<'PY'
import ctypes, ctypes.util, sys
lib = ctypes.CDLL(ctypes.util.find_library("crypt") or "libcrypt.so.1")
lib.crypt.restype = ctypes.c_char_p
lib.crypt.argtypes = [ctypes.c_char_p, ctypes.c_char_p]
out = lib.crypt(sys.argv[1].encode(), sys.argv[2].encode())
sys.exit(0 if out and out.decode() == sys.argv[2] else 1)
PY
}
hash=$(ssh_root "getent shadow $BOOTCREW_USER | cut -d: -f2" 2>/dev/null || true)
case "$hash" in
  '$'*)
    if crypt_matches "$BOOTCREW_USER_PASSWORD" "$hash"; then
      echo "✅ the password matches the recipe's (${hash:0:3}…)"
    else
      echo "❌ the shadow hash does not match the recipe's password (${hash:0:3}…)"; fail=1
    fi
    ;;
  *)
    echo "❌ no password hash for $BOOTCREW_USER: '${hash:0:4}' (locked or empty)"; fail=1
    ;;
esac
exit "$fail"
