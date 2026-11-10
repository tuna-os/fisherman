package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/post"
	"github.com/tuna-os/fisherman/internal/recipe"
	"github.com/tuna-os/fisherman/internal/runner"
)

// fakeBlkid answers `blkid -s UUID|TYPE -o value <dev>` from a table of
// "device" -> {uuid, type}. A device that is not in it has no filesystem.
func fakeBlkid(t *testing.T, devs map[string][2]string) {
	t.Helper()
	orig := runner.OutputFn
	t.Cleanup(func() { runner.OutputFn = orig })
	runner.OutputFn = func(name string, args ...string) ([]byte, error) {
		if name != "blkid" {
			return nil, errors.New("fakeBlkid: unexpected query " + name)
		}
		d, ok := devs[args[len(args)-1]]
		if !ok {
			return nil, errors.New("exit status 2")
		}
		for i, a := range args {
			if a == "-s" && args[i+1] == "UUID" {
				return []byte(d[0] + "\n"), nil
			}
			if a == "-s" && args[i+1] == "TYPE" {
				return []byte(d[1] + "\n"), nil
			}
		}
		return nil, errors.New("fakeBlkid: unexpected arguments")
	}
}

// ostreeTarget builds the tree bootc leaves for an ostree install: one
// deployment whose /home is a symlink to var/home, like a bootc image.
func ostreeTarget(t *testing.T) (target string, dep deployment) {
	t.Helper()
	target = t.TempDir()
	deployDir := filepath.Join(target, "ostree", "deploy", "default", "deploy", "abc123.0")
	for _, d := range []string{"etc", "var", "srv", "usr/lib/ostree"} {
		if err := os.MkdirAll(filepath.Join(deployDir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("var/home", filepath.Join(deployDir, "home")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/var/opt", filepath.Join(deployDir, "opt")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(target, "ostree", "deploy", "default", "var"), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := post.DeploymentDirFn
	post.DeploymentDirFn = func(string) (string, error) { return deployDir, nil }
	t.Cleanup(func() { post.DeploymentDirFn = orig })
	dep, err := resolveDeployment(target)
	if err != nil {
		t.Fatal(err)
	}
	return target, dep
}

func TestResolveDeployment_Ostree(t *testing.T) {
	target, dep := ostreeTarget(t)
	want := deployment{
		EtcDir: filepath.Join(target, "ostree/deploy/default/deploy/abc123.0/etc"),
		VarDir: filepath.Join(target, "ostree/deploy/default/var"),
		Root:   filepath.Join(target, "ostree/deploy/default/deploy/abc123.0"),
	}
	if dep != want {
		t.Errorf("resolveDeployment = %+v, want %+v", dep, want)
	}
}

func TestResolveInRoot(t *testing.T) {
	_, dep := ostreeTarget(t)
	for path, want := range map[string]string{
		"/home":           "/var/home", // relative link
		"/home/alice":     "/var/home/alice",
		"/opt":            "/var/opt", // absolute link stays inside the root
		"/srv":            "/srv",     // a real directory
		"/var/lib/data":   "/var/lib/data",
		"/does/not/exist": "/does/not/exist",
	} {
		got, err := resolveInRoot(dep.Root, path)
		if err != nil || got != want {
			t.Errorf("resolveInRoot(%q) = %q, %v; want %q", path, got, err, want)
		}
	}
	loop := filepath.Join(dep.Root, "loop")
	if err := os.Symlink("loop", loop); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveInRoot(dep.Root, "/loop"); err == nil {
		t.Error("a symlink loop must be an error")
	}
}

// The varDisk fstab line: type from blkid (a kept disk is not always xfs),
// nofail, a device timeout, and the local-fs ordering (#406).
func TestPlanPersistentMounts_VarDisk(t *testing.T) {
	_, dep := ostreeTarget(t)
	for _, fstype := range []string{"xfs", "ext4", "btrfs"} {
		fakeBlkid(t, map[string][2]string{"/dev/sdb": {"1111-aaaa", fstype}})
		r := &recipe.Recipe{VarDisk: &recipe.VarDiskSpec{Disk: "/dev/sdb", KeepExisting: fstype != "xfs"}}
		plan, err := planPersistentMounts(r, dep)
		if err != nil {
			t.Fatalf("%s: %v", fstype, err)
		}
		want := []persistentMount{{Field: "varDisk", Device: "/dev/sdb", Mountpoint: "/var", Fstype: fstype, UUID: "1111-aaaa",
			Options: "defaults,nofail,x-systemd.device-timeout=30s,x-systemd.before=local-fs.target"}}
		if !reflect.DeepEqual(plan, want) {
			t.Errorf("%s: plan = %+v, want %+v", fstype, plan, want)
		}
	}
}

// No UUID or no type: the mount cannot be persistent, so the install stops.
// Before, this was a warning and the system booted without its /var disk.
func TestPlanPersistentMounts_NoFilesystemIsAnError(t *testing.T) {
	_, dep := ostreeTarget(t)
	fakeBlkid(t, nil)
	_, err := planPersistentMounts(&recipe.Recipe{VarDisk: &recipe.VarDiskSpec{Disk: "/dev/sdb"}}, dep)
	if err == nil || !strings.Contains(err.Error(), "/dev/sdb") {
		t.Fatalf("plan = %v, want an error that names the disk", err)
	}
}

func manualWith(extra ...recipe.CustomMount) *recipe.Recipe {
	return &recipe.Recipe{CustomMounts: append([]recipe.CustomMount{
		{Partition: "/dev/sda3", Target: "/", Fstype: "xfs"},
		{Partition: "/dev/sda2", Target: "/boot", Fstype: "ext4"},
		{Partition: "/dev/sda1", Target: "/boot/efi", Fstype: "unformatted"},
	}, extra...)}
}

// A manual layout: data mounts and swap get fstab lines with real types and
// resolved mountpoints; the root, /boot and the ESP get none (#372).
func TestPlanPersistentMounts_CustomMounts(t *testing.T) {
	_, dep := ostreeTarget(t)
	fakeBlkid(t, map[string][2]string{
		"/dev/sda1": {"E5P0", "vfat"}, "/dev/sda2": {"b007", "ext4"}, "/dev/sda3": {"r007", "xfs"},
		"/dev/sda4": {"h0me", "ext4"}, "/dev/sda5": {"5wap", "swap"}, "/dev/sda6": {"da7a", "vfat"},
		"/dev/sda7": {"5rv0", "btrfs"},
	})
	r := manualWith(
		recipe.CustomMount{Partition: "/dev/sda4", Target: "/home", Fstype: "ext4"},
		recipe.CustomMount{Partition: "/dev/sda5", Target: "swap", Fstype: "swap"},
		recipe.CustomMount{Partition: "/dev/sda6", Target: "/var/shared/", Fstype: "unformatted"},
		recipe.CustomMount{Partition: "/dev/sda7", Target: "/srv", Fstype: "btrfs"},
		recipe.CustomMount{Partition: "/dev/sda8", Target: "", Fstype: "xfs"},
		recipe.CustomMount{Partition: "/dev/sda9", Target: "swap", Fstype: "unformatted"}, // never activated
	)
	plan, err := planPersistentMounts(r, dep)
	if err != nil {
		t.Fatal(err)
	}
	opts := "defaults,nofail,x-systemd.device-timeout=30s"
	want := []persistentMount{
		// /home is a symlink to var/home in this image: the fstab line
		// names the real directory, not the generic /home.
		{Field: "customMounts[3]", Device: "/dev/sda4", Mountpoint: "/var/home", Fstype: "ext4", UUID: "h0me", Options: opts},
		{Field: "customMounts[4]", Device: "/dev/sda5", Mountpoint: "none", Fstype: "swap", UUID: "5wap", Options: "defaults,nofail"},
		// A kept partition keeps its real type.
		{Field: "customMounts[5]", Device: "/dev/sda6", Mountpoint: "/var/shared", Fstype: "vfat", UUID: "da7a", Options: opts},
		// /srv is a real directory of the image: it can be a mountpoint.
		{Field: "customMounts[6]", Device: "/dev/sda7", Mountpoint: "/srv", Fstype: "btrfs", UUID: "5rv0", Options: opts},
	}
	if !reflect.DeepEqual(plan, want) {
		t.Errorf("plan =\n%+v\nwant\n%+v", plan, want)
	}
}

// A target that is neither below /var nor a directory of the image cannot
// be a mountpoint on a read-only root.
func TestPlanPersistentMounts_MountpointMissingInImage(t *testing.T) {
	_, dep := ostreeTarget(t)
	fakeBlkid(t, map[string][2]string{"/dev/sda4": {"m3d1a", "ext4"}})
	_, err := planPersistentMounts(manualWith(recipe.CustomMount{Partition: "/dev/sda4", Target: "/media", Fstype: "ext4"}), dep)
	if err == nil || !strings.Contains(err.Error(), "customMounts[3]") || !strings.Contains(err.Error(), "/media") {
		t.Fatalf("plan = %v, want an error for customMounts[3] and /media", err)
	}
}

// composefs-native: no root filesystem to resolve in; a /var path is used
// as it is, under state/os/default/var.
func TestPlanPersistentMounts_ComposeFs(t *testing.T) {
	fakeBlkid(t, map[string][2]string{"/dev/sda4": {"h0me", "xfs"}})
	dep := deployment{EtcDir: "/t/state/deploy/abc/etc", VarDir: "/t/state/os/default/var"}
	plan, err := planPersistentMounts(manualWith(recipe.CustomMount{Partition: "/dev/sda4", Target: "/var/home", Fstype: "xfs"}), dep)
	if err != nil || len(plan) != 1 || plan[0].Mountpoint != "/var/home" {
		t.Fatalf("plan = %+v, %v; want one /var/home mount", plan, err)
	}
}

// An image that discards /etc at every boot cannot keep an fstab line.
func TestPlanPersistentMounts_TransientEtc(t *testing.T) {
	_, dep := ostreeTarget(t)
	fakeBlkid(t, map[string][2]string{"/dev/sdb": {"1111", "xfs"}})
	conf := filepath.Join(dep.Root, "usr/lib/ostree/prepare-root.conf")
	if err := os.WriteFile(conf, []byte("[composefs]\nenabled = yes\n\n[etc]\ntransient = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &recipe.Recipe{VarDisk: &recipe.VarDiskSpec{Disk: "/dev/sdb"}}
	if _, err := planPersistentMounts(r, dep); err == nil || !strings.Contains(err.Error(), "transient") {
		t.Fatalf("plan = %v, want the transient /etc error", err)
	}
	// [etc] transient = false, or the key under another section, is fine.
	if err := os.WriteFile(conf, []byte("[root]\ntransient = true\n[etc]\ntransient = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := planPersistentMounts(r, dep); err != nil {
		t.Fatalf("plan with a persistent /etc: %v", err)
	}
	// A recipe with no persistent mount is not affected by the image's /etc.
	if err := os.WriteFile(conf, []byte("[etc]\ntransient=true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if plan, err := planPersistentMounts(manualWith(), dep); err != nil || len(plan) != 0 {
		t.Fatalf("plan with no data mount = %+v, %v", plan, err)
	}
}

func readFstab(t *testing.T, dep deployment) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dep.EtcDir, "fstab"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

// The lines land in the deployment's fstab; a second run adds nothing; an
// entry the image ships for the same mountpoint is not overwritten.
func TestWritePersistentMounts(t *testing.T) {
	target, dep := ostreeTarget(t)
	plan := []persistentMount{
		{Field: "varDisk", Device: "/dev/sdb", Mountpoint: "/var", Fstype: "ext4", UUID: "1111", Options: varOptions},
		{Field: "customMounts[4]", Device: "/dev/sda5", Mountpoint: "none", Fstype: "swap", UUID: "5wap", Options: swapOptions},
	}
	if err := writePersistentMounts(target, dep, plan); err != nil {
		t.Fatal(err)
	}
	want := "UUID=1111\t/var\text4\tdefaults,nofail,x-systemd.device-timeout=30s,x-systemd.before=local-fs.target\t0 0\n" +
		"UUID=5wap\tnone\tswap\tdefaults,nofail\t0 0\n"
	if got := readFstab(t, dep); got != want {
		t.Errorf("fstab =\n%q\nwant\n%q", got, want)
	}
	if err := writePersistentMounts(target, dep, plan); err != nil {
		t.Fatal(err)
	}
	if got := readFstab(t, dep); got != want {
		t.Errorf("a second run changed fstab:\n%q", got)
	}

	other := []persistentMount{{Field: "customMounts[3]", Device: "/dev/sdc1", Mountpoint: "/var", Fstype: "xfs", UUID: "2222", Options: varOptions}}
	if err := writePersistentMounts(target, dep, other); err == nil || !strings.Contains(err.Error(), "already mounts") {
		t.Fatalf("writePersistentMounts = %v, want a conflict with the existing /var line", err)
	}
	if got := readFstab(t, dep); got != want {
		t.Errorf("a refused write changed fstab:\n%q", got)
	}
}

// Each /var device is mounted a second time under the deployment's var, a
// parent before its children, and each mount is handed to the teardown.
func TestMountDataInDeployment(t *testing.T) {
	_, dep := ostreeTarget(t)
	fake := &fakeHostRunner{}
	runner.RunFn = fake.run
	t.Cleanup(func() { runner.RunFn = runner.DefaultRun })

	plan := []persistentMount{
		{Field: "customMounts[3]", Device: "/dev/sda4", Mountpoint: "/var/home", Fstype: "btrfs"},
		{Field: "customMounts[4]", Device: "/dev/sda5", Mountpoint: "none", Fstype: "swap"},
		{Field: "customMounts[5]", Device: "/dev/sda6", Mountpoint: "/srv", Fstype: "ext4"},
		{Field: "varDisk", Device: "/dev/sdb", Mountpoint: "/var", Fstype: "xfs"},
	}
	var registered []string
	if err := mountDataInDeployment(dep, plan, func(p string) { registered = append(registered, p) }); err != nil {
		t.Fatal(err)
	}
	wantCalls := []string{
		"mount -t xfs /dev/sdb " + dep.VarDir,
		"mount -t btrfs -o compress=zstd:1 /dev/sda4 " + filepath.Join(dep.VarDir, "home"),
	}
	if !reflect.DeepEqual(fake.calls, wantCalls) {
		t.Errorf("calls = %q, want %q", fake.calls, wantCalls)
	}
	if want := []string{dep.VarDir, filepath.Join(dep.VarDir, "home")}; !reflect.DeepEqual(registered, want) {
		t.Errorf("registered for teardown = %q, want %q", registered, want)
	}
}

func TestKeptFilesystemProblems(t *testing.T) {
	fakeBlkid(t, map[string][2]string{
		"/dev/sda1": {"E5P0", "vfat"}, "/dev/sdb": {"1111", "crypto_LUKS"}, "/dev/sdc": {"2222", "ext4"},
		"/dev/sda5": {"3333", "vfat"},
	})
	codes := func(r *recipe.Recipe) []string {
		var out []string
		for _, p := range keptFilesystemProblems(r) {
			out = append(out, p.Field+":"+p.Code)
		}
		return out
	}
	want := []string{"varDisk.disk:" + recipe.CodeKeptFilesystemUnusable}

	// A kept var disk that is a LUKS volume: fisherman cannot mount it.
	if got := codes(&recipe.Recipe{VarDisk: &recipe.VarDiskSpec{Disk: "/dev/sdb", KeepExisting: true}}); !reflect.DeepEqual(got, want) {
		t.Errorf("kept LUKS var disk: %v, want %v", got, want)
	}
	// The same disk, formatted by the install: nothing to check.
	if got := codes(&recipe.Recipe{VarDisk: &recipe.VarDiskSpec{Disk: "/dev/sdb"}}); got != nil {
		t.Errorf("formatted var disk: %v, want none", got)
	}
	if got := codes(&recipe.Recipe{VarDisk: &recipe.VarDiskSpec{Disk: "/dev/sdc", KeepExisting: true}}); got != nil {
		t.Errorf("kept ext4 var disk: %v, want none", got)
	}

	r := manualWith(
		recipe.CustomMount{Partition: "/dev/sda4", Target: "/var/home", Fstype: "unformatted"}, // no filesystem
		recipe.CustomMount{Partition: "/dev/sda5", Target: "/var", Fstype: ""},                 // vfat cannot hold /var
		recipe.CustomMount{Partition: "/dev/sda6", Target: "", Fstype: "unformatted"},          // not mounted
	)
	want = []string{
		"customMounts[3].partition:" + recipe.CodeKeptFilesystemUnusable,
		"customMounts[4].partition:" + recipe.CodeKeptFilesystemUnusable,
	}
	if got := codes(r); !reflect.DeepEqual(got, want) {
		t.Errorf("manual layout: %v, want %v (the kept vfat ESP is fine)", got, want)
	}
}

func TestPersistMounts_NothingToDo(t *testing.T) {
	orig := post.DeploymentDirFn
	post.DeploymentDirFn = func(string) (string, error) { return "", errors.New("must not be asked") }
	t.Cleanup(func() { post.DeploymentDirFn = orig })
	if err := persistMounts(manualWith(), t.TempDir(), func(string) {}); err != nil {
		t.Fatalf("a layout with no data mount: %v", err)
	}
	if err := persistMounts(&recipe.Recipe{Disk: "/dev/sda"}, t.TempDir(), func(string) {}); err != nil {
		t.Fatalf("an automatic layout with no varDisk: %v", err)
	}
}

// lsLikeHost makes the fake runner answer `ls <path>` from the real
// filesystem, which is how post tells a composefs-native target from ostree.
func lsLikeHost(mountErr error) func(call string, n int) error {
	return func(call string, _ int) error {
		if path, ok := strings.CutPrefix(call, "ls "); ok {
			_, err := os.Stat(path)
			return err
		}
		if strings.HasPrefix(call, "mount ") {
			return mountErr
		}
		return nil
	}
}

// The whole step for the VM matrix shape (ostree, a formatted XFS var disk).
func TestPersistMounts_VarDisk(t *testing.T) {
	target, dep := ostreeTarget(t)
	fakeBlkid(t, map[string][2]string{"/dev/vdb": {"abcd-1234", "xfs"}})
	fake := &fakeHostRunner{on: lsLikeHost(nil)}
	runner.RunFn = fake.run
	t.Cleanup(func() { runner.RunFn = runner.DefaultRun })

	var registered []string
	r := &recipe.Recipe{Disk: "/dev/vda", VarDisk: &recipe.VarDiskSpec{Disk: "/dev/vdb"}}
	if err := persistMounts(r, target, func(p string) { registered = append(registered, p) }); err != nil {
		t.Fatal(err)
	}
	if got, want := fake.only("mount"), []string{"mount -t xfs /dev/vdb " + dep.VarDir}; !reflect.DeepEqual(got, want) {
		t.Errorf("mounts = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(registered, []string{dep.VarDir}) {
		t.Errorf("registered = %q", registered)
	}
	want := "UUID=abcd-1234\t/var\txfs\tdefaults,nofail,x-systemd.device-timeout=30s,x-systemd.before=local-fs.target\t0 0\n"
	if got := readFstab(t, dep); got != want {
		t.Errorf("fstab = %q, want %q", got, want)
	}

	// A mount failure is an error, and fstab is not written a second time.
	fake.on = lsLikeHost(errors.New("mount: wrong fs type"))
	if err := persistMounts(r, target, func(string) {}); err == nil {
		t.Fatal("a failed mount must be an error")
	}
	if got := readFstab(t, dep); got != want {
		t.Errorf("fstab after a failed run = %q, want it unchanged", got)
	}
}
