package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tuna-os/fisherman/internal/probe"
	"github.com/tuna-os/fisherman/internal/recipe"
	"github.com/tuna-os/fisherman/internal/reuse"
	"github.com/tuna-os/fisherman/internal/validation"
)

var (
	cliReuseDigest = "sha256:" + strings.Repeat("1", 64)
	cliChecksum    = strings.Repeat("a", 64)
)

func reuseTestRecipe() *recipe.Recipe {
	return &recipe.Recipe{
		Disk: "/dev/null", Filesystem: "xfs", Hostname: "h",
		TargetImgref: "ghcr.io/example/os:stable",
		SourceReuse:  &recipe.SourceReuse{Store: "bootc", Digest: cliReuseDigest, Platform: "x86_64"},
		Experimental: []string{recipe.ExperimentalBootcStoreReuse},
	}
}

func eligible() probe.ReuseSource {
	src := "docker://ghcr.io/example/os:stable"
	return probe.ReuseSource{Eligible: true, Reason: probe.ReuseEligible, Candidates: []probe.ReuseCandidate{{
		Role: probe.RoleBooted, Ref: "ghcr.io/example/os:stable", Transport: "registry", SourceRef: src,
		OstreeRef: probe.OstreeImageRef(src), Checksum: cliChecksum, Stateroot: "default",
		Digest: cliReuseDigest, Platform: "x86_64"}}}
}

// stubReuseHost replaces the read-only resolution and counts its calls.
func stubReuseHost(t *testing.T, res probe.ReuseSource, mediaRequired bool) *int {
	t.Helper()
	n := 0
	oldSource, oldMedia := hostReuseSource, hostMediaRequired
	hostReuseSource = func(string, string) probe.ReuseSource { n++; return res }
	hostMediaRequired = func() (bool, error) { return mediaRequired, nil }
	t.Cleanup(func() { hostReuseSource, hostMediaRequired = oldSource, oldMedia })
	return &n
}

func TestReuseProblems(t *testing.T) {
	cases := []struct {
		name  string
		res   probe.ReuseSource
		media bool
		want  string
	}{
		{"eligible", eligible(), false, ""},
		{"non-bootc host", probe.ReuseSource{Reason: probe.ReuseHostNotBootc, HostReason: probe.StoreNotBootcBooted}, false, recipe.CodeSourceReuseHostNotBootc},
		{"bootc missing", probe.ReuseSource{Reason: probe.ReuseHostNotBootc, HostReason: probe.ReasonBootcMissing}, false, recipe.CodeSourceReuseHostNotBootc},
		{"live media", probe.ReuseSource{Reason: probe.ReuseLiveMedia}, false, recipe.CodeSourceReuseLiveMedia},
		{"live-iso-mode marker", eligible(), true, recipe.CodeSourceReuseLiveMedia},
		{"composefs", probe.ReuseSource{Reason: probe.ReuseBackendNotQualified}, false, recipe.CodeSourceReuseBackendNotQualified},
		{"not found", probe.ReuseSource{Reason: probe.ReuseItemNotFound}, false, recipe.CodeSourceReuseItemNotFound},
		{"wrong arch", probe.ReuseSource{Reason: probe.ReusePlatformMismatch}, false, recipe.CodeSourceReusePlatformMismatch},
		{"unverified", probe.ReuseSource{Reason: probe.ReuseItemUnverified}, false, recipe.CodeSourceReuseItemUnverified},
		{"eligible without candidates", probe.ReuseSource{Eligible: true, Reason: probe.ReuseEligible}, false, recipe.CodeSourceReuseItemUnverified},
		{"unknown reason", probe.ReuseSource{Reason: "something_new"}, false, recipe.CodeSourceReuseItemUnverified},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubReuseHost(t, c.res, c.media)
			ps := reuseProblems(reuseTestRecipe(), nil)
			got := ""
			if len(ps) == 1 {
				got = ps[0].Code
				if ps[0].Field != "sourceReuse" {
					t.Errorf("field %q", ps[0].Field)
				}
			} else if len(ps) > 1 {
				t.Fatalf("problems %+v", ps)
			}
			if got != c.want {
				t.Errorf("code %q, want %q", got, c.want)
			}
		})
	}
}

// TestReuseProblems_NoHostAccess: without sourceReuse, or with a static
// sourceReuse problem, the host is not asked anything.
func TestReuseProblems_NoHostAccess(t *testing.T) {
	n := stubReuseHost(t, eligible(), false)
	r := reuseTestRecipe()
	r.SourceReuse = nil
	if ps := reuseProblems(r, nil); ps != nil || *n != 0 {
		t.Errorf("without sourceReuse: %+v, %d host calls", ps, *n)
	}
	r = reuseTestRecipe()
	if ps := reuseProblems(r, []recipe.Problem{{Code: recipe.CodeSourceReuseDigestInvalid}}); ps != nil || *n != 0 {
		t.Errorf("after a static problem: %+v, %d host calls", ps, *n)
	}
}

// recordingProber is a Prober over an empty root whose commands are
// recorded; bootc status names one ostree deployment.
func recordingProber(t *testing.T) (*probe.Prober, *[]string) {
	t.Helper()
	var mu sync.Mutex
	calls := &[]string{}
	status := fmt.Sprintf(`{"status":{"booted":{"image":{"image":{"image":"ghcr.io/example/os:stable","transport":"registry"},
"imageDigest":%q,"architecture":"x86_64"},"ostree":{"checksum":%q,"deploySerial":0,"stateroot":"default"}}}}`, cliReuseDigest, cliChecksum)
	root := t.TempDir()
	p := &probe.Prober{Root: root, Getenv: func(string) string { return "" }, Arch: "x86_64",
		Run: func(name string, args ...string) ([]byte, error) {
			line := strings.Join(append([]string{name}, args...), " ")
			mu.Lock()
			*calls = append(*calls, line)
			mu.Unlock()
			if line == "bootc status --json" {
				return []byte(status), nil
			}
			return nil, errors.New("exit status 1")
		}}
	return p, calls
}

// TestValidate_SourceReuseIsReadOnly: `fisherman validate --json` of a
// recipe with sourceReuse resolves the item with `bootc status --json`
// and reads files; it runs no copy, pull, mount, ostree or podman command
// and writes nothing.
func TestValidate_SourceReuseIsReadOnly(t *testing.T) {
	stubTPM(t, false)
	p, calls := recordingProber(t)
	withProber(t, p)
	oldMedia := hostMediaRequired
	hostMediaRequired = func() (bool, error) { return false, nil }
	t.Cleanup(func() { hostMediaRequired = oldMedia })
	before := snapshotDir(t, p.Root)

	in := `{"disk":"/dev/null","filesystem":"xfs","hostname":"h","encryption":{"type":"none"},
"targetImgref":"ghcr.io/example/os:stable","experimental":["bootc_store_reuse"],
"sourceReuse":{"store":"bootc","digest":"` + cliReuseDigest + `","platform":"x86_64"}}`
	var out bytes.Buffer
	runValidateJSON([]string{"--json", "-"}, strings.NewReader(in), &out, &bytes.Buffer{})
	var res validateResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	for _, e := range res.Errors {
		if strings.HasPrefix(e.Code, "source_reuse_") || e.Code == recipe.CodeImageRequired {
			t.Errorf("an eligible sourceReuse was refused: %+v", e)
		}
	}
	sawStatus := false
	for _, c := range *calls {
		sawStatus = sawStatus || c == "bootc status --json"
		for _, bad := range []string{"bootc image", "bootc switch", "bootc upgrade", "ostree", "podman", "skopeo", "mount", "umount"} {
			if strings.HasPrefix(c, bad) {
				t.Errorf("validate ran %q", c)
			}
		}
	}
	if !sawStatus {
		t.Errorf("validate did not resolve the item (calls %v)", *calls)
	}
	if after := snapshotDir(t, p.Root); after != before {
		t.Error("validate wrote under the probe root")
	}

	// The partial mode never reads the host for it.
	*calls = nil
	out.Reset()
	runValidateJSON([]string{"--json", "--partial", "-"}, strings.NewReader(`{"sourceReuse":{"store":"bootc","digest":"x","platform":"x86_64"}}`), &out, &bytes.Buffer{})
	if len(*calls) != 0 {
		t.Errorf("partial validate ran %v", *calls)
	}
	if !strings.Contains(out.String(), recipe.CodeSourceReuseDigestInvalid) || !strings.Contains(out.String(), recipe.CodeSourceReuseExperimental) {
		t.Errorf("partial output %s", out.String())
	}
	if strings.Contains(out.String(), recipe.CodeSourceReuseTargetImgrefMissing) {
		t.Errorf("partial mode judged a field the fragment does not have: %s", out.String())
	}
}

func snapshotDir(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	_ = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err == nil {
			fmt.Fprintf(&b, "%s %v %d\n", p, fi.ModTime().UnixNano(), fi.Size())
		}
		return nil
	})
	return b.String()
}

type fakeSession struct {
	order *[]string
	res   *reuse.Result
	err   error
}

func (f *fakeSession) Acquire([]probe.ReuseCandidate) (*reuse.Result, error) {
	*f.order = append(*f.order, "acquire")
	return f.res, f.err
}

func (f *fakeSession) Release() error {
	*f.order = append(*f.order, "release")
	return nil
}

func stubSession(t *testing.T, s *fakeSession) *int {
	t.Helper()
	n := 0
	old := newReuseSession
	newReuseSession = func(*recipe.Recipe) reuseSession { n++; return s }
	t.Cleanup(func() { newReuseSession = old })
	return &n
}

// TestAcquireSourceReuse_DefaultPathDoesNothing: a recipe without
// sourceReuse never resolves, never builds a session, never registers a
// release: the default install path performs no reuse write and no reuse
// command.
func TestAcquireSourceReuse_DefaultPathDoesNothing(t *testing.T) {
	n := stubReuseHost(t, eligible(), false)
	var order []string
	built := stubSession(t, &fakeSession{order: &order})
	registered := 0
	r := reuseTestRecipe()
	r.SourceReuse = nil
	res, p := acquireSourceReuse(r, func(func() error) { registered++ })
	if res != nil || p != nil || *n != 0 || *built != 0 || registered != 0 || len(order) != 0 {
		t.Errorf("default path: res %v problem %v resolve %d sessions %d releases %d order %v", res, p, *n, *built, registered, order)
	}
}

func TestAcquireSourceReuse(t *testing.T) {
	stubReuseHost(t, eligible(), false)
	var order []string
	want := &reuse.Result{SourceImgref: "containers-storage:" + reuse.DestinationRepo + ":x", DestinationDigest: cliReuseDigest}
	stubSession(t, &fakeSession{order: &order, res: want})
	register := func(fn func() error) { order = append(order, "register"); _ = fn }
	res, p := acquireSourceReuse(reuseTestRecipe(), register)
	if p != nil || res != want {
		t.Fatalf("res %v problem %v", res, p)
	}
	if strings.Join(order, ",") != "register,acquire" {
		t.Errorf("the release must be registered before the copy starts: %v", order)
	}

	// A failure keeps its code, on field sourceReuse.
	order = nil
	stubSession(t, &fakeSession{order: &order, err: &reuse.Failure{Code: recipe.CodeSourceReuseDiskExhausted, Message: "full"}})
	_, p = acquireSourceReuse(reuseTestRecipe(), register)
	if p == nil || p.Code != recipe.CodeSourceReuseDiskExhausted || p.Field != "sourceReuse" {
		t.Errorf("problem %+v", p)
	}

	// Inside a Flatpak sandbox: refused before anything runs.
	oldSandboxed := reuseSandboxed
	reuseSandboxed = func() bool { return true }
	built := stubSession(t, &fakeSession{order: &order})
	_, p = acquireSourceReuse(reuseTestRecipe(), register)
	reuseSandboxed = oldSandboxed
	if p == nil || p.Code != recipe.CodeSourceReuseSandboxed || *built != 0 {
		t.Errorf("sandboxed: problem %+v, sessions %d", p, *built)
	}

	// The host changed between validate and the install: refused before
	// any session exists.
	stubReuseHost(t, probe.ReuseSource{Reason: probe.ReuseItemNotFound}, false)
	built = stubSession(t, &fakeSession{order: &order})
	_, p = acquireSourceReuse(reuseTestRecipe(), register)
	if p == nil || p.Code != recipe.CodeSourceReuseItemNotFound || *built != 0 {
		t.Errorf("problem %+v, sessions %d", p, *built)
	}
}

func TestCheckReusedInstall(t *testing.T) {
	old := installedDigest
	t.Cleanup(func() { installedDigest = old })
	res := &reuse.Result{DestinationDigest: cliReuseDigest}

	installedDigest = func(string) string { return cliReuseDigest }
	if msg, p := checkReusedInstall("/t", res); p != nil || !strings.Contains(msg, "verified copy") {
		t.Errorf("same digest: %q %+v", msg, p)
	}
	installedDigest = func(string) string { return "" }
	if msg, p := checkReusedInstall("/t", res); p != nil || !strings.HasPrefix(msg, "Warning:") {
		t.Errorf("unreadable digest: %q %+v", msg, p)
	}
	installedDigest = func(string) string { return "sha256:" + strings.Repeat("e", 64) }
	if _, p := checkReusedInstall("/t", res); p == nil || p.Code != recipe.CodeSourceReuseInstalledDigestDiffs {
		t.Errorf("different digest: %+v", p)
	}
}

func TestRequiredTools_SourceReuse(t *testing.T) {
	has := func(r *recipe.Recipe, tool string) bool {
		for _, req := range validation.RequiredTools(r) {
			if req.Tool == tool {
				return true
			}
		}
		return false
	}
	r := reuseTestRecipe()
	if !has(r, "bootc") || !has(r, "ostree") {
		t.Error("a sourceReuse install needs bootc and ostree on the host")
	}
	r.SourceReuse = nil
	if has(r, "ostree") {
		t.Error("the default install must not require ostree")
	}
}
