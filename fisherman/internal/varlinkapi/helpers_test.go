package varlinkapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tuna-os/fisherman/internal/varlink"
)

var update = flag.Bool("update", false, "rewrite golden files")

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// env is a daemon over a temp socket, driven by a real client connection.
type env struct {
	path string
	mock *Mock
	svc  *Service
	logs *lockedBuf
}

func newEnv(t *testing.T, scenario string) *env {
	t.Helper()
	sc, err := ParseScenario(scenario)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "fd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	logs := &lockedBuf{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	mock := NewMock(sc, 0)
	svc := NewService(mock, log)
	srv := varlink.NewServer(varlink.ServiceInfo{Product: "fishermand-test"}, log)
	svc.Register(srv)
	path := filepath.Join(dir, "s")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = srv.Serve(ctx, l); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return &env{path: path, mock: mock, svc: svc, logs: logs}
}

func (e *env) client(t *testing.T) *varlink.Client {
	t.Helper()
	c, err := varlink.Dial(e.path)
	if err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(10 * time.Second))
	t.Cleanup(func() { c.Close() })
	return c
}

// raw sends one call and returns every frame up to the first frame that is
// not marked "continues": true, or up to stopAfter frames when stopAfter > 0.
func (e *env) raw(t *testing.T, call string, stopAfter int) []string {
	t.Helper()
	c := e.client(t)
	if err := c.SendRaw([]byte(call)); err != nil {
		t.Fatal(err)
	}
	var out []string
	for {
		b, err := c.ReadFrame()
		if err != nil {
			t.Fatalf("after %d frames: %v", len(out), err)
		}
		out = append(out, string(b))
		var f struct {
			Continues bool `json:"continues"`
		}
		_ = json.Unmarshal(b, &f)
		if !f.Continues || (stopAfter > 0 && len(out) >= stopAfter) {
			return out
		}
	}
}

// golden compares got with testdata/golden/<name>; -update rewrites it.
func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run with -update to create)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}

func joinLines(f []string) string { return strings.Join(f, "\n") + "\n" }

// callErr returns the Varlink error name of err, or "" for none.
func callErr(err error) string {
	var ve *varlink.Error
	if errors.As(err, &ve) {
		return ve.Name
	}
	if err != nil {
		return "transport: " + err.Error()
	}
	return ""
}

// install runs Install and returns every event.
func install(t *testing.T, c *varlink.Client, cfg any) ([]Event, error) {
	t.Helper()
	st, err := c.CallMore(IfaceFisherman+".Install", map[string]any{"config": cfg, "confirmed": true})
	if err != nil {
		t.Fatal(err)
	}
	var evs []Event
	for {
		var ev Event
		ok, err := st.Next(&ev)
		if err != nil {
			return evs, err
		}
		if !ok {
			return evs, nil
		}
		evs = append(evs, ev)
	}
}

func goodConfig() map[string]any {
	return map[string]any{
		"disk": "/dev/nvme0n1", "installMode": "erase",
		"encryption": map[string]any{"type": "tpm2_luks"},
	}
}

// CallMoreErr is a test helper: it starts a streaming call and returns the
// name of the first reply's error, or "" when the first reply is a success.
func callMoreErr(t *testing.T, c *varlink.Client, method string, params any) string {
	t.Helper()
	st, err := c.CallMore(method, params)
	if err != nil {
		t.Fatal(err)
	}
	var ev map[string]any
	_, err = st.Next(&ev)
	return callErr(err)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
