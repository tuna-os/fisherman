package varlinkapi

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// luksImage formats a 32 MiB LUKS2 file image with key (empty: the empty
// passphrase, what repart used before the transient key). Unprivileged:
// formatting and keyslot changes need no device mapper. Skips without the
// tools.
func luksImage(t *testing.T, key []byte) string {
	t.Helper()
	for _, tool := range []string{"cryptsetup", "systemd-cryptenroll"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	dir := t.TempDir()
	img := filepath.Join(dir, "luks.img")
	if err := os.WriteFile(img, make([]byte, 32<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	kf := filepath.Join(dir, "key")
	if err := os.WriteFile(kf, key, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("cryptsetup", "luksFormat", "-q", "--type", "luks2",
		"--pbkdf", "pbkdf2", "--pbkdf-force-iterations", "1000", "--key-file", kf, img).CombinedOutput()
	if err != nil {
		t.Skipf("luksFormat: %v %s", err, out)
	}
	return img
}

// TestTransientKeyOnARealVolume walks gnome-setup's CryptEnroll calls over a
// real LUKS2 volume that the transient key encrypts: the empty password opens
// it at no point, and both of gnome-setup's empty unlocks work.
func TestTransientKeyOnARealVolume(t *testing.T) {
	if testing.Short() {
		t.Skip("formats LUKS2 images (about 40 s)")
	}
	ctx := context.Background()
	r := Real{WorkDir: t.TempDir()}

	// The check itself: an empty keyslot is detected.
	if empty := luksImage(t, nil); !r.opens(t, empty, nil) {
		t.Fatal("opensWith does not see an empty keyslot")
	}

	key, err := newTransientKey()
	if err != nil {
		t.Fatal(err)
	}
	img := luksImage(t, key)
	_, uuid, err := r.enroller().LUKSUUID(ctx, img)
	if err != nil {
		t.Fatal(err)
	}
	path, ok := r.transientKeyPath(uuid)
	if !ok {
		t.Fatalf("UUID %q refused", uuid)
	}
	if _, err := r.writeTransientKey(uuid, key); err != nil {
		t.Fatal(err)
	}
	if r.opens(t, img, nil) {
		t.Fatal("after the install, the empty password opens the volume")
	}

	// Passphrase page: Enroll(password, unlockPassword "").
	if _, err := r.Enroll(ctx, EnrollParams{Node: img, Mechanism: MechPassword,
		UnlockPassword: ptr(Secret("")), Password: ptr(Secret("correct horse"))}); err != nil {
		t.Fatalf("passphrase enroll: %v", err)
	}
	if r.opens(t, img, nil) {
		t.Fatal("after the passphrase page, the empty password opens the volume")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the key went before the recovery key page: %v", err)
	}

	// Recovery key page: ListSlots, then Enroll(recovery, unlock "", wipe
	// every password slot but the passphrase).
	slots, err := r.ListSlots(ctx, img)
	if err != nil {
		t.Fatal(err)
	}
	var wipe []int64
	for _, s := range slots {
		if s.Type == MechPassword && !r.slotOpens(t, img, s.Slot, "correct horse") {
			wipe = append(wipe, s.Slot)
		}
	}
	res, err := r.Enroll(ctx, EnrollParams{Node: img, Mechanism: MechRecovery,
		UnlockPassword: ptr(Secret("")), WipeSlots: wipe})
	if err != nil || res.RecoveryKey == nil {
		t.Fatalf("recovery enroll: %v %+v", err, res)
	}
	if r.opens(t, img, nil) || r.opens(t, img, key) {
		t.Fatal("after the recovery key page, the empty password or the install key opens the volume")
	}
	if !r.opens(t, img, []byte("correct horse")) || !r.opens(t, img, []byte(res.RecoveryKey.Reveal())) {
		t.Fatal("the passphrase or the recovery key does not open the volume")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("the install key file outlived its slot")
	}

	// A volume with no transient key gets the empty password as sent.
	if _, err := r.Enroll(ctx, EnrollParams{Node: img, Mechanism: MechPassword,
		UnlockPassword: ptr(Secret("")), Password: ptr(Secret("x"))}); err == nil {
		t.Error("the empty password unlocked the finished volume")
	}
}

// slotOpens reports whether secret opens keyslot slot.
func (r Real) slotOpens(t *testing.T, img string, slot int64, secret string) bool {
	t.Helper()
	c := exec.Command("cryptsetup", "open", "--test-passphrase", "--key-slot", strconv.FormatInt(slot, 10), "--key-file=-", img)
	c.Stdin = strings.NewReader(secret)
	return c.Run() == nil
}

// opens is opensWith for a test: an error cannot pass for an answer.
func (r Real) opens(t *testing.T, img string, secret []byte) bool {
	t.Helper()
	ok, err := r.opensWith(context.Background(), img, secret)
	if err != nil {
		t.Fatalf("opensWith: %v", err)
	}
	return ok
}

// TestOpensWithIgnoresTokens: a token that opens the volume does not make
// the empty passphrase open it, and an empty keyslot next to the token still
// shows. On a TPM install the systemd-tpm2 token opened the disk for every
// key file, so the finalize guard refused it (albacore:gnome run
// 45820164130). The built-in luks2-keyring token stands in for the TPM: it
// works unprivileged, and --disable-external-tokens alone does not shut it
// out.
func TestOpensWithIgnoresTokens(t *testing.T) {
	if testing.Short() {
		t.Skip("formats a LUKS2 image")
	}
	if _, err := exec.LookPath("keyctl"); err != nil {
		t.Skip("keyctl not installed")
	}
	r := Real{WorkDir: t.TempDir()}
	key, err := newTransientKey()
	if err != nil {
		t.Fatal(err)
	}
	img := luksImage(t, key)
	dir := t.TempDir()
	keyFile, emptyFile := filepath.Join(dir, "key"), filepath.Join(dir, "empty")
	if err := os.WriteFile(keyFile, key, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(emptyFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	// The token: keyslot 0's key in the session keyring.
	desc := fmt.Sprintf("fisherman-test-%d", time.Now().UnixNano())
	add := exec.Command("keyctl", "padd", "user", desc, "@s")
	add.Stdin = bytes.NewReader(key)
	out, err := add.Output()
	if err != nil {
		t.Skipf("keyctl padd: %v", err)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("keyctl", "unlink", id, "@s").Run() })
	if out, err := exec.Command("cryptsetup", "token", "add", "--key-description", desc, "--key-slot", "0", img).CombinedOutput(); err != nil {
		t.Fatalf("token add: %v %s", err, out)
	}
	// What the check ran before: the token opens the volume, whatever the
	// key file holds.
	if err := exec.Command("cryptsetup", "open", "--test-passphrase", "--key-file="+emptyFile, img).Run(); err != nil {
		t.Skipf("the keyring token does not open the volume here: %v", err)
	}

	if r.opens(t, img, nil) {
		t.Fatal("the empty passphrase opens the volume: a token answered, not a keyslot")
	}
	if !r.opens(t, img, key) {
		t.Fatal("the key of keyslot 0 does not open the volume")
	}

	// An empty keyslot next to the token still counts.
	if out, err := exec.Command("cryptsetup", "luksAddKey", "-q", "--pbkdf", "pbkdf2", "--pbkdf-force-iterations", "1000",
		"--key-file="+keyFile, img, emptyFile).CombinedOutput(); err != nil {
		t.Fatalf("luksAddKey: %v %s", err, out)
	}
	if !r.opens(t, img, nil) {
		t.Fatal("opensWith does not see an empty keyslot next to a token")
	}
}
