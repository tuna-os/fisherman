# Bootcrew Testing: Using `just` for CI & Local Development

## Overview

The bootcrew E2E testing workflow has been refactored to use `just` recipes as the single source of truth for both CI (GitHub Actions) and local testing. This makes troubleshooting much easier—any failure in CI can be reproduced locally by running the same `just` commands.

## Architecture

```
┌─────────────────────────────────────────┐
│     GitHub Actions (bootcrew-vm.yml)    │
│  - Parse image matrix                   │
│  - Install just                         │
│  - Run: just bootcrew-ci-test <JSON>    │
└────────────┬────────────────────────────┘
             │
             ▼
     ┌───────────────────┐
     │   justfile        │
     │ - bootcrew-ci-test│
     │ - prepare-ssh-img │
     │ - boot-verify     │
     │ - verify-install  │
     └────────┬──────────┘
              │
              ▼
     ┌───────────────────────────┐
     │   scripts/ (shell scripts) │
     │ - boot-verify.sh          │
     │ - prepare-ssh-image.sh    │
     │ - verify-installation.sh  │
     └───────────────────────────┘
```

## Files Created

### `justfile` (refactored)
- Core recipe: `bootcrew-ci-test` — runs full E2E test for one image
- Helper recipes: `setup-ssh-keys`, `build`, `prepare-ssh-image`, etc.
- CI recipes: `ci-install-tools`, `bootcrew-ci-test`
- All complex logic delegated to scripts (3-4 lines max per recipe)

**Key recipes:**
- `just bootcrew-vm` — Full test locally (default: centos-bootc, ostree)
- `just bootcrew-ci-test '{...}'` — Single matrix entry (used by CI)
- `just boot-verify` — Boot VM and verify bootc status
- `just prepare-ssh-image IMAGE` — Add SSH to container image
- `just ci-install-tools` — Install dependencies on CI runner

### `scripts/boot-verify.sh`
Boots QEMU VM with SSH forwarding and verifies system state.
- Starts QEMU with `-netdev user` for SSH port forwarding
- Polls SSH until ready (up to 60s)
- Runs: `bootc status`, `bootc status --json-pretty`, `bootc upgrade --check`
- Gracefully shuts down VM

### `scripts/prepare-ssh-image.sh`
Takes a base image and adds SSH support.
- Auto-detects package manager (apt/dnf/zypper/pacman)
- Installs openssh-server
- Generates host keys & enables sshd
- Injects SSH public key from CI
- Tags as `:ci-ssh-enabled` for caching

### `scripts/verify-installation.sh`
Verifies fisherman created a valid installation.
- Checks 3-partition layout (EFI, boot, root)
- Mounts partitions and verifies content
- Handles both ostree and composefs backends

### `.github/workflows/bootcrew-vm.yml` (refactored)
Dramatically simplified (40 lines → 1 key step):
```yaml
- name: Run bootcrew test
  run: |
    IMAGE_JSON='${{ toJson(matrix.image) }}'
    just bootcrew-ci-test "$IMAGE_JSON"
```

### `scripts/README.md`
Full documentation for script usage, environment variables, troubleshooting.

## Usage

### Local Testing
```bash
# Full test with default centos-bootc (ostree)
just bootcrew-vm

# Test with debian-bootc (composefs)
just bootcrew-vm quay.io/debian-bootc/debian-bootc:latest xfs true

# Manual step-by-step
just setup-ssh-keys
just prepare-ssh-image quay.io/centos-bootc/centos-bootc:c10s
just build
just setup-loop /tmp/test-disk.img
just generate-recipe quay.io/centos-bootc/centos-bootc:ci-ssh-enabled xfs false
just install
just verify-installation /dev/loop0 false
just boot-verify
just cleanup-loop
```

### CI Usage
The workflow automatically:
1. Parses `tests/bootcrew-matrix.yaml` for images with `vm_boot: true`
2. Installs `just` on the runner
3. For each image, runs: `just bootcrew-ci-test '<JSON>'`
4. The `bootcrew-ci-test` recipe orchestrates all steps

## Benefits

✅ **Single source of truth** — Same commands in CI and local  
✅ **Reproducible failures** — "It fails in CI" → run locally with `just`  
✅ **Maintainability** — Workflow is 40 lines, complex logic in scripts  
✅ **Modularity** — Each script has one job, can be tested independently  
✅ **Documentation** — Scripts are self-contained with comments and help text  
✅ **Flexibility** — Run full test or individual steps as needed  

## Testing the Refactor

Before merging, verify:

```bash
# 1. Justfile syntax
just --list

# 2. Scripts are executable
ls -lh scripts/

# 3. Local test (if you have the hardware)
just bootcrew-vm

# 4. Workflow YAML is valid
python3 -c "import yaml; yaml.safe_load(open('.github/workflows/bootcrew-vm.yml'))"
```

## Next Steps

1. Push this refactor to a branch and test on GitHub Actions
2. Verify both `vm_boot` images (centos-bootc + debian-bootc) pass
3. If a test fails in CI, developers can now reproduce it locally:
   - `just setup-ssh-keys`
   - `just bootcrew-vm <image> <filesystem> <composefs>`
4. Consider applying the same pattern to other workflows (`bootcrew-fast`, `bootcrew-nightly`)
