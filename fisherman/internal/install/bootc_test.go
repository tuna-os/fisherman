package install_test

import (
	"fmt"
	"testing"

	"github.com/tuna-os/fisherman/internal/install"
)

func TestCheckImage_NeedsPullWhenNotCached(t *testing.T) {
	call := 0
	install.SkopeoInspectFn = func(args ...string) ([]byte, error) {
		call++
		if call == 1 {
			// Remote inspect: return manifest with digest + layers
			return []byte(`{"Digest":"sha256:aaaa","Layers":["sha256:l1","sha256:l2"]}`), nil
		}
		// Local inspect: not found
		return nil, fmt.Errorf("image not known")
	}
	defer func() { install.SkopeoInspectFn = install.DefaultSkopeoInspect }()

	result := install.CheckImage("ghcr.io/tuna-os/yellowfin:gnome-hwe")
	if !result.NeedsPull {
		t.Error("NeedsPull should be true when image not in local storage")
	}
	if result.LayerCount != 2 {
		t.Errorf("LayerCount = %d, want 2", result.LayerCount)
	}
}

func TestCheckImage_NoPullWhenCachedAndCurrent(t *testing.T) {
	install.SkopeoInspectFn = func(args ...string) ([]byte, error) {
		// Both remote and local return same digest
		return []byte(`{"Digest":"sha256:bbbb","Layers":["sha256:l1","sha256:l2","sha256:l3"]}`), nil
	}
	defer func() { install.SkopeoInspectFn = install.DefaultSkopeoInspect }()

	result := install.CheckImage("ghcr.io/tuna-os/yellowfin:gnome-hwe")
	if result.NeedsPull {
		t.Error("NeedsPull should be false when local digest matches remote")
	}
	if result.LayerCount != 3 {
		t.Errorf("LayerCount = %d, want 3", result.LayerCount)
	}
}

func TestCheckImage_NeedsPullWhenDigestDiffers(t *testing.T) {
	call := 0
	install.SkopeoInspectFn = func(args ...string) ([]byte, error) {
		call++
		if call == 1 {
			return []byte(`{"Digest":"sha256:remote","Layers":["sha256:l1"]}`), nil
		}
		return []byte(`{"Digest":"sha256:stale","Layers":["sha256:l1"]}`), nil
	}
	defer func() { install.SkopeoInspectFn = install.DefaultSkopeoInspect }()

	result := install.CheckImage("ghcr.io/tuna-os/yellowfin:gnome-hwe")
	if !result.NeedsPull {
		t.Error("NeedsPull should be true when remote digest differs from local")
	}
}

func TestCheckImage_NeedsPullOnNetworkErrorNoCachedImage(t *testing.T) {
	install.SkopeoInspectFn = func(args ...string) ([]byte, error) {
		return nil, fmt.Errorf("network error")
	}
	defer func() { install.SkopeoInspectFn = install.DefaultSkopeoInspect }()

	result := install.CheckImage("ghcr.io/tuna-os/yellowfin:gnome-hwe")
	if !result.NeedsPull {
		t.Error("NeedsPull should be true when offline and image not in local storage")
	}
	if result.Offline {
		t.Error("Offline should be false when image is not in local storage")
	}
}

func TestCheckImage_OfflineWithLocalCache(t *testing.T) {
	call := 0
	install.SkopeoInspectFn = func(args ...string) ([]byte, error) {
		call++
		if call == 1 {
			// Remote inspect: offline / unreachable
			return nil, fmt.Errorf("network unreachable")
		}
		// Local inspect: image is cached
		return []byte(`{"Digest":"sha256:cached","Layers":["sha256:l1","sha256:l2","sha256:l3"]}`), nil
	}
	defer func() { install.SkopeoInspectFn = install.DefaultSkopeoInspect }()

	result := install.CheckImage("ghcr.io/tuna-os/yellowfin:gnome-hwe")
	if result.NeedsPull {
		t.Error("NeedsPull should be false when offline but image is in local storage")
	}
	if !result.Offline {
		t.Error("Offline should be true when registry was unreachable")
	}
	if result.LayerCount != 3 {
		t.Errorf("LayerCount = %d, want 3", result.LayerCount)
	}
}

func TestClassifyLine_LayersNeeded(t *testing.T) {
	line := "layers already present: 0; layers needed: 64 (3.7\u00a0GB)"
	got := install.ClassifyLine(line)
	want := "Deploying: 64 (3.7\u00a0GB)"
	if got != want {
		t.Errorf("ClassifyLine(%q) = %q, want %q", line, got, want)
	}
}

func TestClassifyLine_LayersNeededZero(t *testing.T) {
	line := "layers already present: 64; layers needed: 0"
	got := install.ClassifyLine(line)
	want := "Deploying: 0"
	if got != want {
		t.Errorf("ClassifyLine(%q) = %q, want %q", line, got, want)
	}
}

func TestClassifyLine_InstallingImage(t *testing.T) {
	got := install.ClassifyLine("Installing image: docker://ghcr.io/tuna-os/yellowfin:gnome-hwe")
	if got != "Pulling container image" {
		t.Errorf("got %q, want 'Pulling container image'", got)
	}
}

func TestClassifyLine_NoMatch(t *testing.T) {
	got := install.ClassifyLine("some random line")
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

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
