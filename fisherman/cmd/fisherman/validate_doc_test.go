package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// docExample is one `$ echo '<recipe>' | fisherman validate …` command of a
// console block in docs/VALIDATE.md, with the output the document shows
// under it.
type docExample struct {
	line   int
	recipe string
	args   []string
	output string
}

var echoCommand = regexp.MustCompile(`(?s)^echo '([^']*)' \| fisherman validate (.*)$`)

// validateDocExamples returns every command of the ```console blocks of doc
// that pipes a recipe into `fisherman validate`. A command starts at a line
// "$ " and runs to the line that ends with " -"; the lines after it, up to
// the next "$ " or the end of the block, are its output.
func validateDocExamples(t *testing.T, doc string) []docExample {
	t.Helper()
	var out []docExample
	lines := strings.Split(doc, "\n")
	for i := 0; i < len(lines); i++ {
		if lines[i] != "```console" {
			continue
		}
		var cur *docExample
		var cmd []string
		for i++; i < len(lines) && lines[i] != "```"; i++ {
			line := lines[i]
			switch {
			case strings.HasPrefix(line, "$ "):
				if cur != nil {
					out = append(out, *cur)
				}
				cur, cmd = nil, []string{strings.TrimPrefix(line, "$ ")}
			case cmd != nil:
				cmd = append(cmd, strings.TrimSpace(line))
			case cur != nil:
				cur.output += line + "\n"
				continue
			default:
				continue
			}
			if !strings.HasSuffix(line, " -") {
				continue
			}
			full := strings.Join(cmd, "")
			cmd = nil
			m := echoCommand.FindStringSubmatch(full)
			if m == nil {
				continue // not a validate example
			}
			cur = &docExample{line: i + 1, recipe: m[1], args: strings.Fields(m[2])}
		}
		if cur != nil {
			out = append(out, *cur)
		}
	}
	return out
}

var shownProblem = regexp.MustCompile(`"field": "([^"]*)",\s*"code": "([a-z0-9_]+)"`)

// TestValidateDocExamples runs every `fisherman validate` example of
// docs/VALIDATE.md and holds the document to the real serialization
// (tuna-os/fisherman#326): a complete output must equal the command's
// output byte for byte, and an elided one ("...") must show only field and
// code pairs that the command really reports.
func TestValidateDocExamples(t *testing.T) {
	stubTPM(t, false)
	doc, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "VALIDATE.md"))
	if err != nil {
		t.Fatal(err)
	}
	examples := validateDocExamples(t, string(doc))
	checked := 0
	for _, ex := range examples {
		if strings.TrimSpace(ex.output) == "" {
			continue
		}
		checked++
		var stdout, stderr bytes.Buffer
		runValidateJSON(ex.args, strings.NewReader(ex.recipe), &stdout, &stderr)
		got := stdout.String()
		if !strings.Contains(ex.output, "...") {
			if got != ex.output {
				t.Errorf("VALIDATE.md:%d: the example output differs from `fisherman validate %s`:\ndoc:\n%s\nreal:\n%s",
					ex.line, strings.Join(ex.args, " "), ex.output, got)
			}
			continue
		}
		pairs := shownProblem.FindAllStringSubmatch(ex.output, -1)
		if len(pairs) == 0 {
			t.Errorf("VALIDATE.md:%d: an elided example shows no field and code", ex.line)
		}
		for _, p := range pairs {
			want := regexp.MustCompile(`"field": "` + regexp.QuoteMeta(p[1]) + `",\s*"code": "` + p[2] + `"`)
			if !want.MatchString(got) {
				t.Errorf("VALIDATE.md:%d: the example shows field %q with code %q; the real output does not have it:\n%s",
					ex.line, p[1], p[2], got)
			}
		}
	}
	if checked < 3 {
		t.Fatalf("checked only %d examples of %d commands; the parser missed them", checked, len(examples))
	}
}
