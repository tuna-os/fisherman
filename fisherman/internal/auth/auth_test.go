package auth

import (
	"context"
	"errors"
	"net"
	"os"
	"reflect"
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
	// The start time is this process's own, so the authority can tell it
	// from a later holder of the PID.
	stat, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		t.Fatal(err)
	}
	want, err := parseStartTime(string(stat))
	if err != nil {
		t.Fatal(err)
	}
	if want == 0 || r.c.StartTime != want {
		t.Errorf("start time = %d, want %d", r.c.StartTime, want)
	}
}

// Field 22 is counted from the last ")": the command name is free text.
func TestParseStartTime(t *testing.T) {
	const tail = " S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 424242 20 21"
	for _, comm := range []string{"(sh)", "(a b)", "(evil) R 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 7)"} {
		got, err := parseStartTime("99 " + comm + tail)
		if err != nil || got != 424242 {
			t.Errorf("comm %q: got %d, %v", comm, got, err)
		}
	}
	for _, bad := range []string{"", "99 sh S 1", "99 (sh) S 1 2", "99 (sh) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 x 20"} {
		if _, err := parseStartTime(bad); err == nil {
			t.Errorf("%q must fail", bad)
		}
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
	want := Creds{PID: 12, UID: 1000, GID: 1000, StartTime: 3456}
	p := PkcheckChecker{Run: func(_ context.Context, c Creds, action string) error {
		if c != want || action != "io.Test.Do" {
			t.Errorf("run got %+v %s", c, action)
		}
		return nil
	}}
	if err := p.Check(context.Background(), want, "io.Test.Do"); err != nil {
		t.Fatal(err)
	}
}

// pkcheck gets pid,start-time,uid, never a bare PID: with a bare PID the
// authority judges whichever process holds that PID when it looks, and
// reads the owner from /proc instead of taking the socket's UID.
func TestPkcheckNamesPidStartTimeAndUID(t *testing.T) {
	got := pkcheckArgs(Creds{PID: 12, UID: 1000, GID: 1000, StartTime: 3456}, "io.Test.Do")
	want := []string{"--process", "12,3456,1000", "--action-id", "io.Test.Do"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pkcheck args = %q, want %q", got, want)
	}
}

// Credentials that cannot name the peer exactly are denied before pkcheck
// runs.
func TestPkcheckDeniesPeerWithoutStartTime(t *testing.T) {
	ran := false
	p := PkcheckChecker{Run: func(context.Context, Creds, string) error {
		ran = true
		return nil
	}}
	for _, c := range []Creds{{PID: 12, UID: 1000}, {UID: 1000, StartTime: 3456}, {}} {
		if err := p.Check(context.Background(), c, "io.Test.Do"); !errors.Is(err, ErrDenied) {
			t.Errorf("%+v: got %v, want ErrDenied", c, err)
		}
	}
	if ran {
		t.Error("pkcheck ran for a peer it cannot name")
	}
}
