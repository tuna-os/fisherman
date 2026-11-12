package install

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSelinux sets whether the host has SELinux active for one test.
func fakeSelinux(t *testing.T, active bool) {
	t.Helper()
	old := HostSelinuxActive
	HostSelinuxActive = func() bool { return active }
	t.Cleanup(func() { HostSelinuxActive = old })
}

// The workaround is needed only for a target without SELinux, installed from
// the image's container on a host with SELinux active (#370). Every other
// install must not need, or look for, a compiler.
func TestSelinuxWorkaroundRequired(t *testing.T) {
	registry := Options{SelinuxDisabled: true, SourceImgref: "ghcr.io/example/os:stable"}
	local := Options{SelinuxDisabled: true, SourceImgref: "containers-storage:ghcr.io/example/os:stable",
		SourceRoot: SourceRoot{Established: true, Reason: "digest_match", Backend: "ostree"}}
	unrelated := local
	unrelated.SourceRoot = SourceRoot{Reason: "digest_mismatch"}
	for _, tt := range []struct {
		name      string
		opts      Options
		selinux   bool
		hostBootc bool
		want      bool
	}{
		{"registry source, cross policy", registry, true, false, true},
		{"target keeps SELinux", Options{SourceImgref: registry.SourceImgref}, true, false, false},
		{"host without SELinux", registry, false, false, false},
		{"live root, no source container", Options{SelinuxDisabled: true}, true, false, false},
		{"local source with host bootc runs directly", local, true, true, false},
		{"local source without host bootc runs in the container", local, true, false, true},
		{"local source on an unrelated host runs in the container", unrelated, true, true, true},
		{"source reuse runs in the copy's container", Options{SelinuxDisabled: true, SourceImgref: local.SourceImgref, RunInSourceContainer: true}, true, true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fakeSelinux(t, tt.selinux)
			old := hostHasBootcFn
			hostHasBootcFn = func() bool { return tt.hostBootc }
			defer func() { hostHasBootcFn = old }()
			if got := SelinuxWorkaroundRequired(tt.opts); got != tt.want {
				t.Fatalf("SelinuxWorkaroundRequired = %v, want %v", got, tt.want)
			}
		})
	}
}

// An install that does not need the workaround runs no compiler and gets
// no shim; one that needs it and has no compiler is refused, with a reason
// that says what to install.
func TestPrepareSelinuxWorkaround(t *testing.T) {
	opts := Options{SelinuxDisabled: true, SourceImgref: "ghcr.io/example/os:stable"}

	t.Run("not needed", func(t *testing.T) {
		mock := setupInstallMockExec(t)
		fakeSelinux(t, false)
		path, dir, err := PrepareSelinuxWorkaround(opts)
		if err != nil || path != "" || dir != "" || len(mock.calls) != 0 {
			t.Fatalf("got %q %q %v, calls %v; want nothing run", path, dir, err, mock.calls)
		}
	})

	t.Run("no compiler", func(t *testing.T) {
		mock := setupInstallMockExec(t)
		mock.errFor["cc"] = exec.ErrNotFound
		fakeSelinux(t, true)
		path, dir, err := PrepareSelinuxWorkaround(opts)
		if !errors.Is(err, ErrSelinuxWorkaroundUnavailable) || path != "" || dir != "" {
			t.Fatalf("got %q %q %v, want ErrSelinuxWorkaroundUnavailable", path, dir, err)
		}
		for _, want := range []string{"selinuxDisabled", "C compiler", "headers"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("message %q does not say %q", err, want)
			}
		}
	})

	t.Run("built", func(t *testing.T) {
		if _, err := exec.LookPath("cc"); err != nil {
			t.Skip("cc not found; the build itself is covered by TestBuildSelinuxBypassShim_Compiles")
		}
		fakeSelinux(t, true)
		path, dir, err := PrepareSelinuxWorkaround(opts)
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(dir)
		if filepath.Dir(path) != dir {
			t.Fatalf("shim %s is not in %s", path, dir)
		}
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("shim directory %s: %v %v, want a private 0700 directory", dir, info, err)
		}
	})
}

// bootcViaContainer uses the shim prepared before the disk was touched and
// does not build another. Without one, a shim it cannot build ends the
// install: before #370 it warned and ran bootc, which then failed on the
// first label the host rejected.
func TestBootcViaContainerSelinuxShim(t *testing.T) {
	run := func(t *testing.T, opts Options) (string, error) {
		t.Helper()
		dir := t.TempDir()
		capture := filepath.Join(dir, "capture")
		script := filepath.Join(dir, "owned-command")
		if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \""+capture+"\"\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		oldCmd := CommandFn
		CommandFn = func(name string, args ...string) *exec.Cmd {
			return exec.Command(script, append([]string{name}, args...)...)
		}
		defer func() { CommandFn = oldCmd }()
		oldStdout := os.Stdout
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		os.Stdout = w
		opts.Target = filepath.Join(dir, "target")
		opts.ScratchDir = filepath.Join(dir, "scratch")
		opts.SourceImgref = "containers-storage:ghcr.io/example/os:stable"
		err = bootcViaContainer(opts)
		w.Close()
		os.Stdout = oldStdout
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		raw, _ := os.ReadFile(capture)
		return string(raw), err
	}
	fakeSelinux(t, true)
	restore := SetStorageSpaceConstrainedForTest(false)
	defer restore()

	t.Run("prepared shim", func(t *testing.T) {
		mock := setupInstallMockExec(t)
		got, err := run(t, Options{SelinuxDisabled: true, SelinuxShimPath: "/private/shim.so"})
		if err != nil {
			t.Fatal(err)
		}
		if len(mock.calls) != 0 {
			t.Fatalf("a second shim was built: %v", mock.calls)
		}
		if !strings.Contains(got, "/private/shim.so:/fisherman-selinux-bypass.so:z") || !strings.Contains(got, "LD_PRELOAD=/fisherman-selinux-bypass.so") {
			t.Fatalf("podman did not get the prepared shim:\n%s", got)
		}
	})

	t.Run("no shim and no compiler", func(t *testing.T) {
		mock := setupInstallMockExec(t)
		mock.errFor["cc"] = exec.ErrNotFound
		got, err := run(t, Options{SelinuxDisabled: true})
		if !errors.Is(err, ErrSelinuxWorkaroundUnavailable) {
			t.Fatalf("err = %v, want ErrSelinuxWorkaroundUnavailable", err)
		}
		if strings.Contains(got, "podman") {
			t.Fatalf("bootc ran without the workaround:\n%s", got)
		}
	})
}
