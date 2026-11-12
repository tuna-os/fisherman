#!/usr/bin/env python3
"""Write the fisherman recipe for one tests/bootcrew-matrix.yaml entry.

Usage:
  matrix-recipe.py <image-json> <disk> [<var-disk>]

<image-json> is the matrix entry as JSON (what bootcrew-vm.yml passes to
`just bootcrew-ci-test`). The recipe goes to stdout.

The fields an entry can set (all optional unless noted):

  image               source image (required; `ssh_image_name` picks the
                      pre-built ssh-enabled image the harness installs)
  filesystem          default "xfs"
  composefs_backend   default false
  bootloader          "systemd" | "grub2"; default "" (GRUB), except the
                      name-based systemd-boot fallback below
  unified_storage, selinux_disabled
  luks                true: encryption "luks-passphrase" with luks_passphrase
  encryption          an explicit encryption.type ("tpm2-luks-passphrase");
                      takes luks_passphrase as the passphrase
  user                true: a `user` block (user_name, user_password)
  var_disk            true: a `varDisk` on <var-disk>, formatted
  btrfs_subvolumes    true: "btrfsSubvolumes": true

The harness (justfile bootcrew-ci-test) feeds the same entry to its checks,
so the defaults here and there must agree: DEFAULT_USER and
DEFAULT_USER_PASSWORD are read back by `--print-user`.
"""

import json
import sys

DEFAULT_USER = "bootcrew"
DEFAULT_USER_PASSWORD = "bootcrew-user-test"


def user_of(entry):
    return (entry.get("user_name") or DEFAULT_USER,
            entry.get("user_password") or DEFAULT_USER_PASSWORD)


def recipe(entry, disk, var_disk=""):
    name = entry.get("name", "")
    bootloader = entry.get("bootloader") or ""
    if not bootloader and (name.startswith("debian-bootc") or name.startswith("arch-bootc")):
        bootloader = "systemd"

    passphrase = entry.get("luks_passphrase") or ""
    enc_type = entry.get("encryption") or ("luks-passphrase" if entry.get("luks") else "none")
    encryption = {"type": enc_type}
    if enc_type != "none" and passphrase:
        encryption["passphrase"] = passphrase

    image = "ghcr.io/tuna-os/fisherman/%s:ssh-enabled" % (entry.get("ssh_image_name") or name)
    r = {
        "disk": disk,
        "filesystem": entry.get("filesystem") or "xfs",
        "composeFsBackend": bool(entry.get("composefs_backend", False)),
        "unifiedStorage": bool(entry.get("unified_storage", False)),
        "selinuxDisabled": bool(entry.get("selinux_disabled", False)),
        "encryption": encryption,
        "image": image,
        "hostname": "ci-test",
        "flatpaks": [],
    }
    if bootloader:
        r["bootloader"] = bootloader
    if entry.get("btrfs_subvolumes"):
        r["btrfsSubvolumes"] = True
    if entry.get("user"):
        username, password = user_of(entry)
        r["user"] = {"username": username, "fullname": "Bootcrew CI",
                     "password": password, "groups": ["wheel"]}
    if entry.get("var_disk"):
        if not var_disk:
            sys.exit("matrix-recipe.py: %s sets var_disk but no <var-disk> was given" % name)
        r["varDisk"] = {"disk": var_disk, "keepExisting": False}
    return r


def main(argv):
    if len(argv) == 3 and argv[1] == "--print-user":
        print("%s %s" % user_of(json.loads(argv[2])))
        return 0
    if len(argv) not in (3, 4):
        sys.stderr.write(__doc__)
        return 2
    entry = json.loads(argv[1])
    json.dump(recipe(entry, argv[2], argv[3] if len(argv) == 4 else ""), sys.stdout, indent=2)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
