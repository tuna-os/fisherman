package post

import (
	"errors"
	"fmt"
	"io"
	"sync/atomic"

	"github.com/tuna-os/fisherman/internal/runner"
)

// killer is the optional part of a runner.Command that can be stopped.
// runner's real commands have it; a mock may not.
type killer interface{ Kill() error }

func kill(c runner.Command) {
	if k, ok := c.(killer); ok {
		_ = k.Kill()
	}
}

// errConsumerExited is what the producer's writes fail with once the
// consumer has gone.
var errConsumerExited = errors.New("the extracting process exited")

// pipeCommands runs producer | consumer and returns when BOTH have been
// reaped. count, when not nil, receives the number of bytes handed to the
// consumer.
//
// It never blocks on a half-dead pipeline (tuna-os/fisherman#358):
//
//   - The consumer exits first with an error (a full target, a write error)
//     while the producer still has data. Nobody reads the pipe any more, so
//     the producer would block in write for ever and its Wait would never
//     return. Here the read end is closed with the consumer's error, the
//     producer is killed, and both are waited for.
//   - The consumer exits first with success (tar stops at the end-of-archive
//     marker). The rest of the producer's output is drained, not refused, so
//     a producer that pads its last block still exits 0.
//   - The producer exits first. The write end is closed, the consumer sees
//     end of file. A failed producer does not leave the consumer to unpack a
//     truncated archive at leisure: it is killed.
//   - Either Start fails. Whatever was started is released and reaped.
//
// The error names the side that failed first.
func pipeCommands(producer, consumer runner.Command, count *atomic.Int64) error {
	pr, pw := io.Pipe()
	var in io.Reader = pr
	if count != nil {
		in = &countingReader{r: pr, n: count}
	}
	producer.SetStdout(pw)
	consumer.SetStdin(in)

	if err := consumer.Start(); err != nil {
		pw.Close()
		pr.Close()
		return fmt.Errorf("tar extract start: %w", err)
	}
	if err := producer.Start(); err != nil {
		// The consumer is running and reads the pipe: give it end of file,
		// stop it, and reap it.
		pw.Close()
		kill(consumer)
		_ = consumer.Wait()
		pr.Close()
		return fmt.Errorf("tar create start: %w", err)
	}

	producerDone := make(chan error, 1)
	consumerDone := make(chan error, 1)
	go func() {
		err := producer.Wait()
		// End of file for the consumer, whether the producer failed or not.
		pw.Close()
		producerDone <- err
	}()
	go func() { consumerDone <- consumer.Wait() }()

	var errP, errC error
	select {
	case errC = <-consumerDone:
		if errC != nil {
			// Unblock the producer's pending and future writes, then stop it.
			pr.CloseWithError(errConsumerExited)
			kill(producer)
		} else {
			// Let the producer finish what it has left.
			go func() { _, _ = io.Copy(io.Discard, pr) }()
		}
		errP = <-producerDone
		pr.Close()
		if errC != nil {
			return fmt.Errorf("tar extract: %w", errC)
		}
		if errP != nil {
			return fmt.Errorf("tar create: %w", errP)
		}
		return nil
	case errP = <-producerDone:
		if errP != nil {
			kill(consumer)
		}
		errC = <-consumerDone
		pr.Close()
		if errP != nil {
			return fmt.Errorf("tar create: %w", errP)
		}
		if errC != nil {
			return fmt.Errorf("tar extract: %w", errC)
		}
		return nil
	}
}
