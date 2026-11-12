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
    -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 \
    -p "$SSH_PORT" "$BOOTCREW_USER@127.0.0.1" "$@"
}

fail=0
# 1. The user signs in, and the session is the user's, in a home it owns.
who=$(ssh_user 'id -un; test -d "$HOME" && test -O "$HOME" && test -w "$HOME" && echo home-ok; echo "groups: $(id -Gn)"' 2>&1) || true
echo "$who" | sed 's/^/  /'
if [ "$(echo "$who" | sed -n 1p)" = "$BOOTCREW_USER" ]; then
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

# 2. The password the recipe gave is the one the account has: recompute the
#    shadow hash with its own salt.
hash=$(ssh_root "getent shadow $BOOTCREW_USER | cut -d: -f2" 2>/dev/null || true)
case "$hash" in
  '$6$'*)
    salt=$(printf '%s' "$hash" | cut -d'$' -f3)
    if [ "$(openssl passwd -6 -salt "$salt" "$BOOTCREW_USER_PASSWORD")" = "$hash" ]; then
      echo "✅ the password matches the recipe's"
    else
      echo "❌ the shadow hash does not match the recipe's password"; fail=1
    fi
    ;;
  *)
    echo "❌ unexpected shadow hash for $BOOTCREW_USER: ${hash:0:4}… (locked or not sha512crypt)"; fail=1
    ;;
esac
exit "$fail"
