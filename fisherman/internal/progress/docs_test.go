package progress

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// docs/JSON-PROGRESS-FORMAT.md and docs/TROUBLESHOOTING.md name the event
// types and the step ids frontends and people key on. These tests hold both
// documents to the code (tuna-os/fisherman#419).

func readDoc(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// section returns doc from the heading line start to the next heading of
// the same or a higher level.
func section(t *testing.T, doc, start string) string {
	t.Helper()
	i := strings.Index(doc, "\n"+start+"\n")
	if i < 0 {
		t.Fatalf("section %q not found", start)
	}
	rest := doc[i+len(start)+2:]
	level := strings.Index(start, " ")
	off, fenced := 0, false
	for _, line := range strings.SplitAfter(rest, "\n") {
		if strings.HasPrefix(line, "```") {
			fenced = !fenced
		}
		if n := strings.Index(line, " "); !fenced && n > 0 && n <= level && strings.Trim(line[:n], "#") == "" {
			return rest[:off]
		}
		off += len(line)
	}
	return rest
}

// eventTypes returns every "type" value the package's non-test files emit.
func eventTypes(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	lit := regexp.MustCompile(`Type(?:, e\.Version)?\s*[:=]\s*"([a-z_]+)"`)
	seen := map[string]bool{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range lit.FindAllStringSubmatch(string(src), -1) {
			seen[m[1]] = true
		}
	}
	return sorted(seen)
}

func sorted(m map[string]bool) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// bodyRows returns the table rows of text that are not a header: a header
// row is the one followed by the |---| separator.
func bodyRows(text string) string {
	lines := strings.Split(text, "\n")
	var out []string
	for i, line := range lines {
		if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "|---") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// firstColumn returns the backticked values in the first cell of each table
// body row of text.
func firstColumn(text string) []string {
	row := regexp.MustCompile("(?m)^\\| `([a-z0-9_]+)` \\|")
	seen := map[string]bool{}
	for _, m := range row.FindAllStringSubmatch(bodyRows(text), -1) {
		seen[m[1]] = true
	}
	return sorted(seen)
}

func equal(a, b []string) bool {
	return strings.Join(a, ",") == strings.Join(b, ",")
}

func TestDocumentedEventTypes(t *testing.T) {
	types := eventTypes(t)
	if len(types) < 8 {
		t.Fatalf("found only %v; the scan missed event types", types)
	}
	heading := regexp.MustCompile("(?m)^### `([a-z_]+)`$")
	seen := map[string]bool{}
	for _, m := range heading.FindAllStringSubmatch(section(t, readDoc(t, "JSON-PROGRESS-FORMAT.md"), "## Event Types"), -1) {
		seen[m[1]] = true
	}
	if got := sorted(seen); !equal(got, types) {
		t.Errorf("JSON-PROGRESS-FORMAT.md documents event types %v; the code emits %v", got, types)
	}
	ts := section(t, readDoc(t, "TROUBLESHOOTING.md"), "## During the install: the event stream")
	ts, _, _ = strings.Cut(ts, "\n### ")
	if got := firstColumn(ts); !equal(got, types) {
		t.Errorf("TROUBLESHOOTING.md lists event types %v; the code emits %v", got, types)
	}
}

func TestDocumentedStepIDs(t *testing.T) {
	ids := map[string]bool{}
	for _, id := range StepIDs() {
		ids[id] = true
	}
	want := sorted(ids)

	pipeline := section(t, readDoc(t, "JSON-PROGRESS-FORMAT.md"), "## Pipeline")
	// | `Step name` | `step_id` | When |
	row := regexp.MustCompile("(?m)^\\| `([^`]+)` \\| `([a-z0-9_]+)` \\|")
	seen := map[string]bool{}
	for _, m := range row.FindAllStringSubmatch(bodyRows(pipeline), -1) {
		seen[m[2]] = true
		if StepID(m[1]) != m[2] {
			t.Errorf("JSON-PROGRESS-FORMAT.md: step %q has id %q; the code says %q", m[1], m[2], StepID(m[1]))
		}
	}
	if got := sorted(seen); !equal(got, want) {
		t.Errorf("JSON-PROGRESS-FORMAT.md Pipeline lists step ids %v; the code has %v", got, want)
	}

	steps := section(t, readDoc(t, "TROUBLESHOOTING.md"), "### Steps")
	if got := firstColumn(steps); !equal(got, want) {
		t.Errorf("TROUBLESHOOTING.md Steps lists step ids %v; the code has %v", got, want)
	}
}
