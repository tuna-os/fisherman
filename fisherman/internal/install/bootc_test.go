package install_test

import (
	"testing"

	"github.com/tuna-os/fisherman/internal/install"
)

func TestBuildBootcArgs_BaseArgs(t *testing.T) {
	args := install.BuildBootcArgs(install.Options{Target: "/mnt/target"}, "", "/target")
	// Must always include these
	assertContains(t, args, "install")
	assertContains(t, args, "to-filesystem")
	assertContains(t, args, "--skip-finalize")
	assertContains(t, args, "/target")
}

func TestBuildBootcArgs_ComposeFsBackend(t *testing.T) {
	args := install.BuildBootcArgs(install.Options{ComposeFsBackend: true}, "", "/target")
	assertContains(t, args, "--composefs-backend")
}

func TestBuildBootcArgs_NoComposeFsBackend(t *testing.T) {
	args := install.BuildBootcArgs(install.Options{ComposeFsBackend: false}, "", "/target")
	assertAbsent(t, args, "--composefs-backend")
}

func TestBuildBootcArgs_UnifiedStorage(t *testing.T) {
	args := install.BuildBootcArgs(install.Options{UnifiedStorage: true}, "", "/target")
	assertContains(t, args, "--experimental-unified-storage")
}

func TestBuildBootcArgs_NoUnifiedStorage(t *testing.T) {
	args := install.BuildBootcArgs(install.Options{UnifiedStorage: false}, "", "/target")
	assertAbsent(t, args, "--experimental-unified-storage")
}

func TestBuildBootcArgs_SelinuxDisabled(t *testing.T) {
	args := install.BuildBootcArgs(install.Options{SelinuxDisabled: true}, "", "/target")
	assertContains(t, args, "--disable-selinux")
}

func TestBuildBootcArgs_NoSelinux(t *testing.T) {
	args := install.BuildBootcArgs(install.Options{SelinuxDisabled: false}, "", "/target")
	assertAbsent(t, args, "--disable-selinux")
}

func TestBuildBootcArgs_TargetImgref(t *testing.T) {
	args := install.BuildBootcArgs(install.Options{}, "ghcr.io/tuna-os/yellowfin:gnome50", "/target")
	assertContains(t, args, "--target-imgref")
	assertContains(t, args, "ghcr.io/tuna-os/yellowfin:gnome50")
}

func TestBuildBootcArgs_NoTargetImgref(t *testing.T) {
	args := install.BuildBootcArgs(install.Options{}, "", "/target")
	assertAbsent(t, args, "--target-imgref")
}

func TestBuildBootcArgs_AllFlags(t *testing.T) {
	opts := install.Options{
		ComposeFsBackend: true,
		UnifiedStorage:   true,
		SelinuxDisabled:  true,
	}
	args := install.BuildBootcArgs(opts, "img:tag", "/target")
	assertContains(t, args, "--composefs-backend")
	assertContains(t, args, "--experimental-unified-storage")
	assertContains(t, args, "--disable-selinux")
	assertContains(t, args, "--target-imgref")
}

// assertContains fails the test if s is not present in slice.
func assertContains(t *testing.T, slice []string, s string) {
	t.Helper()
	for _, v := range slice {
		if v == s {
			return
		}
	}
	t.Errorf("expected %q in args %v", s, slice)
}

// assertAbsent fails the test if s is present in slice.
func assertAbsent(t *testing.T, slice []string, s string) {
	t.Helper()
	for _, v := range slice {
		if v == s {
			t.Errorf("unexpected %q in args %v", s, slice)
			return
		}
	}
}
