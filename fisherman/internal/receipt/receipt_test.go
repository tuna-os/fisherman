package receipt

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/fisherman/internal/deploy"
	"github.com/tuna-os/fisherman/internal/deploy/deploytest"
)

const testID = "0123456789abcdef0123456789abcdef"

func ostreeDeployment(t *testing.T) (*deploy.Deployment, deploytest.Sysroot) {
	t.Helper()
	s := deploytest.Ostree(t)
	d, err := deploy.Resolve(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	return d, s
}

func TestNewOstreeReceipt(t *testing.T) {
	d, _ := ostreeDeployment(t)
	digest := "sha256:" + strings.Repeat("1", 64)
	r, err := New(Facts{
		InstallID:        testID,
		InstalledAt:      time.Date(2026, 11, 10, 12, 0, 0, 0, time.FixedZone("x", 3600)),
		FishermanVersion: "v0.3.0",
		Deployment:       d,
		SourceRef:        "containers-storage:ghcr.io/example/os:stable",
		UpdateRef:        "ghcr.io/example/os:stable",
		InstalledDigest:  digest,
		GOARCH:           "amd64",
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Schema != ReceiptSchema || r.Version != ReceiptVersion || r.InstallID != testID {
		t.Fatalf("header %+v", r)
	}
	if r.InstalledAt != "2026-11-10T11:00:00Z" {
		t.Errorf("installed_at %q, want UTC", r.InstalledAt)
	}
	if r.Backend != BackendOstree || r.Deployment.ID != deploytest.OstreeChecksum || r.Deployment.Stateroot != "default" {
		t.Errorf("deployment %s %+v", r.Backend, r.Deployment)
	}
	if r.Expected.PlatformManifestDigest != digest || r.Expected.DigestFrom != DigestFromInstalledDeployment || r.Expected.Platform != "linux/amd64" {
		t.Errorf("expected %+v", r.Expected)
	}
	if r.Source.Transport != TransportContainersStorage || r.UpdateRef != "ghcr.io/example/os:stable" {
		t.Errorf("source %+v update %q", r.Source, r.UpdateRef)
	}
}

func TestNewComposefsMediaReceipt(t *testing.T) {
	verity := strings.Repeat("ab", 64)
	d := &deploy.Deployment{Sysroot: "/t", Layout: deploy.ComposeFs, Stateroot: "default", Root: "/t/state/deploy/" + verity}
	platform, index := "sha256:"+strings.Repeat("1", 64), "sha256:"+strings.Repeat("a", 64)
	r, err := New(Facts{InstallID: testID, Deployment: d, SourceRef: "oci:/run/media/os",
		MediaPlatformDigest: platform, MediaIndexDigest: index, MediaPlatform: "linux/arm64", GOARCH: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Backend != BackendComposefs || r.Deployment.ID != verity {
		t.Errorf("deployment %s %+v", r.Backend, r.Deployment)
	}
	// The verified platform digest is the expectation; the index is kept
	// apart and never takes its place.
	if r.Expected.PlatformManifestDigest != platform || r.Expected.DigestFrom != DigestFromMediaInput ||
		r.Expected.IndexDigest != index || r.Expected.Platform != "linux/arm64" {
		t.Errorf("expected %+v", r.Expected)
	}
}

// A reference pinned by digest may pin an index: it is recorded as the
// reference's digest and never becomes the expected platform digest.
func TestNewPinnedReferenceIsNotExpectation(t *testing.T) {
	d, _ := ostreeDeployment(t)
	pin := "sha256:" + strings.Repeat("a", 64)
	r, err := New(Facts{InstallID: testID, Deployment: d, SourceRef: "ghcr.io/example/os@" + pin, GOARCH: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Source.ReferenceDigest != pin || r.Expected.PlatformManifestDigest != "" || r.Expected.DigestReason != DigestNotRecorded {
		t.Errorf("source %+v expected %+v", r.Source, r.Expected)
	}
}

func TestNewRedactsUnsafeReferences(t *testing.T) {
	d, _ := ostreeDeployment(t)
	for _, ref := range []string{
		"user:secret@registry.example/os:1",
		"docker://user:secret@registry.example/os:1",
		"ghcr.io/x/os:1; rm -rf /",
		"oci:/run/../etc/shadow",
		"ghcr.io/x/os\n",
	} {
		r, err := New(Facts{InstallID: testID, Deployment: d, SourceRef: ref, UpdateRef: ref})
		if err != nil {
			t.Fatal(err)
		}
		if r.Source.Ref != "" || r.UpdateRef != "" {
			t.Errorf("%q recorded as %q / %q", ref, r.Source.Ref, r.UpdateRef)
		}
	}
	if _, err := New(Facts{InstallID: "not-hex", Deployment: d}); err == nil {
		t.Error("an invalid install id was accepted")
	}
}

// The receipt's JSON has a fixed set of keys, none of them a secret or a
// personal value.
func TestReceiptKeysAreNonSecret(t *testing.T) {
	d, _ := ostreeDeployment(t)
	r, _ := New(Facts{InstallID: testID, Deployment: d, SourceRef: "ghcr.io/x/os:1", UpdateRef: "ghcr.io/x/os:1"})
	b, _ := json.Marshal(r)
	var walk func(prefix string, v any)
	var keys []string
	walk = func(prefix string, v any) {
		if m, ok := v.(map[string]any); ok {
			for k, sub := range m {
				keys = append(keys, prefix+k)
				walk(prefix+k+".", sub)
			}
		}
	}
	var v any
	_ = json.Unmarshal(b, &v)
	walk("", v)
	for _, k := range keys {
		for _, bad := range []string{"pass", "secret", "token", "auth", "machine", "host", "user", "serial", "key"} {
			if strings.Contains(strings.ToLower(k), bad) {
				t.Errorf("receipt key %q", k)
			}
		}
	}
}

func TestWriteOstree(t *testing.T) {
	d, s := ostreeDeployment(t)
	r, _ := New(Facts{InstallID: testID, Deployment: d, GOARCH: "amd64"})
	if err := Write(d, r); err != nil {
		t.Fatal(err)
	}
	// The receipt is in the booted system's /var, not the sysroot's.
	b, err := os.ReadFile(filepath.Join(s.Var, "lib", "fisherman", "install-receipt.json"))
	if err != nil {
		t.Fatal(err)
	}
	var back Receipt
	if err := json.Unmarshal(b, &back); err != nil || back.InstallID != testID {
		t.Fatalf("receipt %s: %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(s.Path, "var")); err == nil {
		t.Error("wrote into the physical sysroot's /var")
	}
	unit, err := os.ReadFile(filepath.Join(s.Etc, "systemd", "system", UnitName))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Type=oneshot", "ExecStart=/bin/sh " + ScriptPath, "ConditionPathExists=!" + CollectionDir + "/" + CollectionFile, "WantedBy=multi-user.target"} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("unit lacks %q", want)
		}
	}
	link, err := os.Readlink(filepath.Join(s.Etc, "systemd", "system", "multi-user.target.wants", UnitName))
	if err != nil || link != "/etc/systemd/system/"+UnitName {
		t.Errorf("wants link %q %v", link, err)
	}
	script, err := os.ReadFile(filepath.Join(s.Etc, "fisherman", "boot-receipt.sh"))
	if err != nil || !strings.Contains(string(script), testID) {
		t.Fatalf("script: %v", err)
	}
	// Writing again (a retried step) replaces, never fails on the link.
	if err := Write(d, r); err != nil {
		t.Fatalf("second write: %v", err)
	}
}

// Nothing the collector or the unit runs can change what boots.
func TestCollectorNeverMutates(t *testing.T) {
	// Comments may say what it does not do; the commands may not do it.
	var lines []string
	for _, l := range strings.Split(CollectorUnit()+CollectorScript(testID), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "#") {
			lines = append(lines, l)
		}
	}
	text := strings.Join(lines, "\n")
	for _, bad := range []string{"bootc switch", "bootc upgrade", "bootc rollback", "bootc edit", "efibootmgr",
		"bootctl", "reboot", "systemctl", "curl", "wget", "ostree admin"} {
		if strings.Contains(text, bad) {
			t.Errorf("collector mentions %q", bad)
		}
	}
	if strings.Contains(CollectorScript("$(evil)"), "$(evil)") {
		t.Error("an invalid install id reached the script")
	}
}

func TestWriteTransientEtc(t *testing.T) {
	d, s := ostreeDeployment(t)
	conf := filepath.Join(s.Root, "usr", "lib", "ostree", "prepare-root.conf")
	if err := os.MkdirAll(filepath.Dir(conf), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conf, []byte("[etc]\ntransient=true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, _ := New(Facts{InstallID: testID, Deployment: d})
	if err := Write(d, r); !errors.Is(err, ErrTransientEtc) {
		t.Fatalf("err %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Var, "lib", "fisherman", "install-receipt.json")); err != nil {
		t.Errorf("receipt not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Etc, "systemd", "system", UnitName)); err == nil {
		t.Error("unit staged in a transient /etc")
	}
}

func TestOstreeManifestDigest(t *testing.T) {
	d, s := ostreeDeployment(t)
	digest := "sha256:" + strings.Repeat("1", 64)
	var argv []string
	out := func(name string, args ...string) ([]byte, error) {
		argv = append([]string{name}, args...)
		return []byte("'" + digest + "'\n"), nil
	}
	if got := OstreeManifestDigest(d, out); got != digest {
		t.Fatalf("digest %q", got)
	}
	want := []string{"ostree", "show", "--repo=" + filepath.Join(s.Path, "ostree", "repo"),
		"--print-metadata-key=ostree.manifest-digest", deploytest.OstreeChecksum}
	if strings.Join(argv, " ") != strings.Join(want, " ") {
		t.Errorf("argv %q", argv)
	}
	for _, bad := range []string{"", "'sha256:short'", "error: No such metadata key"} {
		o := func(string, ...string) ([]byte, error) { return []byte(bad), nil }
		if got := OstreeManifestDigest(d, o); got != "" {
			t.Errorf("%q gave %q", bad, got)
		}
	}
	fail := func(string, ...string) ([]byte, error) { return nil, exec.ErrNotFound }
	if got := OstreeManifestDigest(d, fail); got != "" {
		t.Errorf("missing ostree gave %q", got)
	}
	cfs := &deploy.Deployment{Layout: deploy.ComposeFs, Root: "/x/state/deploy/abc"}
	if got := OstreeManifestDigest(cfs, out); got != "" {
		t.Errorf("composefs gave %q", got)
	}
}

// toolDir holds links to the few tools the collector uses, and a fake
// bootc when status is not nil, so the test does not depend on (or run)
// the host's bootc.
func toolDir(t *testing.T, bootc string) string {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range []string{"mkdir", "rm", "cat", "date", "head", "mv", "rmdir"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s: %v", tool, err)
		}
		if err := os.Symlink(p, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	if bootc != "" {
		if err := os.WriteFile(filepath.Join(dir, "bootc"), []byte(bootc), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// runCollector runs the real collector script with its directory moved
// under a temp dir, and returns that directory.
func runCollector(t *testing.T, bootc string) string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	out := filepath.Join(t.TempDir(), "boot-receipt")
	script := strings.Replace(CollectorScript(testID), "dir="+CollectionDir, "dir="+out, 1)
	path := filepath.Join(t.TempDir(), "collect.sh")
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sh, path)
	cmd.Env = []string{"PATH=" + toolDir(t, bootc)}
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("collector: %v\n%s", err, b)
	}
	return out
}

func collectedInput(t *testing.T, dir string) Input {
	t.Helper()
	rc, err := os.ReadFile(filepath.Join("testdata", "receipts", "ostree.json"))
	if err != nil {
		t.Fatal(err)
	}
	in := Input{Receipt: rc, Evidence: EvidenceCollected, Now: testNow}
	in.Collection, in.CollectionErr = os.ReadFile(filepath.Join(dir, CollectionFile))
	in.Status, in.StatusErr = os.ReadFile(filepath.Join(dir, StatusFile))
	return in
}

func TestCollectorWithFakeBootc(t *testing.T) {
	status, err := filepath.Abs(filepath.Join("testdata", "status", "ostree-booted.json"))
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "argv")
	fake := "#!/bin/sh\necho \"$*\" >>" + log + "\n" +
		"case \"$1\" in --version) echo 'bootc 1.17.1';; status) cat " + status + ";; *) exit 9;; esac\n"
	dir := runCollector(t, fake)
	in := collectedInput(t, dir)
	res := Evaluate(in)
	if res.Outcome != OutcomeVerified {
		t.Fatalf("outcome %s/%s", res.Outcome, res.Reason)
	}
	var c map[string]any
	if err := json.Unmarshal(in.Collection, &c); err != nil {
		t.Fatalf("collection is not JSON: %v\n%s", err, in.Collection)
	}
	if c["install_id"] != testID || c["bootc_exit"] != float64(0) {
		t.Errorf("collection %v", c)
	}
	if v, _ := os.ReadFile(filepath.Join(dir, VersionFile)); strings.TrimSpace(string(v)) != "bootc 1.17.1" {
		t.Errorf("version %q", v)
	}
	argv, _ := os.ReadFile(log)
	if got := strings.TrimSpace(string(argv)); got != "status --format json --format-version 1\n--version" {
		t.Errorf("bootc ran with %q", got)
	}
	// No temp directory is left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("left %s", e.Name())
		}
	}
}

func TestCollectorOlderBootc(t *testing.T) {
	dir := runCollector(t, "#!/bin/sh\necho 'error: unexpected argument' >&2\nexit 2\n")
	if res := Evaluate(collectedInput(t, dir)); res.Outcome != OutcomeUnsupported || res.Reason != ReasonStatusFormatUnsupported {
		t.Fatalf("outcome %s/%s", res.Outcome, res.Reason)
	}
}

func TestCollectorWithoutBootc(t *testing.T) {
	dir := runCollector(t, "")
	in := collectedInput(t, dir)
	if res := Evaluate(in); res.Outcome != OutcomeUnavailable || res.Reason != ReasonBootcAbsent {
		t.Fatalf("outcome %s/%s", res.Outcome, res.Reason)
	}
	if _, err := os.Stat(filepath.Join(dir, StatusFile)); err == nil {
		t.Error("a status file without bootc")
	}
}

func TestCollectorGarbageOutput(t *testing.T) {
	dir := runCollector(t, "#!/bin/sh\nprintf 'not json \"\\\\'\n")
	in := collectedInput(t, dir)
	var c map[string]any
	if err := json.Unmarshal(in.Collection, &c); err != nil {
		t.Fatalf("collection is not JSON: %v", err)
	}
	if res := Evaluate(in); res.Outcome != OutcomeUnavailable || res.Reason != ReasonStatusMalformed {
		t.Fatalf("outcome %s/%s", res.Outcome, res.Reason)
	}
}
