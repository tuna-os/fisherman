package recipe_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/recipe"
)

var update = flag.Bool("update", false, "rewrite data/encryption-choices.json from EncryptionChoices")

// code returns the code of the only problem in ps, "" for none, and fails the
// test on more than one: each Check* reports at most one problem per field.
func code(t *testing.T, ps []recipe.Problem) string {
	t.Helper()
	switch len(ps) {
	case 0:
		return ""
	case 1:
		return ps[0].Code
	default:
		t.Fatalf("want at most one problem, got %+v", ps)
		return ""
	}
}

func TestCheckHostname(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"linux", ""},
		{"my-pc", ""},
		{"PC01", ""},
		{"1host", ""}, // RFC 1123 allows a leading digit
		{"a", ""},
		{"host.example.lan", ""},
		{strings.Repeat("a", 63), ""},
		{strings.Repeat("a", 62) + ".b", ""}, // exactly 64
		{strings.Repeat("a", 63) + ".b", recipe.CodeHostnameTooLong}, // 65
		{"", recipe.CodeHostnameRequired},
		{strings.Repeat("a", 32) + "." + strings.Repeat("b", 32), recipe.CodeHostnameTooLong}, // 65
		{strings.Repeat("a", 64), recipe.CodeHostnameLabelTooLong},
		{strings.Repeat("a", 65), recipe.CodeHostnameTooLong},
		{"my_pc", recipe.CodeHostnameInvalidChar},
		{"my pc", recipe.CodeHostnameInvalidChar},
		{"héllo", recipe.CodeHostnameInvalidChar},
		{"host/1", recipe.CodeHostnameInvalidChar},
		{".host", recipe.CodeHostnameLabelEmpty},
		{"host.", recipe.CodeHostnameLabelEmpty},
		{"a..b", recipe.CodeHostnameLabelEmpty},
		{"-host", recipe.CodeHostnameLabelHyphen},
		{"host-", recipe.CodeHostnameLabelHyphen},
		{"a.-b", recipe.CodeHostnameLabelHyphen},
	}
	for _, c := range cases {
		ps := recipe.CheckHostname(c.in)
		if got := code(t, ps); got != c.want {
			t.Errorf("CheckHostname(%q) = %q, want %q", c.in, got, c.want)
		}
		for _, p := range ps {
			if p.Field != "hostname" || p.Message == "" {
				t.Errorf("CheckHostname(%q): bad problem %+v", c.in, p)
			}
		}
	}
}

func TestCheckUsername(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""}, // empty skips user creation
		{"alice", ""},
		{"_svc", ""},
		{"a", ""},
		{"jane-doe_2", ""},
		{strings.Repeat("a", 32), ""},
		{strings.Repeat("a", 33), recipe.CodeUsernameTooLong},
		{"Alice", recipe.CodeUsernameInvalidStart},
		{"1alice", recipe.CodeUsernameInvalidStart},
		{"-alice", recipe.CodeUsernameInvalidStart},
		{".alice", recipe.CodeUsernameInvalidStart},
		{"alIce", recipe.CodeUsernameInvalidChar},
		{"al ice", recipe.CodeUsernameInvalidChar},
		{"al.ice", recipe.CodeUsernameInvalidChar},
		{"alice$", recipe.CodeUsernameInvalidChar},
		{"jöhn", recipe.CodeUsernameInvalidChar},
		{"root", recipe.CodeUsernameReserved},
		{"nobody", recipe.CodeUsernameReserved},
		{"wheel", recipe.CodeUsernameReserved},
		{"docker", recipe.CodeUsernameReserved},
		{"systemd-network", recipe.CodeUsernameReserved},
		{"systemd-anything", recipe.CodeUsernameReserved},
		{"rooted", ""},
	}
	for _, c := range cases {
		ps := recipe.CheckUsername(c.in)
		if got := code(t, ps); got != c.want {
			t.Errorf("CheckUsername(%q) = %q, want %q", c.in, got, c.want)
		}
		for _, p := range ps {
			if p.Field != "user.username" {
				t.Errorf("CheckUsername(%q): field %q", c.in, p.Field)
			}
		}
	}
}

func TestCheckFilesystem(t *testing.T) {
	for _, fs := range []string{"xfs", "ext4", "btrfs", "zfs"} {
		if ps := recipe.CheckFilesystem(fs); len(ps) != 0 {
			t.Errorf("CheckFilesystem(%q) = %+v, want none", fs, ps)
		}
	}
	for _, fs := range []string{"", "XFS", "ext3", "vfat", "ntfs", "f2fs"} {
		if got := code(t, recipe.CheckFilesystem(fs)); got != recipe.CodeFilesystemUnsupported {
			t.Errorf("CheckFilesystem(%q) = %q, want %q", fs, got, recipe.CodeFilesystemUnsupported)
		}
	}
}

func TestCheckLayoutCombos(t *testing.T) {
	cases := []struct {
		name string
		r    recipe.Recipe
		want []string
	}{
		{"plain xfs", recipe.Recipe{Filesystem: "xfs"}, nil},
		{"btrfs subvolumes", recipe.Recipe{Filesystem: "btrfs", BtrfsSubvolumes: true}, nil},
		{"subvolumes on xfs", recipe.Recipe{Filesystem: "xfs", BtrfsSubvolumes: true}, []string{recipe.CodeBtrfsSubvolumesRequireBtrfs}},
		{"composefs on ext4", recipe.Recipe{Filesystem: "ext4", ComposeFsBackend: true}, nil},
		{"composefs on xfs", recipe.Recipe{Filesystem: "xfs", ComposeFsBackend: true}, []string{recipe.CodeComposeFsRequiresFsVerity}},
		{"zfs none", recipe.Recipe{Filesystem: "zfs", Encryption: recipe.Encryption{Type: "none"}}, nil},
		{"zfs luks", recipe.Recipe{Filesystem: "zfs", Encryption: recipe.Encryption{Type: "luks-passphrase", Passphrase: "x"}}, []string{recipe.CodeEncryptionUnsupportedOnZFS}},
		{"two at once", recipe.Recipe{Filesystem: "xfs", BtrfsSubvolumes: true, ComposeFsBackend: true},
			[]string{recipe.CodeBtrfsSubvolumesRequireBtrfs, recipe.CodeComposeFsRequiresFsVerity}},
	}
	for _, c := range cases {
		r := c.r
		var got []string
		for _, p := range recipe.CheckLayoutCombos(&r) {
			got = append(got, p.Code)
		}
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: codes %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCheckEncryption(t *testing.T) {
	cases := []struct {
		e         recipe.Encryption
		want      string
		wantField string
	}{
		{recipe.Encryption{}, "", ""},
		{recipe.Encryption{Type: "none"}, "", ""},
		{recipe.Encryption{Type: "tpm2-luks"}, "", ""},
		{recipe.Encryption{Type: "luks-passphrase", Passphrase: "s"}, "", ""},
		{recipe.Encryption{Type: "tpm2-luks-passphrase", Passphrase: "s"}, "", ""},
		{recipe.Encryption{Type: "luks-passphrase"}, recipe.CodeEncryptionPassphraseRequired, "encryption.passphrase"},
		{recipe.Encryption{Type: "tpm2-luks-passphrase"}, recipe.CodeEncryptionPassphraseRequired, "encryption.passphrase"},
		{recipe.Encryption{Type: "luks"}, recipe.CodeEncryptionUnsupported, "encryption.type"},
		{recipe.Encryption{Type: "LUKS-passphrase", Passphrase: "s"}, recipe.CodeEncryptionUnsupported, "encryption.type"},
	}
	for _, c := range cases {
		ps := recipe.CheckEncryption(c.e)
		if got := code(t, ps); got != c.want {
			t.Errorf("CheckEncryption(%+v) = %q, want %q", c.e, got, c.want)
		}
		if len(ps) == 1 && ps[0].Field != c.wantField {
			t.Errorf("CheckEncryption(%+v) field %q, want %q", c.e, ps[0].Field, c.wantField)
		}
	}
}

func TestCheckTPM(t *testing.T) {
	cases := []struct {
		typ    string
		manual bool
		usable bool
		want   string
	}{
		{"none", false, false, ""},
		{"luks-passphrase", false, false, ""},
		{"tpm2-luks", false, true, ""},
		{"tpm2-luks", false, false, recipe.CodeEncryptionTPMUnavailable},
		{"tpm2-luks-passphrase", false, false, recipe.CodeEncryptionTPMUnavailable},
		// Manual layouts already reject encryption; do not report it twice.
		{"tpm2-luks", true, false, ""},
	}
	for _, c := range cases {
		r := recipe.Recipe{Encryption: recipe.Encryption{Type: c.typ}}
		if c.manual {
			r.CustomMounts = []recipe.CustomMount{{Partition: "/dev/x", Target: "/"}}
		}
		if got := code(t, r.CheckTPM(c.usable)); got != c.want {
			t.Errorf("CheckTPM(%s, manual=%v, usable=%v) = %q, want %q", c.typ, c.manual, c.usable, got, c.want)
		}
	}
}

func TestCheckImageTypeAndBootloader(t *testing.T) {
	for in, want := range map[string]string{"": "", "bootc": "", "ostree": recipe.CodeImageTypeUnsupported, "rpm": recipe.CodeImageTypeUnsupported} {
		if got := code(t, recipe.CheckImageType(in)); got != want {
			t.Errorf("CheckImageType(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"": "", "grub2": "", "systemd": "", "grub": recipe.CodeBootloaderUnsupported} {
		if got := code(t, recipe.CheckBootloader(in)); got != want {
			t.Errorf("CheckBootloader(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestValidateRejectsBadUsernameBeforeInstall is the regression for the bug
// F4 removes: useradd used to be the first thing to look at the username, in
// the configure step after the OS was on disk. Validate() — which the install
// path runs before it touches a disk — now rejects it.
func TestValidateRejectsBadUsernameBeforeInstall(t *testing.T) {
	disk := filepath.Join(t.TempDir(), "disk")
	if err := os.WriteFile(disk, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"Bad User", "root", "1abc", strings.Repeat("x", 40)} {
		r := recipe.Recipe{Disk: disk, Filesystem: "xfs", Hostname: "h", User: recipe.UserSpec{Username: u, Password: "p"}}
		err := r.Validate()
		var p recipe.Problem
		if err == nil {
			t.Errorf("Validate() accepted username %q", u)
			continue
		}
		if pp, ok := err.(recipe.Problem); ok {
			p = pp
		}
		if p.Field != "user.username" || !strings.HasPrefix(p.Code, "username_") {
			t.Errorf("username %q: got %+v", u, p)
		}
	}
	// And a bad hostname, which only one frontend checked.
	r := recipe.Recipe{Disk: disk, Filesystem: "xfs", Hostname: "my_pc"}
	if err := r.Validate(); err == nil || err.(recipe.Problem).Code != recipe.CodeHostnameInvalidChar {
		t.Errorf("Validate() on hostname my_pc = %v", err)
	}
}

// TestProblemsReportsEverything: Problems collects every problem, in the
// documented order, where Validate stops at the first.
func TestProblemsReportsEverything(t *testing.T) {
	r := recipe.Recipe{
		Disk:       "/nonexistent/fisherman-test-disk",
		Filesystem: "ntfs",
		Encryption: recipe.Encryption{Type: "luks-passphrase"},
		Hostname:   "-bad",
		User:       recipe.UserSpec{Username: "Root"},
	}
	var got []string
	for _, p := range r.Problems() {
		got = append(got, p.Field+":"+p.Code)
	}
	want := []string{
		"disk:disk_not_found",
		"filesystem:filesystem_unsupported",
		"encryption.passphrase:encryption_passphrase_required",
		"hostname:hostname_label_hyphen",
		"user.username:username_invalid_start",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("Problems() =\n  %v\nwant\n  %v", got, want)
	}
	if err := r.Validate(); err == nil || err.Error() != r.Problems()[0].Message {
		t.Errorf("Validate() = %v, want the first problem", err)
	}
}

// TestEncryptionChoicesFile holds data/encryption-choices.json to the Go
// table. bootc-installer copies that file to
// shared/recipe/encryption-choices.json. Regenerate with
// `go test ./internal/recipe -run TestEncryptionChoicesFile -update`.
func TestEncryptionChoicesFile(t *testing.T) {
	type file struct {
		Comment         string                    `json:"$comment"`
		ProtocolVersion int                       `json:"protocol_version"`
		Choices         []recipe.EncryptionChoice `json:"choices"`
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(file{
		Comment: "Generated from EncryptionChoices in fisherman/internal/recipe/rules.go; " +
			"do not edit. Regenerate: cd fisherman && go test ./internal/recipe -run TestEncryptionChoicesFile -update. " +
			"Schema: docs/VALIDATE.md.",
		ProtocolVersion: 1,
		Choices:         recipe.EncryptionChoices,
	}); err != nil {
		t.Fatal(err)
	}
	want := buf.Bytes()
	path := filepath.Join("..", "..", "..", "data", "encryption-choices.json")
	if *update {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s is stale; regenerate with -update.\ngot:\n%s\nwant:\n%s", path, got, want)
	}
	// The enum the rules accept is exactly the table, in order.
	for _, c := range recipe.EncryptionChoices {
		e := recipe.Encryption{Type: c.Type, Passphrase: "x"}
		if ps := recipe.CheckEncryption(e); len(ps) != 0 {
			t.Errorf("choice %q rejected: %+v", c.Type, ps)
		}
		if want := "encryption_" + strings.ReplaceAll(c.Type, "-", "_"); c.CopyKey != want {
			t.Errorf("choice %q copy_key %q, want %q", c.Type, c.CopyKey, want)
		}
	}
}
