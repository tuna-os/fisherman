# Integrating fisherman

This guide is for the authors of an installer frontend and of any other
project that runs fisherman or builds it in. It says which command to run
when, how to give fisherman the recipe, how to read its answer, and how to
pin and package it. The contracts it names have their own pages, and each
of those is held to the code by a test:

| Contract | Page |
|---|---|
| The recipe fields | [RECIPE.md](RECIPE.md) |
| The command line | [CLI.md](CLI.md) |
| The machine facts | [PROBE.md](PROBE.md) |
| The recipe rules and their codes | [VALIDATE.md](VALIDATE.md) |
| The install's event stream | [JSON-PROGRESS-FORMAT.md](JSON-PROGRESS-FORMAT.md) |
| The installation receipt | [RECEIPT.md](RECEIPT.md) |
| What to do after a failure | [TROUBLESHOOTING.md](TROUBLESHOOTING.md) |

`TestIntegrationGuide` (`fisherman/internal/progress/docs_test.go`) and
`TestIntegrationExitCodes` (`fisherman/cmd/fisherman/cli_doc_test.go`) hold
the lists on this page to the code.

## The division of work

fisherman is the backend. It owns every rule about the recipe and every
step that changes a disk. A frontend owns the conversation with the user:
it collects choices, writes one recipe, gives it to fisherman, and shows
fisherman's events.

So a frontend does not:

- detect live media, offline image stores, the TPM or eligible disks
  itself. `fisherman probe --json` reports them, and the install judges the
  recipe with the same code;
- copy a recipe rule (hostname syntax, user names, filesystem
  combinations). `fisherman validate --json --partial -` checks a page;
- write a recipe to a file.

The frontend keeps the rules that are not fisherman's: the user password is
not empty, it matches its confirmation, and how strong it is.

## Which command, and when

| When | Command | Root |
|---|---|---|
| At start, and again when the image choice changes | `fisherman probe --json`, with `--source <image>` or `--live-root` to scope `capabilities` to that source | no |
| While the user fills in a page | `fisherman validate --json --partial -`, with that page's fields on stdin | no |
| On the confirm page | `fisherman validate --json -`, with the whole recipe on stdin | no |
| To install | `fisherman -`, with the recipe on stdin | yes |
| On the installed system, after the first boot | `fisherman verify --json` | for `--live` |

`probe` and `validate` never change the machine. The install runs every
rule of the full `validate` again before its first disk step, so a recipe
that validate refused never reaches a disk.

### What to take from the probe

- **Disks.** Offer `disks[]` entries with `eligible: true`. Show
  `excluded_reason` for the others.
- **Encryption.** Offer the types of
  [`data/encryption-choices.json`](../data/encryption-choices.json) in its
  order. Offer a `needs_tpm2` type only when `tpm.usable` is true. Show a
  passphrase field only for a `needs_passphrase` type.
- **The image on live media.** When `live.is_live` is true, an empty
  `image` installs the image that the live media booted; show
  `live.live_image` as its name. Anywhere else, an empty `image` is refused
  (`image_required`).
- **Offline images.** `offline.images` lists the images available with no
  network. Leave `additionalImageStores` out of the recipe: fisherman then
  uses the stores in `offline.stores` itself.
- **Requirements.** `system.unmet` lists what this machine lacks (`ram`,
  `cpu`, `uefi`).

## The recipe

[RECIPE.md](RECIPE.md) lists every field with its type and its default. The
minimum for an automatic layout is `disk`, `filesystem` and `hostname`, plus
`image` off live media and `encryption.passphrase` for a type that needs
one. Take `bootloader`, `composeFsBackend`, `filesystem` choices and
`imageType` from the image catalog: they are facts of the image.

bootc-installer keeps the JSON schema and its frontend rules:
[`shared/recipe/fisherman-recipe.schema.json`](https://github.com/tuna-os/bootc-installer/blob/dev/shared/recipe/fisherman-recipe.schema.json)
and
[`shared/recipe/README.md`](https://github.com/tuna-os/bootc-installer/blob/dev/shared/recipe/README.md).

A key that fisherman does not know is refused (`recipe_unknown_field`), so
a misspelt key cannot install something the user did not choose. The
recipe has no version field yet (#293). To send a new field, first move the
pin to a fisherman that has it.

## Run fisherman with the recipe on stdin

The recipe can hold the LUKS passphrase and the user password. Never write
it to a file and never put it on the command line. Run fisherman with `-`,
write the JSON to its stdin, and close stdin: fisherman reads to EOF, and
refuses an empty stdin (`recipe_malformed`) and a recipe over 1 MiB.

Run the install through a shell that the frontend owns:

```sh
bash -c 'pkexec /usr/local/bin/fisherman -; exit $?'
# from inside a Flatpak:
flatpak-spawn --host bash -c 'pkexec /usr/local/bin/fisherman -; exit $?'
```

- `pkexec`, `sudo` and `flatpak-spawn --host` pass stdin through.
- Start the wrapper in its own process group. To cancel, kill that group.
  The frontend cannot signal a root fisherman itself, so fisherman cancels
  itself when its parent (the bash) dies: it stops its children, tears down
  the target and exits 130. See
  [Cancelling an install](JSON-PROGRESS-FORMAT.md#cancelling-an-install).
- `pkexec` exits 126 when the user dismisses the authentication dialog and
  127 when authorization fails. fisherman did not start in those cases.
- Run `probe` and `validate` as the user, not through `pkexec`. Inside a
  Flatpak, fisherman sends each host command through `flatpak-spawn --host`
  itself.

## Read the event stream

The install writes one JSON object per line on stdout. Skip a line that is
not a JSON object, ignore an event type and a field that you do not know,
and do not depend on key order. The event types are:

| Type | What to do |
|---|---|
| `step` | Move to a new step. Show the label for `step_id`; use `overall_pct` for the bar. |
| `substep` | Show detail inside the step, if you want to. |
| `info` | Add the line to the log. |
| `source_reuse` | Show the progress of the copy from the host's bootc store. |
| `recovery_key` | Keep `key` and show it to the user before the reboot. It comes once, for `tpm2-luks` only. |
| `receipt` | Note whether the installation receipt was written (`written`, `first_boot_check`). |
| `complete` | The install succeeded. Offer the reboot. |
| `error` | The install failed or was cancelled. No event follows. |

### The `error` event

| Field | What it says |
|---|---|
| `message` | What failed, for people. Show it when you have no copy for `code`. |
| `code` | The stable code. A recipe or preflight problem has the code that `fisherman validate --json` gives it; any other failure has a [failure code](JSON-PROGRESS-FORMAT.md#failure-codes) for the operation that failed. Key your copy on this, never on `message`. |
| `field` | The recipe field that `code` is about, when there is one. |
| `step_id` | The step that was running. Absent before the first step. |
| `disk_changed` | `true`: a disk step had started, so the disk may be partly written. `false`: no disk was changed, and the user can fix the choice and try again. |
| `cleanup` | What the teardown did: `status` (`ok`, `failed`, `timed_out`) and an optional `message`. With `failed` or `timed_out`, the target may still be mounted or unlocked; say so. |

A minimal handler, in Python:

```python
def on_error(event, exit_code, copy):
    if exit_code == 130 or event.get("code") == "cancelled":
        return copy["install_cancelled"]
    text = copy.get("error_" + event.get("code", ""), event["message"])
    if event.get("disk_changed") is False:
        text += "\n" + copy["nothing_changed"]
    if event.get("cleanup", {}).get("status") in ("failed", "timed_out"):
        text += "\n" + copy["target_still_mounted"]
    return text
```

### Exit codes

| Exit | Meaning |
|---|---|
| 0 | The install succeeded. The last event is `complete`. |
| 1 | The install failed. The last event is `error`. |
| 2 | The argument is not a recipe path or a command. No event. |
| 130 | The install was cancelled. The last event is `error` with `code` `cancelled`. |

A stream that ends without `complete` or `error` is a failure. Use the exit
code to tell the cases apart. The exit codes of the other commands are in
[CLI.md](CLI.md).

## Test without a disk

Never test an integration by installing to a real disk, a loop device or a
sparse file on a machine you care about: an install partitions, formats and
tears down mounts as root.

- `fisherman probe --json` and `fisherman validate --json` need no root and
  change nothing. Run them on any machine.
- Feed your parser the recorded event streams in
  `fisherman/internal/progress/testdata/`.
- fisherman's Go tests (`go test ./...` in `fisherman/`) replace the host
  commands with fakes, and `bash tests/check-validation.sh` checks that the
  E2E verification scripts catch the bugs they claim to catch. Neither needs
  a disk or a VM.
- Real installs run in CI, in VMs, from the matrix in
  `tests/bootcrew-matrix.yaml`.

## Pin and package fisherman

### Which commit to pin

`dev` is the working branch. `promote.yml` moves `prod` to a `dev` commit
when every required check of that commit is green and it has sat for 30
minutes; it then tags the commit (`vX.Y.Z`) and publishes the release:
`fisherman_<version>_linux_amd64.tar.gz`,
`fisherman_<version>_linux_arm64.tar.gz` and `checksums.txt`. See
[CONTRIBUTING.md](../CONTRIBUTING.md#releasing).

Pin a release: a tagged commit on `prod`. Read the
[CHANGELOG](../CHANGELOG.md) before you move a pin. A recipe field, an
event or a code that your frontend uses must be in the fisherman that you
pin.

### bootc-installer

bootc-installer holds fisherman as a git submodule at `fisherman/`
(`.gitmodules`: `url = https://github.com/tuna-os/fisherman.git`,
`branch = prod`). The gitlink is its only pin:

- Renovate moves the gitlink (`renovate.json`, `git-submodules`, group
  `fisherman`), and `shared/ci/fisherman_pins.py --commit <sha>` moves it by
  hand.
- Every Flatpak builds fisherman from that submodule: GNOME's manifest has a
  `fisherman` module with a `dir` source `../fisherman`, and the other four
  include `shared/flatpak/fisherman.json`. The binary is
  `/app/bin/fisherman`.
- In a Flatpak, the frontend copies `/app/bin/fisherman` to
  `$XDG_CACHE_HOME/bootc-installer/fisherman`, the app cache that the host
  can see. It then hashes the copy through `flatpak-spawn --host sha256sum`
  and runs it only when that SHA256 equals the bundled binary's
  (`shared/python/bootc_shared/fisherman_host.py`). Not `$HOME/.cache`:
  `/var/home` can be private to one sandbox.
- Outside a Flatpak, the frontend runs `/usr/local/bin/fisherman`.

A change in fisherman reaches bootc-installer only when the gitlink moves.
Push the fisherman change first, then move the pin in a separate commit.

### Another consumer

- Build from a pinned commit: `go build -C fisherman ./cmd/fisherman`.
  fisherman's module has no dependencies outside the Go standard library.
- Or take a release archive and check it against `checksums.txt`.
- wootc pins a fisherman commit in its own submodule; moving that pin is
  #206.
- Install the data files with the binary when you use `fisherman images` or
  catalog `extras`: `data/images.json` at
  `/usr/share/fisherman/data/images.json` (or `/app/share/…` in a Flatpak),
  or set `FISHERMAN_IMAGES_PATH`.
