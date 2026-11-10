package post

// Bounded tests for the tar | tar pipe of CopyFlatpaks, with real
// subprocesses (this test binary, re-executed) so that pipe back-pressure is
// real. A command mock cannot show tuna-os/fisherman#358: the hang needs a
// producer blocked in write(2) on a pipe nobody reads.

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const pipeHelperEnv = "FISHERMAN_PIPE_HELPER"

// TestMain turns the test binary into the fake tar when asked to.
func TestMain(m *testing.M) {
	if mode := os.Getenv(pipeHelperEnv); mode != "" {
		os.Exit(pipeHelper(mode))
	}
	os.Exit(m.Run())
}

// pipeHelper is the fake subprocess. Modes:
//
//	produce:<bytes>:<exit>   write that many bytes to stdout, then exit
//	consume:<bytes>:<exit>   read that many bytes (-1: to EOF), then exit
func pipeHelper(mode string) int {
	parts := strings.Split(mode, ":")
	n, _ := strconv.ParseInt(parts[1], 10, 64)
	code, _ := strconv.Atoi(parts[2])
	switch parts[0] {
	case "produce":
		buf := make([]byte, 64<<10)
		for i := range buf {
			buf[i] = 'x'
		}
		for n > 0 {
			chunk := int64(len(buf))
			if n < chunk {
				chunk = n
			}
			if _, err := os.Stdout.Write(buf[:chunk]); err != nil {
				return 141 // what SIGPIPE would have done
			}
			n -= chunk
		}
	case "consume":
		if n < 0 {
			if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
				return 1
			}
		} else if _, err := io.CopyN(io.Discard, os.Stdin, n); err != nil {
			return 1
		}
	}
	return code
}

// helperCmd is a runner.Command over a real subprocess, with Kill.
type helperCmd struct{ *exec.Cmd }

func (c *helperCmd) SetStdin(r io.Reader)  { c.Stdin = r }
func (c *helperCmd) SetStdout(w io.Writer) { c.Stdout = w }
func (c *helperCmd) SetStderr(w io.Writer) { c.Stderr = w }
func (c *helperCmd) Kill() error {
	if c.Process == nil {
		return nil
	}
	return c.Process.Kill()
}

func helper(mode string) *helperCmd {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), pipeHelperEnv+"="+mode)
	return &helperCmd{cmd}
}

func missingBinary() *helperCmd {
	return &helperCmd{exec.Command("/nonexistent/fisherman-test-tar")}
}

// runBounded fails the test if pipeCommands does not return in time: the
// defect under test is a hang, and a hung test must not hang the suite.
func runBounded(t *testing.T, producer, consumer *helperCmd, count *atomic.Int64) (time.Duration, error) {
	t.Helper()
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- pipeCommands(producer, consumer, count) }()
	select {
	case err := <-done:
		return time.Since(start), err
	case <-time.After(30 * time.Second):
		// Free the children so the test process can exit.
		_ = producer.Kill()
		_ = consumer.Kill()
		t.Fatal("pipeCommands did not return within 30s: the pipeline is hung")
		return 0, nil
	}
}

// reaped reports whether the command was started and waited for.
func reaped(c *helperCmd) bool { return c.ProcessState != nil }

// assertNoGoroutineLeak waits for the goroutine count to come back to base.
func assertNoGoroutineLeak(t *testing.T, base int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if n := runtime.NumGoroutine(); n <= base {
			return
		} else if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			t.Fatalf("goroutines: %d, want at most %d\n%s", n, base, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPipeCommands_Success(t *testing.T) {
	base := runtime.NumGoroutine()
	const size = 8 << 20
	p, c := helper(fmt.Sprintf("produce:%d:0", size)), helper("consume:-1:0")
	var n atomic.Int64
	_, err := runBounded(t, p, c, &n)
	if err != nil {
		t.Fatalf("pipeCommands: %v", err)
	}
	if n.Load() != size {
		t.Errorf("consumer was handed %d bytes, want %d", n.Load(), size)
	}
	if !reaped(p) || !reaped(c) {
		t.Errorf("reaped: producer=%v consumer=%v", reaped(p), reaped(c))
	}
	assertNoGoroutineLeak(t, base)
}

// The hang of #358: the extractor dies after a prefix (a full target), the
// producer still has 1 GiB to write.
func TestPipeCommands_ConsumerFailsEarly(t *testing.T) {
	base := runtime.NumGoroutine()
	p, c := helper(fmt.Sprintf("produce:%d:0", 1<<30)), helper("consume:65536:3")
	took, err := runBounded(t, p, c, nil)
	if err == nil || !strings.Contains(err.Error(), "tar extract") || !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("err = %v, want the extractor's exit status", err)
	}
	if took > 10*time.Second {
		t.Errorf("returned after %v: not prompt", took)
	}
	if !reaped(p) || !reaped(c) {
		t.Errorf("reaped: producer=%v consumer=%v", reaped(p), reaped(c))
	}
	// The producer was stopped, it did not write its whole gigabyte away.
	if p.ProcessState != nil && p.ProcessState.Success() {
		t.Error("the producer ran to completion after the extractor had failed")
	}
	assertNoGoroutineLeak(t, base)
}

// tar x stops at the end-of-archive marker; a producer with trailing output
// is drained, and the copy is a success.
func TestPipeCommands_ConsumerDoneEarlyIsNotAnError(t *testing.T) {
	base := runtime.NumGoroutine()
	p, c := helper(fmt.Sprintf("produce:%d:0", 4<<20)), helper("consume:65536:0")
	_, err := runBounded(t, p, c, nil)
	if err != nil {
		t.Errorf("pipeCommands: %v", err)
	}
	if !reaped(p) || !reaped(c) {
		t.Errorf("reaped: producer=%v consumer=%v", reaped(p), reaped(c))
	}
	assertNoGoroutineLeak(t, base)
}

func TestPipeCommands_ProducerExitFailure(t *testing.T) {
	base := runtime.NumGoroutine()
	p, c := helper("produce:1048576:2"), helper("consume:-1:0")
	_, err := runBounded(t, p, c, nil)
	if err == nil || !strings.Contains(err.Error(), "tar create") || !strings.Contains(err.Error(), "exit status 2") {
		t.Errorf("err = %v, want the producer's exit status", err)
	}
	if !reaped(p) || !reaped(c) {
		t.Errorf("reaped: producer=%v consumer=%v", reaped(p), reaped(c))
	}
	assertNoGoroutineLeak(t, base)
}

// The extractor is already running when the producer cannot start. It used
// to be left behind, blocked on a pipe that nobody would ever close.
func TestPipeCommands_ProducerStartFailure(t *testing.T) {
	base := runtime.NumGoroutine()
	p, c := missingBinary(), helper("consume:-1:0")
	took, err := runBounded(t, p, c, nil)
	if err == nil || !strings.Contains(err.Error(), "tar create start") {
		t.Errorf("err = %v, want a producer start error", err)
	}
	if took > 10*time.Second {
		t.Errorf("returned after %v: not prompt", took)
	}
	if !reaped(c) {
		t.Error("the extractor was started and never reaped")
	}
	assertNoGoroutineLeak(t, base)
}

func TestPipeCommands_ConsumerStartFailure(t *testing.T) {
	base := runtime.NumGoroutine()
	p, c := helper("produce:1048576:0"), missingBinary()
	_, err := runBounded(t, p, c, nil)
	if err == nil || !strings.Contains(err.Error(), "tar extract start") {
		t.Errorf("err = %v, want an extractor start error", err)
	}
	if p.Process != nil {
		t.Error("the producer was started although the extractor could not start")
	}
	assertNoGoroutineLeak(t, base)
}
