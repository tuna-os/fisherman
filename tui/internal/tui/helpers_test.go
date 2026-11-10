package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tuna-os/fisherman/tui/internal/backend"
	"github.com/tuna-os/fisherman/tui/internal/catalog"
	"github.com/tuna-os/fisherman/tui/internal/config"
)

var (
	testGrub   = catalog.Custom("ghcr.io/example/marlin:latest", false, "")
	testDakota = catalog.Image{Label: "Dakota", Ref: "ghcr.io/projectbluefin/dakota:latest", Bootloader: "systemd",
		ComposeFsBackend: true, Filesystem: "btrfs", Filesystems: []string{"btrfs", "ext4"}}
)

func testConfig() *config.InstallConfig {
	return &config.InstallConfig{
		DiskDevice: "/dev/sda", Filesystem: "xfs", Image: testGrub, Hostname: "marlin",
		Username: "alice", FullName: "Alice", Password: "hunter2-password",
	}
}

// acceptAllFields makes the backend's field check pass without a backend.
func acceptAllFields(t *testing.T) {
	t.Helper()
	orig := validateFields
	validateFields = func([]byte) ([]backend.Problem, error) { return nil, nil }
	t.Cleanup(func() { validateFields = orig })
}

// drain runs cmd, and every command a batch holds, and returns the messages
// they produce. Timer commands (the progress bar's animation) are run too;
// they are short.
func drain(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(2 * time.Second):
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, drain(c)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

// steps returns the stepMsg targets among msgs.
func steps(msgs []tea.Msg) []step {
	var out []step
	for _, m := range msgs {
		if s, ok := m.(stepMsg); ok {
			out = append(out, s.next)
		}
	}
	return out
}

func quits(msgs []tea.Msg) int {
	n := 0
	for _, m := range msgs {
		if _, ok := m.(tea.QuitMsg); ok {
			n++
		}
	}
	return n
}
