package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/fisherman/internal/recipe"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/validate/*.golden")

func stubTPM(t *testing.T, usable bool) {
	t.Helper()
	old := tpm2Usable
	tpm2Usable = func() bool { return usable }
	t.Cleanup(func() { tpm2Usable = old })
}

// TestValidateJSONGolden pins the `validate --json` output byte for byte: the
// schema in docs/VALIDATE.md is a contract frontends parse.
func TestValidateJSONGolden(t *testing.T) {
	stubTPM(t, false)
	cases := []struct {
		name    string
		partial bool
		exit    int
	}{
		{"valid", false, 0},
		{"invalid", false, 1},
		{"tpm", false, 1},
		{"malformed", false, 1},
		{"partial-hostname", true, 1},
		{"partial-user", true, 1},
		{"partial-page", true, 1},
		{"partial-valid", true, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := filepath.Join("testdata", "validate", c.name+".json")
			args := []string{"--json", in}
			if c.partial {
				args = []string{"--json", "--partial", in}
			}
			var out, errb bytes.Buffer
			if got := runValidateJSON(args, nil, &out, &errb); got != c.exit {
				t.Fatalf("exit %d, want %d (stderr %q)", got, c.exit, errb.String())
			}
			if errb.Len() != 0 {
				t.Errorf("stderr not empty: %q", errb.String())
			}
			golden := filepath.Join("testdata", "validate", c.name+".golden")
			if *updateGolden {
				if err := os.WriteFile(golden, out.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out.Bytes(), want) {
				t.Errorf("output differs from %s:\ngot:\n%s\nwant:\n%s", golden, out.String(), want)
			}
			var res validateResult
			if err := json.Unmarshal(out.Bytes(), &res); err != nil {
				t.Fatalf("output is not JSON: %v", err)
			}
			if res.Valid != (c.exit == 0) || res.Errors == nil || res.ProtocolVersion != 1 {
				t.Errorf("inconsistent result: %+v", res)
			}
		})
	}
}

func TestValidateJSONStdin(t *testing.T) {
	stubTPM(t, true)
	var out bytes.Buffer
	stdin := strings.NewReader(`{"hostname":"bad host"}`)
	if got := runValidateJSON([]string{"--json", "--partial", "-"}, stdin, &out, &bytes.Buffer{}); got != 1 {
		t.Fatalf("exit %d, want 1", got)
	}
	var res validateResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 1 || res.Errors[0].Code != recipe.CodeHostnameInvalidChar || res.Errors[0].Field != "hostname" {
		t.Errorf("errors = %+v", res.Errors)
	}

	// A whole recipe on stdin, with a usable TPM: valid.
	out.Reset()
	stdin = strings.NewReader(`{"disk":"/dev/null","filesystem":"xfs","hostname":"h","encryption":{"type":"tpm2-luks"}}`)
	if got := runValidateJSON([]string{"-", "--json"}, stdin, &out, &bytes.Buffer{}); got != 0 {
		t.Fatalf("exit %d, want 0: %s", got, out.String())
	}
}

func TestValidateJSONUnreadable(t *testing.T) {
	var out bytes.Buffer
	if got := runValidateJSON([]string{"--json", filepath.Join(t.TempDir(), "missing.json")}, nil, &out, &bytes.Buffer{}); got != 1 {
		t.Fatalf("exit %d, want 1", got)
	}
	var res validateResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Valid || len(res.Errors) != 1 || res.Errors[0].Code != recipe.CodeRecipeUnreadable || res.Errors[0].Field != "" {
		t.Errorf("result = %+v", res)
	}
}

func TestValidateJSONUsage(t *testing.T) {
	for _, args := range [][]string{
		{"--json"},
		{"--json", "a.json", "b.json"},
		{"--json", "--choices", "a.json"},
		{"--json", "--field", "hostname=x"},
	} {
		var out, errb bytes.Buffer
		if got := runValidateJSON(args, nil, &out, &errb); got != 2 {
			t.Errorf("%v: exit %d, want 2", args, got)
		}
		if out.Len() != 0 || !strings.Contains(errb.String(), "usage:") {
			t.Errorf("%v: stdout %q stderr %q", args, out.String(), errb.String())
		}
	}
	var out bytes.Buffer
	if got := runValidateJSON([]string{"--json", "--help"}, nil, &out, &bytes.Buffer{}); got != 0 || !strings.Contains(out.String(), "--partial") {
		t.Errorf("--help: exit %d, out %q", got, out.String())
	}
}

// TestInstallProblemsMatchesValidate: the install path and validate share one
// function, and it adds the TPM check to recipe.Problems.
func TestInstallProblemsTPM(t *testing.T) {
	r := &recipe.Recipe{Disk: "/dev/null", Filesystem: "xfs", Hostname: "h", Encryption: recipe.Encryption{Type: "tpm2-luks"}}
	stubTPM(t, false)
	if ps := installProblems(r); len(ps) != 1 || ps[0].Code != recipe.CodeEncryptionTPMUnavailable {
		t.Errorf("no TPM: %+v", ps)
	}
	stubTPM(t, true)
	if ps := installProblems(r); len(ps) != 0 {
		t.Errorf("TPM usable: %+v", ps)
	}
}

// TestInstallRejectsBadUsernameBeforeDiskWork runs the real install entry
// point (main) on a recipe whose only fault is the username. Before F4 the
// username reached useradd in the configure step, after partitioning and the
// OS install. Now the install must refuse it up front: exit 1, an error
// event naming the problem, and no host tool run at all.
func TestInstallRejectsBadUsernameBeforeDiskWork(t *testing.T) {
	if os.Getenv("FISHERMAN_USERNAME_CHILD") == "1" {
		os.Args = []string{os.Args[0], os.Getenv("FISHERMAN_USERNAME_RECIPE")}
		main()
		os.Exit(99)
	}
	dir, err := os.MkdirTemp(".", ".username-preflight-")
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "fisherman-test")
	if err = os.WriteFile(binary, raw, 0o700); err != nil {
		t.Fatal(err)
	}
	// Every tool an install would run first is a stub that records its argv
	// (the tools/ dir next to the binary is first on PATH; see expandPath).
	tools := filepath.Join(dir, "tools")
	if err = os.Mkdir(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(dir, "argv")
	for _, name := range []string{"sfdisk", "mkfs.fat", "mkfs.ext4", "mkfs.xfs", "skopeo", "podman", "wipefs", "sgdisk", "useradd"} {
		script := "#!/bin/sh\nprintf '%s %s\\n' \"${0##*/}\" \"$*\" >> \"$CAPTURE\"\nexit 0\n"
		if err = os.WriteFile(filepath.Join(tools, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	disk := filepath.Join(dir, "owned-file")
	if err = os.WriteFile(disk, []byte("private fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, username := range []string{"Bad User", "root"} {
		raw, err = json.Marshal(map[string]any{
			"disk": disk, "filesystem": "xfs", "hostname": "fixture",
			"image":      "example.invalid/never-pulled:latest",
			"encryption": map[string]any{"type": "none"},
			"user":       map[string]any{"username": username, "password": "pw"},
		})
		if err != nil {
			t.Fatal(err)
		}
		recipePath := filepath.Join(dir, "recipe.json")
		if err = os.WriteFile(recipePath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		command := exec.CommandContext(ctx, binary, "-test.run=^TestInstallRejectsBadUsernameBeforeDiskWork$")
		command.Env = append(os.Environ(), "FISHERMAN_USERNAME_CHILD=1", "FISHERMAN_USERNAME_RECIPE="+recipePath, "CAPTURE="+capture)
		output, runErr := command.CombinedOutput()
		cancel()
		exit, ok := runErr.(*exec.ExitError)
		if !ok || exit.ExitCode() != exitFailure {
			t.Fatalf("username %q: want exit 1, got %v\n%s", username, runErr, output)
		}
		if !strings.Contains(string(output), `"type":"error"`) || !strings.Contains(string(output), "invalid recipe: username") {
			t.Errorf("username %q: no error event naming the username:\n%s", username, output)
		}
		if b, err := os.ReadFile(capture); err == nil {
			t.Fatalf("username %q: host tools ran before the recipe was rejected:\n%s", username, b)
		}
		if b, err := os.ReadFile(disk); err != nil || string(b) != "private fixture" {
			t.Fatal("owned file changed")
		}
	}
}
