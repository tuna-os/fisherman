package receipt

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite testdata/results")

var testNow = time.Date(2026, 11, 10, 12, 10, 0, 0, time.UTC)

func fixture(t *testing.T, rel string) ([]byte, error) {
	t.Helper()
	if rel == "" {
		return nil, fs.ErrNotExist
	}
	return os.ReadFile(filepath.Join("testdata", rel))
}

// exitErr is a real *exec.ExitError with the given status, so the
// classification is tested against what a command really returns.
func exitErr(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("no exit error for %d: %v", code, err)
	}
	return fmt.Errorf("bootc: %w", err)
}

type evalCase struct {
	name       string
	receipt    string // testdata path; "" = missing
	evidence   string
	collection string // "" = missing (collected evidence)
	status     string // "" = missing
	statusErr  func(t *testing.T) error
	outcome    string
	reason     string
}

func evalCases() []evalCase {
	return []evalCase{
		// The supported releases: ostree and composefs, collected and live.
		{"ostree-collected-verified", "receipts/ostree.json", EvidenceCollected, "collections/ok.json", "status/ostree-booted.json", nil, OutcomeVerified, ReasonBootedMatchesReceipt},
		{"ostree-live-verified", "receipts/ostree.json", EvidenceLive, "", "status/ostree-booted.json", nil, OutcomeVerified, ReasonBootedMatchesReceipt},
		{"composefs-collected-verified", "receipts/composefs.json", EvidenceCollected, "collections/ok.json", "status/composefs-booted.json", nil, OutcomeVerified, ReasonBootedMatchesReceipt},
		{"extra-fields-verified", "receipts/ostree.json", EvidenceCollected, "collections/ok.json", "status/extra-fields.json", nil, OutcomeVerified, ReasonBootedMatchesReceipt},
		// Index and platform digests are different objects.
		{"media-index-platform-verified", "receipts/media-index.json", EvidenceCollected, "collections/ok.json", "status/ostree-booted.json", nil, OutcomeVerified, ReasonBootedMatchesReceipt},
		{"booted-index-digest-mismatch", "receipts/media-index.json", EvidenceCollected, "collections/ok.json", "status/index-digest-booted.json", nil, OutcomeMismatch, ReasonBootedDigestMismatch},
		{"index-only-receipt-unavailable", "receipts/index-only.json", EvidenceCollected, "collections/ok.json", "status/composefs-booted.json", nil, OutcomeUnavailable, ReasonExpectedIdentityUnknown},
		// The source reference differs from the update reference: normal.
		{"source-differs-from-update-ref-verified", "receipts/source-differs.json", EvidenceCollected, "collections/ok.json", "status/ostree-booted.json", nil, OutcomeVerified, ReasonBootedMatchesReceipt},
		// The booted image is not the receipt's.
		{"wrong-image-mismatch", "receipts/ostree.json", EvidenceCollected, "collections/ok.json", "status/wrong-image.json", nil, OutcomeMismatch, ReasonDeploymentMismatch},
		{"staged-not-booted", "receipts/ostree.json", EvidenceCollected, "collections/ok.json", "status/staged-not-booted.json", nil, OutcomeMismatch, ReasonExpectedStaged},
		{"cached-not-booted", "receipts/ostree.json", EvidenceCollected, "collections/ok.json", "status/cached-not-booted.json", nil, OutcomeMismatch, ReasonExpectedCached},
		{"rollback-not-booted", "receipts/ostree.json", EvidenceCollected, "collections/ok.json", "status/rollback-not-booted.json", nil, OutcomeMismatch, ReasonExpectedRollback},
		{"update-ref-differs", "receipts/ostree.json", EvidenceCollected, "collections/ok.json", "status/update-ref-differs.json", nil, OutcomeMismatch, ReasonUpdateRefMismatch},
		{"wrong-arch", "receipts/ostree.json", EvidenceCollected, "collections/ok.json", "status/wrong-arch.json", nil, OutcomeMismatch, ReasonPlatformMismatch},
		{"backend-differs", "receipts/ostree.json", EvidenceCollected, "collections/ok.json", "status/composefs-on-ostree-receipt.json", nil, OutcomeMismatch, ReasonBackendMismatch},
		// Missing, malformed and older output is never success.
		{"missing-fields-unavailable", "receipts/ostree.json", EvidenceCollected, "collections/ok.json", "status/missing-fields.json", nil, OutcomeUnavailable, ReasonBootedIdentityUnreported},
		{"malformed-status", "receipts/ostree.json", EvidenceCollected, "collections/ok.json", "status/malformed.json", nil, OutcomeUnavailable, ReasonStatusMalformed},
		{"wrong-types-status", "receipts/ostree.json", EvidenceCollected, "collections/ok.json", "status/wrong-types.json", nil, OutcomeUnavailable, ReasonStatusMalformed},
		{"older-api-status", "receipts/ostree.json", EvidenceCollected, "collections/ok.json", "status/older-api.json", nil, OutcomeUnsupported, ReasonStatusAPIUnsupported},
		{"older-cli-collected", "receipts/ostree.json", EvidenceCollected, "collections/older-cli.json", "", nil, OutcomeUnsupported, ReasonStatusFormatUnsupported},
		{"older-cli-live", "receipts/ostree.json", EvidenceLive, "", "", func(t *testing.T) error { return exitErr(t, 2) }, OutcomeUnsupported, ReasonStatusFormatUnsupported},
		{"bootc-absent-collected", "receipts/ostree.json", EvidenceCollected, "collections/bootc-absent.json", "", nil, OutcomeUnavailable, ReasonBootcAbsent},
		{"bootc-absent-live", "receipts/ostree.json", EvidenceLive, "", "", func(*testing.T) error { return fmt.Errorf("bootc: %w", exec.ErrNotFound) }, OutcomeUnavailable, ReasonBootcAbsent},
		{"status-failed-collected", "receipts/ostree.json", EvidenceCollected, "collections/status-failed.json", "", nil, OutcomeUnavailable, ReasonStatusFailed},
		{"status-failed-live", "receipts/ostree.json", EvidenceLive, "", "", func(t *testing.T) error { return exitErr(t, 1) }, OutcomeUnavailable, ReasonStatusFailed},
		{"status-file-missing", "receipts/ostree.json", EvidenceCollected, "collections/ok.json", "", nil, OutcomeUnavailable, ReasonCollectionMalformed},
		{"not-booted-bootc", "receipts/ostree.json", EvidenceLive, "", "status/not-booted.json", nil, OutcomeUnavailable, ReasonNotBootedBootc},
		// Wrong execution context: no receipt (a live ISO or another host),
		// or a collection another install's unit wrote.
		{"no-receipt-live", "", EvidenceLive, "", "status/ostree-booted.json", nil, OutcomeUnavailable, ReasonReceiptMissing},
		{"other-install-collection", "receipts/ostree.json", EvidenceCollected, "collections/other-install.json", "status/ostree-booted.json", nil, OutcomeUnavailable, ReasonWrongExecutionContext},
		{"unknown-evidence", "receipts/ostree.json", "", "", "status/ostree-booted.json", nil, OutcomeUnavailable, ReasonWrongExecutionContext},
		{"not-yet-run", "receipts/ostree.json", EvidenceCollected, "", "", nil, OutcomeNotYetRun, ReasonCollectionMissing},
		{"malformed-collection", "receipts/ostree.json", EvidenceCollected, "collections/malformed.json", "status/ostree-booted.json", nil, OutcomeUnavailable, ReasonCollectionMalformed},
		{"collection-v2", "receipts/ostree.json", EvidenceCollected, "collections/v2.json", "status/ostree-booted.json", nil, OutcomeUnsupported, ReasonCollectionVersionUnsupported},
		{"receipt-malformed", "receipts/malformed.json", EvidenceCollected, "collections/ok.json", "status/ostree-booted.json", nil, OutcomeUnavailable, ReasonReceiptMalformed},
		{"receipt-v2", "receipts/v2.json", EvidenceCollected, "collections/ok.json", "status/ostree-booted.json", nil, OutcomeUnsupported, ReasonReceiptVersionUnsupported},
	}
}

func TestEvaluateFixtures(t *testing.T) {
	for _, tc := range evalCases() {
		t.Run(tc.name, func(t *testing.T) {
			in := Input{Evidence: tc.evidence, FishermanVersion: "v0.3.0-test", BootcVersion: "bootc 1.17.1", Now: testNow}
			in.Receipt, in.ReceiptErr = fixture(t, tc.receipt)
			if tc.evidence == EvidenceCollected {
				in.Collection, in.CollectionErr = fixture(t, tc.collection)
			}
			in.Status, in.StatusErr = fixture(t, tc.status)
			if tc.evidence == EvidenceLive && tc.status != "" {
				in.StatusErr = nil
			}
			if tc.statusErr != nil {
				in.Status, in.StatusErr = nil, tc.statusErr(t)
			}
			res := Evaluate(in)
			if res.Outcome != tc.outcome || res.Reason != tc.reason {
				t.Fatalf("outcome %s/%s, want %s/%s", res.Outcome, res.Reason, tc.outcome, tc.reason)
			}
			if res.Schema != ResultSchema || res.Version != ResultVersion {
				t.Fatalf("schema %s v%d", res.Schema, res.Version)
			}
			got, err := json.MarshalIndent(res, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			golden := filepath.Join("testdata", "results", tc.name+".json")
			if *update {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run go test ./internal/receipt -update)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("result differs from %s:\n%s", golden, got)
			}
		})
	}
}

// Only "verified" is success, and it needs a compared identity.
func TestEvaluateNeverVerifiesWithoutIdentity(t *testing.T) {
	for _, tc := range evalCases() {
		if tc.outcome == OutcomeVerified {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			golden, err := os.ReadFile(filepath.Join("testdata", "results", tc.name+".json"))
			if err != nil {
				t.Skip("golden not written yet")
			}
			var r Result
			if err := json.Unmarshal(golden, &r); err != nil {
				t.Fatal(err)
			}
			if r.Outcome == OutcomeVerified {
				t.Fatalf("%s verified", tc.name)
			}
		})
	}
	// A verified result always has a matched deployment or digest.
	for _, tc := range evalCases() {
		if tc.outcome != OutcomeVerified {
			continue
		}
		golden, err := os.ReadFile(filepath.Join("testdata", "results", tc.name+".json"))
		if err != nil {
			continue
		}
		var r Result
		if err := json.Unmarshal(golden, &r); err != nil {
			t.Fatal(err)
		}
		if r.Checks[CheckDeployment] != CheckMatch && r.Checks[CheckDigest] != CheckMatch {
			t.Errorf("%s: verified without a matched identity: %v", tc.name, r.Checks)
		}
	}
}

// An empty status from a command that "succeeded" is not success.
func TestEvaluateEmptyLiveStatus(t *testing.T) {
	rc, _ := fixture(t, "receipts/ostree.json")
	for _, status := range []string{"", "null", "{}", `{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost"}`} {
		res := Evaluate(Input{Receipt: rc, Evidence: EvidenceLive, Status: []byte(status), Now: testNow})
		if res.Outcome == OutcomeVerified {
			t.Errorf("status %q verified", status)
		}
	}
}
