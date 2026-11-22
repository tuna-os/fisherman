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

	"github.com/tuna-os/fisherman/internal/runner"
)

// ErrDenied means the polkit authority refused the action.
var ErrDenied = errors.New("auth: polkit authority denied the action")

// Creds identifies a connected peer: the PID, UID and GID the kernel
// reports for the other end of the socket (SO_PEERCRED).
type Creds struct {
	PID int
	UID uint32
	GID uint32
}

type ctxKey struct{}

// WithCreds returns ctx carrying c.
func WithCreds(ctx context.Context, c Creds) context.Context {
	return context.WithValue(ctx, ctxKey{}, c)
}

// Of returns the peer credentials in ctx, if the server attached any.
// In-process tests never dial a socket, so they have none: callers treat
// absent credentials as unattributable and allow the call.
func Of(ctx context.Context) (Creds, bool) {
	c, ok := ctx.Value(ctxKey{}).(Creds)
	return c, ok
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
// client: pkcheck exits 0 only when the authority authorizes the PID for
// the action. No --allow-user-interaction: the daemon has no session to
// answer a prompt, so anything but yes stays a denial.
//
// The matching policy (which UIDs may install) ships with the installer
// image; until then real-mode mutating calls are denied.
type PkcheckChecker struct {
	// Run executes pkcheck. Nil shells out; tests substitute a stub.
	Run func(ctx context.Context, pid int, action string) error
}

// Check implements Checker.
func (p PkcheckChecker) Check(ctx context.Context, c Creds, action string) error {
	run := p.Run
	if run == nil {
		run = pkcheck
	}
	return run(ctx, c.PID, action)
}

func pkcheck(ctx context.Context, pid int, action string) error {
	// HostArgs routes through flatpak-spawn on the host inside a sandbox;
	// CommandContext (not Command) keeps context cancellation.
	name, args := runner.HostArgs("pkcheck",
		[]string{"--process", fmt.Sprint(pid), "--action-id", action})
	cmd := exec.CommandContext(ctx, name, args...)
	if err := cmd.Run(); err != nil {
		// Name the tool only: PIDs and action names stay out of errors,
		// and pkcheck's stderr may describe local policy.
		return fmt.Errorf("pkcheck: %w", ErrDenied)
	}
	return nil
}
