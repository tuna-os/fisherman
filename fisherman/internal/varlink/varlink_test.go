package varlink

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func startServer(t *testing.T, setup func(*Server)) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "vl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(ServiceInfo{Vendor: "v", Product: "p", Version: "1", URL: "u"}, nil)
	setup(srv)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = srv.Serve(ctx, l); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return path
}

func dial(t *testing.T, path string) *Client {
	t.Helper()
	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(5 * time.Second))
	t.Cleanup(func() { c.Close() })
	return c
}

func TestCallAndError(t *testing.T) {
	path := startServer(t, func(s *Server) {
		s.Handle("t.Echo", func(_ context.Context, c *Call, w ReplyWriter) error {
			var p map[string]any
			if err := c.Decode(&p); err != nil {
				return err
			}
			return w.Reply(p, false)
		})
		s.Handle("t.Fail", func(context.Context, *Call, ReplyWriter) error {
			return &Error{Name: "t.Nope", Params: map[string]int{"n": 3}}
		})
		s.Handle("t.Silent", func(context.Context, *Call, ReplyWriter) error { return nil })
		s.Handle("t.Boom", func(context.Context, *Call, ReplyWriter) error { return errors.New("secret detail") })
	})
	c := dial(t, path)

	var out map[string]any
	if err := c.Call("t.Echo", map[string]any{"a": 1}, &out); err != nil || out["a"] != float64(1) {
		t.Fatalf("echo: %v %v", out, err)
	}
	var ve *Error
	if err := c.Call("t.Fail", nil, nil); !errors.As(err, &ve) || ve.Name != "t.Nope" {
		t.Fatalf("fail: %v", err)
	}
	if err := c.Call("t.Silent", nil, nil); err != nil {
		t.Fatalf("silent handler must get an empty reply: %v", err)
	}
	err := c.Call("t.Boom", nil, nil)
	if !errors.As(err, &ve) || ve.Name != ErrInternal || strings.Contains(ve.Error(), "secret detail") {
		t.Fatalf("internal errors must not leak detail: %v", err)
	}
	if err := c.Call("t.Missing", nil, nil); !errors.As(err, &ve) || ve.Name != ErrMethodNotFound {
		t.Fatalf("missing: %v", err)
	}
}

func TestWireBytes(t *testing.T) {
	path := startServer(t, func(s *Server) {
		s.Handle("t.Stream", func(_ context.Context, _ *Call, w ReplyWriter) error {
			_ = w.Reply(map[string]int{"n": 1}, true)
			_ = w.Reply(NoParameters, true)
			return w.Reply(map[string]int{"n": 2}, false)
		})
	})
	c := dial(t, path)
	if err := c.SendRaw([]byte(`{"method":"t.Stream","more":true}`)); err != nil {
		t.Fatal(err)
	}
	want := []string{`{"parameters":{"n":1},"continues":true}`, `{"continues":true}`, `{"parameters":{"n":2}}`}
	for _, w := range want {
		got, err := c.ReadFrame()
		if err != nil || string(got) != w {
			t.Fatalf("frame %q, want %q (%v)", got, w, err)
		}
	}
}

func TestStream(t *testing.T) {
	path := startServer(t, func(s *Server) {
		s.Handle("t.Count", func(_ context.Context, _ *Call, w ReplyWriter) error {
			for i := 1; i <= 3; i++ {
				if err := w.Reply(map[string]int{"i": i}, i < 3); err != nil {
					return err
				}
			}
			return nil
		})
	})
	c := dial(t, path)
	st, err := c.CallMore("t.Count", nil)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for {
		var o struct{ I int }
		ok, err := st.Next(&o)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		n++
		if o.I != n {
			t.Fatalf("i=%d at %d", o.I, n)
		}
	}
	if n != 3 {
		t.Fatalf("got %d replies", n)
	}
	// the connection is reusable after a stream
	var o map[string]any
	if err := c.Call("org.varlink.service.GetInfo", nil, &o); err != nil || o["vendor"] != "v" {
		t.Fatalf("getinfo: %v %v", o, err)
	}
}

func TestDisconnectCancelsHandler(t *testing.T) {
	cancelled := make(chan struct{})
	started := make(chan struct{})
	path := startServer(t, func(s *Server) {
		s.Handle("t.Hang", func(ctx context.Context, _ *Call, w ReplyWriter) error {
			_ = w.Reply(map[string]int{}, true)
			close(started)
			<-ctx.Done()
			close(cancelled)
			return ctx.Err()
		})
	})
	c := dial(t, path)
	if _, err := c.CallMore("t.Hang", nil); err != nil {
		t.Fatal(err)
	}
	<-started
	c.Close()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("handler context was not cancelled after the client left")
	}
}

func TestIntrospection(t *testing.T) {
	path := startServer(t, func(s *Server) { s.AddInterface("t.x", "interface t.x\n") })
	c := dial(t, path)
	var d struct{ Description string }
	if err := c.Call("org.varlink.service.GetInterfaceDescription", map[string]string{"interface": "t.x"}, &d); err != nil || d.Description != "interface t.x\n" {
		t.Fatalf("%v %v", d, err)
	}
	var ve *Error
	err := c.Call("org.varlink.service.GetInterfaceDescription", map[string]string{"interface": "nope"}, nil)
	if !errors.As(err, &ve) || ve.Name != ErrInterfaceNotFound {
		t.Fatalf("%v", err)
	}
}

func TestFrameLimitAndGarbage(t *testing.T) {
	path := startServer(t, func(*Server) {})
	c := dial(t, path)
	// not JSON: the server drops the connection
	if err := c.SendRaw([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReadFrame(); !errors.Is(err, io.EOF) && err == nil {
		t.Fatalf("want a closed connection, got %v", err)
	}
	// oversized frame: also dropped, and the server survives
	c2 := dial(t, path)
	_, _ = c2.conn.Write([]byte(strings.Repeat("a", maxFrame+10)))
	_, _ = c2.conn.Write([]byte{0})
	c3 := dial(t, path)
	if err := c3.Call("org.varlink.service.GetInfo", nil, nil); err != nil {
		t.Fatalf("server must survive: %v", err)
	}
}
