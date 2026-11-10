package probe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Digests used by the fixtures. digestHost is what the live host booted.
var (
	digestHost  = "sha256:" + strings.Repeat("1", 64)
	digestOther = "sha256:" + strings.Repeat("2", 64)
	digestList  = "sha256:" + strings.Repeat("3", 64)
)

// secretSentinel is planted in command output and file contents; it must
// never reach the JSON.
const secretSentinel = "SECRET-SENTINEL-c0ffee"

// statusJSON is `bootc status --json` for a host that booted image at
// digest with the given backend ("ostree", "composefs" or "").
func statusJSON(image, digest, backend string) string {
	extra := ""
	switch backend {
	case BackendOstree:
		extra = `,"ostree":{"checksum":"0a1b","deploySerial":0,"stateroot":"default"}`
	case BackendComposefs:
		extra = `,"composefs":{"verity":"abcd","bootType":"uki"},"ostree":null`
	}
	return fmt.Sprintf(`{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost","spec":{"image":{"image":%q}},
"status":{"booted":{"image":{"image":{"image":%q,"transport":"registry"},"imageDigest":%q,"architecture":"x86_64",
"version":"42.20261101","timestamp":null}%s,"pinned":false,"store":"ostreeContainer","futureField":{"x":1}}}}`, image, image, digest, extra)
}

// storeJSON is a containers-storage image index with one image.
func storeJSON(name, digest string, digests ...string) string {
	all, _ := json.Marshal(append([]string{digest}, digests...))
	return fmt.Sprintf(`[{"id":"cafe","names":[%q],"digest":%q,"digests":%s,"metadata":%q}]`, name, digest, all, secretSentinel)
}

const (
	inspectCurrent  = `{"kargs":["quiet"],"kernel":{"version":"6.12.0-55.el10.x86_64","unified":false}}`
	printConfigXFS  = `{"root-fs-type":"xfs"}`
	printConfigFull = `{"filesystem":{"root":{"type":"btrfs"}},"block":["direct","tpm2-luks"],"bootloader":"grub"}`
)

// sourceRoot is a current bootc whose source-root commands all answer.
func sourceRoot(status, printConfig, inspect string) map[string]string {
	c := map[string]string{
		"bootc status --json":               status,
		"bootc container inspect --json":    inspect,
		"bootc install print-configuration": printConfig,
	}
	for k, v := range bootcCurrent {
		c[k] = v
	}
	return c
}

const storePath = "usr/share/tuna-installer/oci-store/overlay-images/images.json"

type capsCase struct {
	fixture string
	files   map[string]string
	host    *fakeHost
	sel     Selection
}

func capsCases(t *testing.T) []capsCase {
	usage := exitStatus(t, 2)
	matchingStore := map[string]string{storePath: storeJSON("ghcr.io/example/os:stable", digestHost, digestList)}
	csSource := Selection{Source: "containers-storage:ghcr.io/example/os:stable", TargetImgref: "ghcr.io/example/os:stable"}
	ostreeHost := statusJSON("ghcr.io/example/os:stable", digestHost, BackendOstree)
	return []capsCase{
		// Nothing selected: every source feature is unknown, the live host is
		// still described, and no source-root command runs.
		{"caps-no-source", matchingStore,
			&fakeHost{canned: sourceRoot(ostreeHost, printConfigXFS, inspectCurrent)}, Selection{}},
		// The live host booted the very image the store holds: a current
		// bootc, OSTree backend.
		{"caps-matching-ostree-current", matchingStore,
			&fakeHost{canned: sourceRoot(ostreeHost, printConfigFull, inspectCurrent)}, csSource},
		// The same with the composefs backend and a UKI.
		{"caps-matching-composefs", map[string]string{storePath: storeJSON("ghcr.io/example/cfs:latest", digestHost)},
			&fakeHost{canned: sourceRoot(statusJSON("ghcr.io/example/cfs:latest", digestHost, BackendComposefs),
				`{"root-fs-type":"ext4","bootloader":"systemd"}`,
				`{"kernel":{"version":"6.15.2-arch1-1","unified":true}}`)},
			Selection{Source: "containers-storage:ghcr.io/example/cfs"}},
		// An older bootc in the source root: no `container inspect`, no
		// `install finalize`, a print-configuration with only root-fs-type.
		{"caps-matching-older", matchingStore, &fakeHost{
			canned: map[string]string{
				"bootc --version":                          "bootc 1.1.4\n",
				"bootc install print-configuration --help": "",
				"bootc install print-configuration":        printConfigXFS,
				"bootc status --json":                      ostreeHost,
			},
			fail: map[string]error{
				"bootc container inspect --help": usage,
				"bootc container inspect --json": usage,
				"bootc install finalize --help":  usage,
			}}, csSource},
		// The live host runs an unrelated image (another digest), with an
		// install configuration that conflicts with the source's. Nothing of
		// the host's may be reported for the source.
		{"caps-unrelated-live-host", map[string]string{storePath: storeJSON("ghcr.io/example/os:stable", digestOther)},
			&fakeHost{canned: sourceRoot(ostreeHost, `{"root-fs-type":"btrfs","bootloader":"systemd"}`, inspectCurrent)}, csSource},
		// A registry source: the probe does not contact registries.
		{"caps-remote-source", matchingStore,
			&fakeHost{canned: sourceRoot(ostreeHost, printConfigXFS, inspectCurrent)},
			Selection{Source: "ghcr.io/example/os:stable"}},
		// A registry reference pinned to the digest the host booted.
		{"caps-pinned-reference", nil,
			&fakeHost{canned: sourceRoot(ostreeHost, printConfigXFS, inspectCurrent)},
			Selection{Source: "docker://ghcr.io/example/os@" + digestHost}},
		// No bootc on the host at all.
		{"caps-host-bootc-missing", matchingStore, &fakeHost{fail: map[string]error{
			"bootc --version":     &exec.Error{Name: "bootc", Err: exec.ErrNotFound},
			"bootc status --json": &exec.Error{Name: "bootc", Err: exec.ErrNotFound},
		}}, csSource},
		// The source root answers with output the adapter cannot read.
		{"caps-malformed", matchingStore,
			&fakeHost{canned: sourceRoot(ostreeHost,
				"\x1b[31merror\x1b[0m "+secretSentinel+" <script>",
				`{"kernel":{"version":"$(echo `+secretSentinel+`)","unified":false}}`)}, csSource},
		// Fields the adapter does not know are ignored, never copied.
		{"caps-unknown-fields", matchingStore,
			&fakeHost{canned: sourceRoot(ostreeHost,
				`{"root-fs-type":"xfs","block":["direct"],"kargs":["luks.key=`+secretSentinel+`"],"future":{"nested":[1,2]},"stateroot":"default"}`,
				`{"kargs":["`+secretSentinel+`"],"kernel":{"version":"6.12.0","unified":false,"future":true},"newTopLevel":"`+secretSentinel+`"}`)},
			csSource},
		// A known key with the wrong type is malformed, not a partial answer.
		{"caps-wrong-types", matchingStore,
			&fakeHost{canned: sourceRoot(ostreeHost, `{"root-fs-type":["xfs"]}`, `{"kernel":"6.12.0"}`)}, csSource},
		// One source-root check dies for an unrelated reason (a timeout).
		{"caps-check-failed", matchingStore, &fakeHost{
			canned: sourceRoot(ostreeHost, printConfigXFS, inspectCurrent),
			fail:   map[string]error{"bootc install print-configuration": exitStatus(t, 137)},
		}, csSource},
		// A local OCI layout of another image (offline media).
		{"caps-oci-layout", map[string]string{"srv/layout/index.json": fmt.Sprintf(
			`{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":%q,"size":1,
"platform":{"os":"linux","architecture":"amd64"},"annotations":{"org.opencontainers.image.ref.name":"stable"}},
{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":%q,"size":1,"annotations":{"org.opencontainers.image.ref.name":"beta"}}]}`,
			digestOther, digestList)},
			&fakeHost{canned: sourceRoot(ostreeHost, printConfigXFS, inspectCurrent)},
			Selection{Source: "oci:/srv/layout:stable", TargetImgref: "ghcr.io/example/os:stable"}},
		// A local containers-storage source no readable store has.
		{"caps-local-not-found", nil,
			&fakeHost{canned: sourceRoot(ostreeHost, printConfigXFS, inspectCurrent)}, csSource},
		// Two stores hold different images under the selected name.
		{"caps-ambiguous", map[string]string{
			storePath: storeJSON("ghcr.io/example/os:stable", digestHost),
			"var/lib/superiso-store/overlay-images/images.json": storeJSON("ghcr.io/example/os:stable", digestOther),
		}, &fakeHost{canned: sourceRoot(ostreeHost, printConfigXFS, inspectCurrent)}, csSource},
		// The recipe omits image: the live root is the source.
		{"caps-live-root", nil,
			&fakeHost{canned: sourceRoot(ostreeHost, printConfigXFS, inspectCurrent)}, Selection{LiveRoot: true}},
	}
}

func runCaps(t *testing.T, c capsCase) Capabilities {
	t.Helper()
	p := newProber(t, c.files, c.host, nil)
	p.Selection = c.sel
	return p.Capabilities(p.Live(), p.Offline(), p.Bootc())
}

// TestCapabilitiesGolden pins the capabilities object for every case
// tuna-os/fisherman#383 lists. Regenerate with `go test ./internal/probe
// -update`.
func TestCapabilitiesGolden(t *testing.T) {
	for _, c := range capsCases(t) {
		t.Run(c.fixture, func(t *testing.T) {
			got, err := json.MarshalIndent(runCaps(t, c), "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			golden := filepath.Join("testdata", "golden", c.fixture+".json")
			if *update {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("capabilities differ from %s:\n%s", golden, got)
			}
		})
	}
}

// Invariants that hold in every fixture.
func TestCapabilitiesInvariants(t *testing.T) {
	names := []string{FeatNativeInspection, FeatInstallConfiguration, FeatDeploymentMount,
		FeatFinalization, FeatUnifiedStorage, FeatBootcImageStorage}
	for _, c := range capsCases(t) {
		caps := runCaps(t, c)
		b, _ := json.Marshal(caps)
		if bytes.Contains(b, []byte(secretSentinel)) || bytes.Contains(b, []byte("<script>")) {
			t.Errorf("%s: command output or file content reached the JSON: %s", c.fixture, b)
		}
		if caps.Version != CapabilitiesVersion {
			t.Errorf("%s: version %d", c.fixture, caps.Version)
		}
		if len(caps.Features) != len(names) {
			t.Errorf("%s: %d features, want %d", c.fixture, len(caps.Features), len(names))
		}
		for _, n := range names {
			f, ok := caps.Features[n]
			if !ok || f.Reason == "" || f.Context == "" ||
				(f.Status != CapSupported && f.Status != CapUnsupported && f.Status != CapUnknown) {
				t.Errorf("%s: feature %s = %+v (present %v)", c.fixture, n, f, ok)
			}
			// Only a command that did the work, in the source root, may say
			// supported.
			if f.Status == CapSupported && (f.Reason != ReasonObserved || f.Context != ContextSourceRoot) {
				t.Errorf("%s: %s supported with %+v", c.fixture, n, f)
			}
		}
		// Nothing read-only qualifies these; they are never supported.
		for _, n := range []string{FeatDeploymentMount, FeatBootcImageStorage, FeatFinalization} {
			if caps.Features[n].Status == CapSupported {
				t.Errorf("%s: %s became supported", c.fixture, n)
			}
		}
		if caps.Features[FeatUnifiedStorage] != (Feature{CapUnsupported, ReasonFishermanRefuses, ContextFisherman}) {
			t.Errorf("%s: unified_storage = %+v", c.fixture, caps.Features[FeatUnifiedStorage])
		}
		// A source-root answer exists only with an established root.
		for n, f := range caps.Features {
			if f.Context == ContextSourceRoot && !caps.ExecutionRoot.Established {
				t.Errorf("%s: %s answered in the source root without one", c.fixture, n)
			}
		}
		if caps.InstallDefaults.BlockSetups == nil {
			t.Errorf("%s: block_setups is null", c.fixture)
		}
		if caps.LiveHost.Context != BootcContextLiveHost {
			t.Errorf("%s: live_host.context %q", c.fixture, caps.LiveHost.Context)
		}
	}
}

// The probe runs bootc in the source root only when the root's identity
// matches, and there only the three read-only calls. Without a match it
// runs nothing beyond the live-host calls, so a conflicting host
// configuration cannot leak into the source's answer.
func TestCapabilitiesCommands(t *testing.T) {
	liveHostCalls := map[string]bool{"bootc --version": true, "bootc status --json": true,
		"bootc container inspect --help": true, "bootc install print-configuration --help": true,
		"bootc install finalize --help": true}
	sourceCalls := map[string]bool{"bootc container inspect --json": true,
		"bootc install print-configuration": true, "bootc install finalize --help": true}
	for _, c := range capsCases(t) {
		caps := runCaps(t, c)
		var source []string
		for _, call := range c.host.calls {
			switch {
			case strings.HasPrefix(call, "lsblk"), strings.HasPrefix(call, "test "), strings.HasPrefix(call, "cat "):
				t.Errorf("%s: unexpected call %q", c.fixture, call)
			case sourceCalls[call] && !liveHostCalls[call]:
				source = append(source, call)
			case !liveHostCalls[call]:
				t.Errorf("%s: ran %q", c.fixture, call)
			}
		}
		if !caps.ExecutionRoot.Established && len(source) != 0 {
			t.Errorf("%s: ran %v without an established source root", c.fixture, source)
		}
		if caps.ExecutionRoot.Established && len(source) != 2 {
			t.Errorf("%s: source-root calls %v", c.fixture, source)
		}
		n := 0
		for _, call := range c.host.calls {
			if call == "bootc status --json" {
				n++
			}
		}
		if n > 1 {
			t.Errorf("%s: bootc status ran %d times", c.fixture, n)
		}
	}
}

// The unrelated host's configuration (btrfs, systemd-boot) never appears as
// the source's defaults.
func TestCapabilitiesConflictingLiveHost(t *testing.T) {
	for _, c := range capsCases(t) {
		if c.fixture != "caps-unrelated-live-host" {
			continue
		}
		caps := runCaps(t, c)
		if caps.ExecutionRoot.Established || caps.ExecutionRoot.Reason != RootDigestMismatch {
			t.Fatalf("execution root %+v", caps.ExecutionRoot)
		}
		if caps.InstallDefaults.Observed || caps.InstallDefaults.RootFilesystem != "" || caps.InstallDefaults.Bootloader != "" {
			t.Errorf("host defaults reported for the source: %+v", caps.InstallDefaults)
		}
		if caps.ExecutionRoot.BootcVersion != "" || caps.ExecutionRoot.Backend != "" {
			t.Errorf("host bootc reported as the source's: %+v", caps.ExecutionRoot)
		}
	}
}

// Same image name, same version string, but no digest proof: not
// established. And a matching digest with any version gives the same
// answers: no feature follows from a version number.
func TestCapabilitiesNeverFromVersionOrName(t *testing.T) {
	ostreeHost := statusJSON("ghcr.io/example/os:stable", digestHost, BackendOstree)
	// The store has the name but no digest.
	files := map[string]string{storePath: `[{"id":"x","names":["ghcr.io/example/os:stable"]}]`}
	p := newProber(t, files, &fakeHost{canned: sourceRoot(ostreeHost, printConfigXFS, inspectCurrent)}, nil)
	p.Selection = Selection{Source: "containers-storage:ghcr.io/example/os:stable"}
	caps := p.Capabilities(p.Live(), p.Offline(), p.Bootc())
	if caps.ExecutionRoot.Established || caps.ExecutionRoot.Reason != RootSourceDigestUnknown {
		t.Errorf("name match established %+v", caps.ExecutionRoot)
	}

	var answers []string
	for _, v := range []string{"bootc 0.1.0\n", "bootc 1.17.1\n", "bootc 99.0.0\n"} {
		canned := sourceRoot(ostreeHost, printConfigXFS, inspectCurrent)
		canned["bootc --version"] = v
		p := newProber(t, map[string]string{storePath: storeJSON("ghcr.io/example/os:stable", digestHost)}, &fakeHost{canned: canned}, nil)
		p.Selection = Selection{Source: "containers-storage:ghcr.io/example/os:stable"}
		caps := p.Capabilities(p.Live(), p.Offline(), p.Bootc())
		b, _ := json.Marshal(caps.Features)
		answers = append(answers, string(b))
	}
	sort.Strings(answers)
	if answers[0] != answers[2] {
		t.Errorf("features change with the version string:\n%s", strings.Join(answers, "\n"))
	}
}

func TestValidateReference(t *testing.T) {
	for _, ok := range []string{
		"ghcr.io/example/os:stable",
		"containers-storage:ghcr.io/example/os:stable",
		"docker://quay.io/example/os@" + digestHost,
		"oci:/var/lib/oci/layout:stable",
		"localhost:5000/os",
	} {
		if err := ValidateReference(ok); err != nil {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{
		"", " ghcr.io/x", "ghcr.io/x y", "$(reboot)", "a;b", "x\n", "oci:/srv/../etc", "[overlay@/x]img",
		"\x1b[31m", strings.Repeat("a", 513), "-rf",
	} {
		err := ValidateReference(bad)
		if err == nil {
			t.Errorf("%q accepted", bad)
		} else if bad != "" && strings.Contains(err.Error(), bad) {
			t.Errorf("error echoes the reference: %v", err)
		}
	}
}

func TestParseBootcStatusBackends(t *testing.T) {
	for in, want := range map[string]string{
		statusJSON("x", digestHost, BackendOstree):    BackendOstree,
		statusJSON("x", digestHost, BackendComposefs): BackendComposefs,
		statusJSON("x", digestHost, ""):               "",
	} {
		if got := parseBootcStatus([]byte(in), nil).backend; got != want {
			t.Errorf("backend %q, want %q", got, want)
		}
	}
	st := parseBootcStatus([]byte(statusJSON("x", "sha256:short", "")), nil)
	if st.digest != "" || !st.booted {
		t.Errorf("malformed digest kept: %+v", st)
	}
	if st := parseBootcStatus([]byte(`{"status":{"booted":null}}`), nil); st.booted || !st.ok {
		t.Errorf("unbooted host: %+v", st)
	}
}
