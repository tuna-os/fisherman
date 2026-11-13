package sha512crypt

import (
	"strings"
	"testing"
)

// Test vectors from Drepper's specification, tests2[] for SHA-512
// (https://www.akkadia.org/drepper/SHA-crypt.txt).
var specVectors = []struct {
	setting, key, want string
}{
	{
		"$6$saltstring", "Hello world!",
		"$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJu" +
			"esI68u4OTLiBFdcbYEdFCoEOfaS35inz1",
	},
	{
		"$6$rounds=10000$saltstringsaltstring", "Hello world!",
		"$6$rounds=10000$saltstringsaltst$OW1/O6BYHV6BcXZu8QVeXbDWra3Oeqh0sb" +
			"HbbMCVNSnCM/UrjmM0Dp8vOuZeHBy/YTBmSK6H9qs/y3RnOaw5v.",
	},
	{
		"$6$rounds=5000$toolongsaltstring", "This is just a test",
		"$6$rounds=5000$toolongsaltstrin$lQ8jolhgVRVhY4b5pZKaysCLi0QBxGoNeKQ" +
			"zQ3glMhwllF7oGDZxUhx1yxdYcz/e1JSbq3y6JMxxl8audkUEm0",
	},
	{
		"$6$rounds=1400$anotherlongsaltstring",
		"a very much longer text to encrypt.  This one even stretches over more" +
			"than one line.",
		"$6$rounds=1400$anotherlongsalts$POfYwTEok97VWcjxIiSOjiykti.o/pQs.wP" +
			"vMxQ6Fm7I6IoYN3CmLs66x9t0oSwbtEW7o7UmJEiDwGqd8p4ur1",
	},
	{
		"$6$rounds=77777$short",
		"we have a short salt string but not a short password",
		"$6$rounds=77777$short$WuQyW2YR.hBNpjjRhpYD/ifIw05xdfeEyQoMxIXbkvr0g" +
			"ge1a1x3yRULJ5CCaUeOxFmtlcGZelFl5CxtgfiAc0",
	},
	{
		"$6$rounds=123456$asaltof16chars..", "a short string",
		"$6$rounds=123456$asaltof16chars..$BtCwjqMJGx5hrJhZywWvt0RLE8uZ4oPwc" +
			"elCjmw2kSYu.Ec6ycULevoBK25fs2xXgMNrCzIMVcgEJAstJeonj1",
	},
	{
		"$6$rounds=10$roundstoolow", "the minimum number is still observed",
		"$6$rounds=1000$roundstoolow$kUMsbe306n21p9R.FRkW3IGn.S9NPN0x50YhH1x" +
			"hLsPuWGsUSklZt58jaTfF4ZEQpyUNGc0dqbpBYYBaHHrsX.",
	},
}

func TestCryptSpecVectors(t *testing.T) {
	for _, v := range specVectors {
		got, err := Crypt(v.key, v.setting)
		if err != nil {
			t.Errorf("Crypt(%q, %q): %v", v.key, v.setting, err)
			continue
		}
		if got != v.want {
			t.Errorf("Crypt(%q, %q)\n got %s\nwant %s", v.key, v.setting, got, v.want)
		}
	}
}

// A stored hash used as the setting reproduces itself for the right key —
// the verification idiom crypt(3) callers rely on.
func TestCryptStoredHashAsSetting(t *testing.T) {
	for _, v := range specVectors {
		got, err := Crypt(v.key, v.want)
		if err != nil || got != v.want {
			t.Errorf("Crypt(%q, %q) = %q, %v; want itself", v.key, v.want, got, err)
		}
	}
}

func TestCryptRejectsOtherSchemes(t *testing.T) {
	for _, s := range []string{"", "saltstring", "$5$saltstring", "$1$abc"} {
		if _, err := Crypt("pw", s); err == nil {
			t.Errorf("Crypt(%q) accepted a non-$6$ setting", s)
		}
	}
}

func TestHashFormatAndVerifies(t *testing.T) {
	const pw = "correct horse battery staple"
	h, err := Hash(pw)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	fields := strings.Split(h, "$")
	// "", "6", salt, hash — no rounds= field at the default.
	if len(fields) != 4 || fields[0] != "" || fields[1] != "6" {
		t.Fatalf("Hash = %q; want $6$<salt>$<hash>", h)
	}
	if len(fields[2]) != SaltLen || len(fields[3]) != 86 {
		t.Errorf("Hash = %q; want %d-char salt and 86-char hash", h, SaltLen)
	}
	for _, c := range fields[2] + fields[3] {
		if !strings.ContainsRune(alphabet, c) {
			t.Errorf("Hash = %q contains %q outside the crypt alphabet", h, c)
		}
	}
	if strings.Contains(h, pw) {
		t.Errorf("Hash leaked the plaintext: %q", h)
	}
	again, err := Crypt(pw, h)
	if err != nil || again != h {
		t.Errorf("Crypt(pw, Hash(pw)) = %q, %v; want %q", again, err, h)
	}
	if other, _ := Hash(pw); other == h {
		t.Errorf("two Hash calls produced the same salt: %q", h)
	}
}

func TestIsHashSetting(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"$6$salt$hash", true},
		{"$6$rounds=5000$salt$hash", true},
		{"$5$salt$hash", true},
		{"$1$salt$hash", true},
		{"$2b$12$saltandsomehashcharacters0123456789abcdef", true},
		{"$y$salt$hash", true},
		{"hunter2", false},
		{"$notacryptsetting", false},
		{"$ecret$1", false},
		{"$6$", false},
		{"$6$salt", false},
		{"", false},
		{"$", false},
	}
	for _, c := range cases {
		if got := IsHashSetting(c.in); got != c.want {
			t.Errorf("IsHashSetting(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
