package main

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
)

// Why fisherman watches its parent
//
// Frontends run fisherman as root through pkexec (or sudo), usually inside a
// wrapper they own: `bash -c 'pkexec fisherman "$1" >log 2>&1; exit $?'`,
// under Flatpak behind `flatpak-spawn --host`. pkexec execs fisherman with
// real, effective and saved UID 0. kill(2) only lets an unprivileged sender
// signal a process whose real or saved set-user-ID equals the sender's real
// or effective UID, so the frontend's SIGTERM — even a killpg on the
// wrapper's group — gets EPERM for fisherman and never reaches the cancel
// path in terminate.go.
//
// The frontend can always kill the wrapper it spawned. PR_SET_PDEATHSIG makes
// the kernel send fisherman SIGTERM when that parent dies, which crosses the
// privilege boundary and lands in the same signal handler as any other
// cancel. The frontend contract is therefore: kill your wrapper process (the
// direct child you spawned) and fisherman cancels cleanly. A terminal run
// (`sudo fisherman recipe.json`) behaves the same way: if sudo or the shell
// above it dies, the install is cancelled and torn down rather than left
// running unattended.
//
// Details that make it reliable:
//
//   - The kernel stores the parent-death signal on the thread that set it,
//     and drops it if that thread exits. init locks the main goroutine to the
//     main OS thread, which lives as long as the process, and
//     armParentDeathCancel runs on it from main().
//   - The setting is cleared by execve of a set-user-ID binary and by a
//     credential change. pkexec's exec happens before fisherman sets it, and
//     fisherman never execs itself or changes its own credentials afterwards;
//     the children it forks do not inherit it.
//   - The signal fires when the parent thread exits; the wrappers above
//     (bash, sudo, pkexec's caller) are single-threaded.
//   - If the parent died before the signal was armed, nothing would ever
//     fire. startPPID is read during package initialisation, before main;
//     if getppid() no longer matches once armed, fisherman was re-parented
//     (to init or a subreaper) and cancels at once.

func init() {
	runtime.LockOSThread()
}

// startPPID is fisherman's parent as early as Go code can observe it.
var startPPID = os.Getppid()

// Seams for tests.
var (
	setParentDeathSignal = func(sig syscall.Signal) error {
		_, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_PDEATHSIG, uintptr(sig), 0)
		if errno != 0 {
			return errno
		}
		return nil
	}
	getppid     = os.Getppid
	raiseToSelf = func(sig syscall.Signal) error { return syscall.Kill(os.Getpid(), sig) }
)

// armParentDeathCancel asks the kernel to send SIGTERM when fisherman's parent
// exits, and cancels at once if the parent is already gone. It must run after
// watchSignals so the signal is handled rather than fatal.
func (t *terminator) armParentDeathCancel() {
	if err := setParentDeathSignal(syscall.SIGTERM); err != nil {
		fmt.Fprintf(t.stderr, "fisherman: warning: cannot cancel on parent exit (prctl PR_SET_PDEATHSIG): %v\n", err)
		return
	}
	if ppid := getppid(); ppid != startPPID {
		fmt.Fprintf(t.stderr, "fisherman: parent process %d exited before startup finished (now %d); cancelling\n", startPPID, ppid)
		if err := raiseToSelf(syscall.SIGTERM); err != nil {
			go t.onSignal(syscall.SIGTERM)
		}
	}
}
