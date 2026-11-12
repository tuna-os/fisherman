# Fisherman Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### ✨ Features

- **`fisherman diagnostics`: a local support bundle** (#375): a new command
  writes one JSON file of allowlisted facts about an install: versions, the
  probe's facts, the recipe's effective options (never the recipe), the
  progress timeline with the runtime failure code and cleanup outcome, and
  the mounts and LUKS mapper still open under the target and scratch
  directory. It never holds a passphrase, recovery key, password, password
  hash or token; messages are redacted, unknown fields and events are only
  counted, and host, user and device identifiers are masked unless
  `--include-identifiers`. The file is new, mode 0600, created with
  `O_EXCL|O_NOFOLLOW`, bounded in size, and removed on a write error;
  `--stdout` prints it for review. The command only reads, and an install
  never runs it. Sentinel tests plant secrets in every input. See
  `docs/DIAGNOSTICS.md`.

- **Runtime failure codes on the `error` event** (#369): every `error` event
  now has a `code`. It is the validate code for a recipe or preflight
  problem, as before, and otherwise one of the documented failure codes for
  the operation that failed (`partition_failed`, `pull_failed`,
  `bootc_install_failed`, …, `cancelled`, `unknown`). The event also carries
  `step_id` (the running step), `disk_changed` (a disk step had started) and
  `cleanup` (`status` `ok`, `failed` or `timed_out`, and its `message`). A
  cleanup failure no longer hides the primary cause. An unloadable recipe
  gets the code `validate --json` gives it (`recipe_malformed`,
  `recipe_unreadable`). The LUKS passphrase, the user password and the
  recovery key are redacted from the event and the stderr line, and the
  message is bounded. `message`, exit 1 versus 130 and the one terminal event
  after teardown are unchanged.

### 🐛 Fixes

- **A separate `/var` on a composefs deployment no longer gets the
  Flatpaks** (#252): `var_disk_unsupported_composefs` refuses a `varDisk` when
  the recipe says composefs (`composeFsBackend`, or `filesystem: zfs`). After
  bootc ran, the persistent-mounts step now also checks what bootc really
  installed. On a composefs-native deployment with a `varDisk` or a manual
  `/var` partition it fails (`persistent_mounts_failed`) before it points the
  Flatpak copy and the user's home at that disk, which the composefs
  initramfs hides behind the deployment's own `/var`. Before, they were
  copied onto a disk that the first boot does not mount. Mounts below `/var`
  are unchanged. Tests on the composefs deploy fixture and for
  `validate --json --partial`.
- **btrfs roots booted without the recipe's mount options on composefs**
  (#418): bootc 1.15.x writes the composefs boot entry from the root's own
  arguments, `composefs=` and `kargs.d`, and never reads `--karg`
  (`setup_composefs_bls_boot`; newer bootc reads it). fisherman's
  `rootflags=subvol=@,compress=zstd:1` (or `compress=zstd:1` without
  subvolumes) was dropped, the entry had bootc's own
  `rootflags=subvol=/@` (or `subvol=/`), and the installed system mounted
  its root uncompressed. Found by the new `dakota-btrfs-subvolumes` VM entry;
  `dakota-luks-btrfs` had the same entry (`rootflags=subvol=/`). fisherman
  now replaces bootc's `rootflags=` in every BLS entry with the recipe's
  and reads each entry back (`post.RequireRootFlags`). Where bootc honours
  `--karg` (ostree), the entry had both, and one `rootflags=` remains. A
  failure is the new `error` code `root_flags_failed`. An image that boots a
  unified kernel image has no entry to change, and the log says so.

- **A cancel during the source reuse copy could leave `/var/tmp` bound to
  a deleted directory** (#384): a cancel stops the copy's `bootc` and runs
  the teardown, and the copy's own unbind and `Release` raced for the
  bind. When the copy took it first, the session was already released, so
  its `umount` was refused. `Release` then saw no bind, skipped the unbind
  and removed the scratch. `/var/tmp` stayed on the deleted
  `source-reuse/var-tmp`, and the next install failed with "cannot bind ...
  over /var/tmp" (store-reuse VM run 43186222564). The bind, the copy's
  unbind and `Release`'s unbind now run one at a time. One `umount` undoes
  one `mount`, even after `Release` has begun, and `Release` waits for a
  bind in flight. Neither unmounts `/var/tmp` a second time. If the bind
  cannot be undone, `Release` keeps the scratch and reports it, so it does
  not remove it from under the bind.
- **The host's bootc installed a local source it did not boot** (#363): an
  install from a local `containers-storage:` source took the direct path
  (the host's bootc, no podman) whenever the host had a bootc. bootc reads
  the install configuration and the backend from the root it runs in, so
  an unrelated live host could give its configuration to the source. The
  direct path now needs an established source execution root, by the rules
  of `fisherman probe`'s `capabilities.execution_root` (the host booted the
  source's digest). Otherwise the install runs the source image's own bootc
  in its container, and an `info` event before the first step says which
  path it takes and why. The direct path for a live ISO that installs its
  own image (#237) stays. See `docs/PROBE.md`.

- **A source the install cannot use was found only after the wipe** (#364):
  with no copy in local containers-storage, a registry that did not answer,
  refused access or did not know the tag gave "pull required", and the pull
  failed after the disk was partitioned. The pre-flight now refuses such a
  source, and an image that is not a bootc image or not for this host, before
  the first step (`image_not_found`, `image_source_unauthorized`,
  `image_source_unreachable`, `image_inspect_failed`,
  `image_inspect_malformed`, `image_platform_mismatch`, `image_not_bootc`). A
  network or registry-side failure is tried three times first. A cached image
  is used as before, offline too. The install log names the source's manifest
  and platform manifest digests, apart from `targetImgref`.
- **The SELinux label workaround was built after the wipe** (#370): an
  install with `selinuxDisabled` on a host with SELinux active compiled its
  preload library only when bootc ran, and went on without it when the host
  had no compiler. It is now built before the first step, and an install that
  cannot build it is refused (`selinux_workaround_unavailable`). No other
  install needs a compiler.
- **`needs_user_creation` defaults to true in the image catalog resolver**
  (tuna-os/bootc-installer#288): a leaf that does not set it now asks for
  the first account, as bootc-installer's catalog contract and all five
  frontends already do; an explicit `false` on the leaf or an ancestor is
  kept. `fisherman images` showed "Needs user creation: no" for the 69
  catalog leaves that leave it unset.

### 🐛 Fixes

- **`bootc_store` matched no real image on amd64** (#384): bootc status
  spells the architecture as OCI does (`amd64`, `arm64`); the probe compared
  it with this machine's kernel spelling (`x86_64`, `aarch64`), so every
  item read `platform_differs` and `matches_machine` was never true outside
  fixtures. The probe now reports every architecture in the kernel's
  spelling (`probe.NormalizeArch`), and `sourceReuse.platform` accepts
  either. Found by the first run of the store-reuse VM profile.
- **`image` and `targetImgref` were read only after the wipe** (#414): a
  newline, a space or a leading `-` in either reached `skopeo`, `podman` and
  `bootc --target-imgref` after the disk was partitioned, and `-v` would be
  read as a flag. A non-empty value now has no whitespace or control
  character and no `-` first (`image_invalid`, `target_imgref_invalid`), in
  `validate`, `validate --partial` and the install preflight. With
  `sourceReuse` its own rules judge both fields, as before.

### 📝 Documentation

- **The Secure Boot profile** (#342): `SECURITY.md` names the one
  supported profile, image-owned: bootc and bootupd install the image's own
  signed chain, fisherman prefers the `.signed` systemd-boot binary, keeps an
  existing fallback and keeps every signed byte. It lists what fisherman
  does not do (no shim or MokManager staging from image metadata, no MOK
  enrolment, no PK/KEK/db/dbx change) and what a new profile needs first.
  Each guarantee links to the code and the test. No code change.
- **Supported versions and response** (#294): `SECURITY.md` says that `dev`
  and the latest release get security fixes, with no backports, and that
  the response is best effort with no SLA: we aim to acknowledge a report
  within 14 days. README links to `SECURITY.md`.
- **One project home** (#303): README, AGENTS.md and ROADMAP.md say that
  tuna-os/fisherman is the only origin, that projectbluefin/fisherman is a
  fork whose changes come in through a PR here, and that consumers pin
  tuna-os/fisherman. The text that offered either repository as a home is
  gone.
- **An integration guide** (#328): `docs/INTEGRATION.md` says which
  command a frontend runs when, what to take from the probe, the recipe
  contract, how to run the install with the recipe on stdin, how to read
  the event stream, the `error` event and the exit codes, how to test
  without a disk, and how to pin and package fisherman. Tests hold its
  event types, `error` fields, cleanup statuses and exit codes to the code.
- **A recipe field reference** (#320): `docs/RECIPE.md` has every recipe
  key at every depth with its type, whether it is required and its
  default. A reflection test fails on a missing, extra or mistyped row,
  and the page's example is parsed strictly.
- **A command-line reference** (#322): `docs/CLI.md` has every command and
  flag with its value, default and exit codes, and the environment
  variables. A go/ast test holds it to the argument parsers.
- **The VALIDATE.md examples are real** (#326): `TestValidateDocExamples`
  runs every `fisherman validate` example of the page and compares the
  output byte for byte (field and code pairs for an elided one).
- **ROADMAP.md refreshed to 2026-11-12** (#286): re-measured release,
  branch and pin figures; Q3 moved to a dated historical section; Q4
  marked current; a 2027 Q1 forward look.
- **Firn compatibility matrix** (#386): `docs/FIRN-COMPATIBILITY.md` compares
  Firn's schema-2 TOML recipe with the fisherman recipe field by field, from
  the code of both at pinned commits: encryption, bootloader (opposite
  defaults), filesystem and btrfs subvolumes (same name, other layout),
  image references and transports, the user, Flatpaks (install vs. copy),
  and explicit vs. absent values, with the mappings that carry over and the
  ones a converter must refuse. No adapter: out of scope by owner decision;
  fisherman JSON stays authoritative.
- **Getting help uses the support bundle**: TROUBLESHOOTING.md now points to
  `fisherman diagnostics` instead of collecting and cleaning files by hand.

- **TROUBLESHOOTING.md and VALIDATE.md match the code** (#419): the probe
  fields (`disks[]`, `system.ram_bytes`, `system.cpu_threads`/`cpu_cores`,
  `unmet` is advice except `uefi`), the event `type`s and `step_id`s, the
  real error-message prefixes, cancel (teardown, exit 130), `jq` commands
  that skip the non-JSON lines of stdout, `.errors[]`, the full rule order,
  and no `slurpRescueStore` field. New tests hold the problem codes
  (`internal/recipe/docs_test.go`), the event types and step ids
  (`internal/progress/docs_test.go`) and the probe fields
  (`internal/probe/docs_test.go`) to the documents in both directions.
- The `CheckImage` comment no longer claims a digest comparison, and the
  info line says `Image found in local cache, no pull needed`.
- The Flatpak manifest installs the polkit policy that exists
  (`org.tunaos.Installer.policy`). `.goreleaser.yml` no longer sets
  `main.commit`/`main.date`, which do not exist, and its archives, like the
  meson install (`<datadir>/fisherman/data/`), now ship `data/images.json`.

### ♻️ Refactoring

- **The recipe answers its own install-state questions** (#317):
  `recipe.Recipe` has `HasEncryption`, `HasTPM2`, `IsManualLayout`, `IsZFS`,
  `UsesSystemdBoot`, `UsesComposeFs`, `HasVarDisk`, `FormatsVarDisk`,
  `EncryptsRoot` and `EnrollsTPM2` (`internal/recipe/state.go`). The install
  path, `validate` and the host-tool chain call them in place of the
  booleans `main()` computed and passed around. No behaviour change.
- **One step plan, announced by a sequencer** (#316): `planSteps`
  (`cmd/fisherman/steps.go`) builds the ordered step list and its weights
  from the recipe, and `total_steps` is its length. `main()` announces each
  step with `seq.begin`, which supplies the step number, the total and the
  bar position: no manual index any more. A step out of plan is logged as a
  warning at the finalize step. `TestMainAnnouncesThePlan` replays `main()`
  for each recipe variant (plain, LUKS, TPM2, manual layout, ZFS, btrfs
  subvolumes, systemd-boot, source reuse, a `/var` disk) and fails when the
  steps it announces differ from the plan. The events are unchanged.

### 🔧 CI

- **GoReleaser is pinned** (#344): `release-publish.yml` runs GoReleaser
  `v2.20.1`, not `latest`.

### 🧪 Testing

- **The capability report is checked against the bootc that installs**
  (#383): the advisory store reuse VM profile takes
  `fisherman probe --json --source <booted ref>@<booted digest>` inside the
  VM that installs, before it installs that image from its own bootc store.
  The new guest check `scripts/vm-checks/capabilities.sh` then holds the
  report to the installed system: an established `digest_match` root, one
  bootc version in the report, the installing VM and the installed system,
  the recipe's backend, the `print-configuration` bootloader (when it names
  one) and the `container inspect` kernel. The negative case, the same name
  pinned by a digest the VM did not boot, must be `digest_mismatch` with no
  answer from the VM's bootc. `tests/check-validation.sh` proves each check
  catches its bug, also on the probe's own golden output.
- **A VM entry for `btrfsSubvolumes`** (#418): `dakota-btrfs-subvolumes`
  (advisory, PR gate) installs with `"btrfsSubvolumes": true`. The guest
  check `scripts/vm-checks/btrfs-subvolumes.sh` requires the root on the `@`
  subvolume with `subvol=/@,compress=zstd:1`, the same `rootflags` on the
  kernel command line, `/var` inside `@`, and `@` as the only top-level
  subvolume (#416). The harness scripts that write into the installed root
  now mount `@` when the top level holds only it.

- **The TPM2 guest check said "no LUKS device" when ssh had failed**: a
  `dakota-tpm2` run under memory pressure missed ssh's one-second connect
  timeout, and `tpm2-enrolled.sh` read the empty answer as an unencrypted
  guest. It now tries the query again while ssh fails, and says "the guest
  did not answer over SSH" when it never does.

- **The VM matrix covers a user, GRUB with LUKS, TPM2 and a /var disk**

- **A recipe field that never reaches the install fails a test** (#289):
  `recipeFieldUses` (`cmd/fisherman/recipe_consumers_test.go`) lists every
  field of `recipe.Recipe`, nested ones included, with the files that pass
  it on to the install, or the reason it only accepts or refuses the recipe.
  The test fails on a field without an entry, an entry for a field that is
  gone, and a listed file that no longer reads the field. A second test
  holds the `install.Options` projection (`buildBootcInstallOptions`) to a
  source table: each option comes from a named recipe field, an argument, or
  a later step, and follows its recipe field.
, GRUB with LUKS, TPM2 and a /var disk**
  (#418), as advisory PR-gate entries: `centos-bootc-luks-user` (GRUB,
  ostree, XFS, LUKS and a `user` that must sign in with the recipe's
  password), `centos-bootc-var-disk` (`/var` must be the `varDisk` on the
  booted system) and `dakota-tpm2` (`tpm2-luks-passphrase` against swtpm:
  the first boot unlocks with the passphrase and enrolls, the second boot
  types nothing). `scripts/matrix-recipe.py` writes each entry's recipe,
  and `tests/check-validation.sh` runs every one through
  `fisherman validate --json --partial` and holds the new guest checks
  (`scripts/vm-checks/`) to the bugs they catch. A new advisory job builds
  and unit-tests fisherman and the TUI on arm64. Not covered, with the
  reasons in `tests/bootcrew-matrix.yaml`: ZFS, a manual layout, an arm64
  boot, and `tpm2-luks` without a passphrase.
- **The installation receipt checks are required on the required canaries**
  (#385): `required / dakota` (composefs, systemd-boot) and
  `required / centos-bootc` (ostree, GRUB) now run `scripts/boot-verify.sh`
  with `BOOTCREW_RECEIPT_REQUIRED=1`. In #438's run, both canaries got
  `verified` from the first-boot collection and from `verify --live` on the
  installed system. A receipt for another image gave `mismatch`, and the CI
  host's own status gave `unavailable`. The advisory images keep the checks
  advisory.
- **The #418 VM shapes gate** (#423): `centos-bootc-luks-user`,
  `centos-bootc-var-disk` and `dakota-tpm2` are required entries, and the
  arm64 unit tests are a need of `required-canaries`. `bootcrew-vm.yml` also
  runs every night on dev, so these shapes gate the nightly as well as each
  PR. `dakota-tpm2-luks` stays advisory.
- **The VM matrix boots `tpm2-luks` and checks the enabled units** (#418,
  #421, #422): `dakota-tpm2-luks` installs with TPM2 and no passphrase
  against swtpm. The harness takes the recovery key from the install's
  `recovery_key` event (`scripts/capture-recovery-key.py`: a 0600 file
  under /tmp, the event printed with the key redacted, the key masked on
  GitHub), types it on the first boot, and the second boot must unlock with
  no prompt (`scripts/vm-checks/no-passphrase-prompt.sh`, also on
  `dakota-tpm2`). The required `centos-bootc` canary runs
  `scripts/vm-checks/enabled-units.sh`: the booted system must enable no
  unit that the image's own `/etc` (at `/usr/etc`) and presets do not
  enable, except an allowlist of fisherman's own units and the harness's
  ssh. The check passed as an advisory entry first. `dakota-tpm2-luks` is
  advisory. `tests/check-validation.sh` holds the
  check, the capture and the prompt check to the cases they catch.

### ✨ Features

- **Install from the live host's bootc store: `sourceReuse`** (#384,
  phase 2): an explicit, experimental recipe field (`"experimental":
  ["bootc_store_reuse"]`) that names an image the host's bootc keeps by its
  manifest digest and platform. On an installed, bootc-booted ostree host
  the install checks that the image's ostree ref still points at the
  deployment, estimates scratch and disk and refuses when they will not fit,
  runs `bootc image copy-to-storage --source <ref> --target
  localhost/fisherman-reuse:<tag>` with `/var/tmp` on its own scratch,
  verifies the copy (source unmoved, deployment unchanged, sha256 digest,
  platform), installs from it with the recipe's `targetImgref` as the update
  reference, checks the installed digest against the copy, and removes the
  copy, the bind and the scratch on success, failure and cancel. Every
  command passes a strict allowlist: no `set-unified`, pull, `image cmd`,
  GC or credentials. Never part of probe, validate or a recipe without the
  field. 26 stable `source_reuse_*` codes (docs/VALIDATE.md), the
  `source_reuse` progress event (docs/JSON-PROGRESS-FORMAT.md), and an
  advisory offline VM profile in `bootcrew-vm.yml` that installs a VM from
  its own store onto a second disk with no registry reachable, boots the
  result and checks its identity. docs/SOURCE-REUSE.md.

- **`receipt` progress event** (#439): after it writes the installation
  receipt, the install emits `{"type":"receipt","version":1,"written":…,
  "install_id":…,"path":…,"first_boot_check":…}`, with a stable `reason`
  (`resolve_failed`, `build_failed`, `write_failed`, `transient_etc`,
  `collector_failed`) when the receipt or the first-boot collector is
  missing, so a frontend's done page can show the install id and key its
  copy on the outcome instead of on the English log line, which stays.
  It never fails the install and holds nothing the receipt does not hold.
  A collector that fails to stage after the receipt was written is now
  reported as written (it was logged as "not written"). Fixtures:
  `internal/progress/testdata/receipt-events.ndjson`.
- **Installed-system identity receipt and `fisherman verify --json`**
  (#385): the install writes a non-secret receipt to the installed
  system's `/var/lib/fisherman/install-receipt.json` (through the
  deployment resolver): install id and time, fisherman version, backend,
  deployment id (ostree commit or composefs verity), the expected platform
  manifest digest (from the installed ostree commit or verified media,
  never from a pinned reference, which can name an index), platform and
  update reference. No passphrase, password, user name, hostname,
  machine-id or registry credential. A oneshot unit in the deployment's
  `/etc` runs once at the first boot, stores the image's own `bootc status
  --format json --format-version 1` next to the receipt, and changes
  nothing else. `fisherman verify --json` compares the two and answers
  `verified`, `mismatch`, `unsupported`, `unavailable` or `not-yet-run`
  with a stable reason; a staged, cached or rollback image is never the
  booted one, and a missing or unreadable status is never success. The
  install itself still says only "installed". `scripts/boot-verify.sh`
  checks the receipt in every VM boot (advisory), with a wrong-target and a
  wrong-context negative. Schemas, codes and fixtures: docs/RECEIPT.md,
  `internal/receipt/testdata/`.
- **bootc store inventory in `fisherman probe --json`** (#384, phase 1):
  a new `bootc_store` object with its own `version` (1), present only when
  the capability report found a bootc-booted host; otherwise `available` is
  `false` with a reason (`not_bootc_booted`, `bootc_missing`,
  `check_failed`, `malformed_output`). Each item names its owner (`bootc`),
  context (`live-host`), storage (`ostree`, `composefs`,
  `bootc-containers-storage`), roles, availability (`deployed`, `stored`,
  `listed`), manifest digest and platform, each with a reason when unknown,
  and `matches_machine`. The default probe reads only the `bootc status
  --json` it already ran and bootc's store index as a file: no extra
  command, no write, no network. `bootc image list --type host --format
  json` runs only with `--bootc-image-list`, as root, on an installed
  ostree host, because bootc 1.17.1 needs root for it and opens its store
  to answer (on composefs it mounts the ESP read-write). References with
  credentials are redacted. Reuse through `copy-to-storage` (phase 2) is
  not implemented: it needs a VM to qualify. Golden fixtures in
  `internal/probe/testdata/golden/store-*.json`; contract in docs/PROBE.md.

- **Source-scoped bootc capabilities in `fisherman probe --json`** (#383):
  a new `capabilities` object with its own `version` (1). `--source <ref>`,
  `--live-root` and `--target-imgref <ref>` select the source; the live
  host, the source, the execution root and the target are separate objects.
  The probe runs bootc for the source only when the host booted the
  source's exact digest, and there only `bootc container inspect --json`,
  `bootc install print-configuration` and `bootc install finalize --help`.
  Six features (`native_inspection`, `install_configuration`,
  `finalization`, `deployment_mount`, `unified_storage`,
  `bootc_image_storage`) answer `supported`/`unsupported`/`unknown` with a
  stable reason and the context that established it; `supported` needs an
  observed command, never a version or help text. Adapters copy only
  documented fields, and every probe command is now bounded (20 s, 8 MiB,
  stderr discarded). No pull, mount, container or registry access. Golden
  fixtures in `internal/probe/testdata/golden/caps-*.json`; contract in
  docs/PROBE.md.

- **fisherman sets `BootNext` itself** (tuna-os/bootc-installer#318): after
  the install it points the firmware's next boot at the installed system's
  entry, while it is root. On live media by default; elsewhere only with
  `"setBootNext": true`; `false` never. A failure is a warning. The
  `complete` event still carries `boot_id`.

- **Locale, keyboard layout and time zone** (#405): optional recipe fields
  `locale`, `keyboard` (`layout`, `variant`, `model`) and `timezone`.
  fisherman writes `/etc/locale.conf`, `/etc/vconsole.conf` (console keymap
  from systemd's `kbd-model-map`, and the XKB keys), the X11 keyboard file
  and `/etc/localtime` into the installed deployment. For an encrypted
  install the keymap is added to the boot entries as `rd.vconsole.keymap=`,
  so the passphrase prompt uses the layout the passphrase was typed with.
  Rules with stable codes (`locale_invalid`, `keyboard_layout_required`,
  `keyboard_layout_invalid`, `keyboard_variant_invalid`,
  `keyboard_model_invalid`, `timezone_invalid`), also in `--partial`.

- **Recipe on stdin**: `fisherman -` and `fisherman validate -` read the recipe
  JSON from stdin until EOF. A frontend pipes it through
  `pkexec`/`sudo`/`flatpak-spawn --host` instead of writing a temporary file
  that holds the LUKS passphrase and user password. Empty stdin fails with
  `loading recipe: recipe is empty`; recipes are capped at 1 MiB. A file
  path works as before, and a lone `-` is no longer reported as an unknown
  command (exit 2).
- **fisherman finds offline image stores on the host**: when a recipe omits
  `additionalImageStores`, fisherman exposes the stores `fisherman probe`
  reports (`$TUNA_OFFLINE_STORES`, `/etc/tuna-installer/offline-stores`,
  `/usr/share/tuna-installer/oci-store`, `/var/lib/superiso-store`) and logs
  them as an `info` event. Four frontends looked for these inside their
  Flatpak sandbox, where the host's paths are not visible. A recipe that sets
  the field (`[]` included) is used exactly as given.
- **Empty `image` is rejected off live media**: it means "install the running
  live image", so on a host that did not boot from live media (probe's live
  detection: `/run/ostree-live`, `rd.live.image`/`root=live:`, or
  `/etc/bootc-installer/live-iso-mode`) the install and `fisherman validate`
  now fail with `invalid recipe: image is required: …` before touching a disk,
  instead of reaching `bootc install` with nothing to install after
  partitioning. On live media the behaviour is unchanged.
- **`fisherman validate --json`**: every recipe problem as one JSON object,
  `{"protocol_version":1,"valid":…,"errors":[{"field","code","message"}]}`,
  with stable codes frontends key their copy on. Exit 0 valid, 1 invalid, 2
  bad arguments; `-` reads the recipe from stdin. `--partial` checks only the
  fields present and reads nothing from the machine, so a frontend can
  validate a page as it is filled in. The plain `fisherman validate` output is
  unchanged. Schema and codes in `docs/VALIDATE.md`.
- **One rule set for validate and install** (`internal/recipe/rules.go`):
  `Recipe.Problems()` reports every problem where `Validate()` still returns
  the first, with its existing messages. New rules: the hostname (RFC 1123
  labels, at most 64 characters), the username (at most 32 characters,
  lowercase letter or `_` first, then `[a-z0-9_-]`, not a reserved system
  user or group such as `root`, `wheel` or `systemd-*`), and a `tpm2-*`
  encryption type on a machine without a usable TPM 2.0 (probe's
  `tpm.usable`).
  An empty `image` off live media (above) is reported as `image_required`.
- **`data/encryption-choices.json`**: the encryption types, in display order,
  with `needs_passphrase`, `needs_tpm2` and the branding `copy_key`, generated
  from the Go table the rules use (a test fails if they differ).
  bootc-installer copies it to `shared/recipe/encryption-choices.json`.

- **Progress events carry the finished bar position and a stable step id**:
  additive fields on the JSON progress protocol, so frontends render the bar
  instead of re-deriving it. Every existing field and event is unchanged.
  - `overall_pct` (number, 0–100, two decimals) on `step`, `substep` and
    `complete`. It is the bar position after that event, ported from
    bootc-installer's reference parser (`shared/progress/progress_parser.py`)
    and checked against its golden `fraction-cases.json`: a step starts at its
    `cumulative_pct`; `Pulling image: layer N/M` fills the first 60% of the
    step; the post-pull bootc phases sit at fixed points in the rest; other
    substeps hold the bar. It never decreases, stays at or below 99 until
    `complete`, and `complete` is exactly 100.
  - New over the reference parser: `Copying Flatpak data: N%` now moves the bar
    across the Flatpak step's weight (11% of the bar, 29% when the image was
    cached), where every frontend's bar used to stand still.
  - `step_id` on `step` events: a stable snake_case id per step (`partition`,
    `format_efi`, `luks`, `format_root`, `mount`, `prepare_disk`, `format_var`,
    `install_os`, `tpm2_enroll`, `flatpaks`, `configure`, `finalize`), from one
    table in `internal/progress/bar.go`. Ids never change; `step_name` stays
    the English name.
- **Clean cancel on SIGTERM, SIGINT and SIGHUP**: fisherman used to die at once
  when a frontend cancelled an install, leaving the target mounted, the LUKS
  mapping open and the `/var/fisherman-tmp` bind mount in place, with no JSON
  event. It now stops its child processes (SIGTERM, then SIGKILL after 10 s,
  grandchildren included), runs the same teardown as a fatal error, emits one
  `{"type":"error","message":"installation cancelled (SIGTERM)"}` event and
  exits with the new code **130**. Failures still exit 1 and bad usage 2. A
  second signal during teardown is logged and ignored rather than aborting
  it; teardown is bounded at 3 minutes instead. A teardown failure is now
  appended to the error message (`…; cleanup failed, …`), and on the failure
  path the `error` event is emitted after teardown rather than before it.
- **Cancel by killing the wrapper**: frontends run fisherman as root through
  `pkexec`, so their SIGTERM gets EPERM and never reaches it. fisherman now
  sets `PR_SET_PDEATHSIG` to SIGTERM, so when its parent (the frontend's
  `bash` wrapper, `sudo`, or the shell) dies, it cancels through the same path
  and exits 130. Frontend contract: kill the wrapper you spawned, in its own
  process group, and fisherman cancels cleanly.
- **`fisherman probe --json`**: a read-only, no-root command that prints the
  disks (with eligibility and the reason a disk is excluded), TPM, RAM/CPU/UEFI,
  live-media and offline-store facts every installer frontend computed for
  itself, as one JSON object. Schema and example in `docs/PROBE.md`.

### 🔒 Security

- **Desktop and product steps are opt-in** (#412). `cups-browsed`,
  `avahi-daemon` and `ipp-usb`, the Homebrew OEM unit, the WirePlumber rules
  and `rhgb quiet` were applied to every image. They now need the recipe's
  `extras` object or an `extras` entry in the image catalog, and are off for
  an image fisherman does not know. A unit is enabled only when the
  installed image has it. The running session's audio configuration is
  changed only on live media. Every image of the shipped catalog keeps all
  four.

- **First-boot TPM2 enrolment never stages the user's passphrase** (#409,
  #360). The install adds a random key in its own LUKS keyslot (31) and
  stages only that, in the installed deployment's `/etc`. The unit retries
  at most three times in one boot, then removes the keyslot and the file in
  `ExecStopPost=`, which also runs when the enrolment fails. The unit and
  key were staged in the physical sysroot before, where no booted system
  reads them: the enrolment never ran and the key stayed on the disk.
  `systemd-cryptenroll` is now looked for in the installed image, not on the
  installer host.
- **Host Wi-Fi profiles and Bluetooth keys are copied only on live media**
  (#411), or when the recipe sets `copyNetworkProfiles` /
  `copyBluetoothPairings` to `true`. `false` opts out on live media.

- **`distroID` and `brewTap` are validated before use** (#299): both are
  written as root into generated paths, unit names and the OEM setup shell
  script (`/etc/<distroID>/oem/`, `<distroID>-oem-setup.service`,
  `brew tap <brewTap>`), unchecked, so a recipe could traverse paths, inject
  unit directives or run shell commands. `distroID` must match
  `^[a-z][a-z0-9-]{0,62}$` (`distro_id_invalid`) and `brewTap` an
  `owner/name` pair of letters, digits, `.`, `_` and `-` (`brew_tap_invalid`);
  empty values keep their defaults.
- **Recipe host paths cannot escape the target** (#333): root appended
  `customMounts[].target` to the target mount, mounted the root filesystem at
  `targetMount`, and joined `flatpakVarPath` under the installed root, all
  unchecked, so `/../../etc` or `targetMount: "/"` would mount a partition
  over the host. The install and `fisherman validate` now refuse a relative
  or `..`-bearing custom target (`custom_mount_target_invalid`), a
  `targetMount` that is `/`, relative or `..`-bearing
  (`target_mount_invalid`), and a `..`-bearing `flatpakVarPath`
  (`flatpak_var_path_invalid`), before any disk work. Every value that worked
  before (`/boot/efi`, `/mnt/altroot`, `state/os/default/var`, …) still does.

### 🐛 Bug Fixes

- **ZFS: the pool is exported, and its host files go where the system
  boots** (#413). The pool is registered for teardown and exported after the
  unmounts, on success and on every failure; it stayed imported on the
  installer host before. `hostid` and `zpool.cache` go to the deployment's
  `/etc` through the resolver, not to `<target>/etc`. `filesystem: zfs` is
  experimental: `validate` and the install refuse it (`zfs_experimental`)
  unless the recipe has `"experimental": ["zfs"]`.

- **`btrfsSubvolumes` creates `@` only** (#416). `@home` and `@snapshots`
  were created and never mounted by anything. The remount after the GPT
  retag of a systemd-boot install now uses the root mount options, so on
  btrfs it lands in `@` with compression and not in the top-level subvolume.

- **`flatpaks` means what the code does** (#408): absent copies the host's
  system Flatpaks, `[]` copies nothing, and a list copies the whole
  installation too, with a log line that says it is not a filter and a
  warning for each listed app the host lacks. The lines "Installing N
  per-image flatpak apps" and "using 3 fallback apps" are gone: nothing was
  ever installed or downloaded. fisherman no longer runs
  `flatpak install --system` on the installer host.
- **The Flatpak copy cannot hang** (#358): when `tar x` exits early (a full
  target), the read end of the pipe is closed, `tar c` is killed and both are
  reaped. A producer that cannot start no longer leaves the extractor
  behind.

- **`varDisk` with an encrypted root is refused** (#406): fisherman has no
  LUKS step for the `/var` disk, so `/var` and `/var/home` stayed plain text
  beside an encrypted root. Validation now fails with
  `var_disk_encryption_unsupported` before any disk is touched.
- **`varDisk` is refused on composefs-native targets** (#252): with
  `composeFsBackend` or `filesystem: zfs`, the initramfs mounts the
  deployment's own `/var`, so the separate disk was formatted but never used.
  Validation now fails with `var_disk_unsupported_composefs` before any disk
  is partitioned or formatted. ostree-backed `varDisk` installs are unchanged.
- **The `/var` disk fstab line is correct, and the disk is released before
  the format** (#406): the type comes from `blkid` (a kept ext4 or btrfs disk
  got an `xfs` line and the boot dropped to emergency mode), the options are
  `defaults,nofail,x-systemd.device-timeout=30s,x-systemd.before=local-fs.target`,
  and a missing UUID or a failed write ends the install (it was a warning).
  Before `mkfs.xfs`, the disk is unmounted, its LVM and device-mapper holders
  are removed and old signatures are wiped. A kept disk is checked with
  `blkid` before any disk work (`kept_filesystem_unusable`).
- **Data and swap entries of a manual layout stay mounted after the reboot**
  (#372): each gets an fstab line in the deployment (UUID and type from
  `blkid`, `nofail`, mountpoint resolved in the installed image). A device
  for a path below `/var` is mounted on the deployment's `var` for the
  post-install steps, and a manual `/var` partition is the resolver's `/var`,
  so the user's home and the Flatpaks are on them. New rules refuse a layout
  before any disk work: `custom_mount_target_duplicate`,
  `custom_mount_partition_duplicate` and `custom_mount_not_persistable` (a
  target on the read-only root, or `/var` on a composefs-native target).
- **A missing `rd.luks.name` ends an encrypted install** (#410): the
  injection was a warning, and no loader entry at all was silent, so the
  install reported `complete` for a system that could not find its root.
  fisherman now passes `--karg=rd.luks.name=<UUID>=root` to bootc where the
  layout has no GPT auto-discovery (every layout except systemd-boot with
  composefs), reads every BLS entry back, patches what is missing, and fails
  if any entry lacks the argument. A LUKS UUID that cannot be read fails
  before bootc runs.
- **One deployment resolver** (`internal/deploy`) says where the booted
  system's root, `/etc` and `/var` are on the mounted target, for ostree and
  composefs. Bluetooth pairings and migrated wallpapers went to
  `<target>/var`, and audio configuration to `<target>/etc`, on ostree: the
  physical sysroot, which is never mounted there (#407).
- **No cache warming, no thumbnails** (#407). The warmers looked for
  `<target>/usr`, which exists on neither layout, did nothing, and logged
  "Pre-warmed 8/8 system caches". Pointed at the real deployment they would
  write the host's caches into a tree that is hard-linked into the ostree
  repository. They are removed; an image carries its own caches.
- **Target-resident scratch is deleted while the target is mounted** (#307):
  before finalize on success, first thing in teardown otherwise. It was
  deleted after the unmount, which removed an empty mountpoint on the host
  and left the image cache on the installed disk. A scratch bind mount that
  fails leaves no directory behind (#291).

- **Bound offline media accepts full uncompressed images**: the aggregate unique
  blob budget now matches the existing 16 GiB per-blob ceiling instead of rejecting
  valid raw layers above 10 GiB. Layer bytes are still streamed and hashed once per
  digest; metadata, layer-count, read-only, platform and symlink checks are unchanged.
- **composefs installs accept a plaintext user password**
  (projectbluefin/utah#1129): `chpasswd --root` hashes a plaintext password
  through PAM, and a composefs deploy root holds only `/etc` state, so user
  creation failed with `pam_chauthtok() failed, error: Module is unknown`.
  fisherman now hashes it in-process to a SHA-512 crypt (`$6$`) string and
  passes it with `chpasswd -e`. Pre-hashed passwords and ostree installs are
  unchanged.

- **Offline OCI imports use target-disk scratch** (#345): Podman's image store,
  runtime state, and host-side blob staging no longer fill the RAM-backed live
  filesystem before the bootc container starts. This also covers the VFS
  fallback and preserves the verified local OCI source without a registry pull.
- **A requested data migration is refused before any disk work** (#245):
  `slurp` copied the selected Windows data into RAM only, and an extraction
  error or partial copy was logged as "skipped" while the install went on to
  partition the disk holding the source. A recipe with `slurp` now fails
  validation (`slurp_unsupported`) in `validate` and in the install, before
  any tool, audio, data or disk step, until a durable rescue store exists.
  `slurpWallpapers` is unchanged.
- **A bad username no longer fails after the OS is on disk**: `useradd` in
  the configure step was the first thing to check it, so `Alice`, `root` or
  `my user` failed the install after partitioning and the OS install. The
  install now rejects it with the other recipe rules, before any disk step,
  and so does the hostname (only one frontend checked it) and a TPM type with
  no TPM 2.0 (which failed at enrolment).

- **Missing supplementary groups no longer abort the install** (#231): `useradd`
  exits 6 when a `--groups` entry does not exist on the target (e.g. `libvirt` on
  images without virt-manager). Requested groups are now filtered against the
  target `/etc/group`, and missing ones are skipped with a warning.
- **Offline non-composefs installs from a live ISO export from the right store**
  (#236): a `containers-storage:` source is never pulled into the redirected
  podman root, so the OCI export now uses the original ref unless the image was
  actually pulled there.
- **Local non-composefs images install with the host's bootc** (#237): a GUI
  install of a live ISO's embedded GRUB/ostree image now runs `bootc` directly
  against the live store with that source as `--source-imgref`, avoiding the OCI
  export and the podman OOM seen in 8 GiB VMs. Composefs keeps the container path.
- **Explicit local image identity survives storage redirects** (#244): an
  explicit `containers-storage:` source is checked locally, and a missing or
  malformed one is refused before any disk work instead of triggering a registry
  lookup.

### 🧪 Tests

- **Slurp unit tests no longer depend on the host's `/run`** (#388): the
  data-migration full-flow test stubs the `/run` capacity observation, so it
  passes on hosts with under 2 GiB free in `/run`. Zero budget and a failed
  capacity observation are now covered: nothing is copied, nothing is reported
  as migrated, and the Windows partition is still unmounted. The production
  budget (real `/run`, 2 GiB reserve) is unchanged.

### 📝 Documentation

- ROADMAP release/branch status refreshed after v0.3.0/v0.4.0 (#240).

---

## [0.4.0] - 2026-09-19

Release-process-only release; no installer behaviour changed since 0.3.0.

### 🚀 Release Automation

- **Cut publishes the tag it just made** (#227): `release-cut.yml` now calls
  `release-publish.yml` via `workflow_call` instead of relying on a tag-push
  trigger that never fires for `GITHUB_TOKEN`-pushed tags, and a
  `workflow_dispatch` recovery path republishes a tag that has no assets.
- **Automatic promotion**: `promote.yml` promotes `dev` to `prod` and releases
  once `dev` is green; `promote-reconcile.yml` reconciles `prod` back into `dev`
  (not the reverse). `scripts/next-version.sh` computes the next version, with
  tests.
- **Two-step release documented** (#228): `CONTRIBUTING.md` gains a Releasing
  section, and the promotion PR body warns that it must be merge-committed, never
  squashed, into `prod`.

---

## [0.3.0] - 2026-09-19

### 🔒 Security

- **Transient TPM2 enrolment key is pinned to 0600** (#221): `StageFirstBootEnrollment`
  wrote the plaintext LUKS unlock key with `os.WriteFile(…, 0o600)`, whose mode
  applies only to a file it creates and is masked by the process umask. An explicit
  `os.Chmod` now guarantees root-only access in the window before the first-boot
  oneshot shreds the key.

### 🐛 Bug Fixes

- **Root partition carries the DPS type from creation** (#219): sealed composefs
  images boot from a UKI with no `root=` on its command line, so
  `systemd-gpt-auto-generator` has to discover the root by GPT type.
  `PartitionSystemdBoot` typed it generically and only unencrypted installs were
  retagged afterwards, so encrypted sealed-composefs installs hung ~90s on
  `/dev/gpt-auto-root` and dropped to an emergency shell. The type is now set when
  the table is written, which covers the encrypted layout the post-install retag
  cannot reach, and it is architecture-aware (x86-64 and aarch64).
- **ENOSPC installing a large image from a live ISO** (#211): in direct mode
  (no podman wrapper) `bootc` staged layer blobs under the RAM-backed `/var/tmp`
  of the dracut overlay while the OCI cache itself sat on the target disk, so a
  ~10 GiB image died part way through "Copying blob". Direct mode now gets the
  same scratch-backed `/var/tmp` and `TMPDIR` the OCI export already used.
- **TUI started the backend with a command that does not exist** (#178): it ran
  `fisherman install --recipe <path>`, but the backend takes the recipe as a bare
  positional argument, so it tried to load a recipe named "install" and every
  non-dry-run install failed before touching the disk. The argv is now built by
  one function covered by a cross-module contract test against a fake backend.
- **Unknown subcommands are named** (#178): `fisherman install …` fell through to
  `recipe.Load("install")` and reported a missing file, pointing at the wrong
  thing. A flag, or a bare word that is not a file, now reports an unknown command
  and prints help; the bare recipe-path form is untouched.

### 🧪 CI

- **Invalid bootcrew matrix entries removed** (#106): `ubuntu-bootc` and
  `opensuse-bootc` still paired `filesystem: xfs` with `composefs_backend: true`,
  which the recipe validator rejects outright ("composefs-backend requires
  fs-verity, which XFS does not support"), so those installs exited before they
  started. #97 dropped the same pair from three other entries but did not reach
  these two, because `vm_boot: false` keeps them out of the PR gate while nightly
  still runs every entry. `tests/check-validation.sh` now fails on the pair so it
  cannot come back quietly.
- **The validation harness runs in CI** (#106): `tests/check-validation.sh` is
  what AGENTS.md asks contributors to run before every push, but no workflow
  executed it, so nothing enforced it. It is now a step in `bootcrew-vm.yml`'s
  `lint` job — no disk, VM or network, about a second.
- **`tui/` runs in CI** (#204): the module had real tests and no workflow ever
  executed them — every job scoped to `fisherman/`. `bootcrew-vm.yml` gains a
  `tui-unit-tests` job running `go vet` and `go test -race` with coverage upload.

- **OCI layout for non-composefs installs**: Non-composefs images (bluefin, lts,
  lts-hwe) now export to an OCI layout at scratch and use `--source-imgref oci:...`
  for `bootc install to-filesystem`. The previous VFS squash path corrupted ostree
  commit structure, producing “Expected commit object, not File”. The overlay
  redirect eliminates ENOSPC on 8+ GiB images by placing working container layers
  on the target disk instead of the 1.4 GiB live overlayfs.
- **`--source-imgref` in direct mode**: Direct mode now emits `--source-imgref`
  so `bootc install to-filesystem` does not fail with “must be executed inside
  a podman container” on offline live-ISO installs.

### 📊 Partition Layout

- **2 GiB /boot**: grub2 `/boot` ext4 partition increased from 1 GiB → 2 GiB.
  Each kernel+initramfs pair (200–400 MiB with NVIDIA drivers) fills 1 GiB quickly
  once deployment + rollback + staged upgrade accumulate.
- **2 GiB ESP for all fleet images**: grub2 EFI System Partition increased from
  512 MiB → 2 GiB for fleet consistency with systemd-boot (dakota) images.

### 🔒 Security (other)

- Temporary LUKS passphrase file in `EnrollTPM2` is created 0600 (#213); the TUI
  writes its installer recipe through a secure temporary file (#216).
- The SELinux bypass shim is built in a private temp dir (#161); test images
  require SSH key auth (#172); the debian-bootc base image is pinned by digest
  (#76); `GITHUB_TOKEN` is passed via env in `bootcrew-vm.yml` (#78); Renovate
  automerge is restricted to patch/pin/digest updates (#109).

### ✨ Features

- **Structured error event on fatal install failure** (#195) in the progress
  stream.
- **TPM2 enrolment moved to first boot** of the installed system instead of
  during install (#48).
- `customMount` fstypes are validated and encryption is rejected on manual
  layouts (#58); ext4 verity is enabled on the manual layout path (#70).

### 🧹 Maintenance

- TUI Go module renamed `projectbluefin` → `tuna-os` (#113); the orphaned Python
  GTK installer copy (`tuna_installer/`) was removed (#114); the TUI stays on the
  charmbracelet v1 APIs (#130, #135).
- Release config repaired so a tag can publish (#225).
- Unit/E2E coverage expanded across `disk`, `install`, `post`, `images` and
  `customMounts` (#94, #100, #107, #115, #118, #119, #125, #142, #148, #149,
  #151).
- Added ROADMAP (#163), CONTRIBUTING (#169), agent guide/CODEOWNERS/EditorConfig
  (#201); switched from Dependabot to Renovate.

---

## [0.2.0] - 2026-05-08

### 🚀 Major Features

#### Storage & Filesystem Support
- **Composefs Overlay Storage**: Full support for overlay storage driver with composefs installations
- **ext4 Root Filesystem**: Added ext4 with verity support for composefs backend
- **Improved Partition Layout**: Auto-detect and optimize storage driver selection

#### systemd-boot & EFI Improvements
- **EFI Fallback Installation**: Robust systemd-boot EFI binary installation with fallback paths
- **Dynamic BOOTX64.EFI Handling**: Search multiple ostree deployment paths for systemd-boot binary
- **GPT Retagging Fixes**: Proper EFI remount handling during partition retagging

#### Release Automation
- **GoReleaser Integration**: Added `.goreleaser.yml` for automated binary releases
- **Release Workflows**: New `release-cut.yml` and `release-publish.yml` workflows
- **Dependabot Updates**: Added `.github/dependabot.yml` for automated dependency checks
- **GolangCI Linting**: Added `.golangci.yml` for consistent code quality

### 🐛 Bug Fixes

#### SSH & Boot Verification
- Enable systemd-networkd DHCP in SSH test images
- Add SSH troubleshooting to boot-verify script
- Improve SSH enablement in Containerfile for debian-bootc

#### Composefs & Storage
- Fix systemd.wants=ssh.service on composefs boot parameters
- Redirect podman container storage to scratch for composefs installs
- Improve overlayfs /var scratch directory detection
- Enable overlay storage driver on btrfs (previously disabled)

#### QEMU & Testing
- Add explicit QEMU boot order for composefs VM testing
- Revert ineffective QEMU boot order and systemd-boot timeout fixes
- Improve boot configuration detection and patching logic

### 📊 Test Coverage
- Add loopback device storage driver tests
- Add LUKS unlocking tests with Python helper script
- Add post-install verification tests
- Expand SSH image building for all VM boot test images

### 🔨 Improvements
- Refactor test matrix with Dakota and improved boot verification
- Enhance script discovery for CI/local portability
- Add `scripts/luks-unlock.py` for LUKS testing automation
- Improve `verify-installation.sh` with comprehensive status checks

---

## [0.1.0] - 2026-03-27

### Initial Release
- First stable release of fisherman bootc installer backend
- Support for bootc install with custom partitioning
- LUKS encryption support
- Composefs with systemd-boot support
- Image catalog and customization
- flatpak installation support
- Post-install hostname and user creation
