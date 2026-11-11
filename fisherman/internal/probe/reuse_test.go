package probe

import (
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

var (
	reuseChecksumA = strings.Repeat("a", 64)
	reuseChecksumB = strings.Repeat("b", 64)
)

// reuseDeployment is a BootEntry with an ostree deployment (checksum,
// serial, stateroot) as bootc 1.17.1 prints it.
func reuseDeployment(image, transport, digest, arch, checksum string, serial int) string {
	return fmt.Sprintf(`{"image":{"image":{"image":%q,"transport":%q},"imageDigest":%q,"architecture":%q,"version":"10"},
"cachedUpdate":null,"incompatible":false,"pinned":false,"store":"ostreeContainer",
"ostree":{"checksum":%q,"deploySerial":%d,"stateroot":"default"}}`, image, transport, digest, arch, checksum, serial)
}

type reuseCase struct {
	name     string
	files    map[string]string
	status   string
	fail     error
	digest   string
	platform string
	want     ReuseSource
}

func reuseCases(t *testing.T) []reuseCase {
	os1 := "ghcr.io/example/os:stable"
	booted := reuseDeployment(os1, "registry", digestHost, "x86_64", reuseChecksumA, 0)
	src := "docker://" + os1
	cand := ReuseCandidate{Role: RoleBooted, Ref: os1, Transport: "registry", SourceRef: src,
		OstreeRef: OstreeImageRef(src), Checksum: reuseChecksumA, Stateroot: "default", Serial: 0,
		Digest: digestHost, Platform: "x86_64"}
	staged := cand
	staged.Role, staged.Checksum, staged.Serial = RoleStaged, reuseChecksumB, 1
	none := []ReuseCandidate{}
	index := map[string]string{bootcStoreIndexPath: bootcStoreIndexJSON([2]string{"quay.io/example/app:1", digestList})}
	return []reuseCase{
		{name: "bootc host", status: hostStatusJSON(booted, "", ""),
			want: ReuseSource{Eligible: true, Reason: ReuseEligible, Candidates: []ReuseCandidate{cand}}},
		{name: "staged and booted share the digest", status: hostStatusJSON(booted,
			reuseDeployment(os1, "registry", digestHost, "x86_64", reuseChecksumB, 1), ""),
			want: ReuseSource{Eligible: true, Reason: ReuseEligible, Candidates: []ReuseCandidate{cand, staged}}},
		{name: "rollback of another image", status: hostStatusJSON(booted, "",
			reuseDeployment("ghcr.io/example/os:old", "registry", digestOther, "x86_64", reuseChecksumB, 0)),
			digest: digestOther, want: ReuseSource{Eligible: true, Reason: ReuseEligible, Candidates: []ReuseCandidate{{
				Role: RoleRollback, Ref: "ghcr.io/example/os:old", Transport: "registry",
				SourceRef: "docker://ghcr.io/example/os:old", OstreeRef: OstreeImageRef("docker://ghcr.io/example/os:old"),
				Checksum: reuseChecksumB, Stateroot: "default", Digest: digestOther, Platform: "x86_64"}}}},
		{name: "non-bootc live media", files: map[string]string{"proc/cmdline": liveCmdline},
			status: `{"apiVersion":"org.containers.bootc/v1","status":{"booted":null,"staged":null,"rollback":null}}`,
			want:   ReuseSource{Reason: ReuseHostNotBootc, HostReason: StoreNotBootcBooted, Candidates: none}},
		{name: "container (bootc ran, booted nothing)", status: `{"status":{"booted":null}}`,
			want: ReuseSource{Reason: ReuseHostNotBootc, HostReason: StoreNotBootcBooted, Candidates: none}},
		{name: "no bootc", fail: &exec.Error{Name: "bootc", Err: exec.ErrNotFound},
			want: ReuseSource{Reason: ReuseHostNotBootc, HostReason: ReasonBootcMissing, Candidates: none}},
		{name: "older CLI (status fails)", fail: exitStatus(t, 2),
			want: ReuseSource{Reason: ReuseHostNotBootc, HostReason: ReasonCheckFailed, Candidates: none}},
		{name: "malformed JSON", status: "\x1b[31m" + secretSentinel,
			want: ReuseSource{Reason: ReuseHostNotBootc, HostReason: ReasonMalformedOutput, Candidates: none}},
		{name: "bootc live media", files: map[string]string{"proc/cmdline": liveCmdline}, status: hostStatusJSON(booted, "", ""),
			want: ReuseSource{Reason: ReuseLiveMedia, Candidates: none}},
		{name: "composefs host", status: hostStatusJSON(deploymentJSON(os1, digestHost, "x86_64", BackendComposefs), "", ""),
			want: ReuseSource{Reason: ReuseBackendNotQualified, Candidates: none}},
		// A logically bound image is in bootc's containers-storage only:
		// copy-to-storage exports from ostree, so it is not a source.
		{name: "container logical-only item", files: index, status: hostStatusJSON(booted, "", ""), digest: digestList,
			want: ReuseSource{Reason: ReuseItemNotFound, Candidates: none}},
		{name: "missing image", status: hostStatusJSON(booted, "", ""), digest: "sha256:" + strings.Repeat("9", 64),
			want: ReuseSource{Reason: ReuseItemNotFound, Candidates: none}},
		{name: "corrupt digest in status", status: hostStatusJSON(reuseDeployment(os1, "registry", "sha256:XYZ", "x86_64", reuseChecksumA, 0), "", ""),
			digest: "sha256:XYZ", want: ReuseSource{Reason: ReuseItemNotFound, Candidates: none}},
		{name: "wrong architecture", status: hostStatusJSON(booted, "",
			reuseDeployment("ghcr.io/example/os:arm", "registry", digestOther, "aarch64", reuseChecksumB, 0)),
			digest: digestOther, platform: "aarch64", want: ReuseSource{Reason: ReusePlatformMismatch, Candidates: none}},
		{name: "recipe platform differs", status: hostStatusJSON(booted, "", ""), platform: "aarch64",
			want: ReuseSource{Reason: ReusePlatformMismatch, Candidates: none}},
		{name: "platform not reported", status: hostStatusJSON(reuseDeployment(os1, "registry", digestHost, "", reuseChecksumA, 0), "", ""),
			want: ReuseSource{Reason: ReuseItemUnverified, Candidates: none}},
		{name: "reference with credentials", status: hostStatusJSON(
			reuseDeployment("user:"+secretSentinel+"@ghcr.io/example/os:stable", "registry", digestHost, "x86_64", reuseChecksumA, 0), "", ""),
			want: ReuseSource{Reason: ReuseItemUnverified, Candidates: none}},
		{name: "unknown transport", status: hostStatusJSON(reuseDeployment(os1, "dir", digestHost, "x86_64", reuseChecksumA, 0), "", ""),
			want: ReuseSource{Reason: ReuseItemUnverified, Candidates: none}},
		{name: "commit not reported", status: hostStatusJSON(reuseDeployment(os1, "registry", digestHost, "x86_64", "0a1b", 0), "", ""),
			want: ReuseSource{Reason: ReuseItemUnverified, Candidates: none}},
	}
}

// TestReuseSource covers each case tuna-os/fisherman#384 lists that the
// read-only half can answer, and that it runs `bootc status --json` and
// nothing else, and writes nothing.
func TestReuseSource(t *testing.T) {
	for _, c := range reuseCases(t) {
		t.Run(c.name, func(t *testing.T) {
			host := &fakeHost{canned: map[string]string{}, fail: map[string]error{}}
			if c.fail != nil {
				host.fail["bootc status --json"] = c.fail
			} else {
				host.canned["bootc status --json"] = c.status
			}
			p := newProber(t, c.files, host, nil)
			p.Arch = "x86_64"
			before := snapshot(t, p.Root)
			digest, platform := c.digest, c.platform
			if digest == "" {
				digest = digestHost
			}
			if platform == "" {
				platform = "x86_64"
			}
			got := p.ReuseSource(digest, platform)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got  %+v\nwant %+v", got, c.want)
			}
			for _, call := range host.calls {
				if call != "bootc status --json" {
					t.Errorf("ran %q; the read-only resolution runs only bootc status", call)
				}
			}
			if after := snapshot(t, p.Root); after != before {
				t.Errorf("the resolution changed the filesystem:\nbefore %v\nafter  %v", before, after)
			}
		})
	}
}

// TestOstreeImageRef pins ostree-ext's ref escaping (bootc v1.17.1
// crates/ostree-ext/src/refescape.rs and its tests).
func TestOstreeImageRef(t *testing.T) {
	cases := map[string]string{
		"registry:quay.io/coreos/fedora:latest":                       "registry_3A_quay_2E_io/coreos/fedora_3A_latest",
		"localhost:5000/foo:latest":                                   "localhost_3A_5000/foo_3A_latest",
		"foo/bar/baz-blah/foo":                                        "foo/bar/baz-blah/foo",
		"docker://quay.io/exampleos/blah:latest":                      "docker_3A__2F__2F_quay_2E_io/exampleos/blah_3A_latest",
		"docker://ghcr.io/tuna-os/fisherman/centos-bootc:ssh-enabled": "docker_3A__2F__2F_ghcr_2E_io/tuna-os/fisherman/centos-bootc_3A_ssh-enabled",
		"a_b":   "a__b",
		"blah/": "blah_2F_",
		"/":     "_2F_",
	}
	for in, want := range cases {
		if got := OstreeImageRef(in); got != "ostree/container/image/"+want {
			t.Errorf("OstreeImageRef(%q) = %q, want %q", in, got, "ostree/container/image/"+want)
		}
	}
}

func TestReuseCandidateDeployPath(t *testing.T) {
	c := ReuseCandidate{Stateroot: "default", Checksum: reuseChecksumA, Serial: 1}
	if got, want := c.DeployPath(), "/sysroot/ostree/deploy/default/deploy/"+reuseChecksumA+".1"; got != want {
		t.Errorf("DeployPath = %q, want %q", got, want)
	}
}
