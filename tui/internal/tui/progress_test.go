package tui

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tuna-os/fisherman/tui/internal/config"
	"github.com/tuna-os/fisherman/tui/internal/testbackend"
)

var errExit = errors.New("exit status 1")

const (
	lineStep     = progressLineMsg(`{"type":"step","step":5,"total_steps":8,"step_name":"Installing OS","cumulative_pct":1,"overall_pct":1.5}`)
	lineComplete = progressLineMsg(`{"type":"complete","message":"Installation complete!","overall_pct":100}`)
	lineError    = progressLineMsg(`{"type":"error","message":"bootc install: exit status 1"}`)
)

// feed sends msgs to a progress model that has no backend attached, in
// order, and returns the model and everything its commands produced.
func feed(cfg *config.InstallConfig, msgs ...tea.Msg) (*progressModel, []tea.Msg) {
	m := newProgressModel(cfg, false)
	var out []tea.Msg
	for _, msg := range msgs {
		_, cmd := m.Update(msg)
		out = append(out, drain(cmd)...)
	}
	return m, out
}

// TestProgress_TerminalEventAndExitInEitherOrder: #359. The `complete`
// line and the process exit come from different goroutines. The install is
// done when both are in and the output is drained, whichever came first.
// Before, exit-then-complete left a successful install on the progress
// screen for ever.
func TestProgress_TerminalEventAndExitInEitherOrder(t *testing.T) {
	orders := map[string][]tea.Msg{
		"complete, EOF, exit": {lineStep, lineComplete, progressEOFMsg{}, progressExitMsg{}},
		"complete, exit, EOF": {lineStep, lineComplete, progressExitMsg{}, progressEOFMsg{}},
		"exit, complete, EOF": {lineStep, progressExitMsg{}, lineComplete, progressEOFMsg{}},
	}
	for name, msgs := range orders {
		t.Run(name, func(t *testing.T) {
			m, out := feed(testConfig(), msgs...)
			if got := steps(out); !reflect.DeepEqual(got, []step{stepDone}) {
				t.Fatalf("transitions = %v, want exactly one, to stepDone", got)
			}
			if m.failure != "" {
				t.Errorf("failure = %q, want none", m.failure)
			}
			if m.pct != 1.0 {
				t.Errorf("pct = %v, want 1.0", m.pct)
			}
		})
	}
}

// Nothing is decided before both the exit and the EOF are in.
func TestProgress_PendingUntilExitAndEOF(t *testing.T) {
	for name, msgs := range map[string][]tea.Msg{
		"complete only":     {lineComplete},
		"complete and EOF":  {lineComplete, progressEOFMsg{}},
		"complete and exit": {lineComplete, progressExitMsg{}},
		"exit only":         {progressExitMsg{}},
	} {
		m, out := feed(testConfig(), msgs...)
		if len(steps(out)) != 0 || m.failure != "" || m.state.settled {
			t.Errorf("%s: decided early (transitions %v, failure %q)", name, steps(out), m.failure)
		}
	}
}

func TestProgress_Failures(t *testing.T) {
	cases := []struct {
		name string
		msgs []tea.Msg
		want string // substring of the failure
	}{
		// fisherman said complete, then its teardown failed.
		{"complete plus nonzero exit", []tea.Msg{lineComplete, progressEOFMsg{}, progressExitMsg{err: errExit}}, "reported completion but exited"},
		{"nonzero exit, complete still buffered", []tea.Msg{progressExitMsg{err: errExit}, lineComplete, progressEOFMsg{}}, "reported completion but exited"},
		// The error line was still in the pipe when the exit arrived: it
		// is the message, not "exit status 1".
		{"nonzero exit plus buffered error", []tea.Msg{progressExitMsg{err: errExit}, lineError, progressEOFMsg{}}, "bootc install: exit status 1"},
		{"error then exit", []tea.Msg{lineError, progressEOFMsg{}, progressExitMsg{err: errExit}}, "bootc install: exit status 1"},
		{"zero exit and EOF without complete", []tea.Msg{lineStep, progressEOFMsg{}, progressExitMsg{}}, "without reporting completion"},
		{"nonzero exit, no event", []tea.Msg{progressEOFMsg{}, progressExitMsg{err: errExit}}, "exit status 1"},
		{"error after complete", []tea.Msg{lineComplete, lineError, progressEOFMsg{}, progressExitMsg{}}, "bootc install: exit status 1"},
		{"did not start", []tea.Msg{progressStartedMsg{err: errors.New("fisherman is not installed")}}, "could not start fisherman"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, out := feed(testConfig(), tc.msgs...)
			if got := steps(out); len(got) != 0 {
				t.Fatalf("a failed install moved on to %v", got)
			}
			if !strings.Contains(m.failure, tc.want) {
				t.Errorf("failure = %q, want it to contain %q", m.failure, tc.want)
			}
			if !strings.Contains(m.View(), "Enter quit") {
				t.Error("a failed install must offer a way out")
			}
			// Enter quits once the failure is shown.
			_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			if quits(drain(cmd)) != 1 {
				t.Error("Enter after a failure must quit")
			}
		})
	}
}

// Repeated terminal messages do not transition twice.
func TestProgress_TransitionsOnce(t *testing.T) {
	_, out := feed(testConfig(),
		lineComplete, lineComplete, progressEOFMsg{}, progressExitMsg{},
		progressExitMsg{}, progressEOFMsg{}, lineComplete, progressExitMsg{err: errExit})
	if got := steps(out); !reflect.DeepEqual(got, []step{stepDone}) {
		t.Fatalf("transitions = %v, want exactly one", got)
	}
	// And a later exit status does not rewrite a settled failure.
	m, out := feed(testConfig(), progressEOFMsg{}, progressExitMsg{err: errExit}, progressExitMsg{}, lineComplete, progressEOFMsg{})
	if len(steps(out)) != 0 || m.failure == "" {
		t.Errorf("a settled failure was undone: transitions %v, failure %q", steps(out), m.failure)
	}
}

// overall_pct drives the bar; cumulative_pct is only the fallback (#417).
func TestProgress_ParsesOverallPct(t *testing.T) {
	m := newProgressModel(testConfig(), false)
	m.parseLine(`{"type":"step","step":5,"total_steps":8,"step_name":"Installing OS","cumulative_pct":1,"overall_pct":1}`)
	if m.pct != 0.01 {
		t.Errorf("after step: pct = %v, want 0.01", m.pct)
	}
	m.parseLine(`{"type":"substep","message":"Pulling image: layer 23/71","overall_pct":17.91}`)
	if m.pct != 0.1791 {
		t.Errorf("after substep: pct = %v, want 0.1791 (the bar moves inside a step)", m.pct)
	}
	m.parseLine(`{"type":"info","message":"no percentage on this one"}`)
	if m.pct != 0.1791 {
		t.Errorf("an event without overall_pct moved the bar to %v", m.pct)
	}
	m.parseLine(`{"type":"step","step":6,"total_steps":8,"step_name":"Old backend","cumulative_pct":88}`)
	if m.pct != 0.88 {
		t.Errorf("fallback to cumulative_pct: pct = %v, want 0.88", m.pct)
	}
	m.parseLine("+ mkfs.xfs -f /dev/sda3") // not JSON
	if m.pct != 0.88 || m.logs[len(m.logs)-1] != "+ mkfs.xfs -f /dev/sda3" {
		t.Errorf("a plain line must be logged and change nothing: pct %v", m.pct)
	}
}

// The recovery key is kept, kept out of the log, and gets its own page
// between the install and the done page (#417).
func TestProgress_RecoveryKey(t *testing.T) {
	const key = "3f9c0a7e5b1d2c4e6f8a0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e"
	cfg := testConfig()
	m, out := feed(cfg,
		progressLineMsg(`{"type":"recovery_key","key":"`+key+`"}`),
		lineComplete, progressEOFMsg{}, progressExitMsg{})
	if cfg.RecoveryKey != key {
		t.Fatalf("RecoveryKey = %q, want the key from the event", cfg.RecoveryKey)
	}
	if got := steps(out); !reflect.DeepEqual(got, []step{stepRecoveryKey}) {
		t.Fatalf("transitions = %v, want the recovery key page", got)
	}
	if strings.Contains(strings.Join(m.logs, "\n"), key) || strings.Contains(m.View(), key) {
		t.Error("the recovery key is in the install log")
	}

	// The page shows the key and does not let go without a yes.
	page := newRecoveryKeyModel(cfg)
	if !strings.Contains(page.View(), key) {
		t.Fatal("the recovery key page does not show the key")
	}
	page.saved = false
	again, cmd := page.acknowledge()
	if len(steps(drain(cmd))) != 0 {
		t.Fatal("\"Not yet\" must not leave the recovery key page")
	}
	if _, ok := again.(*recoveryKeyModel); !ok || again == tea.Model(page) {
		t.Fatal("\"Not yet\" must ask again")
	}
	page.saved = true
	_, cmd = page.acknowledge()
	if got := steps(drain(cmd)); !reflect.DeepEqual(got, []step{stepDone}) {
		t.Fatalf("after the acknowledgement: transitions = %v, want stepDone", got)
	}
	// Ctrl+C is not an acknowledgement.
	_, cmd = newRecoveryKeyModel(cfg).Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if msgs := drain(cmd); quits(msgs) != 0 || len(steps(msgs)) != 0 {
		t.Error("Ctrl+C left the recovery key page")
	}
}

// runModel drives a progress model with its real commands until it settles.
func runModel(t *testing.T, m *progressModel) []tea.Msg {
	t.Helper()
	msgs := make(chan tea.Msg, 64)
	spawn := func(cmd tea.Cmd) {
		if cmd != nil {
			go func() { msgs <- cmd() }()
		}
	}
	spawn(m.Init())
	var seen []tea.Msg
	deadline := time.After(20 * time.Second)
	for !m.state.settled {
		select {
		case msg := <-msgs:
			if batch, ok := msg.(tea.BatchMsg); ok {
				for _, c := range batch {
					spawn(c)
				}
				continue
			}
			if msg == nil {
				continue
			}
			seen = append(seen, msg)
			_, cmd := m.Update(msg)
			spawn(cmd)
		case <-deadline:
			t.Fatalf("the install did not settle; state %+v, logs:\n%s", m.state, strings.Join(m.logs, "\n"))
		}
	}
	// Collect what the settling Update asked for.
	timeout := time.After(time.Second)
	for {
		select {
		case msg := <-msgs:
			if batch, ok := msg.(tea.BatchMsg); ok {
				for _, c := range batch {
					spawn(c)
				}
				continue
			}
			seen = append(seen, msg)
			if _, ok := msg.(stepMsg); ok {
				return seen
			}
		case <-timeout:
			return seen
		}
	}
}

// TestInstall_FakeBackend is the cross-module contract test: the model, its
// real process plumbing and a fake fisherman on PATH. The recipe arrives on
// stdin, argv is "-", no file is written, and both streams are read.
func TestInstall_FakeBackend(t *testing.T) {
	dir := testbackend.Install(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	testbackend.Write(t, dir, "events", strings.Join([]string{
		`{"type":"step","step":1,"total_steps":2,"step_name":"Partitioning disk","overall_pct":0}`,
		`+ sfdisk /dev/sda`,
		`{"type":"substep","message":"Deploying image","overall_pct":64.5}`,
		`{"type":"complete","message":"Installation complete!","overall_pct":100}`,
		``,
	}, "\n"))

	cfg := testConfig()
	cfg.EncryptionEnabled, cfg.Passphrase = true, "correct horse battery"
	m := newProgressModel(cfg, false)
	seen := runModel(t, m)

	if got := steps(seen); !reflect.DeepEqual(got, []step{stepDone}) {
		t.Fatalf("transitions = %v, want stepDone once; failure %q", got, m.failure)
	}
	want, err := cfg.Recipe()
	if err != nil {
		t.Fatal(err)
	}
	if got := testbackend.Read(t, dir, "recipe-stdin.json"); got != string(want) {
		t.Errorf("the backend's stdin = %q, want the recipe %q", got, want)
	}
	if got := strings.TrimSpace(testbackend.Read(t, dir, "argv")); got != "-" {
		t.Errorf("argv = %q, want - (no recipe path, no secret)", got)
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Errorf("the install left files in the temp directory: %v", left)
	}
	if log := strings.Join(m.logs, "\n"); !strings.Contains(log, "+ sfdisk /dev/sda") || !strings.Contains(log, "Deploying image") {
		t.Errorf("the log misses backend output:\n%s", log)
	}
}

// A backend that reports an error and exits non-zero: the error event is the
// message, and the wizard does not move on.
func TestInstall_FakeBackendFailure(t *testing.T) {
	dir := testbackend.Install(t)
	testbackend.Write(t, dir, "events", `{"type":"error","message":"invalid recipe: disk /dev/sda is the boot disk","code":"disk_boot_disk"}`+"\n")
	testbackend.Write(t, dir, "exit", "1")

	m := newProgressModel(testConfig(), false)
	seen := runModel(t, m)
	if len(steps(seen)) != 0 {
		t.Fatalf("a failed install moved on: %v", steps(seen))
	}
	if !strings.Contains(m.failure, "disk /dev/sda is the boot disk") {
		t.Errorf("failure = %q, want the backend's message", m.failure)
	}
}

// No backend at all: the page says so; it does not hang or pretend.
func TestInstall_NoBackend(t *testing.T) {
	orig := startBackend
	startBackend = func([]byte) (*backendRun, error) { return nil, errors.New("fisherman is not installed") }
	t.Cleanup(func() { startBackend = orig })
	m := newProgressModel(testConfig(), false)
	runModel(t, m)
	if !strings.Contains(m.failure, "could not start fisherman") {
		t.Errorf("failure = %q", m.failure)
	}
}

// A dry run starts no process, writes no file, and ends on the done page
// with the recipe shown without its secrets.
func TestDryRun(t *testing.T) {
	dir := testbackend.Install(t) // would record any call
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	origDelay := dryRunDelay
	dryRunDelay = 0
	t.Cleanup(func() { dryRunDelay = origDelay })

	cfg := testConfig()
	cfg.EncryptionEnabled, cfg.Passphrase = true, "luks-secret-value"
	m := newProgressModel(cfg, true)
	seen := runModel(t, m)
	if got := steps(seen); !reflect.DeepEqual(got, []step{stepDone}) {
		t.Fatalf("transitions = %v, want stepDone; failure %q", got, m.failure)
	}
	if argv := testbackend.Read(t, dir, "argv"); argv != "" {
		t.Errorf("a dry run called the backend: %q", argv)
	}

	done := newDoneModel(cfg, true)
	view := done.View()
	if !strings.Contains(view, `"disk": "/dev/sda"`) {
		t.Errorf("the dry run does not show the recipe:\n%s", view)
	}
	for _, secret := range []string{"luks-secret-value", "hunter2-password"} {
		if strings.Contains(view, secret) || strings.Contains(done.recipeJSON, secret) {
			t.Errorf("the dry run shows the secret %q", secret)
		}
	}
	if strings.Count(done.recipeJSON, config.Redacted) != 2 {
		t.Errorf("want the passphrase and the password masked:\n%s", done.recipeJSON)
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Errorf("the dry run left files in the temp directory: %v", left)
	}
}

// Ctrl+C during an install asks the backend to stop and waits for it.
func TestProgress_CancelWaitsForBackend(t *testing.T) {
	cancelled := 0
	lines, exit := make(chan string), make(chan error, 1)
	m := newProgressModel(testConfig(), false)
	m.Update(progressStartedMsg{run: &backendRun{lines: lines, exit: exit, cancel: func() { cancelled++ }}})

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil && quits(drain(cmd)) != 0 {
		t.Fatal("Ctrl+C quit while the backend was still running")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cancelled != 1 {
		t.Fatalf("the backend was signalled %d times, want once", cancelled)
	}
	m.Update(progressEOFMsg{})
	_, cmd = m.Update(progressExitMsg{err: errors.New("exit status 130")})
	if quits(drain(cmd)) != 1 {
		t.Fatal("the TUI must quit when the cancelled backend has exited")
	}
	if !strings.Contains(m.failure, "cancelled") {
		t.Errorf("failure = %q, want cancelled", m.failure)
	}
}
