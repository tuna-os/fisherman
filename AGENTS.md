# AGENTS.md — agent guide for tuna-os/fisherman

A universal **bootc installer backend**: a Go CLI that reads a JSON recipe and
executes a disk install pipeline, emitting newline-delimited JSON
progress on stdout. The recipe decides how many progress steps a run reports:
8 for a plain auto-partitioned install, one more each for LUKS, TPM2 enrolment
and a `/var` disk, and 5 or 6 for a `customMounts` layout. `planShape` in
`fisherman/cmd/fisherman/steps.go` is the one list; do not hard-code a count. It is driven by a frontend (the five in `tuna-os/installer`),
runs as root against real block devices, and is distro-agnostic.

Human docs: [`README.md`](README.md) (pipeline table, recipe format, partition
layouts), [`CONTRIBUTING.md`](CONTRIBUTING.md), [`docs/`](docs/).

## Two things to get right before you start

**The default branch is `dev`, not `main`.** Branch from it and target it in
PRs. `dev` carries the TunaOS fixes.

**This repository is the only origin.** Send issues and PRs here.
[`projectbluefin/fisherman`](https://github.com/projectbluefin/fisherman) is a
fork, not the upstream; its changes reach fisherman only through a PR here.
Consumers pin `https://github.com/tuna-os/fisherman`. It is consumed as a git
submodule by `tuna-os/installer`, so a change here is not live for that
frontend until its submodule pointer is bumped in a separate commit.

## Layout

| Path | What |
|---|---|
| `fisherman/` | the CLI — its **own Go module** (`cmd/fisherman`, `internal/`) |
| `fisherman/cmd/fishermand/` | the Varlink installer daemon. Without `--mock` it runs the real executor, which partitions and overwrites the target disk, can change LUKS key slots and reboots; `--mock` touches nothing. See [`docs/FISHERMAND.md`](docs/FISHERMAND.md) |
| `tui/` | a **second, separate Go module** |
| `tests/` | shell harnesses + `fake-bins/`; `check-validation.sh` runs entirely in `/tmp`, no disk or VM |
| `data/` | image catalog and assets; TunaOS marks are the Noto emoji marks from `tuna-os/tunaOS` `system_files/usr/share/tunaos/logos/` |
| `justfile` | `build`, `install`, `test-checks`, `ci-install-tools`, `cleanup-loop` |

Two modules means `go build ./...` at the repo root does **not** cover
everything — build each module from its own directory.

```bash
just build                      # cd fisherman && go build -o /tmp/fisherman ./cmd/fisherman/
just test-checks                # bash tests/check-validation.sh
cd fisherman && go vet ./...    # and golangci-lint, config in .golangci.yml
```

## Invariants — do not "simplify" these

Each of these encodes a failure that has already happened. They are load-bearing.

- **3-partition GPT for GRUB images, always** (EFI + ext4 `/boot` + root), even
  unencrypted. GRUB cannot read modern XFS features (`nrext64`, `exchange`,
  `rmapbt`), so `/boot` must be ext4 on its own partition. systemd-boot images
  (dakota) use 2 partitions instead, with the kernel and initrd directly on the
  ESP. See the partition-layout table in the README.
- **2 GiB ESP across the whole fleet.** A kernel+initrd pair is 200–400 MiB;
  2 GiB holds the booted entry, a rollback, and a staged upgrade at once.
- **Scratch space is `/var/fisherman-tmp`** (`fisherman/cmd/fisherman/main.go`),
  bind-mounted to `/var/tmp`. Do **not** move it under `/run/*` — that is tmpfs
  and far too small for image blobs.
- **`--skip-finalize` is passed to `bootc install`** (`internal/install/bootc.go`)
  so the last step ("Finalizing installation") can finalize manually
  (fstrim → remount ro → fsfreeze/thaw) and so the target stays writable for
  post-install writes. `bootc install finalize` is a no-op upstream; removing
  the flag breaks that step.
- **Flatpak sandbox detection.** When running inside a sandbox, host subprocess
  calls are wrapped in `flatpak-spawn --host`. Any new host command must go
  through the same path or it will fail only in the Flatpak frontends.

## Testing reality

`tests/check-validation.sh` is a meta-test: it verifies that the E2E checks in
`verify-installation.sh` and `boot-verify.sh` actually catch the bugs they claim
to. It needs no disk or VM and should pass before every push.

Real installs are exercised by the VM workflows (`bootcrew-vm.yml`,
`bootcrew-nightly.yml`, `custom-mounts-e2e.yml`) against the matrix in
`tests/bootcrew-matrix.yaml`. They are slow and need KVM, so they are the CI's
job, not something to reproduce locally by default.

Because this code partitions and formats disks as root, never test a change by
pointing it at a real device. Use the VM matrix or the fake-bins harness.
