package crypt

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const luksDumpTwoSlots = `LUKS header information
Version:       	2
Epoch:         	3
Metadata area: 	16384 [bytes]

Keyslots:
  0: luks2
	Key:       	512 bits
	Priority:  	normal
	Cipher:    	aes-xts-plain64
  1: luks2
	Key:       	512 bits
	Priority:  	normal
Tokens:
  0: systemd-tpm2
	Keyslot:  1
Digests:
  0: pbkdf2
`

const luksDumpThreeSlots = `LUKS header information
Version:       	2

Keyslots:
  0: luks2
	Key:       	512 bits
	Priority:  	normal
  1: luks2
	Key:       	512 bits
	Priority:  	normal
  5: luks2
	Key:       	512 bits
	Priority:  	normal
Tokens:
  0: systemd-tpm2
	Keyslot:  1
`

// fake records calls and replays canned luksDump outputs in order.
type fake struct {
	calls []call
	dumps []string
	err   map[string]error
}

type call struct {
	name  string
	args  []string
	stdin []byte
	unlk  Unlock
}

func (f *fake) Run(_ context.Context, name string, args []string, stdin []byte, unlock Unlock) ([]byte, error) {
	f.calls = append(f.calls, call{name, args, stdin, unlock})
	if err, ok := f.err[name+" "+strings.Join(args, " ")]; ok {
		return nil, err
	}
	if name == "cryptsetup" && len(args) == 2 && args[0] == "luksUUID" {
		return []byte("99999999-aaaa-bbbb-cccc-dddddddddddd\n"), nil
	}
	if name == "cryptsetup" && len(args) == 2 && args[0] == "luksDump" {
		if len(f.dumps) == 0 {
			return nil, errors.New("cryptsetup: no dump canned")
		}
		out := f.dumps[0]
		f.dumps = f.dumps[1:]
		return []byte(out), nil
	}
	return nil, nil
}

func (f *fake) find(name string) []call {
	var out []call
	for _, c := range f.calls {
		if c.name == name {
			out = append(out, c)
		}
	}
	return out
}

func TestParseSlots(t *testing.T) {
	slots := parseSlots(luksDumpTwoSlots)
	if len(slots) != 2 {
		t.Fatalf("slots = %+v", slots)
	}
	if slots[0] != (Slot{Number: 0, Type: "password"}) {
		t.Errorf("slot 0 = %+v", slots[0])
	}
	if slots[1] != (Slot{Number: 1, Type: "tpm2"}) {
		t.Errorf("slot 1 = %+v", slots[1])
	}
	if got := parseSlots("garbage\n"); len(got) != 0 {
		t.Errorf("garbage parses to %+v", got)
	}
}

func TestListSlots(t *testing.T) {
	e := NewEnroller(&fake{dumps: []string{luksDumpThreeSlots}})
	slots, err := e.ListSlots(context.Background(), "/dev/vda3")
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 3 || slots[2].Number != 5 {
		t.Fatalf("slots = %+v", slots)
	}
}

func TestListSlotsNoDevice(t *testing.T) {
	f := &fake{err: map[string]error{"cryptsetup luksDump /dev/void": errors.New("cryptsetup: exited")}}
	e := NewEnroller(f)
	if _, err := e.ListSlots(context.Background(), "/dev/void"); !errors.Is(err, ErrNoDevice) {
		t.Fatalf("err = %v", err)
	}
}

func TestEnrollPassword(t *testing.T) {
	f := &fake{dumps: []string{luksDumpTwoSlots, luksDumpThreeSlots}}
	e := NewEnroller(f)
	res, err := e.Enroll(context.Background(), EnrollOpts{
		Node:        "/dev/vda3",
		Mechanism:   MechPassword,
		NewPassword: []byte("correct horse"),
		Unlock:      Unlock{Empty: true},
		WipeSlots:   []int64{0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Slot == nil || *res.Slot != 5 {
		t.Errorf("slot = %+v, want 5 (the fresh one)", res.Slot)
	}
	if len(res.WipedSlots) != 1 || res.WipedSlots[0] != 0 {
		t.Errorf("wiped = %+v", res.WipedSlots)
	}
	calls := f.find("systemd-cryptenroll")
	if len(calls) != 1 {
		t.Fatalf("cryptenroll calls = %+v", calls)
	}
	if got := strings.Join(calls[0].args, " "); got != "--wipe-slot=0 --password /dev/vda3" {
		t.Errorf("argv = %q", got)
	}
	if string(calls[0].stdin) != "correct horse" {
		t.Error("the new passphrase must ride on stdin")
	}
	if !calls[0].unlk.Empty {
		t.Error("the empty unlock must take the Empty form")
	}
}

func TestEnrollRecoveryGeneratesKey(t *testing.T) {
	f := &fake{dumps: []string{luksDumpTwoSlots, luksDumpThreeSlots}}
	e := NewEnroller(f)
	res, err := e.Enroll(context.Background(), EnrollOpts{
		Node:      "/dev/vda3",
		Mechanism: MechRecovery,
		Unlock:    Unlock{Secret: []byte("user passphrase")},
		WipeSlots: []int64{0},
	})
	if err != nil {
		t.Fatal(err)
	}
	key := string(res.RecoveryKey)
	groups := strings.Split(key, "-")
	if len(groups) != 8 {
		t.Fatalf("recovery key has %d groups: %q", len(groups), key)
	}
	for _, g := range groups {
		if len(g) != 8 {
			t.Fatalf("bad group in %q", key)
		}
		for _, c := range g {
			if !strings.ContainsRune(recoveryAlphabet, c) {
				t.Fatalf("bad character in %q", key)
			}
		}
	}
	calls := f.find("systemd-cryptenroll")
	if string(calls[0].stdin) != key {
		t.Error("the generated key must ride on stdin")
	}
	if string(calls[0].unlk.Secret) != "user passphrase" {
		t.Error("the unlock passphrase must ride the pipe, not the argv")
	}
	for _, c := range f.calls {
		for _, a := range c.args {
			if strings.Contains(a, "user passphrase") || strings.Contains(a, key) {
				t.Errorf("secret in argv: %q", a)
			}
		}
	}
}

func TestEnrollNeedsUnlock(t *testing.T) {
	e := NewEnroller(&fake{})
	_, err := e.Enroll(context.Background(), EnrollOpts{Node: "/dev/vda3", Mechanism: MechPassword, NewPassword: []byte("x")})
	if !errors.Is(err, ErrPasswordRequired) {
		t.Errorf("err = %v", err)
	}
}

func TestEnrollRejectsHardwareMechanisms(t *testing.T) {
	e := NewEnroller(&fake{})
	for mech, want := range map[string]error{"tpm2": ErrForeignVolume, "pkcs11": ErrForeignVolume, "fido2": ErrFidoDeviceNotFound} {
		_, err := e.Enroll(context.Background(), EnrollOpts{Node: "/dev/vda3", Mechanism: mech, Unlock: Unlock{Empty: true}})
		if !errors.Is(err, want) {
			t.Errorf("%s: err = %v", mech, err)
		}
	}
}

func TestEnrollWrongPassword(t *testing.T) {
	f := &fake{
		dumps: []string{luksDumpTwoSlots},
		err:   map[string]error{"systemd-cryptenroll --password /dev/vda3": errors.New("systemd-cryptenroll: exit status 1")},
	}
	e := NewEnroller(f)
	_, err := e.Enroll(context.Background(), EnrollOpts{
		Node: "/dev/vda3", Mechanism: MechPassword,
		NewPassword: []byte("new"), Unlock: Unlock{Secret: []byte("wrong")},
	})
	if !errors.Is(err, ErrPasswordIncorrect) {
		t.Errorf("err = %v", err)
	}
	// The failure names the program only: no device, slot or secret.
	if strings.Contains(err.Error(), "vda3") || strings.Contains(err.Error(), "wrong") {
		t.Errorf("error leaks detail: %v", err)
	}
}

// TestSecretsStayOutOfLogsAndErrors fails when a secret reaches any error
// string the wrapper returns. The wrapper holds no logger at all; the fake
// sees only the designated stdin/unlock fields.
func TestSecretsStayOutOfLogsAndErrors(t *testing.T) {
	f := &fake{dumps: []string{luksDumpTwoSlots, luksDumpThreeSlots}}
	e := NewEnroller(f)
	pass, rec := "passphrase-1", "fallback-2"
	_, err := e.Enroll(context.Background(), EnrollOpts{
		Node: "node", Mechanism: MechRecovery, RecoveryKey: []byte(rec),
		Unlock: Unlock{Secret: []byte(pass)}, WipeSlots: []int64{0},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		blob := c.name + " " + strings.Join(c.args, " ")
		if strings.Contains(blob, pass) || strings.Contains(blob, rec) {
			t.Errorf("secret in call: %q", blob)
		}
	}
	f.dumps = append(f.dumps, luksDumpTwoSlots)
	if _, err := e.ListSlots(context.Background(), "node"); err != nil {
		t.Fatal(err)
	}
}

// A recovery key is enrolled as a password: systemd-cryptenroll
// --recovery-key would invent its own key, and the one returned to the
// caller would unlock nothing.
func TestEnrollRecoveryUsesPasswordFlag(t *testing.T) {
	f := &fake{dumps: []string{luksDumpTwoSlots, luksDumpThreeSlots}}
	_, err := NewEnroller(f).Enroll(context.Background(), EnrollOpts{
		Node: "/dev/vda3", Mechanism: MechRecovery, Unlock: Unlock{Empty: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(f.find("systemd-cryptenroll")[0].args, " ")
	if args != "--password /dev/vda3" {
		t.Errorf("args = %q, want --password", args)
	}
}

// scripted is a Runner over a table of outputs; a program that is not in it
// fails. It records the systemd-cryptenroll call.
type scripted struct {
	out    map[string]string
	args   []string
	unlock Unlock
}

func (s *scripted) Run(_ context.Context, name string, args []string, _ []byte, unlock Unlock) ([]byte, error) {
	if name == "systemd-cryptenroll" {
		s.args, s.unlock = args, unlock
		return nil, nil
	}
	out, ok := s.out[name+" "+strings.Join(args, " ")]
	if !ok {
		return nil, errors.New("exit status 1")
	}
	return []byte(out), nil
}

const tpmOnlyDump = "Keyslots:\n  1: luks2\nTokens:\n  0: systemd-tpm2\n\tKeyslot:    1\n"

// The native frontends send the disk and an empty password. After a TPM-only
// install the volume is a partition of it and has no empty slot left: the
// recovery key "could not be saved to the disk" (tuna-os/tunaOS run
// 45671929202).
func TestEnrollRecoveryOnTheDiskOfATPMOnlyInstall(t *testing.T) {
	s := &scripted{out: map[string]string{
		"lsblk -nrpo NAME,FSTYPE /dev/vda": "/dev/vda \n/dev/vda1 vfat\n/dev/vda2 ext4\n/dev/vda3 crypto_LUKS\n",
		"cryptsetup luksDump /dev/vda3":    tpmOnlyDump,
	}}
	_, err := NewEnroller(s).Enroll(context.Background(), EnrollOpts{
		Node: "/dev/vda", Mechanism: MechRecovery, RecoveryKey: []byte("k"), Unlock: Unlock{Secret: []byte{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.args[len(s.args)-1] != "/dev/vda3" || !s.unlock.TPM2 {
		t.Fatalf("args %v, unlock %+v", s.args, s.unlock)
	}
}

func TestEnrollKeepsThePasswordThatWasGiven(t *testing.T) {
	s := &scripted{out: map[string]string{
		"cryptsetup luksUUID /dev/vda3": "uuid\n",
		"cryptsetup luksDump /dev/vda3": tpmOnlyDump,
	}}
	_, err := NewEnroller(s).Enroll(context.Background(), EnrollOpts{
		Node: "/dev/vda3", Mechanism: MechRecovery, RecoveryKey: []byte("k"), Unlock: Unlock{Secret: []byte("pw")},
	})
	if err != nil || s.unlock.TPM2 || string(s.unlock.Secret) != "pw" {
		t.Fatalf("err %v, unlock %+v", err, s.unlock)
	}
}

func TestEnrollRefusesADiskWithTwoVolumes(t *testing.T) {
	s := &scripted{out: map[string]string{
		"lsblk -nrpo NAME,FSTYPE /dev/vda": "/dev/vda1 crypto_LUKS\n/dev/vda2 crypto_LUKS\n",
	}}
	_, err := NewEnroller(s).Enroll(context.Background(), EnrollOpts{
		Node: "/dev/vda", Mechanism: MechRecovery, RecoveryKey: []byte("k"), Unlock: Unlock{Empty: true},
	})
	if !errors.Is(err, ErrNoDevice) || s.args != nil {
		t.Fatalf("err %v, args %v", err, s.args)
	}
}
