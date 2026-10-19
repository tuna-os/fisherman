package efifixture

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"testing"
)

func TestFixtureHasUKISectionsAndCertificate(t *testing.T) {
	data := Bytes()
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
