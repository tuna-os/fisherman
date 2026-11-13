package main

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

// helpArgs are accepted by every command and documented once, not per flag
// table.
var helpArgs = map[string]bool{"-h": true, "--help": true, "help": true}

// parserLiterals returns, for each function of this package's non-test
// files, the string literals of its switch case clauses and the names its
// flag.FlagSet registers (fs.String("file", …) gives "--file").
func parserLiterals(t *testing.T) map[string]map[string]bool {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]bool{}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			set := map[string]bool{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.SwitchStmt:
					// main() switches on more than the command; keep only
					// `switch os.Args[1]` there.
					if fn.Name.Name == "main" && !isArgsSwitch(n) {
						return false
					}
				case *ast.CaseClause:
					for _, e := range n.List {
						if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
							v, _ := strconv.Unquote(lit.Value)
							set[v] = true
						}
					}
				case *ast.CallExpr:
					sel, ok := n.Fun.(*ast.SelectorExpr)
					if !ok || len(n.Args) == 0 {
						break
					}
					if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "fs" {
						break
					}
					switch sel.Sel.Name {
					case "String", "Bool", "Int", "Duration":
						if lit, ok := n.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							v, _ := strconv.Unquote(lit.Value)
							set["--"+v] = true
						}
					}
				}
				return true
			})
			out[fn.Name.Name] = set
		}
	}
	return out
}

// isArgsSwitch reports whether s is `switch os.Args[1]`.
func isArgsSwitch(s *ast.SwitchStmt) bool {
	ix, ok := s.Tag.(*ast.IndexExpr)
	if !ok {
		return false
	}
	sel, ok := ix.X.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Args" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "os"
}

// flagsOf returns the flags ("--x") among the literals of the named
// functions, without the help arguments.
func flagsOf(lits map[string]map[string]bool, funcs ...string) []string {
	seen := map[string]bool{}
	for _, fn := range funcs {
		for v := range lits[fn] {
			if strings.HasPrefix(v, "--") && !helpArgs[v] {
				seen[v] = true
			}
		}
	}
	return sortedSet(seen)
}

func sortedSet(m map[string]bool) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// cliSection returns the text of docs/CLI.md from the heading line to the
// next "## " heading.
func cliSection(t *testing.T, doc, heading string) string {
	t.Helper()
	i := strings.Index(doc, "\n"+heading+"\n")
	if i < 0 {
		t.Fatalf("CLI.md: section %q not found", heading)
	}
	rest := doc[i+len(heading)+2:]
	if j := strings.Index(rest, "\n## "); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

var flagRow = regexp.MustCompile("(?m)^\\| `(--[a-z-]+)` \\|")

// TestCLIReferenceCoversEveryFlag holds docs/CLI.md to the argument parsers
// (tuna-os/fisherman#322): every flag a command's parser accepts has one
// row in that command's section, and every row is a flag it accepts.
func TestCLIReferenceCoversEveryFlag(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "CLI.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	lits := parserLiterals(t)

	for _, c := range []struct {
		heading string
		funcs   []string
	}{
		{"## `fisherman validate`", []string{"runValidate", "runValidateJSON"}},
		{"## `fisherman plan`", []string{"runPlan"}},
		{"## `fisherman probe`", []string{"runProbe"}},
		{"## `fisherman verify`", []string{"runVerify"}},
		{"## `fisherman images`", []string{"runImages"}},
		{"## `fisherman diagnostics`", []string{"runDiagnostics"}},
	} {
		want := flagsOf(lits, c.funcs...)
		if len(want) == 0 {
			t.Errorf("found no flags in %v; the parser scan missed them", c.funcs)
			continue
		}
		seen := map[string]bool{}
		for _, m := range flagRow.FindAllStringSubmatch(cliSection(t, doc, c.heading), -1) {
			if seen[m[1]] {
				t.Errorf("CLI.md %s: %s has more than one row", c.heading, m[1])
			}
			seen[m[1]] = true
		}
		if got := sortedSet(seen); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("CLI.md %s lists flags %v; the code accepts %v", c.heading, got, want)
		}
	}

	// The install takes no flags: its section has no flag table.
	for _, h := range []string{"## `fisherman <recipe.json>` and `fisherman -`"} {
		if m := flagRow.FindString(cliSection(t, doc, h)); m != "" {
			t.Errorf("CLI.md %s has a flag row %q; that command takes no flags", h, m)
		}
	}

	// Every command main() dispatches has a row in the Commands table.
	commands := cliSection(t, doc, "## Commands")
	n := 0
	for v := range lits["main"] {
		if strings.HasPrefix(v, "-") {
			continue // --help, --version: aliases named in the rows
		}
		n++
		if !strings.Contains(commands, "| `fisherman "+v+"` |") {
			t.Errorf("CLI.md Commands has no row for `fisherman %s`", v)
		}
	}
	if n < 7 {
		t.Fatalf("found only %d commands in main; the scan missed them", n)
	}
	for _, alias := range []string{"--version", "--help", "-h"} {
		if !lits["main"][alias] {
			t.Errorf("main no longer accepts %s, which CLI.md names", alias)
		} else if !strings.Contains(commands, "`"+alias+"`") {
			t.Errorf("CLI.md Commands does not name %s", alias)
		}
	}
}

// TestIntegrationExitCodes: the install's exit codes in docs/INTEGRATION.md
// are 0 and the exit* constants, no more and no fewer (#328).
func TestIntegrationExitCodes(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "INTEGRATION.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	i := strings.Index(doc, "\n### Exit codes\n")
	if i < 0 {
		t.Fatal("INTEGRATION.md: no Exit codes section")
	}
	rest := doc[i+1:]
	if j := strings.Index(rest[1:], "\n#"); j >= 0 {
		rest = rest[:j+1]
	}
	got := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\| (\d+) \|`).FindAllStringSubmatch(rest, -1) {
		got[m[1]] = true
	}
	want := map[string]bool{"0": true}
	for _, c := range []int{exitFailure, exitUsage, exitCancelled} {
		want[strconv.Itoa(c)] = true
	}
	if g, w := sortedSet(got), sortedSet(want); strings.Join(g, ",") != strings.Join(w, ",") {
		t.Errorf("INTEGRATION.md lists exit codes %v; the code has %v", g, w)
	}
}
