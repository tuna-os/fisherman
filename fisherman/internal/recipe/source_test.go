package recipe

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr string // "" = success
		empty   bool   // want ErrEmptyRecipe
	}{
		{name: "valid", in: `{"disk":"/dev/sda","image":"ghcr.io/example/os:stable","hostname":"h"}`},
		{name: "valid with surrounding whitespace", in: "\n  {\"hostname\":\"h\"}\n"},
		{name: "empty", in: "", empty: true},
		{name: "whitespace only", in: " \n\t\n", empty: true},
		{name: "truncated", in: `{"disk":"/dev/sda"`, wantErr: "parsing recipe"},
		{name: "not json", in: "disk=/dev/sda", wantErr: "parsing recipe"},
		{name: "trailing garbage", in: `{"disk":"/dev/sda"} x`, wantErr: "parsing recipe"},
		{name: "wrong type", in: `{"disk":42}`, wantErr: "parsing recipe"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := Parse([]byte(tt.in))
			switch {
			case tt.empty:
				if !errors.Is(err, ErrEmptyRecipe) {
					t.Fatalf("Parse() error = %v, want ErrEmptyRecipe", err)
				}
			case tt.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Parse() error = %v, want containing %q", err, tt.wantErr)
				}
			default:
				if err != nil {
					t.Fatalf("Parse() unexpected error: %v", err)
				}
				if r == nil {
					t.Fatal("Parse() returned nil recipe")
				}
			}
		})
	}
}

func TestReadFromStdin(t *testing.T) {
	r, err := Read(strings.NewReader(`{"disk":"/dev/vda","image":"ghcr.io/example/os:stable","hostname":"box"}`))
	if err != nil {
		t.Fatalf("Read() error: %v", err)
	}
	if r.Disk != "/dev/vda" || r.Image != "ghcr.io/example/os:stable" || r.Hostname != "box" {
		t.Errorf("Read() = %+v", r)
	}

	if _, err := Read(strings.NewReader("")); !errors.Is(err, ErrEmptyRecipe) {
		t.Errorf("Read(empty) error = %v, want ErrEmptyRecipe", err)
	}
	if _, err := Read(strings.NewReader("{")); err == nil || !strings.Contains(err.Error(), "parsing recipe") {
		t.Errorf("Read(invalid) error = %v, want a parse error", err)
	}
	if _, err := Read(errReader{}); err == nil || !strings.Contains(err.Error(), "reading recipe from stdin") {
		t.Errorf("Read(failing reader) error = %v", err)
	}
}

func TestReadRejectsOversizedRecipe(t *testing.T) {
	big := `{"hostname":"` + strings.Repeat("a", MaxRecipeBytes) + `"}`
	_, err := Read(strings.NewReader(big))
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("Read(oversized) error = %v, want a size error", err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestLoadArg(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recipe.json")
	if err := os.WriteFile(path, []byte(`{"hostname":"from-file"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stdin := strings.NewReader(`{"hostname":"from-stdin"}`)

	r, err := LoadArg(path, stdin)
	if err != nil || r.Hostname != "from-file" {
		t.Fatalf("LoadArg(path) = %+v, %v", r, err)
	}
	r, err = LoadArg(StdinArg, stdin)
	if err != nil || r.Hostname != "from-stdin" {
		t.Fatalf("LoadArg(-) = %+v, %v", r, err)
	}
	// A file literally named "-" is not reachable through LoadArg; ./- is.
	if _, err := LoadArg(StdinArg, strings.NewReader("")); !errors.Is(err, ErrEmptyRecipe) {
		t.Errorf("LoadArg(-, empty) error = %v, want ErrEmptyRecipe", err)
	}
}

func TestLoadEmptyFileIsEmptyRecipe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.Is(err, ErrEmptyRecipe) {
		t.Errorf("Load(empty file) error = %v, want ErrEmptyRecipe", err)
	}
}

// The additionalImageStores contract: absent or null means "discover the
// host's stores", a list (even []) is used as given. That rests on
// encoding/json leaving the slice nil only for absent and null.
func TestAdditionalImageStoresNilVersusEmpty(t *testing.T) {
	for _, tt := range []struct {
		in      string
		wantNil bool
		wantLen int
	}{
		{`{}`, true, 0},
		{`{"additionalImageStores":null}`, true, 0},
		{`{"additionalImageStores":[]}`, false, 0},
		{`{"additionalImageStores":["/srv/store"]}`, false, 1},
	} {
		r, err := Parse([]byte(tt.in))
		if err != nil {
			t.Fatalf("Parse(%s): %v", tt.in, err)
		}
		if (r.AdditionalImageStores == nil) != tt.wantNil || len(r.AdditionalImageStores) != tt.wantLen {
			t.Errorf("Parse(%s).AdditionalImageStores = %#v, want nil=%v len=%d",
				tt.in, r.AdditionalImageStores, tt.wantNil, tt.wantLen)
		}
	}
}

func TestValidateImage(t *testing.T) {
	for _, tt := range []struct {
		image   string
		live    bool
		wantErr bool
	}{
		{"ghcr.io/example/os:stable", false, false},
		{"ghcr.io/example/os:stable", true, false},
		{"containers-storage:ghcr.io/example/os:stable", false, false},
		{"", true, false}, // live media: install the running image
		{"", false, true}, // installed host: nothing to install
		{"   ", false, true},
	} {
		err := (&Recipe{Image: tt.image}).ValidateImage(tt.live)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateImage(image=%q, live=%v) = %v, wantErr %v", tt.image, tt.live, err, tt.wantErr)
		}
		if err != nil && !errors.Is(err, ErrImageRequired) {
			t.Errorf("ValidateImage error = %v, want ErrImageRequired", err)
		}
	}
}
