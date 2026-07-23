package post

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tuna-os/fisherman/internal/runner"
)

// UserConfig describes a user account to create in the installed system.
type UserConfig struct {
	Username string
	Fullname string
	Password string
	Groups   []string // additional groups beyond the primary group
}

// CreateUser creates a user account inside the installed system rooted at sysroot.
//
// For ostree/bootc deployments, the deployment directory (found via DeploymentDirFn)
// is used as the --root for useradd so that the image's /etc/passwd, /etc/shadow,
// and /etc/group are updated. The sysroot root itself does not contain these files.
//
// Returns nil if Username is empty (no-op).
func CreateUser(sysroot string, u UserConfig) error {
	if u.Username == "" {
		return nil
	}

	var root string
	if isComposeFsNative(sysroot) {
		root = sysroot
	} else {
		deployDir, err := DeploymentDirFn(sysroot)
		if err != nil {
			return fmt.Errorf("finding deployment dir: %w", err)
		}
		root = deployDir

		// On ostree/bootc, /home inside the deployment is a symlink to
		// var/home (the stateroot var). Pre-create the stateroot home dir so
		// that useradd --create-home has a real directory to populate.
		staterootHome := filepath.Join(sysroot, "ostree", "deploy", "default", "var", "home")
		if err := runner.Run("mkdir", "-p", staterootHome); err != nil {
			return fmt.Errorf("mkdir stateroot home: %w", err)
		}
	}

	// Build useradd arguments.
	args := []string{
		"--root", root,
		"--create-home",
		"--shell", "/bin/bash",
	}
	if u.Fullname != "" {
		args = append(args, "--comment", u.Fullname)
	}
	if len(u.Groups) > 0 {
		args = append(args, "--groups", strings.Join(u.Groups, ","))
	}
	args = append(args, u.Username)

	if err := runner.Run("useradd", args...); err != nil {
		return fmt.Errorf("useradd: %w", err)
	}

	// Set the password via chpasswd stdin to avoid it appearing in ps output.
	if u.Password != "" {
		input := fmt.Sprintf("%s:%s\n", u.Username, u.Password)
		chpasswdArgs := []string{"--root", root}
		// A pre-hashed crypt(3) string ("$id$salt$hash", e.g. wootc's vault
		// $6$ SHA-512) must be written verbatim with -e. Without it chpasswd
		// (a) treats the hash as a PLAINTEXT password — the account's real
		// password becomes the literal hash text — and (b) invokes the
		// hashing stack (PAM/crypt config) inside the --root chroot, which
		// exits 1 on EL10-family targets (proven live on bluefin:lts).
		if strings.HasPrefix(u.Password, "$") {
			chpasswdArgs = append(chpasswdArgs, "-e")
		}
		if err := runner.RunWithStdin(bytes.NewBufferString(input), "chpasswd", chpasswdArgs...); err != nil {
			return fmt.Errorf("chpasswd: %w", err)
		}
	}

	fmt.Printf("  created user %q in installed system\n", u.Username)
	return nil
}
