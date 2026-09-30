package install

import (
	"os"
	"testing"
)

// TestMain makes the direct-install decision independent of the machine: a
// developer box or runner with bootc installed would otherwise send every
// local-source test down the direct path. Tests opt in with
// SetHostHasBootcForTest(true).
func TestMain(m *testing.M) {
	hostHasBootcFn = func() bool { return false }
	os.Exit(m.Run())
}

// Test-only seams (compiled only under `go test`).

// SetStorageSpaceConstrainedForTest forces the non-composefs storage-redirect
// decision for external-package tests, returning a restore func. Lets tests
// exercise the OCI-export path deterministically regardless of the runner's
// /var/lib/containers free space.
func SetStorageSpaceConstrainedForTest(v bool) (restore func()) {
	prev := storageSpaceConstrainedFn
	storageSpaceConstrainedFn = func() bool { return v }
	return func() { storageSpaceConstrainedFn = prev }
}

// SetSelectStorageDriverForTest forces the storage-driver decision (e.g.
// "overlay") so the OCI-redirect export path is exercised without a real
// podman probe. Returns a restore func.
func SetSelectStorageDriverForTest(driver, reason string) (restore func()) {
	prev := selectStorageDriverFn
	selectStorageDriverFn = func(string) (string, string) { return driver, reason }
	return func() { selectStorageDriverFn = prev }
}

// SetHostHasBootcForTest forces whether the host's bootc is usable, so the
// direct-install decision does not depend on the machine running the tests.
func SetHostHasBootcForTest(v bool) (restore func()) {
	prev := hostHasBootcFn
	hostHasBootcFn = func() bool { return v }
	return func() { hostHasBootcFn = prev }
}
