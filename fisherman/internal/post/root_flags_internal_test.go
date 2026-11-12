package post

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The options line bootc 1.15.2 wrote for dakota-btrfs-subvolumes in VM run
// 43177376442: fisherman passed --karg=rootflags=subvol=@,compress=zstd:1,
// and the composefs entry has bootc's own rootflags=subvol=/@ only.
const bootcComposefsSubvolEntry = "title Bluefin Dakota\nversion 1\nlinux /EFI/Linux/bootc_composefs-ebe3/vmlinuz\n" +
	"initrd /EFI/Linux/bootc_composefs-ebe3/initrd\n" +
	"options root=UUID=1e395e4e-01af-4933-8c86-fb909486fb5f rootflags=subvol=/@ rw composefs=ebe3\n"

func rootFlagsTokens(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "options ") {
			out = append(out, line[len("options "):])
		}
	}
	return out
}

func TestRequireRootFlags_ReplacesBootcDerivedSubvol(t *testing.T) {
	dir := luksFixture(t, map[string]string{"boot/efi/loader/entries/bootc.conf": bootcComposefsSubvolEntry})
	res, err := RequireRootFlags(dir, "subvol=@,compress=zstd:1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Entries != 1 || res.Patched != 1 {
		t.Fatalf("got %+v, want 1 entry, 1 patched", res)
	}
	got := rootFlagsTokens(t, filepath.Join(dir, "boot/efi/loader/entries/bootc.conf"))
	want := "root=UUID=1e395e4e-01af-4933-8c86-fb909486fb5f rootflags=subvol=@,compress=zstd:1 rw composefs=ebe3"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("options %q, want %q", got, want)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "boot/efi/loader/entries/bootc.conf"))
	if !strings.HasPrefix(string(b), "title Bluefin Dakota\nversion 1\nlinux ") {
		t.Fatalf("other lines changed:\n%s", b)
	}
}

// No subvolumes (dakota-luks-btrfs): bootc writes rootflags=subvol=/ and
// drops compress=zstd:1; rd.luks.name stays the last argument.
func TestRequireRootFlags_TopLevelBtrfsKeepsLuksLast(t *testing.T) {
	dir := luksFixture(t, map[string]string{
		"boot/efi/loader/entries/a.conf": "options root=UUID=x rootflags=subvol=/ rw composefs=h rd.luks.name=u=root\n",
	})
	if _, err := RequireRootFlags(dir, "compress=zstd:1"); err != nil {
		t.Fatal(err)
	}
	got := rootFlagsTokens(t, filepath.Join(dir, "boot/efi/loader/entries/a.conf"))
	if want := "root=UUID=x rootflags=compress=zstd:1 rw composefs=h rd.luks.name=u=root"; len(got) != 1 || got[0] != want {
		t.Fatalf("options %q, want %q", got, want)
	}
}

// Where bootc honours --karg (ostree), the entry has two rootflags=: one
// remains, the recipe's, in the first one's place.
func TestRequireRootFlags_DeduplicatesHonouredKarg(t *testing.T) {
	dir := luksFixture(t, map[string]string{
		"boot/loader/entries/ostree-1.conf": "options root=UUID=x rootflags=subvol=/@ rw rootflags=subvol=@,compress=zstd:1 ostree=/ostree/boot.1/default/abc/0\n",
	})
	if _, err := RequireRootFlags(dir, "subvol=@,compress=zstd:1"); err != nil {
		t.Fatal(err)
	}
	got := rootFlagsTokens(t, filepath.Join(dir, "boot/loader/entries/ostree-1.conf"))
	if want := "root=UUID=x rootflags=subvol=@,compress=zstd:1 rw ostree=/ostree/boot.1/default/abc/0"; len(got) != 1 || got[0] != want {
		t.Fatalf("options %q, want %q", got, want)
	}
}

func TestRequireRootFlags_AppendsWhenAbsentAndIsIdempotent(t *testing.T) {
	dir := luksFixture(t, map[string]string{"boot/loader/entries/a.conf": "options root=UUID=x rw\n"})
	if _, err := RequireRootFlags(dir, "compress=zstd:1"); err != nil {
		t.Fatal(err)
	}
	res, err := RequireRootFlags(dir, "compress=zstd:1")
	if err != nil || res.Patched != 0 || res.Entries != 1 {
		t.Fatalf("second call: %+v, %v; want 1 entry, nothing patched", res, err)
	}
	got := rootFlagsTokens(t, filepath.Join(dir, "boot/loader/entries/a.conf"))
	if want := "root=UUID=x rw rootflags=compress=zstd:1"; got[0] != want {
		t.Fatalf("options %q, want %q", got, want)
	}
}

func TestRequireRootFlags_Errors(t *testing.T) {
	if _, err := RequireRootFlags(t.TempDir(), "compress=zstd:1"); !errors.Is(err, ErrNoLoaderEntries) {
		t.Fatalf("no entries: %v, want ErrNoLoaderEntries", err)
	}
	dir := luksFixture(t, map[string]string{"boot/loader/entries/a.conf": "title no options\n"})
	if _, err := RequireRootFlags(dir, "compress=zstd:1"); err == nil {
		t.Fatal("an entry without an options line must be an error")
	}
	for _, bad := range []string{"", "subvol=@ quiet"} {
		if _, err := RequireRootFlags(dir, bad); err == nil {
			t.Fatalf("options %q must be refused", bad)
		}
	}
}

// A write that does not land is caught by the read-back.
func TestRequireRootFlags_VerifiesWrite(t *testing.T) {
	dir := luksFixture(t, map[string]string{"boot/efi/loader/entries/a.conf": bootcComposefsSubvolEntry})
	orig := rootFlagsWriteFile
	rootFlagsWriteFile = func(string, []byte, fs.FileMode) error { return nil }
	t.Cleanup(func() { rootFlagsWriteFile = orig })
	if _, err := RequireRootFlags(dir, "subvol=@,compress=zstd:1"); err == nil {
		t.Fatal("an entry still without the recipe's rootflags must be an error")
	}
}
