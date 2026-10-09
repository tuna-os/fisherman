package runner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// ErrHalted is returned instead of starting a subprocess once Halt has been
// called. It stops the install pipeline from launching its next step (a mkfs,
// a mount, a bootc install) while fisherman is tearing the target down after
// a cancel or a fatal error.
var ErrHalted = errors.New("installation is shutting down; subprocess not started")

var halted atomic.Bool

// teardownCommands may still run after Halt: they are what post.Cleanup and
// the disk unmount helpers use to release mounts and LUKS mappings, so
// refusing them would leave the disk in the state Halt exists to avoid.
// cryptsetup is allowed only for closing a mapping, never for creating one.
var teardownCommands = map[string]bool{
	"umount":   true,
	"fuser":    true,
	"blockdev": true,
	"udevadm":  true,
	"sync":     true,
}

// Halt makes every later subprocess start through this package fail with
// ErrHalted, except the teardown commands cleanup needs. It cannot be undone:
// it is called only on the way to os.Exit.
func Halt() { halted.Store(true) }

// Halted reports whether Halt has been called. Packages that build their own
// exec.Cmd (internal/install) check it before Start.
func Halted() bool { return halted.Load() }

// resetHaltForTest clears the halt flag; used by this package's tests.
func resetHaltForTest() { halted.Store(false) }

// checkHalted returns ErrHalted when name/args may not start after Halt.
func checkHalted(name string, args []string) error {
	if !halted.Load() {
		return nil
	}
	if teardownCommands[name] {
		return nil
	}
	if name == "cryptsetup" && len(args) > 0 && (args[0] == "luksClose" || args[0] == "close") {
		return nil
	}
	return fmt.Errorf("%s: %w", name, ErrHalted)
}

// procRoot is /proc; tests point it at a fake tree.
var procRoot = "/proc"

// procEntry identifies one process. The start time (field 22 of
// /proc/<pid>/stat) guards against signalling an unrelated process that
// reused a pid after ours exited.
type procEntry struct {
	pid   int
	start string
}

// readStat returns the parent pid, state and start time of pid.
func readStat(pid int) (ppid int, state, start string, ok bool) {
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, "", "", false
	}
	// The command name (field 2) is parenthesised and may itself contain
	// spaces or parentheses, so split after the last ')'.
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, "", "", false
	}
	fields := strings.Fields(s[i+1:])
	// fields[0] is field 3 (state); fields[1] is ppid; fields[19] is starttime.
	if len(fields) < 20 {
		return 0, "", "", false
	}
	ppid, err = strconv.Atoi(fields[1])
	if err != nil {
		return 0, "", "", false
	}
	return ppid, fields[0], fields[19], true
}

// descendants returns every live (non-zombie) process below root.
func descendants(root int) []procEntry {
	dirs, err := os.ReadDir(procRoot)
	if err != nil {
		return nil
	}
	children := map[int][]procEntry{}
	for _, d := range dirs {
		pid, err := strconv.Atoi(d.Name())
		if err != nil {
			continue
		}
		ppid, state, start, ok := readStat(pid)
		if !ok || state == "Z" || state == "X" {
			continue
		}
		children[ppid] = append(children[ppid], procEntry{pid: pid, start: start})
	}
	var out []procEntry
	queue := []int{root}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, c := range children[p] {
			out = append(out, c)
			queue = append(queue, c.pid)
		}
	}
	return out
}

// alive reports whether e is still the same running process.
func alive(e procEntry) bool {
	_, state, start, ok := readStat(e.pid)
	return ok && start == e.start && state != "Z" && state != "X"
}

// StopDescendants stops every process fisherman has started, directly or
// indirectly, whichever package spawned it (runner, install's CommandFn, the
// disk helpers' exec.Command). It sends SIGTERM, waits up to grace for them
// to exit, then sends SIGKILL to whatever is left.
//
// Processes are tracked by pid and start time from the first scan, so a
// grandchild orphaned when its parent exits (and reparented away from
// fisherman) still receives the SIGKILL. Inside a Flatpak, the child is
// flatpak-spawn, which forwards SIGTERM to the host process; SIGKILL cannot
// be forwarded.
//
// It returns the number of processes that had to be killed with SIGKILL.
func StopDescendants(grace time.Duration) int {
	self := os.Getpid()
	seen := map[int]procEntry{}
	signalAll := func(sig syscall.Signal) {
		for _, e := range descendants(self) {
			seen[e.pid] = e
		}
		for _, e := range seen {
			if alive(e) {
				_ = syscall.Kill(e.pid, sig)
			}
		}
	}

	signalAll(syscall.SIGTERM)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		remaining := 0
		for _, e := range seen {
			if alive(e) {
				remaining++
			}
		}
		if remaining == 0 && len(descendants(self)) == 0 {
			return 0
		}
		time.Sleep(50 * time.Millisecond)
	}

	killed := 0
	for _, e := range descendants(self) {
		seen[e.pid] = e
	}
	for _, e := range seen {
		if alive(e) {
			if syscall.Kill(e.pid, syscall.SIGKILL) == nil {
				killed++
			}
		}
	}
	return killed
}
