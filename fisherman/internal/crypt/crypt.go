// Package crypt wraps systemd-cryptenroll for the installer daemon: enroll
// a passphrase or a recovery key next to the empty passphrase repart leaves
// behind, wipe the empty slot, and list keyslots.
//
// Secrets never reach a log, an error or the disk. The unlock secret travels
// through an anonymous pipe (/dev/fd/3), never --unlock-key-file=- (which
// breaks under pkexec: it resolves against a home directory that may not
// exist, see internal/luks) and never a temp file. The new secret travels
// on stdin. Failures name only the program, never arguments or output.
package crypt

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/tuna-os/fisherman/internal/runner"
)

// Sentinels. The daemon maps them to its wire errors.
var (
	ErrNoDevice           = errors.New("no such LUKS2 volume")
	ErrPasswordRequired   = errors.New("password required")
	ErrPasswordIncorrect  = errors.New("password incorrect")
	ErrForeignVolume      = errors.New("volume under foreign management")
	ErrFidoDeviceNotFound = errors.New("fido device not found")
	ErrFidoTimeout        = errors.New("fido action timeout")
)

// Mechanisms this wrapper enrolls. Hardware mechanisms go through their own
// flows; asking for one here is a foreign volume.
const (
	MechPassword = "password"
	MechRecovery = "recovery"
)

// recoveryAlphabet is systemd's modhex alphabet for recovery keys.
const recoveryAlphabet = "cbdefghijklnrtuv"

// Unlock selects the credential that opens the volume for an operation.
// Exactly one form applies: Empty tries the empty passphrase (the state
// repart leaves behind); Secret carries a passphrase through an anonymous
// pipe. A zero Unlock means no unlock flag (the volume is already open).
type Unlock struct {
	Empty  bool
	Secret []byte
}

// EnrollOpts is one enroll call.
type EnrollOpts struct {
	Node string
	// Mechanism is password or recovery.
	Mechanism string
	// NewPassword is the passphrase to enroll (password mechanism).
	NewPassword []byte
	// RecoveryKey is the key to enroll (recovery mechanism). Nil generates
	// one, which comes back in EnrollResult.RecoveryKey.
	RecoveryKey []byte
	Unlock      Unlock
	// WipeSlots are wiped before the new key lands, as
	// systemd-cryptenroll does.
	WipeSlots []int64
}

// EnrollResult is one enroll call's answer.
type EnrollResult struct {
	// Slot is the new key's slot, nil when it cannot be told apart.
	Slot *int64
	// WipedSlots are the slots wiped beforehand.
	WipedSlots []int64
	// RecoveryKey is the enrolled key (recovery mechanism only).
	RecoveryKey []byte
}

// Slot is one enabled LUKS2 keyslot. Type is password (recovery keys read
// the same), tpm2, pkcs11 or fido2 for token-bound slots.
type Slot struct {
	Number int64
	Type   string
}

// Runner executes a helper. stdin carries the new secret (nil when none);
// unlock carries the unlock credential. Implementations must keep both out
// of logs, errors and the disk.
type Runner interface {
	Run(ctx context.Context, name string, args []string, stdin []byte, unlock Unlock) ([]byte, error)
}

// SystemRunner shells out to the systemd tools. It applies the Flatpak
// wrapping, so host subprocess calls work from the Flatpak frontends too.
func SystemRunner(ctx context.Context, name string, args []string, stdin []byte, unlock Unlock) ([]byte, error) {
	name, args = runner.HostArgs(name, args)
	var extra []*os.File
	if unlock.Secret != nil || unlock.Empty {
		r, w, err := os.Pipe()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if len(unlock.Secret) > 0 {
			if _, err := w.Write(unlock.Secret); err != nil {
				r.Close()
				w.Close()
				return nil, fmt.Errorf("%s: %w", name, err)
			}
		}
		w.Close()
		extra = append(extra, r)
		args = append(args, "--unlock-key-file=/dev/fd/3")
	}
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	cmd.ExtraFiles = extra
	for _, f := range extra {
		defer f.Close()
	}
	out, err := cmd.Output()
	if err != nil {
		// Name the program only: arguments name devices and slots, and
		// output may sit next to a secret on a hostile volume.
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

// Enroller wraps systemd-cryptenroll around a Runner. The zero value is
// useless: build one with NewEnroller.
type Enroller struct {
	run Runner
}

// NewEnroller returns an Enroller that shells out through run.
func NewEnroller(run Runner) *Enroller { return &Enroller{run: run} }

// SystemEnroller shells out to the real systemd tools.
func SystemEnroller() *Enroller { return NewEnroller(RunnerFunc(SystemRunner)) }

// RunnerFunc adapts a function to a Runner.
type RunnerFunc func(ctx context.Context, name string, args []string, stdin []byte, unlock Unlock) ([]byte, error)

// Run implements Runner.
func (f RunnerFunc) Run(ctx context.Context, name string, args []string, stdin []byte, unlock Unlock) ([]byte, error) {
	return f(ctx, name, args, stdin, unlock)
}

// GenerateRecoveryKey makes a systemd-shaped recovery key: 64 modhex
// characters in 8 groups of 8, joined by "-".
func GenerateRecoveryKey() ([]byte, error) {
	b := make([]byte, 64)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("crypt: random recovery key: %w", err)
	}
	var sb strings.Builder
	for i, c := range b {
		if i > 0 && i%8 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(recoveryAlphabet[int(c)%len(recoveryAlphabet)])
	}
	return []byte(sb.String()), nil
}

// isLuks reports whether node holds a LUKS container.
func (e *Enroller) isLuks(ctx context.Context, node string) bool {
	_, err := e.run.Run(ctx, "cryptsetup", []string{"luksUUID", node}, nil, Unlock{})
	return err == nil
}

// ListSlots returns the enabled keyslots of node with their types.
func (e *Enroller) ListSlots(ctx context.Context, node string) ([]Slot, error) {
	out, err := e.run.Run(ctx, "cryptsetup", []string{"luksDump", node}, nil, Unlock{})
	if err != nil {
		return nil, ErrNoDevice
	}
	return parseSlots(string(out)), nil
}

// parseSlots reads cryptsetup luksDump text. Only enabled keyslots print;
// token lines map a slot to tpm2, pkcs11 or fido2. Anything else stays
// password: recovery keys read exactly like passphrases.
func parseSlots(dump string) []Slot {
	var slots []int64
	seen := map[int64]bool{}
	types := map[int64]string{}
	section := ""
	pendingToken := ""
	for _, line := range strings.Split(dump, "\n") {
		switch line {
		case "Keyslots:":
			section = "slots"
			continue
		case "Tokens:":
			section = "tokens"
			continue
		}
		if strings.HasSuffix(line, ":") && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			section = ""
		}
		trimmed := strings.TrimSpace(line)
		fields := strings.Fields(trimmed)
		switch section {
		case "slots":
			// "  0: luks2"
			if len(fields) == 2 && strings.HasSuffix(fields[0], ":") && (fields[1] == "luks2" || fields[1] == "luks1") {
				if n, err := strconv.ParseInt(strings.TrimSuffix(fields[0], ":"), 10, 64); err == nil && !seen[n] {
					seen[n] = true
					slots = append(slots, n)
				}
			}
		case "tokens":
			// "  0: systemd-tpm2" then "Keyslot:  1".
			if len(fields) == 2 && strings.HasSuffix(fields[0], ":") && !strings.HasPrefix(line, "\t") && strings.HasPrefix(line, " ") {
				pendingToken = fields[1]
			} else if pendingToken != "" && len(fields) == 2 && fields[0] == "Keyslot:" {
				if n, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
					switch {
					case strings.Contains(pendingToken, "tpm2"):
						types[n] = "tpm2"
					case strings.Contains(pendingToken, "pkcs11"):
						types[n] = "pkcs11"
					case strings.Contains(pendingToken, "fido"):
						types[n] = "fido2"
					}
				}
				pendingToken = ""
			}
		}
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })
	out := make([]Slot, 0, len(slots))
	for _, n := range slots {
		typ := types[n]
		if typ == "" {
			typ = MechPassword
		}
		out = append(out, Slot{Number: n, Type: typ})
	}
	return out
}

// Enroll enrolls a passphrase or a recovery key on node, wiping WipeSlots
// first. It needs the unlock credential (the empty passphrase right after
// repart, the user's passphrase later): without one it fails with
// ErrPasswordRequired, like the mock and the wire error.
func (e *Enroller) Enroll(ctx context.Context, o EnrollOpts) (EnrollResult, error) {
	var res EnrollResult
	if o.Node == "" {
		return res, ErrNoDevice
	}
	var newSecret []byte
	var flag string
	switch o.Mechanism {
	case MechPassword:
		if len(o.NewPassword) == 0 {
			return res, ErrPasswordRequired
		}
		newSecret, flag = o.NewPassword, "--password"
	case MechRecovery:
		newSecret = o.RecoveryKey
		if len(newSecret) == 0 {
			key, err := GenerateRecoveryKey()
			if err != nil {
				return res, err
			}
			newSecret = key
		}
		flag = "--recovery-key"
	case "pkcs11", "tpm2":
		return res, ErrForeignVolume
	case "fido2":
		return res, ErrFidoDeviceNotFound
	default:
		return res, fmt.Errorf("crypt: unknown mechanism %q", o.Mechanism)
	}
	if !o.Unlock.Empty && o.Unlock.Secret == nil {
		return res, ErrPasswordRequired
	}
	if !e.isLuks(ctx, o.Node) {
		return res, ErrNoDevice
	}
	before, err := e.ListSlots(ctx, o.Node)
	if err != nil {
		return res, err
	}
	var args []string
	if len(o.WipeSlots) > 0 {
		var wiped []string
		for _, w := range o.WipeSlots {
			wiped = append(wiped, strconv.FormatInt(w, 10))
		}
		args = append(args, "--wipe-slot="+strings.Join(wiped, ","))
		res.WipedSlots = append([]int64(nil), o.WipeSlots...)
	}
	args = append(args, flag, o.Node)
	if _, err := e.run.Run(ctx, "systemd-cryptenroll", args, newSecret, o.Unlock); err != nil {
		// The unlock credential was tried and refused. Anything else
		// (the precheck already ruled out a missing volume) surfaces as
		// a wrong credential too: systemd-cryptenroll exits nonzero for
		// both, and they are told apart only by its (discarded) output.
		return res, ErrPasswordIncorrect
	}
	after, err := e.ListSlots(ctx, o.Node)
	if err != nil {
		return res, err
	}
	had := map[int64]bool{}
	for _, s := range before {
		had[s.Number] = true
	}
	var fresh []int64
	for _, s := range after {
		if !had[s.Number] {
			fresh = append(fresh, s.Number)
		}
	}
	if len(fresh) == 1 {
		res.Slot = &fresh[0]
	}
	if o.Mechanism == MechRecovery {
		res.RecoveryKey = append([]byte(nil), newSecret...)
	}
	return res, nil
}

// WipeSlots wipes slots off node. It needs the unlock credential.
func (e *Enroller) WipeSlots(ctx context.Context, node string, unlock Unlock, slots ...int64) error {
	if node == "" {
		return ErrNoDevice
	}
	if len(slots) == 0 {
		return fmt.Errorf("crypt: nothing to wipe")
	}
	if !unlock.Empty && unlock.Secret == nil {
		return ErrPasswordRequired
	}
	if !e.isLuks(ctx, node) {
		return ErrNoDevice
	}
	var wiped []string
	for _, s := range slots {
		wiped = append(wiped, strconv.FormatInt(s, 10))
	}
	if _, err := e.run.Run(ctx, "systemd-cryptenroll",
		[]string{"--wipe-slot=" + strings.Join(wiped, ","), node}, nil, unlock); err != nil {
		return ErrPasswordIncorrect
	}
	return nil
}
