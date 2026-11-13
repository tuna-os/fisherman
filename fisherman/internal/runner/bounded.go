package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"syscall"
	"time"
)

// ErrOutputTooLarge is returned by BoundedOutput when a command writes more
// than the limit to stdout. The output is discarded.
var ErrOutputTooLarge = errors.New("output exceeds the limit")

// ErrTimedOut is returned by BoundedOutput when a command runs longer than
// its timeout. The command is stopped.
var ErrTimedOut = errors.New("command timed out")

// BoundedOutput runs a read-only host command for a probe. It is Output
// with three limits:
//
//   - the command is stopped after timeout (SIGTERM, which flatpak-spawn
//     forwards to the host process, then SIGKILL two seconds later);
//   - more than limit bytes on stdout is an error, and nothing is returned;
//   - stderr is discarded, and an error names only the program, never its
//     arguments or output, so text a broken or hostile program prints (or an
//     argument that came from a user) cannot reach a caller's JSON.
//
// An exit status is kept in the error chain (errors.As with
// *exec.ExitError works). The Flatpak wrapping and the halt gate apply as in
// Output.
func BoundedOutput(timeout time.Duration, limit int, name string, args ...string) ([]byte, error) {
	if err := checkHalted(name, args); err != nil {
		return nil, err
	}
	name, args = HostArgs(name, args)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 2 * time.Second
	out := &limitedBuffer{limit: limit}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	err := cmd.Run()
	switch {
	case out.overflow:
		return nil, fmt.Errorf("%s: %w", name, ErrOutputTooLarge)
	case ctx.Err() != nil:
		return nil, fmt.Errorf("%s: %w", name, ErrTimedOut)
	case err != nil:
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return out.buf.Bytes(), nil
}

// limitedBuffer keeps at most limit bytes and records an overflow. The
// failed Write makes exec stop copying, so the child sees a closed pipe.
type limitedBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.limit {
		b.overflow = true
		return 0, ErrOutputTooLarge
	}
	return b.buf.Write(p)
}
