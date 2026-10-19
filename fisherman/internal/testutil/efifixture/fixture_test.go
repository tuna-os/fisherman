package efifixture

import (
	"bytes"
	"crypto/sha256"
	"debug/pe"
	"encoding/binary"
	"fmt"
	"testing"
)

func TestFixtureHasUKISectionsAndCertificate(t *testing.T) {
	data := Bytes()
	// Pin the exact fixture verified with sbverify at creation. Structural PE
	// checks alone cannot detect a damaged Authenticode signature.
	const verifiedSHA256 = "d0db0f7cfeb2dfdc4ee9bb90346515a1c1b9a91f07d8c58d8163a2ec18e7f3a5"
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != verifiedSHA256 {
		t.Fatalf("fixture changed: SHA256 %s; verify replacement with sbverify before updating pin", got)
	}
	cert := CertificateTable(t, data)
	if binary.LittleEndian.Uint16(cert[4:6]) != 0x200 || binary.LittleEndian.Uint16(cert[6:8]) != 2 {
		t.Fatal("expected WIN_CERTIFICATE revision 2 and PKCS signed-data")
	}
	f, err := pe.NewFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, name := range []string{".linux", ".osrel", ".cmdline", ".pcrpkey", ".sbat"} {
		s := f.Section(name)
		if s == nil {
			t.Fatalf("missing %s", name)
		}
		if _, err := s.Data(); err != nil {
			t.Fatal(err)
		}
	}
	if f.Section(".pcrsig") != nil {
		t.Fatal("fixture intentionally has no .pcrsig")
	}
	if !bytes.Equal(data, Bytes()) {
		t.Fatal("read-only PE inspection changed fixture")
	}
}
