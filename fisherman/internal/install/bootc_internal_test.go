package install

import (
	"os/exec"
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

// A failed restore of the /var/tmp bind used to be ignored, leaving the live
// /var/tmp on the target filesystem (#505). It must be logged and retried
// lazily.
func TestOverrideVarTmpRestoreFallsBackToLazyUmount(t *testing.T) {
	prev := CommandFn
	t.Cleanup(func() { CommandFn = prev })
	var calls []string
	CommandFn = func(name string, args ...string) *exec.Cmd {
		line := name + " " + strings.Join(args, " ")
		calls = append(calls, line)
		if strings.Contains(line, "umount /var/tmp") && !strings.Contains(line, "-l") {
			return exec.Command("false")
		}
		return exec.Command("true")
	}
	restore := overrideVarTmp(t.TempDir())
	restore()
	var lazy bool
	for _, c := range calls {
		if strings.Contains(c, "umount -l /var/tmp") {
			lazy = true
		}
	}
	if !lazy {
		t.Fatalf("no lazy umount after failed restore; calls: %q", calls)
	}
}

// The /var/tmp redirect must live in a private mount namespace, not on the
// host, so no other service can pin the target filesystem (#505).
func TestScopeVarTmpUsesPrivateMountNamespace(t *testing.T) {
	prev := CommandFn
	t.Cleanup(func() { CommandFn = prev })
	CommandFn = func(name string, args ...string) *exec.Cmd { return exec.Command("true") }
	dir := t.TempDir()
	name, args, restore := scopeVarTmp(dir, "skopeo", []string{"copy", "a", "b"})
	restore()
	line := name + " " + strings.Join(args, " ")
	for _, want := range []string{"unshare --mount --propagation private", dir + "/var-tmp-override skopeo copy a b"} {
		if !strings.Contains(line, want) {
			t.Fatalf("argv %q missing %q", line, want)
		}
	}
}

func TestScopeVarTmpFallsBackWhenNamespaceRefused(t *testing.T) {
	prev := CommandFn
	t.Cleanup(func() { CommandFn = prev })
	var calls []string
	CommandFn = func(name string, args ...string) *exec.Cmd {
		line := name + " " + strings.Join(args, " ")
		calls = append(calls, line)
		if name == "unshare" {
			return exec.Command("false")
		}
		return exec.Command("true")
	}
	name, args, restore := scopeVarTmp(t.TempDir(), "skopeo", []string{"copy"})
	restore()
	if name != "skopeo" || len(args) != 1 {
		t.Fatalf("argv rewritten on fallback: %s %q", name, args)
	}
	var bound, unbound bool
	for _, c := range calls {
		bound = bound || strings.HasPrefix(c, "mount --bind")
		unbound = unbound || strings.HasPrefix(c, "umount /var/tmp")
	}
	if !bound || !unbound {
		t.Fatalf("fallback did not bind/restore: %q", calls)
	}
}
