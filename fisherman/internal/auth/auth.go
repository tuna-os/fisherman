// Package auth attributes Varlink calls to a peer and authorizes the
// mutating ones: socket activation hands the daemon its listeners,
// SO_PEERCRED identifies who called, and a Checker approves the action
// through the polkit authority.
//
// The real Checker shells out to pkcheck, which asks polkitd over D-Bus;
// no D-Bus client code lives here. Secrets never appear: actions name an
// interface and a method, never a device, a slot or a credential.
package auth

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/tuna-os/fisherman/internal/runner"
)

// ErrDenied means the polkit authority refused the action.
var ErrDenied = errors.New("auth: polkit authority denied the action")

// Creds identifies a connected peer: the PID, UID and GID the kernel
// reports for the other end of the socket (SO_PEERCRED), and the start time
// of that process.
type Creds struct {
	PID int
	UID uint32
	GID uint32
	// StartTime is the peer's start time in clock ticks since boot (field
	// 22 of /proc/<pid>/stat), read when the connection is accepted. With
	// it the authority can tell the peer from a later process that got the
	// same PID. Zero means unknown, and PkcheckChecker denies it.
	StartTime uint64
}

type ctxKey struct{}

// WithCreds returns ctx carrying c.
func WithCreds(ctx context.Context, c Creds) context.Context {
	return context.WithValue(ctx, ctxKey{}, c)
}

// Of returns the peer credentials in ctx, if the server attached any. A
// peer without credentials is unattributable: a caller that authorizes must
// deny it.
func Of(ctx context.Context) (Creds, bool) {
	c, ok := ctx.Value(ctxKey{}).(Creds)
	return c, ok
}

// parseStartTime returns field 22 (starttime) of a Linux /proc/<pid>/stat
// line.
// Field 2 is the command name in parentheses and may itself hold spaces and
// parentheses, so the fields are counted from the last ")".
func parseStartTime(stat string) (uint64, error) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, fmt.Errorf("auth: malformed /proc stat line")
	}
	// rest[0] is field 3 (state), so starttime is rest[19].
	rest := strings.Fields(stat[i+1:])
	if len(rest) < 20 {
		return 0, fmt.Errorf("auth: short /proc stat line")
	}
	start, err := strconv.ParseUint(rest[19], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("auth: bad start time in /proc stat line")
	}
	return start, nil
}

// Checker approves one mutating call. action names the method, for example
// "io.tunaos.Fisherman.Install". Implementations must not log anything
// beyond the action: details may sit next to a secret.
type Checker interface {
	Check(ctx context.Context, c Creds, action string) error
}

// Fake records checks and answers as told. Tests use it to prove the
// daemon consulted the authority without shelling out.
type Fake struct {
	Calls []string
	Allow bool
}

// Check implements Checker.
func (f *Fake) Check(_ context.Context, _ Creds, action string) error {
	f.Calls = append(f.Calls, action)
	if f.Allow {
		return nil
	}
	return ErrDenied
}

// PkcheckChecker asks the polkit authority through pkcheck, its D-Bus
// client: pkcheck exits 0 only when the authority authorizes the peer for
// the action. No --allow-user-interaction: the daemon has no session to
// answer a prompt, so anything but yes stays a denial.
//
// The matching policy (which UIDs may install) ships with the installer
// image; until then real-mode mutating calls are denied.
type PkcheckChecker struct {
	// Run executes pkcheck. Nil shells out; tests substitute a stub.
	Run func(ctx context.Context, c Creds, action string) error
}

// Check implements Checker. Credentials without a start time are denied
// and pkcheck does not run: a bare PID would let the authority judge
// whichever process holds that PID when it looks.
func (p PkcheckChecker) Check(ctx context.Context, c Creds, action string) error {
	if c.PID <= 0 || c.StartTime == 0 {
		return fmt.Errorf("pkcheck: peer has no start time: %w", ErrDenied)
	}
	run := p.Run
	if run == nil {
		run = pkcheck
	}
	return run(ctx, c, action)
}

// pkcheckArgs names the peer as pid,start-time,uid. pkcheck(1) asks for
// this form and calls the shorter ones racy: given only a PID, pkcheck
// reads the owner from /proc when it runs, so a peer that connects and
// exits, or executes a set-user-ID program, is judged as whoever holds the
// PID then (root, when a root process reuses it). Here the UID is the one
// the kernel recorded for the socket, and the authority refuses a PID whose
// start time no longer matches.
func pkcheckArgs(c Creds, action string) []string {
	return []string{"--process", fmt.Sprintf("%d,%d,%d", c.PID, c.StartTime, c.UID),
		"--action-id", action}
}

func pkcheck(ctx context.Context, c Creds, action string) error {
	// HostArgs routes through flatpak-spawn on the host inside a sandbox;
	// CommandContext (not Command) keeps context cancellation.
	name, args := runner.HostArgs("pkcheck", pkcheckArgs(c, action))
	cmd := exec.CommandContext(ctx, name, args...)
	if err := cmd.Run(); err != nil {
		// Name the tool only: PIDs and action names stay out of errors,
		// and pkcheck's stderr may describe local policy.
		return fmt.Errorf("pkcheck: %w", ErrDenied)
	}
	return nil
}
