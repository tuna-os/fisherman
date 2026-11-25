package install

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubSourceInspect answers both the install's image check (SkopeoInspectFn)
// and the plan's (PlanInspectFn) from one table of `skopeo inspect`
// arguments, so the two read the same store and registry. Anything not in
// the table fails as an unreachable registry would; podman is never
// answered.
func stubSourceInspect(t *testing.T, answers map[string]string) {
	t.Helper()
	oldSkopeo, oldPlan, oldArch, oldWaits := SkopeoInspectFn, PlanInspectFn, HostArchitecture, InspectRetryWaits
	answer := func(args []string) ([]byte, error) {
		if out, ok := answers[strings.Join(args, " ")]; ok {
			return []byte(out), nil
		}
		return nil, errors.New("pinging container registry example.invalid: dial tcp: no such host")
	}
	SkopeoInspectFn = func(args ...string) ([]byte, error) { return answer(args) }
	PlanInspectFn = func(name string, args ...string) ([]byte, error) {
		if name != "skopeo" || len(args) == 0 || args[0] != "inspect" {
			return nil, errors.New("not answered")
		}
		return answer(args[1:])
	}
	HostArchitecture = func() string { return "amd64" }
	InspectRetryWaits = []time.Duration{0, 0}
	t.Cleanup(func() {
		SkopeoInspectFn, PlanInspectFn, HostArchitecture, InspectRetryWaits = oldSkopeo, oldPlan, oldArch, oldWaits
	})
}

// foreignOCILayout is a complete layout whose only manifest declares a
// platform that cannot be this host's.
func foreignOCILayout(t *testing.T) string {
	t.Helper()
	layout, digest := writeOCILayoutFixture(t)
	manifest, err := os.ReadFile(filepath.Join(layout, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:")))
	if err != nil {
		t.Fatal(err)
	}
	index, err := json.Marshal(map[string]any{"schemaVersion": 2, "manifests": []any{map[string]any{
		"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": digest, "size": len(manifest),
		"platform": map[string]string{"os": "other-os", "architecture": "other-arch"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout, "index.json"), index, 0o644); err != nil {
		t.Fatal(err)
	}
	return layout
}

// `plan --read-image` reports the source the install uses: the plan reads
// a local copy (containers-storage or oci) exactly when the install's
// image check uses one, reads the registry only when the install pulls,
// and names the same manifest digest. Both route the reference through
// routeImage, decide on a local copy with localCopy and read an OCI layout
// with readOCIManifest.
func TestPlanAndInstallAgreeOnImageSource(t *testing.T) {
	const ref = "example.invalid/os:1"
	image := func(digest string) string {
		return `{"Digest":"` + digest + `","Layers":["sha256:l"],"Labels":{"containers.bootc":"1"},"Os":"linux","Architecture":"amd64"}`
	}
	local, remote := planTestDigest, planTestArm
	goodLayout, _ := writeOCILayoutFixture(t)
	for _, c := range []struct {
		name     string
		image    string
		answers  map[string]string
		readFrom string
	}{
		{"bare reference, local copy", ref, map[string]string{
			"containers-storage:" + ref: image(local), "--no-tags docker://" + ref: image(remote)}, "containers-storage"},
		{"transport-prefixed reference, local copy", "docker://" + ref, map[string]string{
			"containers-storage:" + ref: image(local)}, "containers-storage"},
		// The install used to deploy a local copy with no digest while
		// the plan said it would pull and named the registry's digest.
		{"bare reference, local copy without a digest", ref, map[string]string{
			"containers-storage:" + ref: image(""), "--no-tags docker://" + ref: image(remote)}, "registry"},
		{"bare reference, local copy with a malformed digest", ref, map[string]string{
			"containers-storage:" + ref: image("md5:abc"), "--no-tags docker://" + ref: image(remote)}, "registry"},
		{"bare reference, registry only", ref, map[string]string{
			"--no-tags docker://" + ref: image(remote)}, "registry"},
		{"explicit store copy", "containers-storage:" + ref, map[string]string{
			"containers-storage:" + ref: image(local), "--no-tags docker://" + ref: image(remote)}, "containers-storage"},
		{"explicit store copy without a digest", "containers-storage:" + ref, map[string]string{
			"containers-storage:" + ref: image(""), "--no-tags docker://" + ref: image(remote)}, ""},
		{"OCI layout", "oci:" + goodLayout, nil, "oci"},
		{"missing OCI layout", "oci:" + filepath.Join(goodLayout, "no-such-dir"), nil, ""},
		// The plan used to describe a layout whose only manifest is for
		// another platform; the install refuses it.
		{"OCI layout for another platform", "oci:" + foreignOCILayout(t), nil, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			stubSourceInspect(t, c.answers)
			check := CheckImage(c.image)
			facts := InspectImageForPlan(c.image, true)
			if facts.ReadFrom != c.readFrom {
				t.Fatalf("plan reads from %q (%s), want %q", facts.ReadFrom, facts.Reason, c.readFrom)
			}
			usesLocal := facts.ReadFrom == "containers-storage" || facts.ReadFrom == "oci"
			if check.Source.Cached != usesLocal || check.NeedsPull == usesLocal {
				t.Errorf("plan reads from %q, install check %+v", facts.ReadFrom, check)
			}
			if (facts.ReadFrom == "containers-storage" || facts.ReadFrom == "registry") && check.Source.ManifestDigest != facts.ManifestDigest {
				t.Errorf("plan names %s, install records %s", facts.ManifestDigest, check.Source.ManifestDigest)
			}
		})
	}
}
