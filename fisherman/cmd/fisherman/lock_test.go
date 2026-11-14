package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/fisherman/internal/recipe"
)

func useLockPath(t *testing.T, path string) {
	t.Helper()
	oldPath, oldLock := installLockPath, installLock
	installLockPath = path
	t.Cleanup(func() {
		if installLock != nil && installLock != oldLock {
			installLock.Close()
		}
		installLockPath, installLock = oldPath, oldLock
	})
}

// tuna-os/fisherman#309: two installs on one host share /var/fisherman-tmp
// and the /var/tmp bind, whatever targetMount and luksMapperName say. The
// second one is refused.
func TestInstallLockRefusesASecondInstall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock", "fisherman-install.lock")
	useLockPath(t, path)

	if err := takeInstallLock(); err != nil {
		t.Fatalf("first lock: %v", err)
	}
	first := installLock
	// A second holder (another open of the file, as another process has).
	err := takeInstallLock()
	if !errors.Is(err, errInstallRunning) {
		t.Fatalf("second lock = %v, want errInstallRunning", err)
	}
	if want := fmt.Sprintf("(process %d)", os.Getpid()); !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not name the holder %s", err, want)
	}
	if installLock != first {
		t.Error("a refused attempt replaced the held lock")
	}
	// The holder ends (here: closes the file, as the kernel does for a dead
	// process): the lock file is still there, and no longer blocks.
	first.Close()
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("lock file: %v", statErr)
	}
	if err := takeInstallLock(); err != nil {
		t.Errorf("lock after the holder ended: %v", err)
	}
}

func TestInstallLockUnusablePathFailsClosed(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	useLockPath(t, filepath.Join(file, "fisherman-install.lock"))
	if err := takeInstallLock(); err == nil || errors.Is(err, errInstallRunning) {
		t.Errorf("takeInstallLock under a file = %v, want a plain error", err)
	}
}

// The lock file is truncated and written as root. A symlink at its name
// must be refused, not followed to the file it points at.
func TestInstallLockDoesNotFollowASymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	const content = "not the lock file\n"
	if err := os.WriteFile(victim, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"existing file": victim,
		"dangling":      filepath.Join(dir, "absent"),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fisherman-install.lock")
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			useLockPath(t, path)
			if err := takeInstallLock(); err == nil || errors.Is(err, errInstallRunning) {
				t.Errorf("takeInstallLock on a symlink = %v, want a plain error", err)
			}
			if got, err := os.ReadFile(victim); err != nil || string(got) != content {
				t.Errorf("the symlink's target = %q, %v; want it untouched", got, err)
			}
			if _, err := os.Lstat(filepath.Join(dir, "absent")); !os.IsNotExist(err) {
				t.Errorf("the dangling symlink's target was created (%v)", err)
			}
		})
	}
}

// The default lock is in a directory only root can write. /run/lock is
// world-writable on Debian and Ubuntu, like the temporary directories: a
// user there could create the lock's name before root does.
func TestInstallLockDefaultDirIsRootOnly(t *testing.T) {
	if dir := filepath.Dir(installLockPath); dir != "/run" {
		t.Errorf("the install lock is in %s, want /run (root-only on every host)", dir)
	}
}

// Two real processes: one holds the lock as a running install does, the
// other is the real CLI and must refuse before it reads its recipe. When the
// holder is killed (no cleanup of its own), the lock is free again.
func TestInstallLockAcrossProcesses(t *testing.T) {
	switch os.Getenv("FISHERMAN_LOCK_CHILD") {
	case "hold":
		installLockPath = os.Getenv("FISHERMAN_LOCK_PATH")
		if err := takeInstallLock(); err != nil {
			fmt.Fprintln(os.Stderr, "hold:", err)
			os.Exit(3)
		}
		fmt.Println("HELD")
		time.Sleep(time.Minute)
		os.Exit(0)
	case "install":
		installLockPath = os.Getenv("FISHERMAN_LOCK_PATH")
		acquireInstallLock = takeInstallLock
		recipeStdin = readerFunc(func([]byte) (int, error) {
			fmt.Fprintln(os.Stderr, "RECIPE READ")
			return 0, errors.New("the recipe must not be read")
		})
		os.Args = []string{os.Args[0], "-"}
		main()
		os.Exit(99)
	}
	if testing.Short() {
		t.Skip("re-exec test")
	}
	path := filepath.Join(t.TempDir(), "fisherman-install.lock")
	child := func(mode string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestInstallLockAcrossProcesses$", "-test.count=1")
		cmd.Env = append(os.Environ(), "FISHERMAN_LOCK_CHILD="+mode, "FISHERMAN_LOCK_PATH="+path)
		return cmd
	}

	holder := child("hold")
	holderOut, err := holder.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Process.Kill(); _ = holder.Wait() }()
	if line, _ := bufio.NewReader(holderOut).ReadString('\n'); strings.TrimSpace(line) != "HELD" {
		t.Fatalf("holder did not take the lock: %q", line)
	}

	install := child("install")
	var stdout, stderr bytes.Buffer
	install.Stdout, install.Stderr = &stdout, &stderr
	runErr := install.Run()
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) || exitErr.ExitCode() != exitFailure {
		t.Fatalf("second install: %v, want exit %d\nstdout:\n%s\nstderr:\n%s", runErr, exitFailure, stdout.String(), stderr.String())
	}
	var ev map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &ev); err != nil {
		t.Fatalf("want one error event, got:\n%s", stdout.String())
	}
	msg, _ := ev["message"].(string)
	want := fmt.Sprintf("cannot install: another fisherman install is running on this host (process %d)", holder.Process.Pid)
	if ev["type"] != "error" || ev["code"] != recipe.CodeInstallAlreadyRunning || !strings.HasPrefix(msg, want) {
		t.Errorf("event = %v, want %s starting %q", ev, recipe.CodeInstallAlreadyRunning, want)
	}
	if strings.Contains(stderr.String(), "RECIPE READ") {
		t.Error("the refused install read its recipe")
	}

	// The holder dies without any cleanup: the kernel drops its lock.
	_ = holder.Process.Kill()
	_ = holder.Wait()
	useLockPath(t, path)
	if err := takeInstallLock(); err != nil {
		t.Errorf("lock after the holder was killed: %v", err)
	}
}
