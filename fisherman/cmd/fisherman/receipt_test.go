package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/deploy/deploytest"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/receipt"
)

// fakeReceiptDigest makes the ostree query of the receipt answer without
// running ostree.
func fakeReceiptDigest(t *testing.T) {
	t.Helper()
	old := receiptOutput
	receiptOutput = func(string, ...string) ([]byte, error) {
		return []byte("'sha256:" + strings.Repeat("1", 64) + "'"), nil
	}
	t.Cleanup(func() { receiptOutput = old })
}

// A file where a directory must be, so the write below it fails whoever
// runs the test (root ignores permission bits).
func blockDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Every way stageReceipt can end gives the `receipt` event its facts: the
// receipt is reported written only when the file is there, with the id it
// holds, and the first-boot check only when the unit is staged. None of
// them fails the install (stageReceipt has no error to return).
func TestStageReceiptStatus(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setup   func(t *testing.T, fx deploytest.Sysroot) string
		want    progress.ReceiptStatus
		logHas  string
		written bool
	}{
		{
			name:   "written",
			want:   progress.ReceiptStatus{Written: true, Path: receipt.ReceiptPath, FirstBootCheck: true},
			logHas: "boot not verified yet", written: true,
		},
		{
			name: "resolve_failed",
			setup: func(t *testing.T, _ deploytest.Sysroot) string {
				return t.TempDir() // no deployment in it
			},
			want:   progress.ReceiptStatus{Reason: progress.ReceiptResolveFailed},
			logHas: "Warning: installation receipt not written",
		},
		{
			name: "build_failed",
			setup: func(t *testing.T, fx deploytest.Sysroot) string {
				old := receiptNewID
				receiptNewID = func() string { return "" }
				t.Cleanup(func() { receiptNewID = old })
				return fx.Path
			},
			want:   progress.ReceiptStatus{Reason: progress.ReceiptBuildFailed},
			logHas: "Warning: installation receipt not written",
		},
		{
			name: "write_failed",
			setup: func(t *testing.T, fx deploytest.Sysroot) string {
				blockDir(t, filepath.Join(fx.Var, "lib", "fisherman"))
				return fx.Path
			},
			want:   progress.ReceiptStatus{Reason: progress.ReceiptWriteFailed},
			logHas: "Warning: installation receipt not written",
		},
		{
			name: "transient_etc",
			setup: func(t *testing.T, fx deploytest.Sysroot) string {
				conf := filepath.Join(fx.Etc, "ostree", "prepare-root.conf")
				if err := os.MkdirAll(filepath.Dir(conf), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(conf, []byte("[etc]\ntransient=true\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return fx.Path
			},
			want:   progress.ReceiptStatus{Written: true, Path: receipt.ReceiptPath, Reason: progress.ReceiptTransientEtc},
			logHas: "fisherman verify --json", written: true,
		},
		{
			name: "collector_failed",
			setup: func(t *testing.T, fx deploytest.Sysroot) string {
				blockDir(t, filepath.Join(fx.Etc, "fisherman"))
				return fx.Path
			},
			want:   progress.ReceiptStatus{Written: true, Path: receipt.ReceiptPath, Reason: progress.ReceiptCollectorFailed},
			logHas: "Warning: installation receipt written to", written: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recordingRunner(t, "")
			fakeReceiptDigest(t)
			fx := deploytest.Ostree(t)
			target := fx.Path
			if tc.setup != nil {
				target = tc.setup(t, fx)
			}
			st, msg := stageReceipt(target, "containers-storage:ghcr.io/x/os:1", "ghcr.io/x/os:1", nil)
			if !strings.Contains(msg, tc.logHas) {
				t.Errorf("log line %q, want it to contain %q", msg, tc.logHas)
			}
			got := st
			got.InstallID = ""
			if got != tc.want {
				t.Errorf("status %+v, want %+v", st, tc.want)
			}
			b, err := os.ReadFile(filepath.Join(fx.Var, "lib", "fisherman", "install-receipt.json"))
			if tc.written != (err == nil) {
				t.Fatalf("receipt on disk: %v, want written %v", err, tc.written)
			}
			if !tc.written {
				if st.InstallID != "" {
					t.Errorf("install id %q for a receipt that is not written", st.InstallID)
				}
				return
			}
			if st.InstallID == "" || !strings.Contains(string(b), `"install_id": "`+st.InstallID+`"`) {
				t.Errorf("event install id %q is not the receipt's: %s", st.InstallID, b)
			}
			_, err = os.Lstat(filepath.Join(fx.Etc, "systemd", "system", "multi-user.target.wants", receipt.UnitName))
			if st.FirstBootCheck != (err == nil) {
				t.Errorf("first_boot_check %v, unit link: %v", st.FirstBootCheck, err)
			}
		})
	}
}
