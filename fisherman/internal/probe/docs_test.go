package probe

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// docs/TROUBLESHOOTING.md tells people which `fisherman probe --json` fields
// to read. Every field it names must be one the probe writes
// (tuna-os/fisherman#419: it named `disk`, `system.ram` and `system.cpu`).

// jsonPaths returns every key path of v as JSON, with "[]" for an array
// element: "disks[].path", "system.ram_bytes".
func jsonPaths(prefix string, v any, out map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			out[p] = true
			jsonPaths(p, e, out)
		}
	case []any:
		out[prefix+"[]"] = true
		for _, e := range x {
			jsonPaths(prefix+"[]", e, out)
		}
	}
}

func TestTroubleshootingProbeFields(t *testing.T) {
	r := Result{
		Disks:   []Disk{{ExcludedReason: ExcludedBootDisk}},
		Offline: Offline{Stores: []string{"/x"}},
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	jsonPaths("", v, paths)

	doc, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "TROUBLESHOOTING.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(doc)
	start := strings.Index(text, "### Check the machine")
	if start < 0 {
		t.Fatal(`"### Check the machine" not found`)
	}
	text = text[start:]
	if end := strings.Index(text[1:], "\n### "); end >= 0 {
		text = text[:end+1]
	}
	field := regexp.MustCompile("`([a-z_]+(?:\\[\\])?(?:\\.[a-z_]+)*)`")
	n := 0
	for _, line := range strings.Split(text, "\n") {
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if !strings.HasPrefix(line, "| `") || len(cells) < 2 {
			continue
		}
		for _, m := range field.FindAllStringSubmatch(cells[0], -1) {
			n++
			if !paths[m[1]] {
				t.Errorf("TROUBLESHOOTING.md names probe field %q, which `fisherman probe --json` does not write", m[1])
			}
		}
		// The disks[] row names its element fields and exclusion reasons.
		if strings.Contains(cells[0], "`disks[]`") {
			for _, m := range field.FindAllStringSubmatch(cells[1], -1) {
				name := m[1]
				switch name {
				case ExcludedBootDisk, ExcludedReadOnly, ExcludedTooSmall, ExcludedNotDisk:
					continue
				}
				if !paths["disks[]."+name] {
					t.Errorf("TROUBLESHOOTING.md names disk field %q, which the probe does not write", name)
				}
			}
		}
	}
	if n < 8 {
		t.Fatalf("read only %d fields; the parser missed the table", n)
	}
}
