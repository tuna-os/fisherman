package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBootcInstall_RunInSourceContainer: the source reuse copy (#384) is
// installed by bootc from the copy's own container, even though the source
// is in local containers-storage and the host has bootc. The host's bootc
// fails there: with --source-imgref on a booted ostree host it runs
// `ostree --repo=/ostree/repo rev-parse --single`, and that repo holds every
// deployment ("Multiple commit objects found", VM profile run 2 of #443).
func TestBootcInstall_RunInSourceContainer(t *testing.T) {
	dir, err := filepath.Abs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	podmanLog := filepath.Join(dir, "podman.argv")
	bootcLog := filepath.Join(dir, "bootc.argv")
	for name, log := range map[string]string{"podman": podmanLog, "bootc": bootcLog} {
		script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + log + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	oldHas, oldSpace := hostHasBootcFn, storageSpaceConstrainedFn
	t.Cleanup(func() { hostHasBootcFn, storageSpaceConstrainedFn = oldHas, oldSpace })
	hostHasBootcFn = func() bool { return true }
	storageSpaceConstrainedFn = func() bool { return false }

	const copyRef = "localhost/fisherman-reuse:111111111111-0123456789abcdef"
	const update = "ghcr.io/example/os:stable"
	opts := Options{SourceImgref: "containers-storage:" + copyRef, TargetImgref: update,
		Target: filepath.Join(dir, "target"), ScratchDir: filepath.Join(dir, "scratch")}

	// Without the flag the host's bootc would run (the existing local path).
	if !useDirectForLocalSource(opts) {
		t.Fatal("precondition: a local source on a bootc host takes the direct path")
	}

	opts.RunInSourceContainer = true
	if err := BootcInstall(opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bootcLog); err == nil {
		raw, _ := os.ReadFile(bootcLog)
		t.Fatalf("the host's bootc ran: %s", raw)
	}
	raw, err := os.ReadFile(podmanLog)
	if err != nil {
		t.Fatal("podman did not run:", err)
	}
	args := "\n" + string(raw)
	for _, want := range []string{
		"\nrun\n", "\n--privileged\n", "\n--pid=host\n",
		"\n/var/lib/containers:/var/lib/containers\n",
		"\n" + copyRef + "\nbootc\ninstall\nto-filesystem\n",
		"\n--target-imgref\n" + update + "\n",
		"\n--skip-finalize\n/target\n",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("podman argv lacks %q:\n%s", strings.TrimSpace(want), raw)
		}
	}
	for _, bad := range []string{"\n--source-imgref\n", "\npull\n", "\n--target-imgref\n" + copyRef, "docker://"} {
		if strings.Contains(args, bad) {
			t.Errorf("podman argv has %q:\n%s", strings.TrimSpace(bad), raw)
		}
	}
}
