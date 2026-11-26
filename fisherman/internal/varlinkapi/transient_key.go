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

// opensWith reports whether secret unlocks the LUKS volume. An empty secret
// tests the empty passphrase. cryptsetup reads an empty key from a regular
// file only: from stdin or /dev/null it stops with "Nothing to read on
// input", which would hide an empty keyslot.
//
// Only cryptsetup's "no key available" status (2) counts as "does not open".
// Any other failure (missing tool, busy device) cannot tell, so it answers
// true: the finalize guard then refuses the install and the key file stays.
func (r Real) opensWith(ctx context.Context, volume string, secret []byte) bool {
	if len(secret) > 0 {
		_, err := r.cmd().Run(ctx, "cryptsetup", []string{"open", "--test-passphrase", "--key-file=-", volume},
			CmdOpts{Stdin: secret, Quiet: true})
		return !noKeyAvailable(err)
	}
	if err := os.MkdirAll(r.workDir(), 0o700); err != nil {
		return true // cannot tell: fail safe
	}
	f, err := os.CreateTemp(r.workDir(), "empty-key-")
	if err != nil {
		return true
	}
	f.Close()
	defer os.Remove(f.Name())
	_, err = r.cmd().Run(ctx, "cryptsetup", []string{"open", "--test-passphrase", "--key-file=" + f.Name(), volume},
		CmdOpts{Quiet: true})
	return !noKeyAvailable(err)
}

// noKeyAvailable: cryptsetup exits 2 when no keyslot takes the key.
func noKeyAvailable(err error) bool {
	var ee interface{ ExitCode() int }
	return err != nil && errors.As(err, &ee) && ee.ExitCode() == 2
}

// forgetTransientKey removes the key file once its slot is gone.
func (r Real) forgetTransientKey(ctx context.Context, volume, path string, key []byte) {
	if path == "" || r.opensWith(ctx, volume, key) {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return
	}
}
