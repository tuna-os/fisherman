package probe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	bootcStoreIndexPath = "sysroot/ostree/bootc/storage/overlay-images/images.json"
	imageListCall       = "bootc image list --type host --format json"
	liveCmdline         = "BOOT_IMAGE=/images/pxeboot/vmlinuz root=live:CDLABEL=Live rd.live.image quiet\n"
)

// deploymentJSON is one bootc BootEntry with an image.
func deploymentJSON(image, digest, arch, backend string) string {
	extra := `,"ostree":{"checksum":"0a1b","deploySerial":0,"stateroot":"default"}`
	if backend == BackendComposefs {
		extra = `,"composefs":{"verity":"abcd","bootType":"uki"},"ostree":null`
	}
	return fmt.Sprintf(`{"image":{"image":{"image":%q,"transport":"registry","signature":{"ostreeRemote":%q}},
"imageDigest":%q,"architecture":%q,"version":"42","timestamp":null}%s,"pinned":false,"store":"ostreeContainer"}`,
		image, secretSentinel, digest, arch, extra)
}

// hostStatusJSON is `bootc status --json` with the given entries ("" for
// none) and extra other deployments.
func hostStatusJSON(booted, staged, rollback string, other ...string) string {
	or := func(s string) string {
		if s == "" {
			return "null"
		}
		return s
	}
	others := ""
	if len(other) > 0 {
		others = `,"otherDeployments":[` + strings.Join(other, ",") + `]`
	}
	return fmt.Sprintf(`{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost","spec":{"image":null},
"status":{"staged":%s,"booted":%s,"rollback":%s%s,"rollbackQueued":false,"type":"bootcHost","futureField":[1]}}`,
		or(staged), or(booted), or(rollback), others)
}

// bootcStoreIndexJSON is bootc's containers-storage index.
func bootcStoreIndexJSON(entries ...[2]string) string {
	var parts []string
	for i, e := range entries {
		parts = append(parts, fmt.Sprintf(`{"id":"%02d","names":[%q],"digest":%q,"metadata":%q}`, i, e[0], e[1], secretSentinel))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

type storeCase struct {
	fixture string
	files   map[string]string
	canned  map[string]string
	fail    map[string]error
	list    bool
	euid    int
	arch    string
	// wantList: `bootc image list` must run.
	wantList bool
}

func storeCases(t *testing.T) []storeCase {
	usage := exitStatus(t, 2)
	os1 := "ghcr.io/example/os:stable"
	booted := deploymentJSON(os1, digestHost, "x86_64", BackendOstree)
	staged := deploymentJSON(os1, digestOther, "x86_64", BackendOstree)
	rollback := deploymentJSON("ghcr.io/example/os:old", digestList, "x86_64", BackendOstree)
	installed := hostStatusJSON(booted, staged, rollback)
	index := map[string]string{bootcStoreIndexPath: bootcStoreIndexJSON(
		[2]string{"quay.io/example/app:1", digestList},
		[2]string{os1, digestHost},
	)}
	listJSON := `[{"image_type":"host","image":"ostree-unverified-registry:ghcr.io/example/os:stable"},
{"image_type":"host","image":"ghcr.io/example/os:old","size":1,"future":"` + secretSentinel + `"},
{"image_type":"unified","image":"ghcr.io/example/os:stable"},
{"image_type":"logical","image":"quay.io/example/app:1"}]`
	with := func(m map[string]string, kv ...string) map[string]string {
		out := map[string]string{}
		for k, v := range m {
			out[k] = v
		}
		for i := 0; i+1 < len(kv); i += 2 {
			out[kv[i]] = kv[i+1]
		}
		return out
	}
	live := map[string]string{"proc/cmdline": liveCmdline}
	return []storeCase{
		// A generic live image: bootc runs, but booted no image.
		{fixture: "store-non-bootc-live", files: live,
			canned: map[string]string{"bootc status --json": `{"apiVersion":"org.containers.bootc/v1","status":{"booted":null,"staged":null,"rollback":null}}`},
			list:   true, euid: 0},
		// No bootc at all.
		{fixture: "store-bootc-missing", fail: map[string]error{"bootc status --json": &exec.Error{Name: "bootc", Err: exec.ErrNotFound}}, list: true},
		// bootc status itself fails.
		{fixture: "store-status-failed", fail: map[string]error{"bootc status --json": exitStatus(t, 1)}, list: true},
		// bootc status prints something that is not JSON.
		{fixture: "store-status-malformed", canned: map[string]string{"bootc status --json": "\x1b[31m" + secretSentinel}, list: true},
		// An installed ostree host, default probe: status and store index,
		// no `bootc image list`.
		{fixture: "store-bootc-host-default", files: index, canned: map[string]string{"bootc status --json": installed}},
		// The same host, image list asked for, as root.
		{fixture: "store-bootc-host-listed", files: index, canned: map[string]string{
			"bootc status --json": installed, imageListCall: listJSON}, list: true, wantList: true},
		// Only logically bound entries come back: none is host inventory.
		{fixture: "store-logical-only", canned: map[string]string{
			"bootc status --json": hostStatusJSON(booted, "", ""),
			imageListCall:         `[{"image_type":"logical","image":"quay.io/example/app:1"},{"image_type":"logical","image":"quay.io/example/db:2"}]`},
			list: true, wantList: true},
		// The store exists, but its index is not readable.
		{fixture: "store-inaccessible", files: map[string]string{bootcStoreIndexPath + "/": ""},
			canned: map[string]string{"bootc status --json": hostStatusJSON(booted, "", "")}},
		// An older bootc without `image list --format`.
		{fixture: "store-older-cli", canned: map[string]string{"bootc status --json": hostStatusJSON(booted, "", "")},
			fail: map[string]error{imageListCall: usage}, list: true, wantList: true},
		// image list fails for another reason (a timeout).
		{fixture: "store-list-failed", canned: map[string]string{"bootc status --json": hostStatusJSON(booted, "", "")},
			fail: map[string]error{imageListCall: exitStatus(t, 137)}, list: true, wantList: true},
		// Malformed list output and a malformed store index.
		{fixture: "store-malformed", files: map[string]string{bootcStoreIndexPath: `{"not":"a list","x":"` + secretSentinel + `"}`},
			canned: map[string]string{"bootc status --json": hostStatusJSON(booted, "", ""),
				imageListCall: `[{"image_type":["host"],"image":"<script>` + secretSentinel + `"}]`},
			list: true, wantList: true},
		// A rollback entry of another architecture.
		{fixture: "store-wrong-arch", canned: map[string]string{"bootc status --json": hostStatusJSON(booted, "",
			deploymentJSON("ghcr.io/example/os:arm", digestList, "aarch64", BackendOstree))}},
		// The same, seen from an aarch64 machine: the x86_64 entries differ.
		{fixture: "store-wrong-arch-machine", arch: "aarch64", canned: map[string]string{"bootc status --json": hostStatusJSON(booted, "", "")}},
		// Missing and corrupt identities.
		{fixture: "store-missing-corrupt", files: map[string]string{bootcStoreIndexPath: bootcStoreIndexJSON(
			[2]string{"quay.io/example/a:1", "sha256:short"},
			[2]string{"quay.io/example/b:1", digestList},
			[2]string{"quay.io/example/b:1", digestOther},
		)}, canned: map[string]string{"bootc status --json": hostStatusJSON(
			deploymentJSON(os1, "", "x86_64", BackendOstree),
			deploymentJSON(os1, "sha256:"+strings.Repeat("Z", 64), "$(uname)", BackendOstree), "")}},
		// A composefs host: image list is refused (bootc mounts the ESP rw).
		{fixture: "store-composefs", canned: map[string]string{"bootc status --json": hostStatusJSON(
			deploymentJSON("ghcr.io/example/cfs:latest", digestHost, "x86_64", BackendComposefs), "", "")},
			list: true},
		// Asked for, but not root.
		{fixture: "store-needs-root", canned: map[string]string{"bootc status --json": installed}, list: true, euid: 1000},
		// Asked for on bootc live media.
		{fixture: "store-live-media", files: with(index, "proc/cmdline", liveCmdline),
			canned: map[string]string{"bootc status --json": hostStatusJSON(booted, "", "")}, list: true},
		// References with credentials or junk are never copied.
		{fixture: "store-credentials", files: map[string]string{bootcStoreIndexPath: bootcStoreIndexJSON(
			[2]string{"robot:" + secretSentinel + "@registry.example/os:1", digestList})},
			canned: map[string]string{
				"bootc status --json": hostStatusJSON(deploymentJSON("user:"+secretSentinel+"@ghcr.io/example/os:stable", digestHost, "x86_64", BackendOstree), "", "",
					deploymentJSON("ghcr.io/example/os:stable\n"+secretSentinel, digestOther, "x86_64", BackendOstree)),
				imageListCall: `[{"image_type":"host","image":"docker://u:` + secretSentinel + `@ghcr.io/x:1"},{"image_type":"host","image":"ghcr.io/x y;` + secretSentinel + `"}]`},
			list: true, wantList: true},
	}
}

func runStore(t *testing.T, c storeCase) (BootcStore, Capabilities, *fakeHost) {
	t.Helper()
	host := &fakeHost{canned: map[string]string{}, fail: c.fail}
	for k, v := range bootcCurrent {
		host.canned[k] = v
	}
	for k, v := range c.canned {
		host.canned[k] = v
	}
	p := newProber(t, c.files, host, nil)
	p.ListBootcImages = c.list
	euid := c.euid
	p.Euid = func() int { return euid }
	p.Arch = c.arch
	if p.Arch == "" {
		p.Arch = "x86_64"
	}
	live := p.Live()
	caps := p.Capabilities(live, p.Offline(), p.Bootc())
	return p.BootcStore(caps, live), caps, host
}

// TestBootcStoreGolden pins the bootc_store object for every fake-command
// case tuna-os/fisherman#384 lists for the inventory. Regenerate with `go
// test ./internal/probe -update`.
func TestBootcStoreGolden(t *testing.T) {
	for _, c := range storeCases(t) {
		t.Run(c.fixture, func(t *testing.T) {
			s, _, _ := runStore(t, c)
			got, err := json.MarshalIndent(s, "", "  ")
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
				t.Errorf("bootc_store differs from %s:\n%s", golden, got)
			}
		})
	}
}

// readOnlyCalls are the only commands a probe may run without
// --bootc-image-list: version, help and status calls, all read-only.
var readOnlyCalls = map[string]bool{
	"bootc --version": true, "bootc status --json": true,
	"bootc container inspect --help": true, "bootc install print-configuration --help": true,
	"bootc install finalize --help": true,
}

// Invariants of every case.
func TestBootcStoreInvariants(t *testing.T) {
	for _, c := range storeCases(t) {
		s, caps, host := runStore(t, c)
		b, _ := json.Marshal(s)
		if bytes.Contains(b, []byte(secretSentinel)) || bytes.Contains(b, []byte("<script>")) {
			t.Errorf("%s: command output or file content reached the JSON: %s", c.fixture, b)
		}
		if s.Version != BootcStoreVersion || s.Context != BootcContextLiveHost || s.Items == nil || s.Reason == "" {
			t.Errorf("%s: header %+v", c.fixture, s)
		}
		// The inventory exists exactly when the capability report found a
		// bootc-booted host.
		booted := caps.LiveHost.IdentityReason != IdentityNotBooted
		if s.Available != booted {
			t.Errorf("%s: available %v, capability report booted %v", c.fixture, s.Available, booted)
		}
		if !s.Available && (len(s.Items) != 0 || s.Sources.ImageList != SourceNotApplicable || s.Sources.StoreIndex != SourceNotApplicable) {
			t.Errorf("%s: unavailable inventory carries data: %+v", c.fixture, s)
		}
		ran := false
		for _, call := range host.calls {
			switch {
			case call == imageListCall:
				ran = true
			case !readOnlyCalls[call]:
				t.Errorf("%s: ran %q", c.fixture, call)
			}
		}
		if ran != c.wantList {
			t.Errorf("%s: bootc image list ran %v, want %v (image_list %q)", c.fixture, ran, c.wantList, s.Sources.ImageList)
		}
		if !c.list && s.Available && s.Sources.ImageList != SourceNotRequested {
			t.Errorf("%s: image_list %q without the opt-in", c.fixture, s.Sources.ImageList)
		}
		for _, it := range s.Items {
			if it.Owner != "bootc" || it.Context != BootcContextLiveHost || it.Storage == "" || len(it.Roles) == 0 || it.Availability == "" {
				t.Errorf("%s: item labels %+v", c.fixture, it)
			}
			if (it.Digest == "") == (it.DigestReason == "") || (it.Digest == "") != (it.DigestFrom == "") {
				t.Errorf("%s: digest without exactly one of value/reason: %+v", c.fixture, it)
			}
			if (it.Platform == "") == (it.PlatformReason == "") {
				t.Errorf("%s: platform without exactly one of value/reason: %+v", c.fixture, it)
			}
			if it.Digest != "" && !validDigest(it.Digest) {
				t.Errorf("%s: digest %q", c.fixture, it.Digest)
			}
			if it.MatchesMachine != (it.MismatchReason == "") {
				t.Errorf("%s: eligibility %+v", c.fixture, it)
			}
			if it.MatchesMachine && (it.Digest == "" || it.Platform != s.MachinePlatform || it.Ref == "") {
				t.Errorf("%s: matches_machine without identity: %+v", c.fixture, it)
			}
			// `bootc image list` never supplies an identity.
			if it.Availability == AvailListed && (it.Digest != "" || it.MatchesMachine) {
				t.Errorf("%s: a listed-only item has an identity: %+v", c.fixture, it)
			}
		}
	}
}

// The default probe of a bootc host writes nothing and runs nothing but the
// read-only calls: no `bootc image list`, no podman, skopeo or registry
// client. The fixture root is unchanged afterwards.
func TestBootcStoreDefaultProbeIsReadOnly(t *testing.T) {
	lsblk, err := os.ReadFile("testdata/lsblk/machine.json")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"proc/meminfo":                  meminfo16G,
		"sys/devices/system/cpu/online": "0-3\n",
		bootcStoreIndexPath:             bootcStoreIndexJSON([2]string{"ghcr.io/example/os:stable", digestHost}),
	}
	host := &fakeHost{canned: map[string]string{
		"lsblk -J -b -p -o " + lsblkColumns: string(lsblk),
		"bootc status --json":               hostStatusJSON(deploymentJSON("ghcr.io/example/os:stable", digestHost, "x86_64", BackendOstree), "", ""),
	}}
	for k, v := range bootcCurrent {
		host.canned[k] = v
	}
	p := &Prober{Root: fakeRoot(t, files), Run: host.run, Getenv: noEnv, Euid: func() int { return 0 }, Arch: "x86_64"}
	p.Selection = Selection{Source: "containers-storage:ghcr.io/example/os:stable"}
	before := snapshot(t, p.Root)
	res, err := p.Probe()
	if err != nil {
		t.Fatal(err)
	}
	if after := snapshot(t, p.Root); after != before {
		t.Errorf("probe changed its root:\nbefore %s\nafter  %s", before, after)
	}
	if !res.BootcStore.Available || res.BootcStore.Sources.ImageList != SourceNotRequested || len(res.BootcStore.Items) != 2 {
		t.Errorf("bootc_store = %+v", res.BootcStore)
	}
	for _, call := range host.calls {
		name, _, _ := strings.Cut(call, " ")
		if name != "lsblk" && !readOnlyCalls[call] && !strings.HasPrefix(call, "bootc container inspect --json") &&
			call != "bootc install print-configuration" {
			t.Errorf("default probe ran %q", call)
		}
		for _, net := range []string{"podman", "skopeo", "curl", "pull", "copy-to-storage", "set-unified", "image list"} {
			if strings.Contains(call, net) {
				t.Errorf("default probe ran %q", call)
			}
		}
	}
}

func TestInventoryRef(t *testing.T) {
	for in, want := range map[string][2]string{
		"ghcr.io/example/os:stable":                                  {"ghcr.io/example/os:stable", ""},
		"ostree-unverified-registry:ghcr.io/example/os:stable":       {"ghcr.io/example/os:stable", ""},
		"ostree-unverified-image:docker://ghcr.io/example/os:stable": {"ghcr.io/example/os:stable", ""},
		"ostree-remote-image:fedora:registry:quay.io/fedora/os:42":   {"quay.io/fedora/os:42", ""},
		"containers-storage:localhost/os":                            {"localhost/os", ""},
		"ghcr.io/example/os@" + digestHost:                           {"ghcr.io/example/os@" + digestHost, ""},
		"user:token@ghcr.io/example/os":                              {"", RefCredentialsRedacted},
		"docker://user:token@ghcr.io/example/os":                     {"", RefCredentialsRedacted},
		"ghcr.io/x y":                                                {"", RefInvalid},
		"":                                                           {"", RefInvalid},
	} {
		ref, reason := inventoryRef(in)
		if ref != want[0] || reason != want[1] {
			t.Errorf("inventoryRef(%q) = %q, %q; want %q, %q", in, ref, reason, want[0], want[1])
		}
	}
}

// Every Probe() result carries the object, also on a host without bootc.
func TestProbeHasBootcStore(t *testing.T) {
	res, err := goldenProber(t).Probe()
	if err != nil {
		t.Fatal(err)
	}
	if res.BootcStore.Version != BootcStoreVersion || res.BootcStore.Items == nil {
		t.Errorf("bootc_store = %+v", res.BootcStore)
	}
}
