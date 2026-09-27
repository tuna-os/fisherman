package install

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Actual installer argument/store mapping with controlled process boundaries;
// this does not prove an OS installation.
func TestVerifiedLocalSourceKeepsOriginalStoreAndTargetPin(t *testing.T) {
	checkVerifiedLocalRoute(t, true)
}
func TestVerifiedLocalSourceRuntimeIdWithoutRedirect(t *testing.T) {
	checkVerifiedLocalRoute(t, false)
}
func checkVerifiedLocalRoute(t *testing.T, redirect bool) {
	dir, err := os.MkdirTemp(".", ".offline-route-")
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	log := filepath.Join(dir, "podman.argv")
	if err = os.WriteFile(filepath.Join(dir, "podman"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$CAPTURE\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("CAPTURE", log)
	oldSpace, oldDriver, oldExport := storageSpaceConstrainedFn, selectStorageDriverFn, SkopeoExportOCIFn
	defer func() {
		storageSpaceConstrainedFn = oldSpace
		selectStorageDriverFn = oldDriver
		SkopeoExportOCIFn = oldExport
	}()
	storageSpaceConstrainedFn = func() bool { return redirect }
	selectStorageDriverFn = func(string) (string, string) { return "overlay", "controlled route" }
	const source = "containers-storage:09dae9ca006837e4962e8f9362c7985758bab0c1c329e3f484e1365babc1ee20"
	const target = "ghcr.io/tuna-os/yellowfin@sha256:3a086506b128b0afa5e8a672d554c587381a99b8ed1a27784eaa18945ccaba4b"
	exported := ""
	SkopeoExportOCIFn = func(image, dest, tmp string) error { exported = image; return nil }
	err = BootcInstall(Options{SourceImgref: source, TargetImgref: target, Target: filepath.Join(dir, "target"), ScratchDir: filepath.Join(dir, "scratch")})
	if err != nil {
		t.Fatal(err)
	}
	if redirect && exported != source {
		t.Fatalf("export source changed to unpopulated store: %q", exported)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal("actual owned command was not executed:", err)
	}
	args := string(raw)
	if strings.Contains(args, "\npull\n") {
		t.Fatal("local source triggered pull")
	}
	if !strings.Contains(args, "--target-imgref\n"+target+"\n") {
		t.Fatalf("original root pin lost: %s", args)
	}
	if redirect && !strings.Contains(args, "oci:"+filepath.Join(dir, "scratch", "oci-cache")+"\n") {
		t.Fatal("local export not used as container source")
	}
	if !redirect && (exported != "" || !strings.Contains(args, strings.TrimPrefix(source, "containers-storage:")+"\nbootc\n")) {
		t.Fatal("unredirected container did not consume bare immutable ID")
	}
}

func TestExplicitLocalCheckNeverQueriesRegistry(t *testing.T) {
	old := SkopeoInspectFn
	defer func() { SkopeoInspectFn = old }()
	const source = "containers-storage:09dae9ca006837e4962e8f9362c7985758bab0c1c329e3f484e1365babc1ee20"
	for _, mode := range []string{"valid", "missing", "malformed", "empty", "bad-digest"} {
		calls := 0
		SkopeoInspectFn = func(args ...string) ([]byte, error) {
			calls++
			if len(args) != 1 || args[0] != source {
				t.Fatalf("unexpected registry/source lookup: %v", args)
			}
			switch mode {
			case "missing":
				return nil, errors.New("controlled missing source")
			case "malformed":
				return []byte("{bad"), nil
			case "empty":
				return []byte("{}"), nil
			case "bad-digest":
				return []byte(`{"Digest":"sha256:zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"}`), nil
			}
			return []byte(`{"Digest":"sha256:2445c8493c47dd0b1406a30066a44ae2738e5b18b9d80bf59190232ae7c56e4d","Layers":["layer"]}`), nil
		}
		result := CheckImage(source)
		if calls != 1 || result.NeedsPull != (mode != "valid") {
			t.Fatalf("mode %s result %v calls %d", mode, result, calls)
		}
	}
}
func TestMissingExplicitLocalSourceCannotInstall(t *testing.T) {
	old := SkopeoExportOCIFn
	defer func() { SkopeoExportOCIFn = old }()
	SkopeoExportOCIFn = func(string, string, string) error { t.Fatal("missing source reached export"); return nil }
	if err := BootcInstall(Options{SourceImgref: "containers-storage:missing", NeedsPull: true}); err == nil {
		t.Fatal("missing source accepted")
	}
}
