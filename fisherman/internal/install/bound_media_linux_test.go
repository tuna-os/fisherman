//go:build linux

package install

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// Falsification: exact-sized corrupt bytes, symlinks and mismatched identities
// must fail. Temporary-directory tests replace ONLY the read-only mount check;
// they do not prove real ISO mounts or installation.
func boundFixture(t *testing.T) (*MediaInput, map[string]string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "blobs", "sha256"), 0700); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{}
	put := func(name string, raw []byte) ociDescriptor {
		d := ociDescriptor{Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), Size: int64(len(raw))}
		p := filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(d.Digest, "sha256:"))
		if err := os.WriteFile(p, raw, 0600); err != nil {
			t.Fatal(err)
		}
		paths[name] = p
		return d
	}
	configRaw, err := json.Marshal(map[string]string{"os": runtime.GOOS, "architecture": runtime.GOARCH})
	if err != nil {
		t.Fatal(err)
	}
	config := put("config", configRaw)
	layer := put("layer", []byte("fixture layer"))
	manifestRaw, err := json.Marshal(ociManifest{SchemaVersion: 2, Config: config, Layers: []ociDescriptor{layer}})
	if err != nil {
		t.Fatal(err)
	}
	manifest := put("manifest", manifestRaw)
	indexRaw, err := json.Marshal(ociIndex{SchemaVersion: 2, Manifests: []ociDescriptor{manifest}})
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{
		"oci-layout": []byte("{\"imageLayoutVersion\":\"1.0.0\"}"),
		"index.json": indexRaw,
	} {
		p := filepath.Join(root, name)
		if err := os.WriteFile(p, raw, 0600); err != nil {
			t.Fatal(err)
		}
		paths[name] = p
	}
	return &MediaInput{
		SchemaVersion: 1, Image: "registry.example/custom:source",
		TargetImgref:           "registry.example/custom:stable",
		PlatformManifestDigest: manifest.Digest, ConfigDigest: config.Digest,
		Platform: runtime.GOOS + "/" + runtime.GOARCH, OCILayoutPath: root,
	}, paths
}

func writableBoundFixture(t *testing.T) {
	t.Helper()
	old := boundReadOnlyCheck
	boundReadOnlyCheck = func(int) error { return nil }
	t.Cleanup(func() { boundReadOnlyCheck = old })
}

func TestBoundMediaActualHashesAndLegacyCompatibility(t *testing.T) {
	writableBoundFixture(t)
	m, _ := boundFixture(t)
	if err := PreflightBoundOCIMediaInput(m); err != nil {
		t.Fatal(err)
	}
	if err := PreflightOCILayout(m.OCILayoutPath, m.ExpectedDigest()); err != nil {
		t.Fatalf("legacy standalone preflight changed: %v", err)
	}
}

func TestBoundMediaSameSizeCorruption(t *testing.T) {
	writableBoundFixture(t)
	for _, part := range []string{"manifest", "config", "layer"} {
		t.Run(part, func(t *testing.T) {
			m, paths := boundFixture(t)
			raw, err := os.ReadFile(paths[part])
			if err != nil {
				t.Fatal(err)
			}
			raw[len(raw)-1] ^= 1
			if err := os.WriteFile(paths[part], raw, 0600); err != nil {
				t.Fatal(err)
			}
			err = PreflightBoundOCIMediaInput(m)
			if err == nil || !strings.Contains(err.Error(), "hash mismatch") {
				t.Fatalf("same-size %s accepted or wrong refusal: %v", part, err)
			}
		})
	}
}

func TestBoundMediaRejectsSymlinksAtEveryBoundary(t *testing.T) {
	writableBoundFixture(t)
	for _, part := range []string{"root", "blobs", "manifest", "config", "layer", "index.json", "oci-layout"} {
		t.Run(part, func(t *testing.T) {
			m, paths := boundFixture(t)
			if part == "root" {
				link := filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(m.OCILayoutPath, link); err != nil {
					t.Fatal(err)
				}
				m.OCILayoutPath = link
			} else {
				p := paths[part]
				if part == "blobs" {
					p = filepath.Join(m.OCILayoutPath, "blobs")
				}
				target := p + "-held"
				if err := os.Rename(p, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, p); err != nil {
					t.Fatal(err)
				}
			}
			if err := PreflightBoundOCIMediaInput(m); err == nil {
				t.Fatalf("symlink %s accepted", part)
			}
		})
	}
}

func TestBoundMediaRejectsIdentityAndPathMismatch(t *testing.T) {
	writableBoundFixture(t)
	for _, change := range []string{"expected", "config", "platform", "platform variant", "unclean", "relative", "empty digest"} {
		t.Run(change, func(t *testing.T) {
			m, _ := boundFixture(t)
			switch change {
			case "expected":
				m.PlatformManifestDigest = "sha256:" + strings.Repeat("b", 64)
			case "config":
				m.ConfigDigest = "sha256:" + strings.Repeat("c", 64)
			case "platform":
				m.Platform = "other/architecture"
			case "platform variant":
				m.Platform = runtime.GOOS + "/" + runtime.GOARCH + "/v2"
			case "unclean":
				m.OCILayoutPath += "/."
			case "relative":
				m.OCILayoutPath = "relative/layout"
			case "empty digest":
				m.PlatformManifestDigest = ""
			}
			if err := PreflightBoundOCIMediaInput(m); err == nil {
				t.Fatal("mismatch accepted")
			}
		})
	}
}

func TestBoundMediaActuallyRefusesWritableFilesystem(t *testing.T) {
	m, _ := boundFixture(t)
	if err := PreflightBoundOCIMediaInput(m); err == nil ||
		!strings.Contains(err.Error(), "not on a read-only filesystem") {
		t.Fatalf("real kernel read-only check did not reject writable fixture: %v", err)
	}
}

// Simulate a writable nested mount at each final file via the statfs seam.
// This proves per-open checking; it does not create or validate a real mount.
func TestBoundMediaRejectsWritableNestedFiles(t *testing.T) {
	old := boundReadOnlyCheck
	t.Cleanup(func() { boundReadOnlyCheck = old })
	for _, part := range []string{"oci-layout", "index.json", "manifest", "config", "layer"} {
		t.Run(part, func(t *testing.T) {
			m, paths := boundFixture(t)
			var wanted syscall.Stat_t
			if err := syscall.Stat(paths[part], &wanted); err != nil {
				t.Fatal(err)
			}
			checked := false
			boundReadOnlyCheck = func(fd int) error {
				var actual syscall.Stat_t
				if err := syscall.Fstat(fd, &actual); err != nil {
					return err
				}
				if actual.Dev == wanted.Dev && actual.Ino == wanted.Ino {
					checked = true
					return errors.New("fixture writable nested file refused")
				}
				return nil
			}
			err := PreflightBoundOCIMediaInput(m)
			if !checked || err == nil || !strings.Contains(err.Error(), "writable nested file refused") {
				t.Fatalf("nested %s bypassed per-open check: checked=%v err=%v", part, checked, err)
			}
		})
	}
}

func TestBoundMediaReadOnlyAndReadBounds(t *testing.T) {
	m, paths := boundFixture(t)
	old := boundReadOnlyCheck
	boundReadOnlyCheck = func(int) error { return errors.New("fixture writable mount refused") }
	t.Cleanup(func() { boundReadOnlyCheck = old })
	if err := PreflightBoundOCIMediaInput(m); err == nil || !strings.Contains(err.Error(), "writable mount refused") {
		t.Fatalf("mount check bypassed: %v", err)
	}
	boundReadOnlyCheck = func(int) error { return nil }
	f, err := os.OpenFile(paths["index.json"], os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(boundMetadataLimit + 1); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := PreflightBoundOCIMediaInput(m); err == nil || !strings.Contains(err.Error(), "oversized") {
		t.Fatalf("metadata bound bypassed: %v", err)
	}
	root, err := os.Open(m.OCILayoutPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, d := range []ociDescriptor{
		{Digest: "sha256:" + strings.Repeat("a", 64), Size: boundBlobLimit + 1},
		{Digest: "../escape:anything", Size: 1},
	} {
		if _, err := readBoundBlob(root, d, false); err == nil {
			t.Fatal("unsafe descriptor accepted")
		}
	}
}

// Actual BootcInstall code, with owned fake executables and all external command
// seams intercepted. This proves argv preservation, not bootc installation.
func TestBoundBrowserSourceAndTargetFakeTransport(t *testing.T) {
	oldCommand, oldExport := CommandFn, SkopeoExportOCIFn
	oldDriver, oldSpace := selectStorageDriverFn, storageSpaceConstrainedFn
	t.Cleanup(func() {
		CommandFn, SkopeoExportOCIFn = oldCommand, oldExport
		selectStorageDriverFn, storageSpaceConstrainedFn = oldDriver, oldSpace
	})
	selectStorageDriverFn = func(string) (string, string) { return "overlay", "fixture" }
	storageSpaceConstrainedFn = func() bool { return true }
	SkopeoExportOCIFn = func(string, string, string) error { t.Fatal("OCI source reached export"); return nil }
	for _, target := range []string{
		"registry.example/custom:stable",
		"registry.example/custom@sha256:" + strings.Repeat("b", 64),
	} {
		t.Run(target, func(t *testing.T) {
			dir := t.TempDir()
			capture := filepath.Join(dir, "argv")
			script := filepath.Join(dir, "podman")
			body := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$CAPTURE\"\nexit 0\n"
			if err := os.WriteFile(script, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
			t.Setenv("CAPTURE", capture)
			CommandFn = func(name string, args ...string) *exec.Cmd {
				return exec.Command(script, append([]string{name}, args...)...)
			}
			m, _ := boundFixture(t)
			err := BootcInstall(Options{
				SourceImgref: "oci:" + m.OCILayoutPath, TargetImgref: target,
				Target: filepath.Join(dir, "target"), ScratchDir: filepath.Join(dir, "scratch"),
			})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			args := string(raw)
			if strings.Contains(args, "\npull\n") || strings.Contains(args, "\ndocker://") {
				t.Fatalf("unexpected registry fallback: %s", args)
			}
			for _, want := range []string{
				"--source-imgref\noci:" + m.OCILayoutPath + "\n",
				"--target-imgref\n" + target + "\n",
				"oci:" + m.OCILayoutPath + "\nbootc\n",
			} {
				if !strings.Contains(args, want) {
					t.Fatalf("missing %q in actual argv: %s", want, args)
				}
			}
		})
	}
}

// Rewrite the selected manifest while keeping its exact new digest in index and
// media input. Oversized descriptors stay tiny fixtures; no huge files are made.
func rewriteBoundManifest(t *testing.T, m *MediaInput, paths map[string]string, change func(*ociManifest)) {
	t.Helper()
	raw, err := os.ReadFile(paths["manifest"])
	if err != nil {
		t.Fatal(err)
	}
	var manifest ociManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	change(&manifest)
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	d := ociDescriptor{Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), Size: int64(len(raw))}
	path := filepath.Join(m.OCILayoutPath, "blobs", "sha256", strings.TrimPrefix(d.Digest, "sha256:"))
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	paths["manifest"] = path
	m.PlatformManifestDigest = d.Digest
	raw, err = json.Marshal(ociIndex{SchemaVersion: 2, Manifests: []ociDescriptor{d}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths["index.json"], raw, 0600); err != nil {
		t.Fatal(err)
	}
}

// Falsification: declared work must be bounded before config/layer opens;
// repeated digests cannot multiply hashing or disagree about the same bytes.
func TestBoundMediaLayerAndAggregateBudgets(t *testing.T) {
	writableBoundFixture(t)
	for _, tc := range []struct {
		name, want string
		change     func(*ociManifest)
	}{
		{"too many layers", "layer count exceeds policy", func(m *ociManifest) {
			layer := m.Layers[0]
			m.Layers = make([]ociDescriptor, boundLayerLimit+1)
			for i := range m.Layers {
				m.Layers[i] = layer
			}
		}},
		{"empty layers", "layer count exceeds policy", func(m *ociManifest) { m.Layers = nil }},
		{"aggregate", "unique blob bytes exceed policy", func(m *ociManifest) {
			m.Layers = []ociDescriptor{
				{Digest: "sha256:" + strings.Repeat("a", 64), Size: 6 << 30},
				{Digest: "sha256:" + strings.Repeat("b", 64), Size: 5 << 30},
			}
		}},
		{"conflicting layer duplicate", "inconsistent repeated bound blob size", func(m *ociManifest) {
			d := m.Layers[0]
			m.Layers = append(m.Layers, d)
			m.Layers[1].Size++
		}},
		{"conflicting config duplicate", "inconsistent repeated bound blob size", func(m *ociManifest) {
			d := m.Config
			d.Size++
			m.Layers = []ociDescriptor{d}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, paths := boundFixture(t)
			rewriteBoundManifest(t, m, paths, tc.change)
			// A budget error must win even if later files are unavailable.
			if err := os.Remove(paths["config"]); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(paths["layer"]); err != nil {
				t.Fatal(err)
			}
			err := PreflightBoundOCIMediaInput(m)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("work budget not rejected before blob I/O: %v", err)
			}
		})
	}
}

func TestBoundMediaRepeatedDigestHashedOnce(t *testing.T) {
	m, paths := boundFixture(t)
	rewriteBoundManifest(t, m, paths, func(manifest *ociManifest) {
		layer := manifest.Layers[0]
		manifest.Layers = make([]ociDescriptor, boundLayerLimit)
		for i := range manifest.Layers {
			manifest.Layers[i] = layer
		}
		// Reuse a digest across metadata and layer descriptors as well.
		manifest.Layers[0] = manifest.Config
	})
	want := make(map[uint64]string)
	for _, part := range []string{"manifest", "config", "layer"} {
		var st syscall.Stat_t
		if err := syscall.Stat(paths[part], &st); err != nil {
			t.Fatal(err)
		}
		want[st.Ino] = part
	}
	calls := make(map[string]int)
	old := boundReadOnlyCheck
	t.Cleanup(func() { boundReadOnlyCheck = old })
	boundReadOnlyCheck = func(fd int) error {
		var st syscall.Stat_t
		if err := syscall.Fstat(fd, &st); err != nil {
			return err
		}
		if part := want[st.Ino]; part != "" {
			calls[part]++
		}
		return nil
	}
	if err := PreflightBoundOCIMediaInput(m); err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"manifest", "config", "layer"} {
		if calls[part] != 1 {
			t.Fatalf("%s opened %d times; want one hash pass", part, calls[part])
		}
	}
}

func TestBoundMediaUniqueByteBudgetBoundary(t *testing.T) {
	r := boundBlobReader{sizes: make(map[string]int64)}
	first := ociDescriptor{Digest: "sha256:" + strings.Repeat("a", 64), Size: boundUniqueBlobLimit}
	if err := r.reserve(first, false); err != nil {
		t.Fatal(err)
	}
	if err := r.reserve(first, false); err != nil {
		t.Fatal("duplicate charged again:", err)
	}
	if r.total != boundUniqueBlobLimit {
		t.Fatalf("wrong total: %d", r.total)
	}
	next := ociDescriptor{Digest: "sha256:" + strings.Repeat("b", 64), Size: 1}
	if err := r.reserve(next, false); err == nil {
		t.Fatal("one byte beyond aggregate accepted")
	}
	if r.total != boundUniqueBlobLimit || len(r.sizes) != 1 {
		t.Fatal("rejected reservation mutated budget")
	}
	first.Size--
	if err := r.reserve(first, false); err == nil {
		t.Fatal("inconsistent duplicate size accepted")
	}
}
