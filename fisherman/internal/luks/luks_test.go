package luks_test

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/luks"
	"github.com/tuna-os/fisherman/internal/runner"
)

// execCall records a single intercepted subprocess invocation.
type execCall struct {
	name  string
	args  []string
	stdin string
}

// recorder captures all Run/RunWithStdin calls made through runner.RunFn.
type recorder struct {
	calls []execCall
	err   error // returned for every call; nil means success
}

func (r *recorder) run(stdin io.Reader, name string, args ...string) error {
	s := ""
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		s = string(b)
	}
	r.calls = append(r.calls, execCall{name: name, args: args, stdin: s})
	return r.err
}

func setup(t *testing.T) *recorder {
	t.Helper()
	rec := &recorder{}
	runner.RunFn = rec.run
	t.Cleanup(func() { runner.RunFn = runner.DefaultRun })
	return rec
}

func TestFormat(t *testing.T) {
	const part = "/dev/sda3"
	const pass = "hunter2"

	rec := setup(t)
	if err := luks.Format(part, pass); err != nil {
		t.Fatalf("Format: %v", err)
	}

	if len(rec.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(rec.calls))
	}
	c := rec.calls[0]

	if c.name != "cryptsetup" {
		t.Errorf("name = %q, want cryptsetup", c.name)
	}
	wantArgs := []string{"luksFormat", "--batch-mode", "--type=luks2", "--key-file=-", part}
	if !equalSlice(c.args, wantArgs) {
		t.Errorf("args = %v, want %v", c.args, wantArgs)
	}
	if c.stdin != pass {
		t.Errorf("stdin = %q, want passphrase %q", c.stdin, pass)
	}
	// Passphrase must not appear in the argv.
	for _, arg := range c.args {
		if strings.Contains(arg, pass) {
			t.Errorf("passphrase leaked into argv: %q", arg)
		}
	}
}

func TestOpen(t *testing.T) {
	const part = "/dev/sda3"
	const pass = "hunter2"
	const mapper = "fisherman-root"

	rec := setup(t)
	if err := luks.Open(part, pass, mapper); err != nil {
		t.Fatalf("Open: %v", err)
	}

	if len(rec.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(rec.calls))
	}
	c := rec.calls[0]

	if c.name != "cryptsetup" {
		t.Errorf("name = %q, want cryptsetup", c.name)
	}
	wantArgs := []string{"luksOpen", "--key-file=-", part, mapper}
	if !equalSlice(c.args, wantArgs) {
		t.Errorf("args = %v, want %v", c.args, wantArgs)
	}
	if c.stdin != pass {
		t.Errorf("stdin = %q, want passphrase %q", c.stdin, pass)
	}
	for _, arg := range c.args {
		if strings.Contains(arg, pass) {
			t.Errorf("passphrase leaked into argv: %q", arg)
		}
	}
}

func TestClose(t *testing.T) {
	const mapper = "fisherman-root"

	rec := setup(t)
	if err := luks.Close(mapper); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if len(rec.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(rec.calls))
	}
	c := rec.calls[0]

	if c.name != "cryptsetup" {
		t.Errorf("name = %q, want cryptsetup", c.name)
	}
	wantArgs := []string{"luksClose", mapper}
	if !equalSlice(c.args, wantArgs) {
		t.Errorf("args = %v, want %v", c.args, wantArgs)
	}
}

func TestEnrollTPM2(t *testing.T) {
	const part = "/dev/sda3"
	const pass = "hunter2"

	rec := setup(t)
	if err := luks.EnrollTPM2(part, pass); err != nil {
		t.Fatalf("EnrollTPM2: %v", err)
	}

	if len(rec.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(rec.calls))
	}
	c := rec.calls[0]

	if c.name != "systemd-cryptenroll" {
		t.Errorf("name = %q, want systemd-cryptenroll", c.name)
	}
	wantArgs := []string{"--tpm2-device=auto", "--tpm2-pcrs=7", "--unlock-key-file=-", part}
	if !equalSlice(c.args, wantArgs) {
		t.Errorf("args = %v, want %v", c.args, wantArgs)
	}
	if c.stdin != pass {
		t.Errorf("stdin = %q, want passphrase %q", c.stdin, pass)
	}
	for _, arg := range c.args {
		if strings.Contains(arg, pass) {
			t.Errorf("passphrase leaked into argv: %q", arg)
		}
	}
}

func TestFormat_ErrorPropagation(t *testing.T) {
	rec := setup(t)
	rec.err = errors.New("cryptsetup: device busy")

	err := luks.Format("/dev/sda3", "pass")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestOpen_ErrorPropagation(t *testing.T) {
	rec := setup(t)
	rec.err = errors.New("cryptsetup: already open")

	err := luks.Open("/dev/sda3", "pass", "mapper")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestMapperPath(t *testing.T) {
	got := luks.MapperPath("fisherman-root")
	if got != "/dev/mapper/fisherman-root" {
		t.Errorf("MapperPath = %q, want /dev/mapper/fisherman-root", got)
	}
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
