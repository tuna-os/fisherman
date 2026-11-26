package varlinkapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

// The transient install key.
//
// systemd-repart encrypts the new root with a key file. With no file it uses
// an empty key, and that empty passphrase stayed on the installed disk until
// the frontend replaced it: gnome-setup only on its recovery key page. A
// closed or crashed frontend, a power cut or a reboot before that page left
// a disk that opens with an empty passphrase, on every machine.
//
// Instead, each install makes a random key, 64 hex characters with no
// newline, so a key file and a typed passphrase are the same bytes. It lives
// only on the daemon's tmpfs (WorkDir, outside the per-install scratch tree),
// named by the volume's LUKS UUID, mode 0600. It dies with the live session:
// a disk whose install was abandoned keeps a slot that nobody can open.
// io.systemd.CryptEnroll calls that unlock with the empty password (what
// gnome-setup sends, as for systemd-sysinstall) get this key for that volume.

const transientKeyDir = "luks-transient"

var luksUUIDRe = regexp.MustCompile(`^[0-9a-fA-F-]{1,64}$`)

func newTransientKey() ([]byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return []byte(hex.EncodeToString(b)), nil
}

func (r Real) transientDir() string { return filepath.Join(r.workDir(), transientKeyDir) }

// writeTransientKey stores key under name and returns the path.
func (r Real) writeTransientKey(name string, key []byte) (string, error) {
	dir := r.transientDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(key); err != nil {
		f.Close()
		os.Remove(path)
		return "", err
	}
	return path, f.Close()
}

// transientKeyPath is the file of the volume with this LUKS UUID.
func (r Real) transientKeyPath(uuid string) (string, bool) {
	if !luksUUIDRe.MatchString(uuid) {
		return "", false
	}
	return filepath.Join(r.transientDir(), uuid), true
}

// transientKeyFor returns the transient key of the volume node names, or nil
// when this live session installed no such volume (or its key is gone).
func (r Real) transientKeyFor(ctx context.Context, node string) (key []byte, volume, path string) {
	volume, uuid, err := r.enroller().LUKSUUID(ctx, node)
	if err != nil {
		return nil, "", ""
	}
	path, ok := r.transientKeyPath(uuid)
	if !ok {
		return nil, "", ""
	}
	key, err = os.ReadFile(path)
	if err != nil || len(key) == 0 {
		return nil, "", ""
	}
	return key, volume, path
}

// opensWith reports whether secret opens a keyslot of the LUKS volume. An
// empty secret tests the empty passphrase. cryptsetup reads an empty key from
// a regular file only: from stdin or /dev/null it stops with "Nothing to read
// on input", which would hide an empty keyslot.
//
// Only keyslots count, never tokens. `cryptsetup open` tries the volume's
// tokens before the key file (luks_try_token_unlock in action_open_luks,
// cryptsetup 2.8), and a token that opens counts as success whatever the key
// file holds. On a TPM install the systemd-tpm2 token opens the disk on the
// machine that enrolled it, so every TPM install "opened with an empty
// passphrase" (albacore:gnome run 45820164130). testPassphraseArgs shuts
// tokens out.
//
// cryptsetup's "no key available" status (2) is "does not open". Any other
// failure (missing tool, busy device, bad header) cannot tell: opensWith
// then returns true and the error, so callers fail closed and can show the
// exit status. The empty-passphrase test is not Quiet, so the error also
// carries the tail of cryptsetup's stderr.
func (r Real) opensWith(ctx context.Context, volume string, secret []byte) (bool, error) {
	if len(secret) > 0 {
		_, err := r.cmd().Run(ctx, "cryptsetup", testPassphraseArgs("-", volume),
			CmdOpts{Stdin: secret, Quiet: true})
		return cryptOpens(err)
	}
	if err := os.MkdirAll(r.workDir(), 0o700); err != nil {
		return true, err
	}
	f, err := os.CreateTemp(r.workDir(), "empty-key-")
	if err != nil {
		return true, err
	}
	f.Close()
	defer os.Remove(f.Name())
	_, err = r.cmd().Run(ctx, "cryptsetup", testPassphraseArgs(f.Name(), volume), CmdOpts{})
	return cryptOpens(err)
}

// noTokenType names a token type that no volume has. With --token-type
// cryptsetup tries only tokens of that type, finds none and goes on to the
// key file: no token takes part, the built-in luks2-keyring token included.
// --disable-external-tokens also keeps the token plugins (systemd-tpm2,
// -fido2, -pkcs11) from loading, so the test never talks to a TPM.
const noTokenType = "fisherman-keyslots-only"

// testPassphraseArgs tests one key file against the keyslots only.
func testPassphraseArgs(keyFile, volume string) []string {
	return []string{"open", "--test-passphrase", "--key-file=" + keyFile,
		"--disable-external-tokens", "--token-type=" + noTokenType, volume}
}

// cryptOpens reads the result of a test-passphrase run: success opens, exit
// status 2 ("No key available with this passphrase") does not, anything
// else is an error that counts as opens.
func cryptOpens(err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	var ee interface{ ExitCode() int }
	if errors.As(err, &ee) && ee.ExitCode() == 2 {
		return false, nil
	}
	return true, err
}

// forgetTransientKey removes the key file once its slot is gone. When
// cryptsetup cannot tell, the file stays.
func (r Real) forgetTransientKey(ctx context.Context, volume, path string, key []byte) {
	if path == "" {
		return
	}
	if opens, err := r.opensWith(ctx, volume, key); opens || err != nil {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return
	}
}
