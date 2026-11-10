package catalog

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// repoCatalog loads the repository's data/images.json: the catalog the
// backend ships.
func repoCatalog(t *testing.T) *Catalog {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "data", "images.json"))
	if err != nil {
		t.Fatalf("reading the repository catalog: %v", err)
	}
	c, err := Parse(data)
	if err != nil {
		t.Fatalf("parsing the repository catalog: %v", err)
	}
	return c
}

// TestImageSettings_Table is the table #367 asks for: Dakota, Arch, Debian
// and a GRUB image, with the boot stack each must reach the recipe with.
// Dakota and the GRUB image are read from data/images.json; Arch and Debian
// are not in it and come from the built-in table.
func TestImageSettings_Table(t *testing.T) {
	c := repoCatalog(t)
	cases := []struct {
		name        string
		ref         string
		inCatalog   bool
		bootloader  string
		composefs   bool
		filesystem  string
		needsUser   bool
		xfsAccepted bool
	}{
		{"Dakota", "ghcr.io/projectbluefin/dakota:latest", true, "systemd", true, "btrfs", false, false},
		{"Arch", "ghcr.io/bootcrew/arch-bootc:latest", false, "systemd", true, "ext4", true, false},
		{"Debian", "ghcr.io/bootcrew/debian-bootc:latest", false, "systemd", true, "ext4", true, false},
		{"Aurora (GRUB, ostree)", "ghcr.io/ublue-os/aurora:latest", true, "", false, "btrfs", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := c.Lookup(tc.ref); ok != tc.inCatalog {
				t.Fatalf("in data/images.json = %v, want %v", ok, tc.inCatalog)
			}
			img, ok := c.Resolve(tc.ref)
			if !ok {
				t.Fatalf("Resolve(%s): not found", tc.ref)
			}
			if img.Bootloader != tc.bootloader || img.ComposeFsBackend != tc.composefs || img.Filesystem != tc.filesystem {
				t.Errorf("got bootloader=%q composefs=%v filesystem=%q, want %q %v %q",
					img.Bootloader, img.ComposeFsBackend, img.Filesystem, tc.bootloader, tc.composefs, tc.filesystem)
			}
			if img.NeedsUserCreation != tc.needsUser {
				t.Errorf("NeedsUserCreation = %v, want %v", img.NeedsUserCreation, tc.needsUser)
			}
			if got := img.SupportsFilesystem("xfs"); got != tc.xfsAccepted {
				t.Errorf("SupportsFilesystem(xfs) = %v, want %v", got, tc.xfsAccepted)
			}
			if !img.SupportsFilesystem(img.Filesystem) {
				t.Errorf("the default filesystem %q is not one of the choices %v", img.Filesystem, img.Filesystems)
			}

			// The list the TUI shows carries the same settings.
			found := false
			for _, listed := range c.Images() {
				if listed.Ref == tc.ref {
					found = true
					if listed.Bootloader != img.Bootloader || listed.ComposeFsBackend != img.ComposeFsBackend {
						t.Errorf("the listed image differs from the resolved one: %+v vs %+v", listed, img)
					}
				}
			}
			if !found {
				t.Errorf("%s is not in the list the TUI offers", tc.ref)
			}
		})
	}
}

// With no catalog installed the built-in table still gives every listed
// image its boot stack.
func TestImages_WithoutCatalog(t *testing.T) {
	var c *Catalog
	imgs := c.Images()
	if len(imgs) != len(builtin) {
		t.Fatalf("got %d images, want %d", len(imgs), len(builtin))
	}
	dakota, ok := c.Resolve("ghcr.io/projectbluefin/dakota:latest")
	if !ok || !dakota.ComposeFsBackend || dakota.Bootloader != "systemd" || dakota.NeedsUserCreation {
		t.Errorf("built-in Dakota = %+v", dakota)
	}
	if _, ok := c.Resolve("registry.example.com/unknown:tag"); ok {
		t.Error("an unknown reference must not resolve: the user is asked for its boot stack")
	}
}

func TestParse_InheritanceAndDefaults(t *testing.T) {
	c, err := Parse([]byte(`{"images":[
	  {"name":"Family","registry":"example.com/family","bootloader":"systemd","composefs":true,
	   "filesystems":["btrfs","xfs","ext4"],"filesystem":"btrfs","needs_user_creation":false,
	   "children":[
	     {"name":"Stable","tag":"stable"},
	     {"name":"Classic","tag":"classic","composefs":false,"bootloader":"grub2","needs_user_creation":true},
	     {"name":"Group","children":[{"name":"Deep","imgref":"example.com/deep:1","filesystem":"ext4"}]}
	   ]},
	  {"name":"Bare","imgref":"example.com/bare:1"},
	  {"name":"OneFS","imgref":"example.com/onefs:1","filesystem":"xfs"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Image{
		// Everything inherited; xfs dropped from the choices by composefs.
		"example.com/family:stable": {Label: "Family › Stable", Ref: "example.com/family:stable", Bootloader: "systemd",
			ComposeFsBackend: true, Filesystem: "btrfs", Filesystems: []string{"btrfs", "ext4"}},
		// The leaf's explicit false and true win over the group.
		"example.com/family:classic": {Label: "Family › Classic", Ref: "example.com/family:classic", Bootloader: "grub2",
			Filesystem: "btrfs", Filesystems: []string{"btrfs", "xfs", "ext4"}, NeedsUserCreation: true},
		"example.com/deep:1": {Label: "Family › Group › Deep", Ref: "example.com/deep:1", Bootloader: "systemd",
			ComposeFsBackend: true, Filesystem: "ext4", Filesystems: []string{"btrfs", "ext4"}},
		// needs_user_creation set nowhere: TRUE. No filesystem keys: all.
		"example.com/bare:1": {Label: "Bare", Ref: "example.com/bare:1", Filesystem: "ext4",
			Filesystems: []string{"ext4", "xfs", "btrfs"}, NeedsUserCreation: true},
		// `filesystem` alone: it is the only choice.
		"example.com/onefs:1": {Label: "OneFS", Ref: "example.com/onefs:1", Filesystem: "xfs",
			Filesystems: []string{"xfs"}, NeedsUserCreation: true},
	}
	for ref, w := range want {
		got, ok := c.Lookup(ref)
		if !ok {
			t.Errorf("%s: not found", ref)
			continue
		}
		if !reflect.DeepEqual(got, w) {
			t.Errorf("%s:\n got %+v\nwant %+v", ref, got, w)
		}
	}
	if _, err := Parse([]byte("{")); err == nil {
		t.Error("malformed JSON must be an error")
	}
}

// A custom reference assumes nothing: the caller passes the boot stack.
func TestCustom(t *testing.T) {
	grub := Custom("registry.example.com/os:1", false, "")
	if grub.ComposeFsBackend || grub.Bootloader != "" || !grub.NeedsUserCreation || !grub.SupportsFilesystem("xfs") {
		t.Errorf("GRUB custom image = %+v", grub)
	}
	composefs := Custom("registry.example.com/os:1", true, "systemd")
	if !composefs.ComposeFsBackend || composefs.Bootloader != "systemd" || composefs.SupportsFilesystem("xfs") || composefs.Filesystem == "xfs" {
		t.Errorf("composefs custom image = %+v", composefs)
	}
}

func TestLoadDefault_EnvPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "images.json")
	if err := os.WriteFile(path, []byte(`{"images":[{"name":"X","imgref":"example.com/x:1","composefs":true,"bootloader":"systemd"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FISHERMAN_IMAGES_PATH", path)
	c := LoadDefault()
	if img, ok := c.Lookup("example.com/x:1"); !ok || !img.ComposeFsBackend {
		t.Fatalf("LoadDefault did not read FISHERMAN_IMAGES_PATH: %+v, %v", img, ok)
	}
}
