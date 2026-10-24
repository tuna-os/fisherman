package recipe

import (
	"encoding/json"
	"testing"
)

func TestFlatpakIntentPresence(t *testing.T) {
	for _, raw := range []string{`{}`, `{"flatpaks":[]}`, `{"flatpaks":["org.mozilla.firefox"]}`} {
		var r Recipe
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			t.Fatal(err)
		}
		if r.FlatpakIntent != nil {
			t.Fatal("legacy recipe became authoritative")
		}
	}
	var r Recipe
	raw := `{"flatpakIntent":{"version":1,"live":{"mode":"inherit"},"target":{"mode":"replace","apps":[]}}}`
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatal(err)
	}
	if err := r.FlatpakIntent.Validate(); err != nil {
		t.Fatal(err)
	}
	roundtrip, _ := json.Marshal(r)
	var got Recipe
	json.Unmarshal(roundtrip, &got)
	if got.FlatpakIntent.Target.Apps == nil {
		t.Fatal("explicit empty lost in transport")
	}
}
func TestFlatpakIntentValidation(t *testing.T) {
	for _, raw := range []string{
		`{"version":2,"live":{"mode":"inherit"},"target":{"mode":"replace","apps":[]}}`,
		`{"version":1,"live":{"mode":"inherit"},"target":{"mode":"replace"}}`,
		`{"version":1,"live":{"mode":"inherit"},"target":{"mode":"replace","apps":[{"id":"org.mozilla.firefox","branch":"stable","arch":"x86_64","source":{"kind":"remote","identity":"https://elsewhere.invalid/"}}]}}`,
		`{"version":1,"live":{"mode":"inherit"},"target":{"mode":"replace","apps":[{"id":"org.bootcinstaller.Installer","branch":"master","arch":"x86_64","source":{"kind":"bundle","identity":"sha256:bundle"}}]}}`,
	} {
		var i FlatpakIntent
		json.Unmarshal([]byte(raw), &i)
		if i.Validate() == nil {
			t.Errorf("accepted %s", raw)
		}
		r := Recipe{FlatpakIntent: &i}
		found := false
		for _, p := range r.Problems() {
			if p.Code == "flatpak_intent_invalid" {
				found = true
			}
		}
		if !found {
			t.Error("pre-install validation omitted intent")
		}
	}
}

func TestFlatpakIntentStrictEnvelopeOnly(t *testing.T) {
	valid := `{"version":1,"live":{"mode":"inherit"},"target":{"mode":"replace","apps":[]}}`
	for _, raw := range []string{
		`{"version":1,"typo":true,"live":{"mode":"inherit"},"target":{"mode":"replace","apps":[]}}`,
		`{"version":1,"live":{"mode":"inherit"},"target":{"mode":"replace","app":[]}}`,
		`{"version":1,"live":{"mode":"inherit"},"target":{"mode":"replace","apps":[{"id":"org.example.App","branch":"stable","arch":"x86_64","source":{"kind":"remote","identity":"https://dl.flathub.org/repo/","url":"ignored"}}]}}`,
	} {
		var r Recipe
		if err := json.Unmarshal([]byte(`{"flatpakIntent":`+raw+`}`), &r); err == nil {
			t.Fatalf("unknown key accepted: %s", raw)
		}
	}
	var r Recipe
	if err := json.Unmarshal([]byte(`{"legacyUnknown":true,"flatpakIntent":`+valid+`}`), &r); err != nil {
		t.Fatalf("changed legacy tolerance: %v", err)
	}
}
func TestFlatpakIntentRejectsUnimplementedLiveReplace(t *testing.T) {
	i := FlatpakIntent{Version: 1, Live: FlatpakSelection{Mode: "replace", Apps: []FlatpakApp{}}, Target: FlatpakSelection{Mode: "replace", Apps: []FlatpakApp{}}}
	if err := i.Validate(); err == nil {
		t.Fatal("unimplemented live scope accepted")
	}
}
