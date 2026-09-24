package install

import (
	"strings"
	"testing"
)

func TestContainersStorageSourcePreservesQualifiedStore(t *testing.T) {
	qualified := "containers-storage:[overlay@/scratch/containers-root+/scratch/containers-runroot]ghcr.io/tuna-os/yellowfin:gnome"
	if got := containersStorageSource(qualified); got != qualified {
		t.Fatalf("containersStorageSource() = %q, want qualified store unchanged", got)
	}
}

func TestContainersStorageSourceAddsDefaultTransport(t *testing.T) {
	const image = "ghcr.io/tuna-os/yellowfin:gnome"
	if got, want := containersStorageSource(image), "containers-storage:"+image; got != want {
		t.Fatalf("containersStorageSource() = %q, want %q", got, want)
	}
}

func TestUseDirectForLocalSource(t *testing.T) {
	prev := hostHasBootcFn
	t.Cleanup(func() { hostHasBootcFn = prev })
	local := "containers-storage:ghcr.io/projectbluefin/utah:testing"
	cases := []struct {
		name     string
		opts     Options
		hasBootc bool
		want     bool
	}{
		{"local source, non-composefs, bootc on host", Options{SourceImgref: local}, true, true},
		{"no bootc on the host keeps the container path", Options{SourceImgref: local}, false, false},
		{"composefs keeps the container path", Options{SourceImgref: local, ComposeFsBackend: true}, true, false},
		{"a registry source keeps the container path", Options{SourceImgref: "ghcr.io/projectbluefin/utah:testing"}, true, false},
		{"no source is direct mode already, not this decision", Options{}, true, false},
	}
	for _, c := range cases {
		hostHasBootcFn = func() bool { return c.hasBootc }
		if got := useDirectForLocalSource(c.opts); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBuildBootcArgsUsesAnExplicitLocalSource(t *testing.T) {
	// The NVIDIA shape: install the image on the ISO, track the base image.
	opts := Options{
		SourceImgref:      "containers-storage:ghcr.io/projectbluefin/utah-nvidia:testing",
		TargetImgref:      "ghcr.io/projectbluefin/utah:testing",
		directLocalSource: true,
	}
	args := strings.Join(BuildBootcArgs(opts, opts.TargetImgref, "/target"), " ")
	want := "--source-imgref containers-storage:ghcr.io/projectbluefin/utah-nvidia:testing"
	if !strings.Contains(args, want) || !strings.Contains(args, "--target-imgref ghcr.io/projectbluefin/utah:testing") {
		t.Fatalf("args = %q, want %q and the base target", args, want)
	}
}
