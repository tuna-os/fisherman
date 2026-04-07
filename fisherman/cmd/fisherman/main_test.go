package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/recipe"
)

// TestExpandPath_AddsSbinDirs verifies that expandPath always ensures the
// standard sbin directories are present in PATH even when pkexec has stripped
// them.
func TestExpandPath_AddsSbinDirs(t *testing.T) {
	orig := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", orig) })

	os.Setenv("PATH", "/usr/bin:/bin") // minimal pkexec PATH
	expandPath()

	got := os.Getenv("PATH")
	for _, dir := range []string{"/usr/sbin", "/sbin", "/usr/local/sbin"} {
		if !strings.Contains(got, dir) {
			t.Errorf("expected %q in PATH after expandPath, got: %s", dir, got)
		}
	}
}

// TestExpandPath_PreservesExisting verifies that existing PATH entries are
// kept (not dropped) after expandPath.
func TestExpandPath_PreservesExisting(t *testing.T) {
	orig := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", orig) })

	os.Setenv("PATH", "/custom/bin")
	expandPath()

	if got := os.Getenv("PATH"); !strings.Contains(got, "/custom/bin") {
		t.Errorf("expected /custom/bin to be preserved in PATH, got: %s", got)
	}
}

// TestCheckRequiredTools_AllPresent verifies no error when all tools exist.
func TestCheckRequiredTools_AllPresent(t *testing.T) {
	orig := lookPath
	t.Cleanup(func() { lookPath = orig })
	lookPath = func(file string) (string, error) { return "/usr/bin/" + file, nil }

	r := &recipe.Recipe{Filesystem: "xfs"}
	if err := checkRequiredTools(r); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestCheckRequiredTools_MissingXfs verifies a clear error when mkfs.xfs is
// absent and the recipe requests XFS.
func TestCheckRequiredTools_MissingXfs(t *testing.T) {
	orig := lookPath
	t.Cleanup(func() { lookPath = orig })
	lookPath = func(file string) (string, error) {
		if file == "mkfs.xfs" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}

	r := &recipe.Recipe{Filesystem: "xfs"}
	err := checkRequiredTools(r)
	if err == nil {
		t.Fatal("expected error for missing mkfs.xfs, got nil")
	}
	if !strings.Contains(err.Error(), "mkfs.xfs") {
		t.Errorf("error should mention mkfs.xfs, got: %v", err)
	}
	if !strings.Contains(err.Error(), "xfsprogs") {
		t.Errorf("error should mention xfsprogs package, got: %v", err)
	}
}

// TestCheckRequiredTools_MissingBtrfs verifies a clear error when mkfs.btrfs
// is absent and the recipe requests btrfs.
func TestCheckRequiredTools_MissingBtrfs(t *testing.T) {
	orig := lookPath
	t.Cleanup(func() { lookPath = orig })
	lookPath = func(file string) (string, error) {
		if file == "mkfs.btrfs" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}

	r := &recipe.Recipe{Filesystem: "btrfs"}
	err := checkRequiredTools(r)
	if err == nil {
		t.Fatal("expected error for missing mkfs.btrfs, got nil")
	}
	if !strings.Contains(err.Error(), "mkfs.btrfs") {
		t.Errorf("error should mention mkfs.btrfs, got: %v", err)
	}
	if !strings.Contains(err.Error(), "btrfs-progs") {
		t.Errorf("error should mention btrfs-progs package, got: %v", err)
	}
}

// TestCheckRequiredTools_BtrfsNotCheckedForXfs verifies that mkfs.btrfs is
// not required when the recipe uses xfs.
func TestCheckRequiredTools_BtrfsNotCheckedForXfs(t *testing.T) {
	orig := lookPath
	t.Cleanup(func() { lookPath = orig })
	lookPath = func(file string) (string, error) {
		if file == "mkfs.btrfs" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}

	r := &recipe.Recipe{Filesystem: "xfs"}
	if err := checkRequiredTools(r); err != nil {
		t.Errorf("mkfs.btrfs should not be checked for xfs recipe, got: %v", err)
	}
}
