//go:build linux

// Command fishermand is the fisherman installer daemon: a Varlink service for
// every installer frontend. See docs/FISHERMAND.md.
//
// This first slice runs in --mock mode only: no disk, TPM or process is
// touched. The real executor is a placeholder (internal/varlinkapi/real.go).
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

	"github.com/tuna-os/fisherman/internal/varlink"
	"github.com/tuna-os/fisherman/internal/varlinkapi"
)

const version = "0.1.0-mock"

// socketSpec names one socket the daemon listens on.
type socketSpec struct{ name, path string }

func main() { os.Exit(run(os.Args[1:], os.Stderr)) }

func run(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("fishermand", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		mock      = fs.Bool("mock", false, "serve scripted data and touch no disk (required in this release)")
		scenario  = fs.String("scenario", os.Getenv("FISHERMAN_MOCK_SCENARIO"), "mock scenario, comma separated; env FISHERMAN_MOCK_SCENARIO; -list-scenarios shows them")
		delay     = fs.Duration("step-delay", envDuration("FISHERMAN_MOCK_STEP_DELAY"), "mock: pause between progress ticks; env FISHERMAN_MOCK_STEP_DELAY")
		socketDir = fs.String("socket-dir", "", "put all four sockets in this directory under their default file names")
		sysinst   = fs.String("sysinstall-socket", "/run/systemd/io.systemd.SysInstall", "io.systemd.SysInstall socket path; empty disables it")
		crypt     = fs.String("cryptenroll-socket", "/run/systemd/io.systemd.CryptEnroll", "io.systemd.CryptEnroll socket path; empty disables it")
		shut      = fs.String("shutdown-socket", "/run/systemd/io.systemd.Shutdown", "io.systemd.Shutdown socket path; empty disables it")
		fisher    = fs.String("fisherman-socket", "/run/fisherman/io.tunaos.Fisherman", "io.tunaos.Fisherman socket path; empty disables it")
		mode      = fs.String("socket-mode", "0600", "octal permission bits of each socket")
		verbose   = fs.Bool("v", false, "debug logging (still never logs call parameters)")
		list      = fs.Bool("list-scenarios", false, "list mock scenarios and exit")
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
	if !*mock {
		fmt.Fprintln(stderr, "fishermand: only --mock is implemented in this release (the real executor is a placeholder)")
		return 2
	}
	perm, err := strconv.ParseUint(*mode, 8, 32)
	if err != nil {
		fmt.Fprintf(stderr, "fishermand: bad --socket-mode %q\n", *mode)
		return 2
	}
	sc, err := varlinkapi.ParseScenario(*scenario)
	if err != nil {
		fmt.Fprintf(stderr, "fishermand: %v\n", err)
		return 2
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

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))
	exec := varlinkapi.NewMock(sc, *delay)
	svc := varlinkapi.NewService(exec, log)
	srv := varlink.NewServer(varlink.ServiceInfo{
		Vendor: "TunaOS", Product: "fishermand", Version: version,
		URL: "https://github.com/tuna-os/fisherman"}, log)
	svc.Register(srv)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, len(socks))
	n := 0
	for _, s := range socks {
		if s.path == "" {
			continue
		}
		l, err := listen(s.path, os.FileMode(perm))
		if err != nil {
			fmt.Fprintf(stderr, "fishermand: %s: %v\n", s.name, err)
			stop()
			return 1
		}
		defer os.Remove(s.path)
		log.Info("listening", "interface", s.name, "socket", s.path, "scenario", sc.Spec, "mode", "mock")
		n++
		go func() { errc <- srv.Serve(ctx, l) }()
	}
	if n == 0 {
		fmt.Fprintln(stderr, "fishermand: every socket is disabled")
		return 2
	}
	for range n {
		if err := <-errc; err != nil {
			fmt.Fprintf(stderr, "fishermand: %v\n", err)
			stop()
			return 1
		}
	}
	return 0
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
