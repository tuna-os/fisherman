package auth

import (
	"context"
	"net"
	"os"
	"testing"
)

func TestCtxRoundTrip(t *testing.T) {
	if _, ok := Of(context.Background()); ok {
		t.Error("empty context has no creds")
	}
	want := Creds{PID: 7, UID: 1000, GID: 1000}
	got, ok := Of(WithCreds(context.Background(), want))
	if !ok || got != want {
		t.Errorf("got %+v, %v", got, ok)
	}
}

func TestPeerCredsOverSocket(t *testing.T) {
	dir := t.TempDir()
	l, err := net.Listen("unix", dir+"/s")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	type result struct {
		c   Creds
		err error
	}
	got := make(chan result, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			got <- result{err: err}
			return
		}
		defer conn.Close()
		c, err := PeerCreds(conn)
		got <- result{c, err}
	}()
	conn, err := net.Dial("unix", dir+"/s")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	r := <-got
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.c.UID != uint32(os.Getuid()) || r.c.GID != uint32(os.Getgid()) || r.c.PID != os.Getpid() {
		t.Errorf("creds = %+v, want this process", r.c)
	}
}

func TestFake(t *testing.T) {
	f := &Fake{Allow: true}
	if err := f.Check(context.Background(), Creds{}, "a.B"); err != nil {
		t.Errorf("allow: %v", err)
	}
	f.Allow = false
	if err := f.Check(context.Background(), Creds{}, "a.B"); err == nil {
		t.Error("deny must fail")
	}
	if len(f.Calls) != 2 || f.Calls[0] != "a.B" {
		t.Errorf("calls = %v", f.Calls)
	}
}

func TestPkcheckStub(t *testing.T) {
	p := PkcheckChecker{Run: func(_ context.Context, pid int, action string) error {
		if pid != 12 || action != "io.Test.Do" {
			t.Errorf("run got %d %s", pid, action)
		}
		return nil
	}}
	if err := p.Check(context.Background(), Creds{PID: 12}, "io.Test.Do"); err != nil {
		t.Fatal(err)
	}
}
