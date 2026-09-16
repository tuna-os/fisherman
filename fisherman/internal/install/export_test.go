package install

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
