# JSON Progress Event Format

fisherman emits installation progress as newline-delimited JSON events to stdout. This document describes the format for frontends and other consumers of the fisherman backend.

## Overview

Each line of output is a valid JSON object representing a single event. The protocol is *streaming* — parse line-by-line rather than reading the entire output and parsing it as a JSON array.

**Common fields (present in every event):**
- `type` — event type identifier (string)
- `timestamp` — RFC3339Nano timestamp (UTC)
- `elapsed_ms` — milliseconds elapsed since fisherman started (integer)

## Event Types

### `step`

Marks the start of a major installation phase.

**Fields:**
- `type` — `"step"`
- `step` — current step number (1-based integer)
- `total_steps` — total number of steps in the pipeline (always 9)
- `step_name` — human-readable step name (string)
- `weight_pct` — estimated percentage of total install time this step occupies (0–100)
- `cumulative_pct` — progress bar position at the start of this step (0–100)
- `timestamp`, `elapsed_ms` — as above

**Example:**
```json
{"type":"step","step":1,"total_steps":9,"step_name":"Partition disk","weight_pct":5,"cumulative_pct":0,"timestamp":"2026-10-07T19:05:30.123456Z","elapsed_ms":245}
```

**Usage:** Use `cumulative_pct` + `weight_pct` to display an overall progress bar. The final percentage at the end of this step will be approximately `cumulative_pct + weight_pct`.

### `substep`

Provides detailed progress within a step (e.g., bootc's internal status messages).

**Fields:**
- `type` — `"substep"`
- `message` — detailed status message (string)
- `timestamp`, `elapsed_ms` — as above

**Example:**
```json
{"type":"substep","message":"Pulling image layers: 512 MiB / 2048 MiB","timestamp":"2026-10-07T19:05:35.456789Z","elapsed_ms":5678}
```

**Usage:** Display substeps as real-time log output beneath the main progress bar.

### `info`

Informational messages about the installation process (not errors).

**Fields:**
- `type` — `"info"`
- `message` — informational message (string)
- `timestamp`, `elapsed_ms` — as above

**Example:**
```json
{"type":"info","message":"Mounted /mnt/fisherman-target","timestamp":"2026-10-07T19:05:40.789012Z","elapsed_ms":10901}
```

**Usage:** Log informational messages to the installation log or verbose output.

### `error`

Terminal error — the installation has failed.

**Fields:**
- `type` — `"error"`
- `message` — human-readable error description (string)
- `timestamp`, `elapsed_ms` — as above

**Example:**
```json
{"type":"error","message":"Failed to mount root filesystem: No space left on device","timestamp":"2026-10-07T19:06:15.234567Z","elapsed_ms":45123}
```

**Usage:** Stop reading events and display the error to the user. A plain-text version of the same error is also written to stderr for terminal/log visibility.

### `recovery_key`

LUKS recovery passphrase for TPM2-protected encryption.

**Fields:**
- `type` — `"recovery_key"`
- `key` — recovery passphrase (string; typically 128–256 characters)
- `timestamp`, `elapsed_ms` — as above

**Note:** This event is emitted **only** when a random (non-user-chosen) passphrase is the sole fallback for TPM2-protected encryption. The user should write this passphrase down before rebooting.

**Example:**
```json
{"type":"recovery_key","key":"abc123def456...","timestamp":"2026-10-07T19:06:20.567890Z","elapsed_ms":50456}
```

**Usage:** Display this key prominently to the user in a readable format (QR code, large text, etc.). Do not proceed past this event until the user acknowledges they have recorded the key.

### `complete`

Installation finished successfully.

**Fields:**
- `type` — `"complete"`
- `message` — completion message (string)
- `boot_id` — EFI boot entry number (4-digit string, e.g. `"0001"`); omitted if unknown
- `timestamp`, `elapsed_ms` — as above

**Example (with boot ID):**
```json
{"type":"complete","message":"Installation complete. System will boot from entry 0001.","boot_id":"0001","timestamp":"2026-10-07T19:06:30.890123Z","elapsed_ms":60789}
```

**Example (without boot ID):**
```json
{"type":"complete","message":"Installation complete.","timestamp":"2026-10-07T19:06:30.890123Z","elapsed_ms":60789}
```

**Usage:** Mark the installation as successful and optionally display the boot entry number to the user.

## Installation Pipeline

fisherman always emits exactly 9 `step` events in this order:

1. **Partition disk** — Create GPT partition table
2. **Format EFI** — Create FAT32 EFI System Partition
3. **Format /boot** — Create ext4 /boot partition (GRUB images only)
4. **Set up LUKS** — Format and open encrypted root (if encryption is enabled)
5. **Format root** — Create XFS or Btrfs root filesystem
6. **Mount filesystems** — Mount everything at `/mnt/fisherman-target`
7. **Install image** — Run `bootc install to-filesystem`
8. **Copy Flatpaks** — Copy Flatpak data from host to target
9. **Finalize** — Write hostname, finalize boot entries, fstrim, fsfreeze

## Parsing Examples

### Python
```python
import json
import sys

for line in sys.stdin:
    event = json.loads(line)
    if event['type'] == 'step':
        print(f"Step {event['step']}/{event['total_steps']}: {event['step_name']}")
        print(f"Progress: {event['cumulative_pct']}%")
    elif event['type'] == 'error':
        print(f"Error: {event['message']}")
        break
    elif event['type'] == 'complete':
        print(f"Success: {event['message']}")
        break
```

### JavaScript
```javascript
const readline = require('readline');
const rl = readline.createInterface({ input: process.stdin });

rl.on('line', (line) => {
  const event = JSON.parse(line);
  switch (event.type) {
    case 'step':
      console.log(`Step ${event.step}/${event.total_steps}: ${event.step_name}`);
      break;
    case 'error':
      console.error(`Error: ${event.message}`);
      process.exit(1);
    case 'complete':
      console.log(`Success: ${event.message}`);
      process.exit(0);
  }
});
```

## Error Handling

- **Unknown event types:** Ignore them (forward compatibility).
- **Malformed JSON:** Log the line to stderr and continue reading.
- **Error event:** Stop processing and report the failure; do not wait for further events.
- **No completion event:** If EOF is reached without a `complete` or `error` event, the process was interrupted (e.g., killed by signal) — treat as an error.

## Protocol Stability

This protocol is considered stable as of fisherman v0.1.0. Changes to event fields are governed by semantic versioning:
- **New event types or fields:** Minor version bump (v0.2.0).
- **Removing fields:** Major version bump (v1.0.0).
- **Changing field semantics:** Major version bump.

Consumers should gracefully ignore unknown event types and unknown fields within known event types.
