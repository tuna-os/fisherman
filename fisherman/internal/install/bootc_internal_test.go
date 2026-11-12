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
	matched := SourceRoot{Established: true, Reason: "digest_match", Backend: "ostree"}
	cases := []struct {
		name     string
		opts     Options
		hasBootc bool
		want     bool
		line     string
	}{
		{"local source, non-composefs, host booted the source", Options{SourceImgref: local, SourceRoot: matched}, true, true,
			"bootc: the host booted the source image (digest_match); installing with the host's bootc"},
		{"no bootc on the host keeps the container path", Options{SourceImgref: local, SourceRoot: matched}, false, false,
			"bootc: the host has no bootc; installing with the source image's own bootc"},
		{"an unrelated host keeps the container path", Options{SourceImgref: local, SourceRoot: SourceRoot{Reason: "digest_mismatch"}}, true, false,
			"(digest_mismatch); installing with the source image's own bootc"},
		{"an unknown host digest keeps the container path", Options{SourceImgref: local, SourceRoot: SourceRoot{Reason: "host_digest_unknown"}}, true, false,
			"(host_digest_unknown)"},
		{"an unknown source digest keeps the container path", Options{SourceImgref: local, SourceRoot: SourceRoot{Reason: "source_digest_unknown"}}, true, false,
			"(source_digest_unknown)"},
		{"bootc present is not enough: nothing established", Options{SourceImgref: local}, true, false,
			"(not established)"},
		{"a composefs-booted host keeps the container path", Options{SourceImgref: local,
			SourceRoot: SourceRoot{Established: true, Reason: "digest_match", Backend: "composefs"}}, true, false,
			"composefs backend"},
		{"composefs keeps the container path", Options{SourceImgref: local, ComposeFsBackend: true, SourceRoot: matched}, true, false, ""},
		{"a registry source keeps the container path", Options{SourceImgref: "ghcr.io/projectbluefin/utah:testing", SourceRoot: matched}, true, false, ""},
		{"no source is direct mode already, not this decision", Options{}, true, false, ""},
	}
	for _, c := range cases {
		hostHasBootcFn = func() bool { return c.hasBootc }
		if got := useDirectForLocalSource(c.opts); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
		line := LocalSourceContextLine(c.opts)
		if (c.line == "") != (line == "") || !strings.Contains(line, c.line) {
			t.Errorf("%s: line %q, want it to contain %q", c.name, line, c.line)
		}
	}
}

// NeedsSourceRoot: the caller asks the probe only when the answer is used.
func TestNeedsSourceRoot(t *testing.T) {
	local := "containers-storage:ghcr.io/example/os:stable"
	for _, c := range []struct {
		opts Options
		want bool
	}{
		{Options{SourceImgref: local}, true},
		{Options{SourceImgref: local, ComposeFsBackend: true}, false},
		{Options{SourceImgref: local, RunInSourceContainer: true}, false},
		{Options{SourceImgref: "oci:/var/lib/media/os"}, false},
		{Options{SourceImgref: "ghcr.io/example/os:stable"}, false},
		{Options{}, false},
	} {
		if got := NeedsSourceRoot(c.opts); got != c.want {
			t.Errorf("NeedsSourceRoot(%+v) = %v, want %v", c.opts, got, c.want)
		}
	}
	// A reuse copy reports its own context; no line here.
	if line := LocalSourceContextLine(Options{SourceImgref: local, RunInSourceContainer: true}); line != "" {
		t.Errorf("reuse copy line %q", line)
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
