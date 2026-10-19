package install

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/tuna-os/fisherman/internal/post"
	"github.com/tuna-os/fisherman/internal/testutil/efifixture"
)

// frostyard/fisherman#28 demonstrated that even section inspection can destroy
// Authenticode signatures. Exercise Tuna's actual copy/post-install boundaries;
// Tuna does not contain that fork's secure.VerifyInstalled or objcopy reader.
func TestCopyFilePreservesSignedUKI(t *testing.T) {
	signed := efifixture.Bytes()
	for name, data := range map[string][]byte{
		"signed UKI with missing optional pcrsig": signed,
		"malformed PE is opaque to copying":       []byte("MZ truncated"),
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			src, dst := filepath.Join(dir, "src.efi"), filepath.Join(dir, "dst.efi")
			efifixture.Write(t, src, data)
			if err := copyFile(src, dst); err != nil {
				t.Fatal(err)
			}
			efifixture.Equal(t, src, data)
			efifixture.Equal(t, dst, data)
			if bytes.Equal(data, signed) {
				got, err := os.ReadFile(dst)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(efifixture.CertificateTable(t, got), efifixture.CertificateTable(t, signed)) {
					t.Fatal("certificate changed")
				}
			}
			// An existing directory forces destination-open failure even when tests run
			// as root. The source, including its signature, must remain unchanged.
			if err := copyFile(src, dir); err == nil {
				t.Fatal("expected destination error")
			}
			efifixture.Equal(t, src, data)
		})
	}
}

func TestSystemdFallbackPreservesSignedPEAndInstalledUKI(t *testing.T) {
	target := t.TempDir()
	signed := efifixture.Bytes()
	src := makeDeployTree(t, target)
	// A distinct unsigned candidate proves selection, not just equal contents.
	efifixture.Write(t, src, []byte("unsigned candidate"))
	efifixture.Write(t, src+".signed", signed)
	uki := filepath.Join(target, "boot/efi/EFI/Linux/bootc/test.efi")
	efifixture.Write(t, uki, signed)
	for i := 0; i < 2; i++ {
		if err := InstallSystemdBoot(target); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{src + ".signed", uki, filepath.Join(target, "boot/efi/EFI/BOOT/BOOTX64.EFI"), filepath.Join(target, "boot/efi/EFI/systemd/systemd-bootx64.efi")} {
			efifixture.Equal(t, p, signed)
		}
	}
	efifixture.Equal(t, src, []byte("unsigned candidate"))
}

func TestFallbackHelpersLeaveExistingSignedChainUntouched(t *testing.T) {
	target := t.TempDir()
	signed := efifixture.Bytes()
	fallback, grub, mm := fallbackBootNames()
	paths := []string{"EFI/BOOT/" + fallback, "EFI/BOOT/" + grub, "EFI/BOOT/" + mm, "EFI/Linux/bootc/test.efi"}
	// InstallSystemdBoot currently supports x64 only, regardless of Go host arch.
	if fallback != "BOOTX64.EFI" {
		paths = append(paths, "EFI/BOOT/BOOTX64.EFI")
	}
	for _, rel := range paths {
		efifixture.Write(t, filepath.Join(target, "boot/efi", rel), signed)
	}
	// Deliberately omit deployment candidates: the existing fallback must win.
	for i := 0; i < 2; i++ {
		if err := InstallSystemdBoot(target); err != nil {
			t.Fatal(err)
		}
		if err := InstallGrubFallback(target, "fedora"); err != nil {
			t.Fatal(err)
		}
		for _, rel := range paths {
			efifixture.Equal(t, filepath.Join(target, "boot/efi", rel), signed)
		}
	}
}

func TestGrubFallbackCopiesSignedCompanionsWithoutChangingUKI(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("signed fixture is x86-64; does not qualify other EFI architectures")
	}
	target := t.TempDir()
	signed := efifixture.Bytes()
	vendor := filepath.Join(target, "boot/efi/EFI/fedora")
	for _, name := range []string{"shimx64.efi", "grubx64.efi", "mmx64.efi"} {
		efifixture.Write(t, filepath.Join(vendor, name), signed)
	}
	uki := filepath.Join(target, "boot/efi/EFI/Linux/bootc/test.efi")
	efifixture.Write(t, uki, signed)
	if err := InstallGrubFallback(target, "fedora"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"BOOTX64.EFI", "grubx64.efi", "mmx64.efi"} {
		efifixture.Equal(t, filepath.Join(target, "boot/efi/EFI/BOOT", name), signed)
	}
	for _, name := range []string{"shimx64.efi", "grubx64.efi", "mmx64.efi"} {
		efifixture.Equal(t, filepath.Join(vendor, name), signed)
	}
	efifixture.Equal(t, uki, signed)
}

func TestFallbackErrorsDoNotMutateSignedUKI(t *testing.T) {
	for _, kind := range []string{"systemd missing source", "systemd destination blocked", "grub missing source", "grub destination blocked"} {
		t.Run(kind, func(t *testing.T) {
			target := t.TempDir()
			signed := efifixture.Bytes()
			uki := filepath.Join(target, "boot/efi/EFI/Linux/bootc/test.efi")
			efifixture.Write(t, uki, signed)
			var source string
			if kind == "systemd destination blocked" {
				source = makeDeployTree(t, target) + ".signed"
				efifixture.Write(t, source, signed)
			}
			if kind == "grub destination blocked" {
				source = filepath.Join(target, "boot/efi/EFI/fedora/shimx64.efi")
				efifixture.Write(t, source, signed)
			}
			if kind == "systemd destination blocked" || kind == "grub destination blocked" {
				efifixture.Write(t, filepath.Join(target, "boot/efi/EFI/BOOT"), []byte("blocked"))
			}
			var err error
			if kind == "systemd missing source" || kind == "systemd destination blocked" {
				err = InstallSystemdBoot(target)
			} else {
				err = InstallGrubFallback(target)
			}
			if err == nil {
				t.Fatal("expected error")
			}
			efifixture.Equal(t, uki, signed)
			if source != "" {
				efifixture.Equal(t, source, signed)
			}
		})
	}
}

func TestBLSPostProcessingDoesNotRewriteSignedUKI(t *testing.T) {
	for _, entry := range []string{
		"title signed UKI\nefi /EFI/Linux/bootc/test.efi\noptions quiet\n",
		"title signed UKI\nuki /EFI/Linux/bootc/test.efi\n", // embedded cmdline, no options
	} {
		t.Run(entry, func(t *testing.T) {
			target := t.TempDir()
			signed := efifixture.Bytes()
			uki := filepath.Join(target, "boot/efi/EFI/Linux/bootc/test.efi")
			efifixture.Write(t, uki, signed)
			conf := filepath.Join(target, "boot/efi/loader/entries/test.conf")
			efifixture.Write(t, conf, []byte(entry))
			for i := 0; i < 2; i++ {
				if _, err := post.EnsurePlymouthArgs(target); err != nil {
					t.Fatal(err)
				}
				if _, err := post.EnsureLuksArgs(target, "test-uuid"); err != nil {
					t.Fatal(err)
				}
				efifixture.Equal(t, uki, signed)
			}
			// The error path must also preserve the PE. A directory matching *.conf
			// makes ReadFile fail without depending on permission checks as root.
			if err := os.Remove(conf); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(conf, 0o755); err != nil {
				t.Fatal(err)
			}
			if _, err := post.EnsurePlymouthArgs(target); err == nil {
				t.Fatal("expected entry read error")
			}
			if _, err := post.EnsureLuksArgs(target, "test-uuid"); err == nil {
				t.Fatal("expected entry read error")
			}
			efifixture.Equal(t, uki, signed)
		})
	}
}
