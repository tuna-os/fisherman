package recipe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// UnknownFields returns one Problem (CodeRecipeUnknownField) for every key in
// the recipe JSON that the Recipe type does not have, at any depth, in
// document-independent sorted order. Field is the JSON path of the key:
// "encrypton", "user.pasword", "customMounts[0].fs".
//
// encoding/json drops a key it does not know. For a recipe that is a
// fail-open: a misspelt "encryption" installs unencrypted, and a field a
// newer frontend sends to an older fisherman is ignored, while the user saw
// it on the confirm page. The install and the full `validate` therefore
// refuse such a recipe; `validate --partial` does not run this (a page sends
// a fragment, and may be ahead of the backend it asks).
//
// Key matching follows encoding/json, which is case-insensitive: a key is
// unknown exactly when the decoder would drop it. Data that is not a JSON
// object, or does not parse, yields nil; Parse reports that.
func UnknownFields(data []byte) []Problem {
	var ps []Problem
	unknownIn(data, reflect.TypeOf(Recipe{}), "", &ps)
	return ps
}

func unknownIn(data []byte, t reflect.Type, path string, ps *[]Problem) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	data = bytes.TrimSpace(data)
	kind := t.Kind()
	if kind == reflect.Struct {
		if len(data) == 0 || data[0] != '{' {
			return
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(data, &obj); err != nil {
			return
		}
		fields := jsonFields(t)
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := k
			if path != "" {
				p = path + "." + k
			}
			ft, ok := lookupField(fields, k)
			if !ok {
				*ps = append(*ps, Problem{p, CodeRecipeUnknownField,
					fmt.Sprintf("unknown recipe field %q: this fisherman does not know it, and would ignore it", p)})
				continue
			}
			unknownIn(obj[k], ft, p, ps)
		}
	} else if kind == reflect.Slice || kind == reflect.Array {
		if len(data) == 0 || data[0] != '[' {
			return
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(data, &arr); err != nil {
			return
		}
		for i, el := range arr {
			unknownIn(el, t.Elem(), fmt.Sprintf("%s[%d]", path, i), ps)
		}
	}
}

// jsonFields maps the JSON name of each exported field of struct type t to
// the field's type.
func jsonFields(t reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type, t.NumField())
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
		fields[name] = f.Type
	}
	return fields
}

func lookupField(fields map[string]reflect.Type, key string) (reflect.Type, bool) {
	if t, ok := fields[key]; ok {
		return t, true
	}
	for name, t := range fields {
		if strings.EqualFold(name, key) {
			return t, true
		}
	}
	return nil, false
}
