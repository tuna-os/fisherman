package recipe

import (
	"strings"
	"testing"
)

func TestCheckLocale(t *testing.T) {
	for _, ok := range []string{"", "en_US.UTF-8", "de_DE.UTF-8", "de_DE", "C", "C.UTF-8", "POSIX",
		"sr_RS@latin", "ca_ES.UTF-8@valencia", "yue_HK.UTF-8", "ja_JP.eucJP", "en_US.ISO-8859-1"} {
		if ps := CheckLocale(ok); len(ps) != 0 {
			t.Errorf("CheckLocale(%q) = %v, want valid", ok, ps)
		}
	}
	for _, bad := range []string{"german", "de_DE.UTF-8\nLC_ALL=C", "de DE", "../../etc/passwd", "de_DE.UTF-8;rm",
		"DE_de", "en_US.", "en_US.UTF-8 ", "$(id)", strings.Repeat("a", 70)} {
		ps := CheckLocale(bad)
		if len(ps) != 1 || ps[0].Code != CodeLocaleInvalid || ps[0].Field != "locale" {
			t.Errorf("CheckLocale(%q) = %v, want %s", bad, ps, CodeLocaleInvalid)
		}
	}
}

func TestCheckKeyboard(t *testing.T) {
	if ps := CheckKeyboard(nil); len(ps) != 0 {
		t.Errorf("nil keyboard: %v", ps)
	}
	for _, ok := range []KeyboardSpec{
		{Layout: "us"},
		{Layout: "de", Variant: "nodeadkeys", Model: "pc105"},
		{Layout: "us,ru", Variant: ",phonetic"},
		{Layout: "gb", Variant: "mac_intl"},
		{Layout: "us", Model: "pc105+inet"},
		{Layout: "fr", Variant: "oss_latin9"},
	} {
		ok := ok
		if ps := CheckKeyboard(&ok); len(ps) != 0 {
			t.Errorf("CheckKeyboard(%+v) = %v, want valid", ok, ps)
		}
	}
	tests := []struct {
		kb   KeyboardSpec
		want string
	}{
		// layout is required when the object is present.
		{KeyboardSpec{}, "keyboard.layout:keyboard_layout_required"},
		{KeyboardSpec{Variant: "nodeadkeys"}, "keyboard.layout:keyboard_layout_required"},
		// The keymap reaches the kernel command line: no space, no quote.
		{KeyboardSpec{Layout: "de rd.break"}, "keyboard.layout:keyboard_layout_invalid"},
		{KeyboardSpec{Layout: `de"`}, "keyboard.layout:keyboard_layout_invalid"},
		{KeyboardSpec{Layout: "us,"}, "keyboard.layout:keyboard_layout_invalid"},
		{KeyboardSpec{Layout: "../x"}, "keyboard.layout:keyboard_layout_invalid"},
		{KeyboardSpec{Layout: "de", Variant: "no dead"}, "keyboard.variant:keyboard_variant_invalid"},
		{KeyboardSpec{Layout: "de", Variant: "x\ny"}, "keyboard.variant:keyboard_variant_invalid"},
		{KeyboardSpec{Layout: "de", Model: `pc105" Option "x`}, "keyboard.model:keyboard_model_invalid"},
		{KeyboardSpec{Layout: "de", Model: "a,b"}, "keyboard.model:keyboard_model_invalid"},
	}
	for _, tt := range tests {
		kb := tt.kb
		got := codes(CheckKeyboard(&kb))
		if len(got) != 1 || got[0] != tt.want {
			t.Errorf("CheckKeyboard(%+v) = %v, want [%s]", tt.kb, got, tt.want)
		}
	}
	// Every bad field is reported, not only the first.
	all := KeyboardSpec{Layout: "d e", Variant: "v v", Model: "m m"}
	if got := codes(CheckKeyboard(&all)); len(got) != 3 {
		t.Errorf("three bad fields: %v", got)
	}
}

func TestCheckTimezone(t *testing.T) {
	for _, ok := range []string{"", "UTC", "Europe/Berlin", "America/Argentina/Buenos_Aires", "Etc/GMT+1",
		"Etc/GMT-14", "Asia/Ho_Chi_Minh", "America/Port-au-Prince", "GMT0", "EST5EDT"} {
		if ps := CheckTimezone(ok); len(ps) != 0 {
			t.Errorf("CheckTimezone(%q) = %v, want valid", ok, ps)
		}
	}
	for _, bad := range []string{"Europe/../../etc/shadow", "/etc/passwd", "Europe/Berlin ", "Europe//Berlin",
		"Europe/Berlin/", "Central European Time", "+0100", "Europe/Berlin\n", "..", strings.Repeat("A", 70)} {
		ps := CheckTimezone(bad)
		if len(ps) != 1 || ps[0].Code != CodeTimezoneInvalid || ps[0].Field != "timezone" {
			t.Errorf("CheckTimezone(%q) = %v, want %s", bad, ps, CodeTimezoneInvalid)
		}
	}
}

// The three fields parse, are optional, and are part of the rule set the
// install and `validate` run.
func TestLocaleFieldsInTheRuleSet(t *testing.T) {
	r, err := Parse([]byte(`{"disk":"/dev/null","filesystem":"xfs","hostname":"h","image":"x",
		"locale":"de_DE.UTF-8","keyboard":{"layout":"de","variant":"nodeadkeys","model":"pc105"},"timezone":"Europe/Berlin"}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.Locale != "de_DE.UTF-8" || r.Timezone != "Europe/Berlin" || r.Keyboard == nil ||
		*r.Keyboard != (KeyboardSpec{"de", "nodeadkeys", "pc105"}) {
		t.Errorf("parsed %q %q %+v", r.Locale, r.Timezone, r.Keyboard)
	}
	for _, p := range r.Problems() {
		if strings.HasPrefix(p.Field, "keyboard") || p.Field == "locale" || p.Field == "timezone" {
			t.Errorf("valid settings refused: %v", p)
		}
	}

	bad := Recipe{Disk: "/dev/null", Filesystem: "xfs", Hostname: "h", Image: "x",
		Locale: "x y", Keyboard: &KeyboardSpec{}, Timezone: "a b"}
	got := strings.Join(codes(bad.Problems()), " ")
	for _, want := range []string{"locale:locale_invalid", "keyboard.layout:keyboard_layout_required", "timezone:timezone_invalid"} {
		if !strings.Contains(got, want) {
			t.Errorf("Problems() lack %s: %s", want, got)
		}
	}
	// A key inside keyboard that the schema does not have is not dropped.
	if _, err := Parse([]byte(`{"keyboard":{"layout":"de","options":"x"}}`)); err == nil {
		t.Error(`"keyboard.options" parsed`)
	}
	// Absent fields are no problem at all.
	none := Recipe{Disk: "/dev/null", Filesystem: "xfs", Hostname: "h", Image: "x"}
	for _, p := range none.Problems() {
		if strings.HasPrefix(p.Field, "keyboard") || p.Field == "locale" || p.Field == "timezone" {
			t.Errorf("absent settings refused: %v", p)
		}
	}
}
