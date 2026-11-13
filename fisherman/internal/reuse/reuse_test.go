package reuse

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/tuna-os/fisherman/internal/probe"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/recipe"
)

var (
	testDigest   = "sha256:" + strings.Repeat("1", 64)
	testChecksum = strings.Repeat("a", 64)
	otherSum     = strings.Repeat("b", 64)
	copyDigest   = "sha256:" + strings.Repeat("c", 64)
)

const (
	scratch = "/var/fisherman-tmp/source-reuse"
	srcRef  = "docker://ghcr.io/example/os:stable"
	randID  = "0123456789abcdef"
	dest    = DestinationRepo + ":111111111111-" + randID
	helpOK  = "Usage: bootc image copy-to-storage [OPTIONS]\n  --source <SOURCE>\n  --target <TARGET>\n"
)

func candidate() probe.ReuseCandidate {
	return probe.ReuseCandidate{Role: probe.RoleBooted, Ref: "ghcr.io/example/os:stable", Transport: "registry",
		SourceRef: srcRef, OstreeRef: probe.OstreeImageRef(srcRef), Checksum: testChecksum,
		Stateroot: "default", Serial: 0, Digest: testDigest, Platform: "x86_64"}
}

func exitErr(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	var e *exec.ExitError
	if !errors.As(err, &e) {
		t.Fatalf("exit %d: %v", code, err)
	}
	return fmt.Errorf("bootc: %w", err)
}

// fakeHost is the live host: a filesystem root with a deployment, scripted
// commands, and free space the copy uses up.
type fakeHost struct {
	t     *testing.T
	root  string
	mu    sync.Mutex
	calls []string
	// handlers answer a command line; the default for each is below.
	handlers map[string]func() ([]byte, error)
	free     uint64
	// copyUses is what the copy takes from free space while it runs, and
	// copyKeeps what it still holds after.
	copyUses, copyKeeps uint64
	events              []progress.SourceReuseEvent
	resolve             probe.ReuseSource
	revParse            []string // successive answers of ostree rev-parse
}

func newFakeHost(t *testing.T) *fakeHost {
	t.Helper()
	root := t.TempDir()
	dep := filepath.Join(root, candidate().DeployPath())
	for _, d := range []string{"usr/bin", "etc", "sysroot/ostree/repo/objects/aa", "var"} {
		if err := os.MkdirAll(filepath.Join(dep, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dep, "usr/bin/bash"), make([]byte, 5000), 0o755); err != nil {
		t.Fatal(err)
	}
	// The object and its hard-linked tree path: data once.
	if err := os.Link(filepath.Join(dep, "usr/bin/bash"), filepath.Join(dep, "sysroot/ostree/repo/objects/aa/bb.file")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dep, "etc/os-release"), []byte("ID=example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("bash", filepath.Join(dep, "usr/bin/sh")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, DefaultStore), 0o700); err != nil {
		t.Fatal(err)
	}
	h := &fakeHost{t: t, root: root, handlers: map[string]func() ([]byte, error){},
		free: 100 << 30, copyUses: 3 << 30, copyKeeps: 1 << 30,
		resolve: probe.ReuseSource{Eligible: true, Reason: probe.ReuseEligible, Candidates: []probe.ReuseCandidate{candidate()}}}
	return h
}

func (h *fakeHost) call(name string, args ...string) ([]byte, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	h.mu.Lock()
	h.calls = append(h.calls, line)
	fn := h.handlers[line]
	h.mu.Unlock()
	if fn != nil {
		return fn()
	}
	switch {
	case line == "bootc image copy-to-storage --help":
		return []byte(helpOK), nil
	case name == "ostree" && args[0] == "rev-parse":
		h.mu.Lock()
		defer h.mu.Unlock()
		if len(h.revParse) > 0 {
			v := h.revParse[0]
			if len(h.revParse) > 1 {
				h.revParse = h.revParse[1:]
			}
			if v == "error" {
				return nil, exitErr(h.t, 1)
			}
			return []byte(v + "\n"), nil
		}
		return []byte(testChecksum + "\n"), nil
	case line == "skopeo inspect containers-storage:"+dest:
		h.mu.Lock()
		copied := false
		for _, c := range h.calls {
			copied = copied || strings.HasPrefix(c, "bootc image copy-to-storage --source")
		}
		h.mu.Unlock()
		if !copied {
			return nil, exitErr(h.t, 1) // not there yet
		}
		return []byte(`{"Name":"localhost/fisherman-reuse","Digest":"` + copyDigest + `","Architecture":"amd64","Os":"linux","Layers":["sha256:x"]}`), nil
	case strings.HasPrefix(line, "bootc image copy-to-storage --source"):
		h.mu.Lock()
		h.free -= h.copyUses
		h.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		h.mu.Lock()
		h.free += h.copyUses - h.copyKeeps
		h.mu.Unlock()
		return nil, nil
	case name == "mount" || name == "umount" || name == "podman":
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected command %q", line)
}

func (h *fakeHost) env() Env {
	return Env{
		Run:    func(name string, args ...string) error { _, err := h.call(name, args...); return err },
		Output: h.call,
		Statfs: func(path string) (uint64, uint64, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.free, 1, nil
		},
		Root:     h.root,
		Resolve:  func() probe.ReuseSource { return h.resolve },
		Emit:     func(e progress.SourceReuseEvent) { h.mu.Lock(); h.events = append(h.events, e); h.mu.Unlock() },
		Sample:   time.Millisecond,
		RandomID: func() string { return randID },
	}
}

func (h *fakeHost) phases() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, e := range h.events {
		p := e.Phase
		if e.Code != "" {
			p += ":" + e.Code + ":" + e.Reason
		}
		out = append(out, p)
	}
	return out
}

func (h *fakeHost) ran(prefix string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func failureOf(t *testing.T, err error) *Failure {
	t.Helper()
	var f *Failure
	if !errors.As(err, &f) {
		t.Fatalf("want a *Failure, got %v", err)
	}
	return f
}

func TestAcquire_BootcHost(t *testing.T) {
	h := newFakeHost(t)
	s := New(h.env(), testDigest, "x86_64", scratch)
	res, err := s.Acquire([]probe.ReuseCandidate{candidate()})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	want := &Result{SourceImgref: "containers-storage:" + dest, Destination: dest, DestinationDigest: copyDigest, Candidate: candidate()}
	if !reflect.DeepEqual(res, want) {
		t.Errorf("result %+v, want %+v", res, want)
	}
	wantCalls := []string{
		"bootc image copy-to-storage --help",
		"ostree rev-parse --repo=/sysroot/ostree/repo " + candidate().OstreeRef,
		"skopeo inspect containers-storage:" + dest,
		"mount --bind " + scratch + "/var-tmp /var/tmp",
		"bootc image copy-to-storage --source " + srcRef + " --target " + dest,
		"umount /var/tmp",
		"ostree rev-parse --repo=/sysroot/ostree/repo " + candidate().OstreeRef,
		"skopeo inspect containers-storage:" + dest,
	}
	if !reflect.DeepEqual(h.calls, wantCalls) {
		t.Errorf("calls\n%s\nwant\n%s", strings.Join(h.calls, "\n"), strings.Join(wantCalls, "\n"))
	}
	if got := h.phases(); !reflect.DeepEqual(got, []string{"planned", "copying", "verified"}) {
		t.Errorf("phases %v", got)
	}
	v := h.events[2]
	if v.DestinationDigest != copyDigest || v.Source != srcRef || v.Destination != "containers-storage:"+dest {
		t.Errorf("verified event %+v", v)
	}
	if v.Measured == nil || v.Measured.ScratchPeakBytes != 3<<30 || v.Measured.StoreBytes != 1<<30 {
		t.Errorf("measured %+v, want peak 3 GiB and 1 GiB kept", v.Measured)
	}
	e := v.Estimate
	// bash (5000 B) once, its object path and tree path both get headers.
	if e == nil || e.Files != 3 || e.StoreBytes < 8192 || e.ScratchBytes < 5120+3*1024 || !e.SameFilesystem ||
		e.RequiredBytes != e.ScratchBytes+e.StoreBytes+Margin {
		t.Errorf("estimate %+v", e)
	}
	// Release removes exactly what the session made.
	h.calls = nil
	if err := s.Release(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.calls, []string{"podman rmi --ignore " + dest}) {
		t.Errorf("release calls %v", h.calls)
	}
	if _, err := os.Stat(filepath.Join(h.root, scratch)); !os.IsNotExist(err) {
		t.Errorf("scratch left behind: %v", err)
	}
	if last := h.events[len(h.events)-1]; last.Phase != progress.ReuseReleased || last.Reason != "removed" {
		t.Errorf("last event %+v", last)
	}
	// Twice is once.
	h.calls = nil
	if err := s.Release(); err != nil || len(h.calls) != 0 {
		t.Errorf("second Release ran %v (%v)", h.calls, err)
	}
}

// failCase runs Acquire on a host one change away from the good one, and
// checks the code, the reason, and whether the copy ran.
type failCase struct {
	name       string
	edit       func(t *testing.T, h *fakeHost)
	candidates []probe.ReuseCandidate
	code       string
	reason     string
	copied     bool
}

func TestAcquire_Refusals(t *testing.T) {
	dep := candidate().DeployPath()
	cases := []failCase{
		{name: "older CLI: no copy-to-storage", code: recipe.CodeSourceReuseCommandUnsupported, reason: "command_absent",
			edit: func(t *testing.T, h *fakeHost) {
				h.handlers["bootc image copy-to-storage --help"] = func() ([]byte, error) { return nil, exitErr(t, 2) }
			}},
		{name: "older CLI: no --source/--target", code: recipe.CodeSourceReuseCommandUnsupported, reason: "flags_absent",
			edit: func(t *testing.T, h *fakeHost) {
				h.handlers["bootc image copy-to-storage --help"] = func() ([]byte, error) { return []byte("Usage: bootc image copy-to-storage\n"), nil }
			}},
		{name: "help fails otherwise", code: recipe.CodeSourceReuseCommandUnsupported, reason: "check_failed",
			edit: func(t *testing.T, h *fakeHost) {
				h.handlers["bootc image copy-to-storage --help"] = func() ([]byte, error) { return nil, exitErr(t, 137) }
			}},
		{name: "inaccessible store: ref unreadable", code: recipe.CodeSourceReuseSourceUnavailable, reason: "ref_unreadable",
			edit: func(t *testing.T, h *fakeHost) { h.revParse = []string{"error"} }},
		{name: "source moved: ref points at a staged update", code: recipe.CodeSourceReuseSourceMoved, reason: "ref_moved",
			edit: func(t *testing.T, h *fakeHost) { h.revParse = []string{otherSum} }},
		{name: "candidate of another digest is never used", code: recipe.CodeSourceReuseSourceUnavailable, reason: "ref_unreadable",
			candidates: []probe.ReuseCandidate{func() probe.ReuseCandidate { c := candidate(); c.Digest = copyDigest; return c }()},
			edit:       func(*testing.T, *fakeHost) {}},
		{name: "missing deployment checkout", code: recipe.CodeSourceReuseEstimateFailed, reason: "missing",
			edit: func(t *testing.T, h *fakeHost) { _ = os.RemoveAll(filepath.Join(h.root, dep)) }},
		{name: "disk exhaustion before the copy", code: recipe.CodeSourceReuseInsufficientSpace, reason: "insufficient_space",
			edit: func(t *testing.T, h *fakeHost) { h.free = 512 << 20 }},
		{name: "destination name taken", code: recipe.CodeSourceReuseDestinationExists, reason: "exists",
			edit: func(t *testing.T, h *fakeHost) {
				h.handlers["skopeo inspect containers-storage:"+dest] = func() ([]byte, error) { return []byte(`{}`), nil }
			}},
		{name: "failed transfer", code: recipe.CodeSourceReuseTransferFailed, reason: "command_failed", copied: true,
			edit: func(t *testing.T, h *fakeHost) {
				h.handlers["bootc image copy-to-storage --source "+srcRef+" --target "+dest] = func() ([]byte, error) { return nil, exitErr(t, 1) }
			}},
		{name: "disk exhaustion during the copy", code: recipe.CodeSourceReuseDiskExhausted, reason: "disk_exhausted", copied: true,
			edit: func(t *testing.T, h *fakeHost) {
				h.handlers["bootc image copy-to-storage --source "+srcRef+" --target "+dest] = func() ([]byte, error) {
					h.mu.Lock()
					h.free = 10 << 20
					h.mu.Unlock()
					time.Sleep(5 * time.Millisecond)
					return nil, fmt.Errorf("bootc: %w", syscall.ENOSPC)
				}
			}},
		{name: "source moved during the copy", code: recipe.CodeSourceReuseIdentityMismatch, reason: "source_moved_during_copy", copied: true,
			edit: func(t *testing.T, h *fakeHost) { h.revParse = []string{testChecksum, otherSum} }},
		{name: "deployment changed during the copy", code: recipe.CodeSourceReuseIdentityMismatch, reason: "deployment_changed", copied: true,
			edit: func(t *testing.T, h *fakeHost) { h.resolve = probe.ReuseSource{Reason: probe.ReuseItemNotFound} }},
		{name: "corrupt image: copy unreadable", code: recipe.CodeSourceReuseVerifyFailed, reason: "destination_unreadable", copied: true,
			edit: func(t *testing.T, h *fakeHost) {
				h.handlers["skopeo inspect containers-storage:"+dest] = func() ([]byte, error) { return nil, exitErr(t, 1) }
			}},
		{name: "malformed JSON from skopeo", code: recipe.CodeSourceReuseVerifyFailed, reason: "malformed_output", copied: true,
			edit: func(t *testing.T, h *fakeHost) {
				h.handlers["skopeo inspect containers-storage:"+dest] = afterCopy(h, []byte("\x1b[31mnot json"))
			}},
		{name: "copy has no digest", code: recipe.CodeSourceReuseVerifyFailed, reason: "destination_digest_invalid", copied: true,
			edit: func(t *testing.T, h *fakeHost) {
				h.handlers["skopeo inspect containers-storage:"+dest] = afterCopy(h, []byte(`{"Digest":"latest","Architecture":"amd64","Os":"linux"}`))
			}},
		{name: "wrong architecture", code: recipe.CodeSourceReuseIdentityMismatch, reason: "platform_mismatch", copied: true,
			edit: func(t *testing.T, h *fakeHost) {
				h.handlers["skopeo inspect containers-storage:"+dest] = afterCopy(h, []byte(`{"Digest":"`+copyDigest+`","Architecture":"arm64","Os":"linux"}`))
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newFakeHost(t)
			c.edit(t, h)
			s := New(h.env(), testDigest, "x86_64", scratch)
			cands := c.candidates
			if cands == nil {
				cands = []probe.ReuseCandidate{candidate()}
			}
			res, err := s.Acquire(cands)
			if res != nil {
				t.Fatalf("Acquire succeeded: %+v", res)
			}
			f := failureOf(t, err)
			if f.Code != c.code || f.Reason != c.reason {
				t.Errorf("got %s/%s (%s), want %s/%s", f.Code, f.Reason, f.Message, c.code, c.reason)
			}
			if got := h.ran("bootc image copy-to-storage --source"); got != c.copied {
				t.Errorf("copy ran: %v, want %v", got, c.copied)
			}
			phases := h.phases()
			if len(phases) == 0 || phases[len(phases)-1] != "failed:"+c.code+":"+c.reason {
				t.Errorf("phases %v", phases)
			}
			// Whatever happened, the bind is gone after the attempt or
			// the release, the copy is removed if it may exist, and the
			// scratch is deleted.
			h.calls = nil
			if err := s.Release(); err != nil {
				t.Fatalf("Release: %v", err)
			}
			wantRmi := c.copied
			if got := h.ran("podman rmi --ignore " + dest); got != wantRmi {
				t.Errorf("rmi ran: %v, want %v (%v)", got, wantRmi, h.calls)
			}
			if h.ran("umount") {
				t.Errorf("Release unmounted again after the copy's own unbind: %v", h.calls)
			}
			if _, err := os.Stat(filepath.Join(h.root, scratch)); !os.IsNotExist(err) {
				t.Errorf("scratch left behind: %v", err)
			}
		})
	}
}

// afterCopy answers skopeo inspect "absent" before the copy and out after.
func afterCopy(h *fakeHost, out []byte) func() ([]byte, error) {
	return func() ([]byte, error) {
		if !h.ran("bootc image copy-to-storage --source") {
			return nil, exitErr(h.t, 1)
		}
		return out, nil
	}
}

// TestAcquire_Cancelled: the install's teardown releases the session while
// the copy is still running (a cancel). The bind and the copy go, the
// scratch is deleted, and nothing else starts.
func TestAcquire_Cancelled(t *testing.T) {
	h := newFakeHost(t)
	started := make(chan struct{})
	finish := make(chan struct{})
	h.handlers["bootc image copy-to-storage --source "+srcRef+" --target "+dest] = func() ([]byte, error) {
		close(started)
		<-finish
		return nil, exitErr(t, 143) // SIGTERM from the terminator
	}
	s := New(h.env(), testDigest, "x86_64", scratch)
	errc := make(chan error, 1)
	go func() {
		_, err := s.Acquire([]probe.ReuseCandidate{candidate()})
		errc <- err
	}()
	<-started
	if err := s.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if !h.ran("umount /var/tmp") || !h.ran("podman rmi --ignore "+dest) {
		t.Errorf("release did not unbind and remove the copy: %v", h.calls)
	}
	if _, err := os.Stat(filepath.Join(h.root, scratch)); !os.IsNotExist(err) {
		t.Errorf("scratch left behind: %v", err)
	}
	h.mu.Lock()
	n := len(h.calls)
	h.mu.Unlock()
	close(finish)
	err := <-errc
	f := failureOf(t, err)
	if f.Code != recipe.CodeSourceReuseTransferFailed {
		t.Errorf("code %s", f.Code)
	}
	h.mu.Lock()
	after := h.calls[n:]
	h.mu.Unlock()
	if len(after) != 0 {
		t.Errorf("commands after the release: %v", after)
	}
}

func TestRelease_NothingDone(t *testing.T) {
	h := newFakeHost(t)
	s := New(h.env(), testDigest, "x86_64", scratch)
	if err := s.Release(); err != nil || len(h.calls) != 0 || len(h.events) != 0 {
		t.Errorf("Release of an unused session: calls %v events %v err %v", h.calls, h.events, err)
	}
}

func TestRelease_ReportsFailures(t *testing.T) {
	h := newFakeHost(t)
	h.handlers["podman rmi --ignore "+dest] = func() ([]byte, error) { return nil, exitErr(t, 125) }
	s := New(h.env(), testDigest, "x86_64", scratch)
	if _, err := s.Acquire([]probe.ReuseCandidate{candidate()}); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(); err == nil {
		t.Error("a failed rmi is not reported")
	}
	if last := h.events[len(h.events)-1]; last.Reason != "remove_failed" {
		t.Errorf("last event %+v", last)
	}
}

// TestAllowed is the allowlist: the commands the session needs, with the
// exact shapes it uses, and nothing else.
func TestAllowed(t *testing.T) {
	vt := scratch + "/var-tmp"
	ok := [][]string{
		{"bootc", "image", "copy-to-storage", "--help"},
		{"bootc", "image", "copy-to-storage", "--source", srcRef, "--target", dest},
		{"bootc", "image", "copy-to-storage", "--source", "containers-storage:localhost/x:1", "--target", dest},
		{"ostree", "rev-parse", "--repo=/sysroot/ostree/repo", candidate().OstreeRef},
		{"skopeo", "inspect", "containers-storage:" + dest},
		{"podman", "rmi", "--ignore", dest},
		{"mount", "--bind", vt, "/var/tmp"},
		{"umount", "/var/tmp"},
	}
	for _, argv := range ok {
		if err := Allowed(vt, argv[0], argv[1:]); err != nil {
			t.Errorf("%v refused: %v", argv, err)
		}
	}
	refused := [][]string{
		{"bootc", "image", "set-unified"},
		{"bootc", "image", "pull-from-default-storage", "ghcr.io/x:1"},
		{"bootc", "image", "cmd", "pull", "ghcr.io/x:1"},
		{"bootc", "image", "cmd", "build", "."},
		{"bootc", "image", "list", "--format", "json"},
		{"bootc", "image", "copy-to-storage"}, // defaults: booted image to localhost/bootc
		{"bootc", "image", "copy-to-storage", "--source", srcRef},
		{"bootc", "image", "copy-to-storage", "--target", dest},
		{"bootc", "image", "copy-to-storage", "--source", srcRef, "--target", "localhost/bootc"},
		{"bootc", "image", "copy-to-storage", "--source", srcRef, "--target", "ghcr.io/example/os:stable"},
		{"bootc", "image", "copy-to-storage", "--source", "ghcr.io/x:1 ; rm -rf /", "--target", dest},
		{"bootc", "image", "copy-to-storage", "--source", "docker://../etc", "--target", dest},
		{"bootc", "switch", "ghcr.io/x:1"},
		{"bootc", "upgrade"},
		{"ostree", "prune", "--repo=/sysroot/ostree/repo"},
		{"ostree", "rev-parse", "--repo=/tmp/repo", candidate().OstreeRef},
		{"ostree", "rev-parse", "--repo=/sysroot/ostree/repo", "ostree/0/1/0"},
		{"skopeo", "inspect", "docker://ghcr.io/example/os:stable"},
		{"skopeo", "copy", "containers-storage:" + dest, "docker://ghcr.io/x:1"},
		{"skopeo", "inspect", "--creds", "u:p", "containers-storage:" + dest},
		{"podman", "pull", "ghcr.io/x:1"},
		{"podman", "login", "ghcr.io"},
		{"podman", "image", "prune", "-a"},
		{"podman", "system", "prune"},
		{"podman", "rmi", "--ignore", "ghcr.io/example/os:stable"},
		{"podman", "rmi", "-f", dest},
		{"podman", "rmi", "--ignore", "localhost/bootc"},
		{"mount", "--bind", "/", "/var/tmp"},
		{"mount", "--bind", vt, "/etc"},
		{"umount", "/var"},
		{"sh", "-c", "true"},
	}
	for _, argv := range refused {
		if err := Allowed(vt, argv[0], argv[1:]); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("%v allowed", argv)
		}
	}
}

// TestSession_RefusesOutsideTheAllowlist: the session's own runner checks
// the allowlist before it calls the host.
func TestSession_RefusesOutsideTheAllowlist(t *testing.T) {
	h := newFakeHost(t)
	s := New(h.env(), testDigest, "x86_64", scratch)
	if _, err := s.output("podman", "pull", "ghcr.io/x:1"); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("err %v", err)
	}
	if err := s.run("bootc", "image", "set-unified"); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("err %v", err)
	}
	if len(h.calls) != 0 {
		t.Errorf("the host saw %v", h.calls)
	}
}

func TestAcquire_VarTmpMode(t *testing.T) {
	h := newFakeHost(t)
	var mode os.FileMode
	h.handlers["mount --bind "+scratch+"/var-tmp /var/tmp"] = func() ([]byte, error) {
		fi, err := os.Stat(filepath.Join(h.root, scratch, "var-tmp"))
		if err != nil {
			return nil, err
		}
		mode = fi.Mode()
		return nil, nil
	}
	s := New(h.env(), testDigest, "x86_64", scratch)
	if _, err := s.Acquire([]probe.ReuseCandidate{candidate()}); err != nil {
		t.Fatal(err)
	}
	if mode.Perm() != 0o777 || mode&os.ModeSticky == 0 {
		t.Errorf("the directory bound over /var/tmp is %v, want drwxrwxrwt", mode)
	}
}

// bindTracker is the /var/tmp mount table the session changes: our bind is
// on or off, and an umount without it would unmount the host's own
// /var/tmp. It also checks that the scratch, the bind's source, is still
// there whenever the bind is undone.
type bindTracker struct {
	mu        sync.Mutex
	bound     bool
	mounts    int
	umounts   int
	hostUmnt  int // umount /var/tmp with our bind not in place
	lateUmnt  int // umount after the scratch was removed
	failUmnts int // umounts still to fail
}

func trackBind(h *fakeHost) *bindTracker {
	b := &bindTracker{}
	h.handlers["mount --bind "+filepath.Join(scratch, "var-tmp")+" "+VarTmp] = func() ([]byte, error) {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.mounts++
		b.bound = true
		return nil, nil
	}
	h.handlers["umount "+VarTmp] = func() ([]byte, error) {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.failUmnts > 0 {
			b.failUmnts--
			return nil, exitErr(h.t, 32)
		}
		b.umounts++
		if !b.bound {
			b.hostUmnt++
		}
		if _, err := os.Stat(filepath.Join(h.root, scratch)); err != nil {
			b.lateUmnt++
		}
		b.bound = false
		return nil, nil
	}
	return b
}

// check: one mount, one umount of it, nothing of the host's, and the
// scratch removed only after the bind went.
func (b *bindTracker) check(t *testing.T, h *fakeHost) {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.bound || b.mounts != 1 || b.umounts != 1 || b.hostUmnt != 0 || b.lateUmnt != 0 {
		t.Errorf("bind: still bound %v, %d mounts, %d umounts, %d of the host's /var/tmp, %d after the scratch went; calls %v",
			b.bound, b.mounts, b.umounts, b.hostUmnt, b.lateUmnt, h.calls)
	}
	if _, err := os.Stat(filepath.Join(h.root, scratch)); !os.IsNotExist(err) {
		t.Errorf("scratch left behind: %v", err)
	}
}

func withReleaseHook(t *testing.T, fn func()) {
	t.Helper()
	releaseHook = fn
	t.Cleanup(func() { releaseHook = nil })
}

// The VM run's order (43186222564): the cancel kills the copy, the copy
// returns and goes to unbind while Release has just begun. The copy's
// unbind must still run, and Release must not remove the scratch under a
// bind nor unmount twice.
func TestRelease_CopyUnbindsDuringRelease(t *testing.T) {
	h := newFakeHost(t)
	b := trackBind(h)
	started := make(chan struct{})
	finish := make(chan struct{})
	h.handlers["bootc image copy-to-storage --source "+srcRef+" --target "+dest] = func() ([]byte, error) {
		close(started)
		<-finish
		return nil, exitErr(t, 143)
	}
	s := New(h.env(), testDigest, "x86_64", scratch)
	errc := make(chan error, 1)
	go func() {
		_, err := s.Acquire([]probe.ReuseCandidate{candidate()})
		errc <- err
	}()
	<-started
	var acquireErr error
	withReleaseHook(t, func() {
		close(finish)       // the terminator's SIGTERM ends the copy
		acquireErr = <-errc // and the copy's own unbind runs to its end
	})
	if err := s.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if f := failureOf(t, acquireErr); f.Code != recipe.CodeSourceReuseTransferFailed {
		t.Errorf("code %s", f.Code)
	}
	b.check(t, h)
}

// The cancel lands while the bind is being made: Release waits for it and
// undoes it; the copy never starts.
func TestRelease_DuringBind(t *testing.T) {
	h := newFakeHost(t)
	b := trackBind(h)
	mountKey := "mount --bind " + filepath.Join(scratch, "var-tmp") + " " + VarTmp
	inner := h.handlers[mountKey]
	started := make(chan struct{})
	finish := make(chan struct{})
	h.handlers[mountKey] = func() ([]byte, error) {
		close(started)
		<-finish
		return inner()
	}
	s := New(h.env(), testDigest, "x86_64", scratch)
	errc := make(chan error, 1)
	go func() {
		_, err := s.Acquire([]probe.ReuseCandidate{candidate()})
		errc <- err
	}()
	<-started
	withReleaseHook(t, func() {
		close(finish)
		<-errc // the copy finds the session released after its bind
	})
	if err := s.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if h.ran("bootc image copy-to-storage --source") {
		t.Errorf("the copy ran after the release: %v", h.calls)
	}
	b.check(t, h)
}

// The copy finished: its own unbind is the only one.
func TestRelease_AfterCopyFinished(t *testing.T) {
	h := newFakeHost(t)
	b := trackBind(h)
	s := New(h.env(), testDigest, "x86_64", scratch)
	if _, err := s.Acquire([]probe.ReuseCandidate{candidate()}); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	b.check(t, h)
}

// The copy failed and so did its unbind: Release unbinds, once.
func TestRelease_RetriesAFailedCopyUnbind(t *testing.T) {
	h := newFakeHost(t)
	b := trackBind(h)
	b.failUmnts = 1
	h.handlers["bootc image copy-to-storage --source "+srcRef+" --target "+dest] = func() ([]byte, error) {
		return nil, exitErr(t, 1)
	}
	s := New(h.env(), testDigest, "x86_64", scratch)
	if _, err := s.Acquire([]probe.ReuseCandidate{candidate()}); err == nil {
		t.Fatal("a failed copy must fail")
	}
	if err := s.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	b.check(t, h)
}

// Release cannot unbind: the scratch stays (removing it would leave
// /var/tmp on a deleted directory), and the error says so.
func TestRelease_KeepsScratchUnderAStuckBind(t *testing.T) {
	h := newFakeHost(t)
	b := trackBind(h)
	started := make(chan struct{})
	finish := make(chan struct{})
	h.handlers["bootc image copy-to-storage --source "+srcRef+" --target "+dest] = func() ([]byte, error) {
		close(started)
		<-finish
		return nil, exitErr(t, 143)
	}
	s := New(h.env(), testDigest, "x86_64", scratch)
	errc := make(chan error, 1)
	go func() {
		_, err := s.Acquire([]probe.ReuseCandidate{candidate()})
		errc <- err
	}()
	<-started
	b.mu.Lock()
	b.failUmnts = 2 // the copy's and Release's
	b.mu.Unlock()
	withReleaseHook(t, func() { close(finish); <-errc })
	err := s.Release()
	if err == nil || !strings.Contains(err.Error(), "still bound") {
		t.Fatalf("Release: %v, want the stuck bind reported", err)
	}
	if _, err := os.Stat(filepath.Join(h.root, scratch)); err != nil {
		t.Errorf("the scratch under the bind was removed: %v", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.hostUmnt != 0 || !b.bound {
		t.Errorf("tracker %+v", b)
	}
}

// Released before the copy: no bind at all.
func TestRelease_BeforeBind(t *testing.T) {
	h := newFakeHost(t)
	b := trackBind(h)
	s := New(h.env(), testDigest, "x86_64", scratch)
	if err := s.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Acquire([]probe.ReuseCandidate{candidate()}); err == nil {
		t.Fatal("Acquire after Release must fail")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.mounts != 0 || b.umounts != 0 {
		t.Errorf("tracker %+v; calls %v", b, h.calls)
	}
}
