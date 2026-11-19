#!/usr/bin/env python3
"""Arch installs must upgrade while they sync (#500).

`pacman -Sy <pkg>` refreshes the sync database without upgrading the
system. Against a bootc image that is a few weeks old it selects a newer
openssl than the image's curl, pacman and cryptsetup were built for, and the
transaction aborts ("breaks dependency 'libcrypto.so=3-64'"). Every script
that installs a package with pacman must use `-Syu`.

No network; run with `python3 tests/test-pacman-full-upgrade.py`.
"""

import pathlib
import re
import unittest

REPO = pathlib.Path(__file__).resolve().parents[1]
SCRIPTS = [
    REPO / "scripts" / "Containerfile.ssh-enable",
    REPO / "scripts" / "prepare-ssh-image.sh",
]
# -Sy, -Syy, -Sy<other> but not -Syu / -Syyu (any flag cluster with S and y but no u).
PARTIAL = re.compile(r"\bpacman\s+(?:-\w+\s+)*-(?=\w*S)(?=\w*y)(?!\w*u)\w+")


class PacmanFullUpgrade(unittest.TestCase):
    def test_scripts_exist_and_use_pacman(self):
        for path in SCRIPTS:
            self.assertIn("pacman", path.read_text(), path)

    def test_no_partial_upgrade(self):
        for path in SCRIPTS:
            for n, line in enumerate(path.read_text().splitlines(), 1):
                self.assertIsNone(
                    PARTIAL.search(line),
                    f"{path.relative_to(REPO)}:{n}: pacman -Sy without -u: {line.strip()}",
                )

    def test_pattern_catches_the_regression(self):
        self.assertTrue(PARTIAL.search("pacman -Sy --noconfirm openssh"))
        self.assertTrue(PARTIAL.search("pacman -Sy openssh --noconfirm"))
        self.assertFalse(PARTIAL.search("pacman -Syu --noconfirm openssh"))
        self.assertFalse(PARTIAL.search("pacman -Syyu openssh --noconfirm"))


if __name__ == "__main__":
    unittest.main()
