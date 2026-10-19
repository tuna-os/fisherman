// Package efifixture supplies a non-bootable, test-signed PE for regression tests.
// Production installer code must not import this package.
package efifixture

import (
	"bytes"
	"crypto/sha256"
	"debug/pe"
	_ "embed"
	"os"
	"path/filepath"
	"testing"
)

//go:embed testdata/signed-uki.efi
var signedUKI []byte

// Bytes returns an independent copy of the Authenticode-signed fixture.
func Bytes() []byte { return append([]byte(nil), signedUKI...) }

// Write stages bytes under a temporary target, never on an actual ESP.
func Write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Equal checks the entire file, including headers, alignment and certificates.
func Equal(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(got) != sha256.Sum256(want) || !bytes.Equal(got, want) {
		t.Fatalf("%s changed: got SHA256 %x, want %x", path, sha256.Sum256(got), sha256.Sum256(want))
	}
}

// CertificateTable reads the PE security directory without rewriting the input.
// This is a structural assertion, not cryptographic signature verification.
func CertificateTable(t *testing.T, data []byte) []byte {
	t.Helper()
	f, err := pe.NewFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	hdr, ok := f.OptionalHeader.(*pe.OptionalHeader64)
	if !ok {
		t.Fatal("fixture is not PE32+")
	}
	d := hdr.DataDirectory[pe.IMAGE_DIRECTORY_ENTRY_SECURITY]
	// Unlike other directories, the certificate directory uses a file offset.
	end := uint64(d.VirtualAddress) + uint64(d.Size)
	if d.Size < 8 || end > uint64(len(data)) {
		t.Fatal("missing or invalid certificate table")
	}
	return append([]byte(nil), data[d.VirtualAddress:end]...)
}
