package probe

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/golden/probe.json")

// goldenProber is a live USB boot of a laptop: the machine.json disks, a TPM
// 2.0, 16 GiB, 8 threads on 4 cores, UEFI, and one offline store.
func goldenProber(t *testing.T) *Prober {
	t.Helper()
	lsblk, err := os.ReadFile("testdata/lsblk/machine.json")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"proc/meminfo":                         meminfo16G,
		"proc/cmdline":                         "BOOT_IMAGE=/images/pxeboot/vmlinuz root=live:CDLABEL=Live rd.live.image quiet\n",
		"sys/firmware/efi/":                    "",
		"sys/class/tpm/tpm0/tpm_version_major": "2\n",
		"usr/share/tuna-installer/oci-store/overlay-images/images.json": storeIndex,
	}
	cpuTopology(files, "0-7", [][2]string{
		{"0", "0"}, {"0", "1"}, {"0", "2"}, {"0", "3"},
		{"0", "0"}, {"0", "1"}, {"0", "2"}, {"0", "3"},
	})
	host := &fakeHost{canned: map[string]string{
		"lsblk -J -b -p -o " + lsblkColumns: string(lsblk),
		"bootc status --json":               bootcStatusLive,
	}}
	return &Prober{Root: fakeRoot(t, files), Run: host.run, Getenv: noEnv}
}

// TestProbeGolden pins the whole JSON document. docs/PROBE.md quotes the
// golden file; regenerate both with `go test ./internal/probe -update`.
func TestProbeGolden(t *testing.T) {
	res, err := goldenProber(t).Probe()
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	golden := filepath.Join("testdata", "golden", "probe.json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("probe output differs from %s (run with -update after an intended change):\n%s", golden, got)
	}
}

// Empty collections serialise as [] so a frontend never meets null.
func TestProbeEmptyArraysNotNull(t *testing.T) {
	files := map[string]string{"proc/meminfo": meminfo16G, "sys/devices/system/cpu/online": "0\n"}
	host := &fakeHost{canned: map[string]string{"lsblk -J -b -p -o " + lsblkColumns: `{"blockdevices":[]}`}}
	p := &Prober{Root: fakeRoot(t, files), Run: host.run, Getenv: noEnv}
	res, err := p.Probe()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res)
	for _, frag := range []string{`"disks":[]`, `"stores":[]`, `"images":[]`} {
		if !bytes.Contains(b, []byte(frag)) {
			t.Errorf("%s missing from %s", frag, b)
		}
	}
}

func TestProbeErrors(t *testing.T) {
	files := map[string]string{"proc/meminfo": meminfo16G, "sys/devices/system/cpu/online": "0\n"}
	failing := &Prober{Root: fakeRoot(t, files), Run: func(string, ...string) ([]byte, error) {
		return nil, errors.New("lsblk: not found")
	}, Getenv: noEnv}
	if _, err := failing.Probe(); err == nil {
		t.Error("lsblk failure was not an error")
	}
	host := &fakeHost{canned: map[string]string{"lsblk -J -b -p -o " + lsblkColumns: `{"blockdevices":[]}`}}
	noMem := &Prober{Root: t.TempDir(), Run: host.run, Getenv: noEnv}
	if _, err := noMem.Probe(); err == nil {
		t.Error("missing /proc/meminfo was not an error")
	}
	var nilRunner Prober
	nilRunner.Root = t.TempDir()
	if _, err := nilRunner.Disks(); err == nil {
		t.Error("a Prober with no runner ran something")
	}
}

// The probe must not write: run it against a root and check nothing in it
// changed.
func TestProbeWritesNothing(t *testing.T) {
	p := goldenProber(t)
	before := snapshot(t, p.Root)
	if _, err := p.Probe(); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(t, p.Root); after != before {
		t.Errorf("probe changed its root:\nbefore %s\nafter  %s", before, after)
	}
}

func snapshot(t *testing.T, root string) string {
	t.Helper()
	var buf bytes.Buffer
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		buf.WriteString(path)
		buf.WriteString(info.ModTime().String())
		buf.WriteByte('\n')
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// docs/PROBE.md shows the golden output as its example; keep them equal.
func TestDocsQuoteGolden(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "PROBE.md"))
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "golden", "probe.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(doc, append(append([]byte("```json\n"), golden...), []byte("```")...)) {
		t.Error("docs/PROBE.md does not quote testdata/golden/probe.json verbatim")
	}
}
