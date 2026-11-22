//go:build linux

// Command fishermand is the fisherman installer daemon: a Varlink service for
// every installer frontend. See docs/FISHERMAND.md.
//
// The real executor (internal/varlinkapi/real.go) changes disks; --mock
// serves scripted data and touches nothing.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/tuna-os/fisherman/internal/auth"
	"github.com/tuna-os/fisherman/internal/varlink"
	"github.com/tuna-os/fisherman/internal/varlinkapi"
)

const version = "0.2.0"

// socketSpec names one socket the daemon listens on.
type socketSpec struct{ name, path string }

func main() { os.Exit(run(os.Args[1:], os.Stderr)) }

func run(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("fishermand", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		dryRun      = fs.Bool("dry-run", false, "print the systemd-repart definitions for this medium and exit (changes nothing)")
		mediumDisk  = fs.String("medium-disk", "", "dry-run: medium whole disk, e.g. /dev/vda (auto-detected otherwise)")
		rootNode    = fs.String("root-node", "", "dry-run: medium partition holding the booted root, e.g. /dev/vda3 (auto-detected otherwise)")
		cmdlineFile = fs.String("cmdline-file", "", "dry-run: read the kernel cmdline from this file instead of /proc/cmdline (testing)")
		devDir      = fs.String("dev-dir", "", "dry-run: device directory instead of /dev (testing)")
		sysRoot     = fs.String("sys-root", "", "dry-run: sysfs root instead of /sys (testing)")
		sfdiskJSON  = fs.String("sfdisk-json", "", "dry-run: read sfdisk --json output from this file instead of running sfdisk (testing)")
		mock        = fs.Bool("mock", false, "serve scripted data and touch no disk")
		scenario    = fs.String("scenario", os.Getenv("FISHERMAN_MOCK_SCENARIO"), "mock scenario, comma separated; env FISHERMAN_MOCK_SCENARIO; -list-scenarios shows them")
		delay       = fs.Duration("step-delay", envDuration("FISHERMAN_MOCK_STEP_DELAY"), "mock: pause between progress ticks; env FISHERMAN_MOCK_STEP_DELAY")
		socketDir   = fs.String("socket-dir", "", "put all four sockets in this directory under their default file names")
		sysinst     = fs.String("sysinstall-socket", "/run/systemd/io.systemd.SysInstall", "io.systemd.SysInstall socket path; empty disables it")
		crypt       = fs.String("cryptenroll-socket", "/run/systemd/io.systemd.CryptEnroll", "io.systemd.CryptEnroll socket path; empty disables it")
		shut        = fs.String("shutdown-socket", "/run/systemd/io.systemd.Shutdown", "io.systemd.Shutdown socket path; empty disables it")
		fisher      = fs.String("fisherman-socket", "/run/fisherman/io.tunaos.Fisherman", "io.tunaos.Fisherman socket path; empty disables it")
		mode        = fs.String("socket-mode", "0600", "octal permission bits of each socket")
		verbose     = fs.Bool("v", false, "debug logging (still never logs call parameters)")
		list        = fs.Bool("list-scenarios", false, "list mock scenarios and exit")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *list {
		for _, n := range varlinkapi.ScenarioNames() {
			fmt.Fprintf(os.Stdout, "%-14s %s\n", n, varlinkapi.ScenarioDoc(n))
		}
		return 0
	}
	if *dryRun {
		if err := runDryRun(os.Stdout, dryRunFlags{
			mediumDisk: *mediumDisk, rootNode: *rootNode, cmdline: *cmdlineFile,
			devDir: *devDir, sysRoot: *sysRoot, sfdiskJSON: *sfdiskJSON,
		}); err != nil {
			fmt.Fprintf(stderr, "fishermand: %v\n", err)
			return 1
		}
		return 0
	}

	socks := []socketSpec{
		{"io.systemd.SysInstall", *sysinst}, {"io.systemd.CryptEnroll", *crypt},
		{"io.systemd.Shutdown", *shut}, {"io.tunaos.Fisherman", *fisher},
	}
	if *socketDir != "" {
		for i := range socks {
			socks[i].path = filepath.Join(*socketDir, socks[i].name)
		}
	}
	var enabled []socketSpec
	for _, s := range socks {
		if s.path != "" {
			enabled = append(enabled, s)
		}
	}
	if len(enabled) == 0 {
		fmt.Fprintln(stderr, "fishermand: every socket is disabled")
		return 2
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

	newServer := func() *varlink.Server {
		return varlink.NewServer(varlink.ServiceInfo{
			Vendor: "TunaOS", Product: "fishermand", Version: version,
			URL: "https://github.com/tuna-os/fisherman"}, log)
	}
	modeName := "real"
	var svc *varlinkapi.Service
	if *mock {
		modeName = "mock"
		sc, err := varlinkapi.ParseScenario(*scenario)
		if err != nil {
			fmt.Fprintf(stderr, "fishermand: %v\n", err)
			return 2
		}
		svc = varlinkapi.NewService(varlinkapi.NewMock(sc, *delay), log)
	} else {
		svc = varlinkapi.NewService(varlinkapi.Real{}, log)
		// Mutating calls ask the polkit authority; the policy ships
		// with the installer image.
		svc.SetChecker(auth.PkcheckChecker{})
	}

	// One socket may carry every enabled interface (mock mode, or a
	// single socket-activated socket), or each socket carries exactly
	// its own interface (real mode, like systemd).
	single := *mock
	listeners, activated, err := activatedListeners(len(enabled))
	if err != nil {
		fmt.Fprintf(stderr, "fishermand: %v\n", err)
		return 1
	}
	if activated && len(listeners) == 1 {
		single = true
	}
	type bound struct {
		spec socketSpec
		srv  *varlink.Server
		l    net.Listener
		path string // bound path, or "activated" for inherited sockets
	}
	var bounds []bound
	if single {
		srv := newServer()
		svc.Register(srv)
		for i := range enabled {
			bounds = append(bounds, bound{spec: enabled[i], srv: srv})
		}
	} else {
		for _, s := range enabled {
			srv := newServer()
			svc.RegisterInterface(srv, s.name)
			bounds = append(bounds, bound{spec: s, srv: srv})
		}
	}
	if activated {
		for i := range bounds {
			bounds[i].l, bounds[i].path = listeners[i%len(listeners)], "activated"
		}
	} else {
		perm, err := strconv.ParseUint(*mode, 8, 32)
		if err != nil {
			fmt.Fprintf(stderr, "fishermand: bad --socket-mode %q\n", *mode)
			return 2
		}
		for i := range bounds {
			l, err := listen(bounds[i].spec.path, os.FileMode(perm))
			if err != nil {
				fmt.Fprintf(stderr, "fishermand: %s: %v\n", bounds[i].spec.name, err)
				return 1
			}
			defer os.Remove(bounds[i].spec.path)
			bounds[i].l, bounds[i].path = l, bounds[i].spec.path
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, len(bounds))
	for _, b := range bounds {
		log.Info("listening", "interface", b.spec.name, "socket", b.path, "mode", modeName)
		go func() { errc <- b.srv.Serve(ctx, b.l) }()
	}
	for range bounds {
		if err := <-errc; err != nil {
			fmt.Fprintf(stderr, "fishermand: %v\n", err)
			stop()
			return 1
		}
	}
	return 0
}

// activatedListeners returns the systemd socket-activation listeners when
// this process was socket-activated (LISTEN_PID is us). One inherited
// socket carries everything; one per enabled socket keeps the interface
// order. Anything else is a unit bug.
func activatedListeners(n int) ([]net.Listener, bool, error) {
	if os.Getenv("LISTEN_PID") != strconv.Itoa(os.Getpid()) {
		return nil, false, nil
	}
	fds, err := strconv.Atoi(os.Getenv("LISTEN_FDS"))
	if err != nil || fds < 1 {
		return nil, false, fmt.Errorf("bad LISTEN_FDS %q", os.Getenv("LISTEN_FDS"))
	}
	if fds != 1 && fds != n {
		return nil, false, fmt.Errorf("LISTEN_FDS=%d for %d sockets", fds, n)
	}
	return inheritedListeners(fds, 3)
}

// inheritedListeners wraps the count socket files starting at base into
// listeners. systemd passes them as fds 3 and up; tests pass a base of
// provably free descriptors instead of squatting low numbers the harness
// may hold open.
func inheritedListeners(count int, base uintptr) ([]net.Listener, bool, error) {
	var out []net.Listener
	for i := range count {
		l, err := net.FileListener(os.NewFile(base+uintptr(i), "fishermand"))
		if err != nil {
			for _, l := range out {
				l.Close()
			}
			return nil, false, fmt.Errorf("inheriting socket %d: %w", i, err)
		}
		out = append(out, l)
	}
	return out, true, nil
}

// listen binds a UNIX socket, replacing a stale one but never a live one.
func listen(path string, perm os.FileMode) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		c.Close()
		return nil, fmt.Errorf("%s: another daemon is listening", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	old := syscall.Umask(0o177) // never briefly world-accessible
	l, err := net.Listen("unix", path)
	syscall.Umask(old)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, perm); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

func envDuration(key string) time.Duration {
	d, _ := time.ParseDuration(os.Getenv(key))
	return d
}
