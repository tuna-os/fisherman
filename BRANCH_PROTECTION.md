# Branch Protection Settings

This file documents the branch protection rules for `tuna-os/fisherman`.

## Configuration

To apply these settings via GitHub CLI:

```bash
# dev branch
gh repo rule create \
  --branch "dev" \
  --allow-force-pushes false \
  --allow-deletions false \
  --require-code-review-count 1 \
  --require-status-checks "bootcrew-fast" "bootcrew-vm" \
  --require-up-to-date-before-merge true

# prod branch (more strict)
gh repo rule create \
  --branch "prod" \
  --allow-force-pushes false \
  --allow-deletions false \
  --require-code-review-count 1 \
  --require-status-checks "bootcrew-fast" "bootcrew-vm" \
  --require-up-to-date-before-merge true
```

## Manual Setup (GitHub Web UI)

1. Go to Settings → Branches → Add rule
2. Apply to branch: `dev` (and separately for `prod`)
3. Enable:
   - ☑ Require status checks to pass before merging
     - **bootcrew-fast** (disk ops validation)
     - **bootcrew-vm** (E2E boot + bootc verification)
   - ☑ Require branches to be up to date before merging
   - ☑ Require code reviews before merging (1 approval)
   - ☑ Dismiss stale pull request approvals when new commits are pushed
   - ☑ Require status checks from branch protection rules to pass

## Workflows

### bootcrew-fast.yml (PR gate - fast)
- **When**: push to dev/prod, pull_request
- **Duration**: ~3-5 min per image
- **Tests**: 6 images × disk ops (partitions, filesystem, labels)
- **Purpose**: Fast validation of disk operations (must pass)

### bootcrew-vm.yml (PR gate - boot validation)
- **When**: push to dev/prod, pull_request  
- **Duration**: ~15-20 min total (2 images tested)
- **Tests**: 
  - **centos-bootc**: ostree backend (partition structure, boot, bootc binary)
  - **debian-bootc**: composefs backend (composefs layout, boot, bootc binary)
- **Validates**:
  - Full install (podman pull + fisherman)
  - Partition layout and labels
  - Installation verification (hostname, filesystem type)
  - Boot success (getty or systemd target reached)
  - No critical failures (coredumps, failed units, kernel panic)
  - bootc binary presence and deployment structure
- **Purpose**: Real QEMU boot validation + bootc operational readiness (must pass)

### bootcrew-nightly.yml (Full matrix - scheduled)
- **When**: Monday 2 AM UTC (scheduled)
- **Duration**: ~60-90 min total (6 images tested)
- **Tests**: All 6 images × full install + boot
- **Purpose**: Comprehensive regression testing (not a PR gate)

## PR Merge Requirements

All PRs to `dev` and `prod` must satisfy:

✅ **bootcrew-fast** — All checks passing
✅ **bootcrew-vm** — All checks passing  
✅ **1 code review** — Approval from maintainer
✅ **Branch up to date** — No conflicts with target branch

This ensures:
- Disk operations work correctly across filesystems (fast check)
- Boot succeeds on real hardware (QEMU) for both ostree and composefs
- bootc status command is functional in installed systems
- No regressions in core install or boot paths

## Note on test duration

Total PR gate duration: ~20-25 minutes (bootcrew-fast in parallel: 5 min, bootcrew-vm in parallel: 15-20 min)

For faster iteration during development, you can:
1. Run bootcrew-fast only locally: `go build ./cmd/fisherman && go test ./...`
2. Push to a draft PR to run full CI before requesting review
3. Maintainers can manually bypass checks if needed (use sparingly)
