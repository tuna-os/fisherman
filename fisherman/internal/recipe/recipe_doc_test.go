package recipe

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// docTypes are the "Type" column values of docs/RECIPE.md for the kinds a
// recipe field has.
var docTypes = map[reflect.Kind]string{
	reflect.String: "string",
	reflect.Bool:   "boolean",
	reflect.Struct: "object",
	reflect.Int64:  "integer",
}

// docType is the "Type" column of docs/RECIPE.md for a Go field type.
func docType(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() == reflect.Slice {
		return docType(t.Elem()) + " array"
	}
	if name, ok := docTypes[t.Kind()]; ok {
		return name
	}
	return t.Kind().String()
}

// recipeFields returns the JSON path and the doc type of every field of t
// and of the structs below it: "encryption.type", "customMounts[].target".
func recipeFields(t reflect.Type, prefix string, out map[string]string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		path := prefix + name
		out[path] = docType(f.Type)
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		switch {
		case ft.Kind() == reflect.Struct:
			recipeFields(ft, path+".", out)
		case ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.Struct:
			recipeFields(ft.Elem(), path+"[].", out)
		}
	}
}

// TestRecipeReferenceCoversEveryField holds docs/RECIPE.md to the Recipe
// type (tuna-os/fisherman#320): one row per JSON key at every depth, no
// row for a key the type does not have, and the right type for each.
func TestRecipeReferenceCoversEveryField(t *testing.T) {
	want := map[string]string{}
	recipeFields(reflect.TypeOf(Recipe{}), "", want)
	if len(want) < 50 {
		t.Fatalf("found only %d recipe fields; the walk missed them", len(want))
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "RECIPE.md"))
	if err != nil {
		t.Fatal(err)
	}
	// | `field` | type | ...
	row := regexp.MustCompile("(?m)^\\| `([A-Za-z0-9.\\[\\]]+)` \\| ([a-z ]+) \\|")
	got := map[string]string{}
	for _, m := range row.FindAllStringSubmatch(docSection(t, string(b), "## Fields"), -1) {
		if _, dup := got[m[1]]; dup {
			t.Errorf("RECIPE.md: %q has more than one row", m[1])
		}
		got[m[1]] = m[2]
	}
	for path, typ := range want {
		doc, ok := got[path]
		switch {
		case !ok:
			t.Errorf("RECIPE.md has no row for recipe field %q (%s)", path, typ)
		case doc != typ:
			t.Errorf("RECIPE.md: %q is %q; the Recipe type says %q", path, doc, typ)
		}
	}
	for path := range got {
		if _, ok := want[path]; !ok {
			t.Errorf("RECIPE.md has a row for %q, which the Recipe type does not have", path)
		}
	}
}

// TestRecipeReferenceExample: the example of docs/RECIPE.md parses, has no
// unknown key, and breaks no rule that does not read the machine.
func TestRecipeReferenceExample(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "RECIPE.md"))
	if err != nil {
		t.Fatal(err)
	}
	ex := docSection(t, string(b), "## Example")
	start, end := strings.Index(ex, "```json\n"), strings.LastIndex(ex, "```")
	if start < 0 || end <= start {
		t.Fatal("RECIPE.md: the Example has no json block")
	}
	data := []byte(ex[start+len("```json\n") : end])
	if ps := UnknownFields(data); len(ps) > 0 {
		t.Errorf("RECIPE.md example: %v", ps)
	}
	r, err := Parse(data)
	if err != nil {
		t.Fatalf("RECIPE.md example: %v", err)
	}
	for _, p := range r.Problems() {
		if p.Code != CodeDiskNotFound {
			t.Errorf("RECIPE.md example: %v", p)
		}
	}
}
