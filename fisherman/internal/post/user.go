package post

import (
	"bytes"
	"fmt"
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
// It uses "useradd --root <sysroot>" so that /etc/passwd, /etc/shadow, and
// /etc/group inside the target are updated without touching the live system.
// The password is set via "chpasswd --root <sysroot>" reading from stdin so
// it is never exposed on the command line.
//
// Returns nil if Username is empty (no-op).
func CreateUser(sysroot string, u UserConfig) error {
	if u.Username == "" {
		return nil
	}

	// Build useradd arguments.
	args := []string{
		"--root", sysroot,
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
		chpasswdArgs := []string{"--root", sysroot}
		if err := runner.RunWithStdin(bytes.NewBufferString(input), "chpasswd", chpasswdArgs...); err != nil {
			return fmt.Errorf("chpasswd: %w", err)
		}
	}

	fmt.Printf("  created user %q in installed system\n", u.Username)
	return nil
}
