#!/usr/bin/env python3
"""Capture the recovery key of a `tpm2-luks` install, and keep it out of logs.

Usage:
  fisherman recipe.json | capture-recovery-key.py <key-file>

fisherman emits the random passphrase of a `tpm2-luks` install once, as the
`recovery_key` progress event (docs/JSON-PROGRESS-FORMAT.md). The VM harness
needs it: the first boot of the installed system has no TPM2 token yet (the
enrolment runs during that boot), so luks-unlock.py types the key at the
prompt, and enable-ssh-installed.sh and verify-installation.sh open the
container with it.

Every line of stdin goes to stdout unchanged, except the `recovery_key`
event: its key is written to <key-file> (mode 0600, no newline) and printed
as "<redacted>". Lines that are not JSON pass through as they are. The exit
status is 0 (2 on a usage error) whether or not a key was seen, so that in
a pipeline fisherman's own status decides: the caller checks <key-file>.
"""

import json
import os
import sys


def main(argv):
    if len(argv) != 2:
        sys.stderr.write(__doc__)
        return 2
    key_file = argv[1]
    for line in sys.stdin:
        try:
            ev = json.loads(line)
        except ValueError:
            ev = None
        if isinstance(ev, dict) and ev.get("type") == "recovery_key" and ev.get("key"):
            fd = os.open(key_file, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
            with os.fdopen(fd, "w") as f:
                f.write(ev["key"])
            ev["key"] = "<redacted>"
            line = json.dumps(ev, separators=(",", ":")) + "\n"
        sys.stdout.write(line)
        sys.stdout.flush()
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
