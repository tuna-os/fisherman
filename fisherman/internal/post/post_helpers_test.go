package post

// Direct unit tests for the small pure/injectable helpers in post.go that
// had no direct coverage: humanBytes, flatpakAppName, countingReader,
// dirSize and flatpakList. This is an internal (white-box) test file so it
// can reach the unexported helpers; Exec injection matches the pattern the
// external test file uses.

import (
	"bytes"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tuna-os/fisherman/internal/runner"
)

// minimal mock executor for the Exec var.
type stubCommand struct {
	output []byte
	err    error
}

func (c *stubCommand) Run() error   { return c.err }
func (c *stubCommand) Start() error { return c.err }
func (c *stubCommand) Wait() error  { return c.err }
func (c *stubCommand) Output() ([]byte, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.output, nil
}
func (c *stubCommand) SetStdin(io.Reader)  {}
func (c *stubCommand) SetStdout(io.Writer) {}
func (c *stubCommand) SetStderr(io.Writer) {}

type stubExecutor struct {
	responses map[string]*stubCommand
}

func (e *stubExecutor) Command(name string, args ...string) runner.Command {
	full := name
	if len(args) > 0 {
		full += " " + strings.Join(args, " ")
	}
	for k, v := range e.responses {
		if strings.HasPrefix(full, k) {
			return v
		}
	}
	return &stubCommand{}
}

func setupStubExec(t *testing.T) *stubExecutor {
	t.Helper()
	stub := &stubExecutor{responses: map[string]*stubCommand{}}
	old := Exec
	Exec = stub
	t.Cleanup(func() { Exec = old })
	return stub
}

// ── humanBytes ───────────────────────────────────────────────────────────────

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{1536 * 1024, "1.5 MB"},
		{1 << 30, "1.0 GB"},
		{3 << 30, "3.0 GB"},
		{1024 << 30, "1024.0 GB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.in); got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ── flatpakAppName ───────────────────────────────────────────────────────────

func TestFlatpakAppName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"org.mozilla.Firefox/x86_64/stable", "org.mozilla.Firefox"},
		{"org.tunaos.Installer/aarch64/main", "org.tunaos.Installer"},
		{"org.gnome.Nautilus", "org.gnome.Nautilus"}, // no ref suffix
		{"", ""},
	}
	for _, c := range cases {
		if got := flatpakAppName(c.in); got != c.want {
			t.Errorf("flatpakAppName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ── countingReader ───────────────────────────────────────────────────────────

func TestCountingReaderCountsBytesRead(t *testing.T) {
	var n atomic.Int64
	r := &countingReader{r: strings.NewReader("hello world"), n: &n}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello world" {
		t.Errorf("countingReader content = %q", data)
	}
	if n.Load() != int64(len("hello world")) {
		t.Errorf("countingReader counted %d bytes, want %d", n.Load(), len("hello world"))
	}
}

func TestCountingReaderCountsPartialReads(t *testing.T) {
	var n atomic.Int64
	src := bytes.NewBufferString(strings.Repeat("x", 100))
	r := &countingReader{r: src, n: &n}
	buf := make([]byte, 7)
	total := 0
	for {
		k, err := r.Read(buf)
		total += k
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if total != 100 {
		t.Errorf("read %d bytes total, want 100", total)
	}
	if n.Load() != 100 {
		t.Errorf("countingReader counted %d, want 100", n.Load())
	}
}

// ── dirSize ──────────────────────────────────────────────────────────────────

func TestDirSizeParsesDuOutput(t *testing.T) {
	stub := setupStubExec(t)
	stub.responses["du -sb /tmp/some-dir"] = &stubCommand{output: []byte("1234567\t/tmp/some-dir\n")}
	if got := dirSize("/tmp/some-dir"); got != 1234567 {
		t.Errorf("dirSize = %d, want 1234567", got)
	}
}

func TestDirSizeFailsClosed(t *testing.T) {
	stub := setupStubExec(t)
	stub.responses["du -sb"] = &stubCommand{err: io.ErrUnexpectedEOF}
	if got := dirSize("/nonexistent"); got != 0 {
		t.Errorf("dirSize on du failure = %d, want 0", got)
	}
}

func TestDirSizeEmptyOutput(t *testing.T) {
	stub := setupStubExec(t)
	stub.responses["du -sb"] = &stubCommand{output: []byte("")}
	if got := dirSize("/tmp/x"); got != 0 {
		t.Errorf("dirSize on empty du output = %d, want 0", got)
	}
}

// ── flatpakList ──────────────────────────────────────────────────────────────

func TestFlatpakListSystemApps(t *testing.T) {
	stub := setupStubExec(t)
	stub.responses["flatpak list --system --columns=ref --app"] = &stubCommand{
		output: []byte("Ref\norg.mozilla.Firefox/x86_64/stable\norg.tunaos.Installer/aarch64/main\n"),
	}
	got := flatpakList("--system", "--app")
	if len(got) != 2 {
		t.Fatalf("flatpakList = %v, want 2 refs", got)
	}
	if got[0] != "org.mozilla.Firefox/x86_64/stable" || got[1] != "org.tunaos.Installer/aarch64/main" {
		t.Errorf("flatpakList refs = %v", got)
	}
}

func TestFlatpakListIncludesRuntimes(t *testing.T) {
	stub := setupStubExec(t)
	stub.responses["flatpak list --user --columns=ref --runtime"] = &stubCommand{
		output: []byte("org.gnome.Platform/x86_64/50\n"),
	}
	got := flatpakList("--user", "--runtime")
	if len(got) != 1 || got[0] != "org.gnome.Platform/x86_64/50" {
		t.Errorf("flatpakList runtimes = %v", got)
	}
}

func TestFlatpakListCommandFailure(t *testing.T) {
	stub := setupStubExec(t)
	stub.responses["flatpak list"] = &stubCommand{err: io.ErrUnexpectedEOF}
	if got := flatpakList("--system", ""); got != nil {
		t.Errorf("flatpakList on failure = %v, want nil", got)
	}
}
