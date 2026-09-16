package luks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStageFirstBootEnrollment(t *testing.T) {
	target := t.TempDir()
	uuid := "abcd-1234-uuid"
	if err := StageFirstBootEnrollment(target, uuid, "secret-recovery-key"); err != nil {
		t.Fatal(err)
	}
	// transient key: present, 0600, correct content
	kp := filepath.Join(target, "etc/fisherman/tpm2-enroll.key")
	fi, err := os.Stat(kp)
	if err != nil {
		t.Fatalf("key file: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("key perm %o, want 600", fi.Mode().Perm())
	}
	if b, _ := os.ReadFile(kp); string(b) != "secret-recovery-key" {
		t.Errorf("key content mismatch")
	}
	// unit: references the UUID device, shreds the key, self-disables
	up := filepath.Join(target, "usr/lib/systemd/system/fisherman-tpm2-enroll.service")
	u, err := os.ReadFile(up)
	if err != nil {
		t.Fatalf("unit: %v", err)
	}
	us := string(u)
	for _, want := range []string{
		"/dev/disk/by-uuid/" + uuid,
		"--tpm2-pcrs=7",
		"shred -u /etc/fisherman/tpm2-enroll.key",
		"systemctl disable fisherman-tpm2-enroll.service",
		"ConditionPathExists=/etc/fisherman/tpm2-enroll.key",
	} {
		if !strings.Contains(us, want) {
			t.Errorf("unit missing %q", want)
		}
	}
	// enabled via wants symlink
	link := filepath.Join(target, "etc/systemd/system/multi-user.target.wants/fisherman-tpm2-enroll.service")
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("wants symlink missing: %v", err)
	}
	// empty UUID is rejected
	if err := StageFirstBootEnrollment(t.TempDir(), "", "k"); err == nil {
		t.Error("expected error for empty UUID")
	}
}
