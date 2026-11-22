//go:build linux

package main

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

// freeBase returns the first of count consecutive free descriptors at or
// above 100, so the test never squats a descriptor the harness holds.
func freeBase(t *testing.T, count int) uintptr {
	t.Helper()
	used := map[int]bool{}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if n, err := strconv.Atoi(e.Name()); err == nil {
			used[n] = true
		}
	}
outer:
	for base := 100; ; base++ {
		for i := range count {
			if used[base+i] {
				continue outer
			}
		}
		return uintptr(base)
	}
}

// socketFileAt binds a socket on a temp path and duplicates its file onto
// fd, which must be free. It returns the original listener, still bound.
func socketFileAt(t *testing.T, path string, fd uintptr) net.Listener {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := l.(*net.UnixListener).File()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Dup3(int(f.Fd()), int(fd), 0); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestInheritedListenersPair(t *testing.T) {
	dir := t.TempDir()
	base := freeBase(t, 2)
	la := socketFileAt(t, filepath.Join(dir, "a"), base)
	defer la.Close()
	lb := socketFileAt(t, filepath.Join(dir, "b"), base+1)
	defer lb.Close()
	defer syscall.Close(int(base))
	defer syscall.Close(int(base + 1))
	ls, ok, err := inheritedListeners(2, base)
	if err != nil || !ok || len(ls) != 2 {
		t.Fatalf("got %v %v %v", ls, ok, err)
	}
	// Both inherited sockets accept: dial through the original paths.
	for _, name := range []string{"a", "b"} {
		c, err := net.Dial("unix", filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
	}
	for _, l := range ls {
		l.Close()
	}
}

func TestInheritedListenersRejectNonSockets(t *testing.T) {
	base := freeBase(t, 1)
	f, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Dup3(int(f.Fd()), int(base), 0); err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(int(base))
	if _, _, err := inheritedListeners(1, base); err == nil {
		t.Error("a non-socket fd must fail")
	}
}

func TestActivatedListenersNone(t *testing.T) {
	t.Setenv("LISTEN_PID", "1")
	ls, ok, err := activatedListeners(4)
	if err != nil || ok || ls != nil {
		t.Errorf("got %v %v %v", ls, ok, err)
	}
}

func TestActivatedListenersMismatch(t *testing.T) {
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("LISTEN_FDS", "3")
	if _, _, err := activatedListeners(2); err == nil {
		t.Error("LISTEN_FDS=3 for 2 sockets must fail")
	}
}
