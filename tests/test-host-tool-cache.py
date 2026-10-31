#!/usr/bin/env python3
"""Exercise the composite action's manifest and archive trust boundaries, without APT."""

import hashlib
import json
import os
from pathlib import Path
import re
import tempfile
import unittest
from unittest.mock import patch

import yaml


ACTION = Path(__file__).resolve().parents[1] / ".github/actions/setup-host-tools/action.yml"
STEPS = yaml.safe_load(ACTION.read_text())["runs"]["steps"]


def python_block(step_name):
    step = next(step for step in STEPS if step.get("name") == step_name)
    match = re.search(r"python3 <<'PY'\n(.*?)\nPY(?:\n|$)", step["run"], re.S)
    if match is None:
        raise AssertionError(f"No embedded Python in {step_name}")
    return compile(match.group(1), str(ACTION), "exec")


PREPARE = python_block("Resolve host package downloads")
VERIFY = python_block("Verify restored archives and install host tools")


class HostToolCacheTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.temp = Path(temporary.name)
        self.root = self.temp / "fisherman-host-tools"
        self.archives = self.root / "archives"
        self.archives.mkdir(parents=True)
        self.output = self.temp / "output"
        environment = patch.dict(os.environ, {
            "RUNNER_TEMP": str(self.temp), "GITHUB_OUTPUT": str(self.output),
        })
        environment.start()
        self.addCleanup(environment.stop)

    def prepare(self, lines):
        (self.root / "plan").write_text("Reading package lists...\n" + "\n".join(lines))
        exec(PREPARE, {})
        return json.loads((self.root / "manifest.json").read_text())

    def archive_line(self, name, content):
        digest = hashlib.sha256(content).hexdigest()
        return f"'http://archive.ubuntu.com/pool/{name}' {name} {len(content)} SHA256:{digest}"

    def test_manifest_carries_exact_identity_and_digest(self):
        name = "cryptsetup-bin_2%3a2.7.0-1ubuntu4.2_amd64.deb"
        content = b"downloaded archive"
        manifest = self.prepare([self.archive_line(name, content)])
        self.assertEqual(manifest[name], [
            f"http://archive.ubuntu.com/pool/{name}", len(content),
            hashlib.sha256(content).hexdigest(),
        ])
        digest = hashlib.sha256(json.dumps(manifest, sort_keys=True).encode()).hexdigest()
        self.assertIn(f"digest={digest}\n", self.output.read_text())
        self.assertIn("has-archives=true\n", self.output.read_text())

    def test_installed_tools_need_no_archive_cache(self):
        self.assertEqual(self.prepare([]), {})
        self.assertIn("has-archives=false\n", self.output.read_text())

    def test_invalid_hash_or_path_fails_closed(self):
        for line in [
            "'http://example.org/a.deb' a.deb 4 MD5Sum:abcd",
            "'http://example.org/a.deb' a.deb 4 SHA256:abcd",
            self.archive_line("../outside.deb", b"data"),
            self.archive_line("/outside.deb", b"data"),
        ]:
            with self.subTest(line=line), self.assertRaises(SystemExit):
                self.prepare([line])

    def test_only_matching_archives_survive(self):
        good = b"correct"
        names = ["valid_1_amd64.deb", "corrupt_1_amd64.deb", "missing_1_amd64.deb"]
        self.prepare([self.archive_line(name, good) for name in names])
        (self.archives / names[0]).write_bytes(good)
        # Same length: size alone cannot authenticate a cached archive.
        (self.archives / names[1]).write_bytes(b"invalid")
        (self.archives / "unrequested_1_amd64.deb").write_bytes(good)
        exec(VERIFY, {})
        self.assertEqual(sorted(path.name for path in self.archives.iterdir()), [names[0]])
        self.assertEqual((self.archives / names[0]).read_bytes(), good)

    def test_symlink_is_not_a_cached_archive(self):
        name = "tool_1_amd64.deb"
        content = b"outside file"
        self.prepare([self.archive_line(name, content)])
        outside = self.temp / "outside"
        outside.write_bytes(content)
        (self.archives / name).symlink_to(outside)
        exec(VERIFY, {})
        self.assertFalse((self.archives / name).is_symlink())
        self.assertEqual(outside.read_bytes(), content)

    def test_directory_entry_is_removed_without_following_nested_symlinks(self):
        name = "tool_1_amd64.deb"
        content = b"verified download"
        self.prepare([self.archive_line(name, content)])
        outside = self.temp / "outside-directory"
        outside.mkdir()
        protected = outside / "keep"
        protected.write_bytes(b"outside contents")
        archive = self.archives / name
        archive.mkdir()
        nested = archive / "nested"
        nested.mkdir()
        (nested / "outside-directory").symlink_to(outside, target_is_directory=True)
        (nested / "outside-file").symlink_to(protected)
        (nested / "cached-junk").write_bytes(b"junk")
        exec(VERIFY, {})
        self.assertFalse(archive.exists())
        self.assertEqual(protected.read_bytes(), b"outside contents")
        self.assertEqual(list(outside.iterdir()), [protected])
        # The same pathname is available for APT's ordinary retrieval afterward.
        archive.write_bytes(content)
        exec(VERIFY, {})
        self.assertEqual(archive.read_bytes(), content)


if __name__ == "__main__":
    unittest.main()
