package recipe

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The problem codes are a contract that frontends key their copy on, and
// docs/VALIDATE.md is where they read it. These tests hold the document to
// the code in both directions (tuna-os/fisherman#419): a code added without
// a row, a row for a code that does not exist, and a code named in the rules
// table that the code does not have all fail.

// problemCodeConsts returns the value of every string constant named Code…
// in the package's non-test files.
func problemCodeConsts(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, id := range vs.Names {
					if !strings.HasPrefix(id.Name, "Code") || i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					v, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatal(err)
					}
					out[v] = id.Name
				}
			}
		}
	}
	if len(out) < 50 {
		t.Fatalf("found only %d Code constants; the parser missed them", len(out))
	}
	return out
}

// docSection returns the text of doc from the line that starts with start
// up to the next heading of the same level.
func docSection(t *testing.T, doc, start string) string {
	t.Helper()
	i := strings.Index(doc, "\n"+start+"\n")
	if i < 0 {
		t.Fatalf("section %q not found", start)
	}
	rest := doc[i+len(start)+2:]
	level := start[:strings.Index(start, " ")+1]
	if j := strings.Index(rest, "\n"+level); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func readValidateDoc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "VALIDATE.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestProblemCodesDocumented: the "Error Code Reference" has exactly one row
// for each code constant.
func TestProblemCodesDocumented(t *testing.T) {
	consts := problemCodeConsts(t)
	ref := docSection(t, readValidateDoc(t), "## Error Code Reference")
	row := regexp.MustCompile("(?m)^\\| `([a-z0-9_]+)` \\|")
	documented := map[string]bool{}
	for _, m := range row.FindAllStringSubmatch(ref, -1) {
		if documented[m[1]] {
			t.Errorf("VALIDATE.md: code %q has more than one row", m[1])
		}
		documented[m[1]] = true
	}
	missing := map[string]bool{}
	for code := range consts {
		if !documented[code] {
			missing[code] = true
		}
	}
	extra := map[string]bool{}
	for code := range documented {
		if _, ok := consts[code]; !ok {
			extra[code] = true
		}
	}
	if len(missing) > 0 {
		t.Errorf("codes with no row in VALIDATE.md's Error Code Reference: %v", sortedKeys(missing))
	}
	if len(extra) > 0 {
		t.Errorf("rows in VALIDATE.md's Error Code Reference for codes the code does not have: %v", sortedKeys(extra))
	}
}

// TestRulesTableCodesExist: every code in the Codes column of the "Rules"
// table is a real code. A wildcard (`source_reuse_*`) must match one.
func TestRulesTableCodesExist(t *testing.T) {
	consts := problemCodeConsts(t)
	rules := docSection(t, readValidateDoc(t), "## Rules")
	code := regexp.MustCompile("`([a-z0-9_]+\\*?)`")
	n := 0
	for _, line := range strings.Split(rules, "\n") {
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) < 3 || strings.HasPrefix(strings.TrimSpace(cells[0]), "---") || strings.TrimSpace(cells[0]) == "Field" {
			continue
		}
		for _, m := range code.FindAllStringSubmatch(cells[len(cells)-1], -1) {
			n++
			c := m[1]
			if prefix, ok := strings.CutSuffix(c, "*"); ok {
				found := false
				for v := range consts {
					found = found || strings.HasPrefix(v, prefix)
				}
				if !found {
					t.Errorf("VALIDATE.md Rules: %q matches no code", c)
				}
				continue
			}
			if _, ok := consts[c]; !ok {
				t.Errorf("VALIDATE.md Rules names code %q, which the code does not have", c)
			}
		}
	}
	if n < 50 {
		t.Fatalf("read only %d codes from the Rules table; the parser missed them", n)
	}
}
