package post

import (
	"bytes"
	"fmt"
	"os"
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
	var staterootHome string
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
		// the relocation below has a destination.
		staterootHome = filepath.Join(sysroot, "ostree", "deploy", "default", "var", "home")
		if err := runner.Run("mkdir", "-p", staterootHome); err != nil {
			return fmt.Errorf("mkdir stateroot home: %w", err)
		}
	}

	// Run the TARGET's useradd via chroot rather than `useradd --root`:
	// --root chroots too, but first initializes the HOST's PAM/SELinux
	// stack — from a booted host it dies with "failure while writing
	// changes to /etc/passwd" against a target whose etc is perfectly
	// writable (wootc run 20260723T0738: the deployer-initramfs
	// environment masked this; the same call from booted Phase 2 failed
	// every time while `chroot <dep> useradd` succeeded on the same
	// files). Plain chroot uses only the target's own libraries.
	args := []string{
		root,
		"useradd",
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

	if err := runner.Run("chroot", args...); err != nil {
		return fmt.Errorf("useradd (chroot %s): %w", root, err)
	}

	// useradd --create-home resolved /home through the deployment's
	// /home -> var/home symlink and wrote the directory into the
	// DEPLOYMENT's own var/ — which the booted system never sees: the
	// stateroot var is mounted over /var at runtime. The account then
	// boots with a passwd entry but no home directory at all (wootc E2E
	// run 20260723T0423: var/home held only the image's seed content).
	// Relocate the freshly created home into the stateroot var, and pin
	// it with a tmpfiles.d snippet so the first boot (re)creates it from
	// /etc/skel if missing and restores ownership + SELinux labels under
	// the live policy — offline useradd cannot label correctly.
	if staterootHome != "" {
		deployHome := filepath.Join(root, "var", "home", u.Username)
		stateHome := filepath.Join(staterootHome, u.Username)
		if _, err := os.Stat(deployHome); err == nil {
			if _, err := os.Stat(stateHome); os.IsNotExist(err) {
				if err := runner.Run("mv", deployHome, stateHome); err != nil {
					return fmt.Errorf("relocating home to stateroot var: %w", err)
				}
			}
		}
		tmpfilesDir := filepath.Join(root, "etc", "tmpfiles.d")
		if err := os.MkdirAll(tmpfilesDir, 0o755); err != nil {
			return fmt.Errorf("mkdir tmpfiles.d: %w", err)
		}
		// Z mode "-": fix ownership and restore SELinux contexts recursively
		// but keep each file's own mode — 0700 here would mark every migrated
		// document executable.
		snippet := fmt.Sprintf(
			"C /var/home/%[1]s 0700 %[1]s %[1]s - /etc/skel\nZ /var/home/%[1]s - %[1]s %[1]s -\n",
			u.Username)
		snippetPath := filepath.Join(tmpfilesDir, "fisherman-home-"+u.Username+".conf")
		if err := os.WriteFile(snippetPath, []byte(snippet), 0o644); err != nil {
			return fmt.Errorf("writing home tmpfiles snippet: %w", err)
		}
	}

	// Set the password via chpasswd stdin to avoid it appearing in ps output.
	// Same chroot rationale as useradd above.
	if u.Password != "" {
		input := fmt.Sprintf("%s:%s\n", u.Username, u.Password)
		chpasswdArgs := []string{root, "chpasswd"}
		// A pre-hashed crypt(3) string ("$id$salt$hash", e.g. wootc's vault
		// $6$ SHA-512) must be written verbatim with -e. Without it chpasswd
		// (a) treats the hash as a PLAINTEXT password — the account's real
		// password becomes the literal hash text — and (b) invokes the
		// hashing stack (PAM/crypt config) inside the --root chroot, which
		// exits 1 on EL10-family targets (proven live on bluefin:lts).
		if strings.HasPrefix(u.Password, "$") {
			chpasswdArgs = append(chpasswdArgs, "-e")
		}
		if err := runner.RunWithStdin(bytes.NewBufferString(input), "chroot", chpasswdArgs...); err != nil {
			return fmt.Errorf("chpasswd (chroot %s): %w", root, err)
		}
	}

	fmt.Printf("  created user %q in installed system\n", u.Username)
	return nil
}
