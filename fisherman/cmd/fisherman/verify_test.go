package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/fisherman/internal/deploy/deploytest"
	"github.com/tuna-os/fisherman/internal/receipt"
)

const fixtureDir = "../../internal/receipt/testdata"

func copyFixture(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtureDir, from))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeVerifyBootc replaces bootc for verify and records every call.
func fakeVerifyBootc(t *testing.T, status string, statusErr error) *[]string {
	t.Helper()
	var calls []string
	oldRun, oldNow := verifyRun, verifyNow
	verifyRun = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if name != "bootc" {
			return nil, errors.New("unexpected command")
		}
		if len(args) == 1 && args[0] == "--version" {
			return []byte("bootc 1.17.1\n"), nil
		}
		if statusErr != nil {
			return nil, statusErr
		}
		b, err := os.ReadFile(filepath.Join(fixtureDir, status))
		if err != nil {
			t.Fatal(err)
		}
		return b, nil
	}
	verifyNow = func() time.Time { return time.Date(2026, 11, 10, 12, 10, 0, 0, time.UTC) }
	t.Cleanup(func() { verifyRun, verifyNow = oldRun, oldNow })
	return &calls
}

func runVerifyJSON(t *testing.T, args ...string) (int, receipt.Result, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := runVerify(args, &out, &errOut)
	var r receipt.Result
	if out.Len() > 0 {
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			t.Fatalf("stdout is not JSON: %v\n%s", err, out.String())
		}
	}
	return code, r, errOut.String()
}

// Live evidence: only `bootc status --format json --format-version 1` and
// `bootc --version` run, never anything that changes the system.
func TestVerifyLiveRunsOnlyStatus(t *testing.T) {
	dir := t.TempDir()
	rc := filepath.Join(dir, "receipt.json")
	copyFixture(t, "receipts/ostree.json", rc)
	calls := fakeVerifyBootc(t, "status/ostree-booted.json", nil)
	code, r, stderr := runVerifyJSON(t, "--json", "--live", "--receipt", rc)
	if code != 0 || r.Outcome != receipt.OutcomeVerified || r.Evidence != receipt.EvidenceLive {
		t.Fatalf("exit %d %s/%s %s", code, r.Outcome, r.Reason, stderr)
	}
	if r.Tools.Bootc != "bootc 1.17.1" || r.Tools.Fisherman == "" {
		t.Errorf("tools %+v", r.Tools)
	}
	want := []string{"bootc status --format json --format-version 1", "bootc --version"}
	if strings.Join(*calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls %q", *calls)
	}
}

// Every outcome but verified exits non-zero, and still prints the result.
func TestVerifyNonVerifiedExitsNonZero(t *testing.T) {
	dir := t.TempDir()
	rc := filepath.Join(dir, "receipt.json")
	copyFixture(t, "receipts/ostree.json", rc)
	for status, outcome := range map[string]string{
		"status/wrong-image.json":       receipt.OutcomeMismatch,
		"status/staged-not-booted.json": receipt.OutcomeMismatch,
		"status/malformed.json":         receipt.OutcomeUnavailable,
		"status/older-api.json":         receipt.OutcomeUnsupported,
	} {
		fakeVerifyBootc(t, status, nil)
		code, r, _ := runVerifyJSON(t, "--json", "--receipt", rc)
		if code != exitFailure || r.Outcome != outcome {
			t.Errorf("%s: exit %d outcome %s", status, code, r.Outcome)
		}
	}
	// A bootc without --format-version (clap exits 2).
	err := exec.Command("sh", "-c", "exit 2").Run()
	fakeVerifyBootc(t, "", err)
	if code, r, _ := runVerifyJSON(t, "--json", "--receipt", rc); code != exitFailure || r.Reason != receipt.ReasonStatusFormatUnsupported {
		t.Errorf("older bootc: exit %d %s", code, r.Reason)
	}
}

// On a host with no receipt (a live ISO, the CI runner) the host's bootc
// is not even asked, and the answer is not success.
func TestVerifyWrongContextNoReceipt(t *testing.T) {
	calls := fakeVerifyBootc(t, "status/ostree-booted.json", nil)
	code, r, _ := runVerifyJSON(t, "--json", "--receipt", filepath.Join(t.TempDir(), "absent.json"))
	if code != exitFailure || r.Outcome != receipt.OutcomeUnavailable || r.Reason != receipt.ReasonReceiptMissing {
		t.Fatalf("exit %d %s/%s", code, r.Outcome, r.Reason)
	}
	if len(*calls) != 0 {
		t.Errorf("bootc ran without a receipt: %q", *calls)
	}
}

// Another root is judged from its collection only: the host's bootc never
// answers for it.
func TestVerifyRootUsesCollection(t *testing.T) {
	root := t.TempDir()
	calls := fakeVerifyBootc(t, "status/ostree-booted.json", nil)
	copyFixture(t, "receipts/ostree.json", filepath.Join(root, receipt.ReceiptPath))

	code, r, _ := runVerifyJSON(t, "--json", "--root", root)
	if code != exitFailure || r.Outcome != receipt.OutcomeNotYetRun || r.Evidence != receipt.EvidenceCollected {
		t.Fatalf("before the first boot: exit %d %s/%s", code, r.Outcome, r.Reason)
	}

	coll := filepath.Join(root, receipt.CollectionDir)
	copyFixture(t, "collections/ok.json", filepath.Join(coll, receipt.CollectionFile))
	copyFixture(t, "status/ostree-booted.json", filepath.Join(coll, receipt.StatusFile))
	if err := os.WriteFile(filepath.Join(coll, receipt.VersionFile), []byte("bootc 1.17.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, r, _ = runVerifyJSON(t, "--json", "--root", root, "--write")
	if code != 0 || r.Outcome != receipt.OutcomeVerified || r.Tools.Bootc != "bootc 1.17.1" {
		t.Fatalf("after: exit %d %s/%s %+v", code, r.Outcome, r.Reason, r.Tools)
	}
	written, err := os.ReadFile(filepath.Join(coll, receipt.ResultFile))
	if err != nil {
		t.Fatal(err)
	}
	var back receipt.Result
	if err := json.Unmarshal(written, &back); err != nil || back.Outcome != receipt.OutcomeVerified {
		t.Errorf("written result %s %v", written, err)
	}
	if len(*calls) != 0 {
		t.Errorf("host bootc ran for another root: %q", *calls)
	}
}

func TestVerifyUsage(t *testing.T) {
	fakeVerifyBootc(t, "status/ostree-booted.json", nil)
	for _, args := range [][]string{
		{},
		{"--json", "--live", "--collected"},
		{"--json", "--live", "--root", "/mnt"},
		{"--json", "--live", "--collection", "/tmp/x"},
		{"--json", "--root"},
		{"--json", "--bogus"},
	} {
		var out, errOut bytes.Buffer
		if code := runVerify(args, &out, &errOut); code != exitUsage {
			t.Errorf("%q: exit %d", args, code)
		}
		if out.Len() != 0 {
			t.Errorf("%q: stdout %q", args, out.String())
		}
	}
}

// The install writes the receipt through the deployment resolver, into the
// deployment the installed system boots, on both layouts.
func TestStageReceiptInBootedDeployment(t *testing.T) {
	for name, fx := range map[string]deploytest.Sysroot{
		"ostree":    deploytest.Ostree(t),
		"composefs": deploytest.ComposeFs(t),
	} {
		t.Run(name, func(t *testing.T) {
			recordingRunner(t, "")
			oldOut := receiptOutput
			receiptOutput = func(name string, args ...string) ([]byte, error) {
				return []byte("'sha256:" + strings.Repeat("1", 64) + "'"), nil
			}
			t.Cleanup(func() { receiptOutput = oldOut })

			msg := stageReceipt(fx.Path, "containers-storage:ghcr.io/x/os:1", "ghcr.io/x/os:1", nil)
			if !strings.Contains(msg, "boot not verified yet") {
				t.Fatalf("message %q", msg)
			}
			b, err := os.ReadFile(filepath.Join(fx.Var, "lib", "fisherman", "install-receipt.json"))
			if err != nil {
				t.Fatal(err)
			}
			var rc receipt.Receipt
			if err := json.Unmarshal(b, &rc); err != nil {
				t.Fatal(err)
			}
			if rc.Backend != name || rc.UpdateRef != "ghcr.io/x/os:1" {
				t.Errorf("receipt %+v", rc)
			}
			if name == "ostree" && rc.Expected.DigestFrom != receipt.DigestFromInstalledDeployment {
				t.Errorf("expected %+v", rc.Expected)
			}
			if name == "composefs" && rc.Expected.PlatformManifestDigest != "" {
				t.Errorf("composefs digest from ostree: %+v", rc.Expected)
			}
			if _, err := os.Stat(filepath.Join(fx.Etc, "systemd", "system", receipt.UnitName)); err != nil {
				t.Errorf("unit: %v", err)
			}
		})
	}
}

func TestReceiptUpdateRef(t *testing.T) {
	for _, tc := range []struct{ src, target, live, want string }{
		{"containers-storage:a", "ghcr.io/a:1", "", "ghcr.io/a:1"},
		{"ghcr.io/a:1", "", "", "ghcr.io/a:1"},
		{"containers-storage:localhost/ci:latest", "", "", "localhost/ci:latest"},
		{"docker://ghcr.io/a:1", "", "", "ghcr.io/a:1"},
		{"registry.local:5000/a:1", "", "", "registry.local:5000/a:1"},
		{"", "", "ghcr.io/live:1", "ghcr.io/live:1"},
	} {
		if got := receiptUpdateRef(tc.src, tc.target, tc.live); got != tc.want {
			t.Errorf("%+v: %q", tc, got)
		}
	}
}
