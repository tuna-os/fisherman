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
	composefs := isComposeFsNative(sysroot)
	if composefs {
		// composefs-native has no full chrootable rootfs during deploy — the
		// sealed image's /usr (with useradd) is mounted read-only elsewhere,
		// so `chroot <sysroot> useradd` exits 127 (dakota, GH matrix
		// 20260724T1508). Point at the writable deployment root (parent of
		// the state/deploy/<hash>/etc dir) and use `useradd --root` below,
		// which edits the passwd files without needing a chroot rootfs.
		etcDir, err := ComposeFsDeployEtcDirFn(sysroot)
		if err != nil {
			return fmt.Errorf("finding composefs deploy root: %w", err)
		}
		root = filepath.Dir(etcDir)
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
	// ostree: `chroot <deployDir> useradd` (the deployDir is a full rootfs).
	// composefs-native: `useradd --root <deployRoot>` (no chroot rootfs
	// exists during deploy; --root only edits the passwd files, and the
	// booted-host PAM problem that forced chroot is ostree-only — composefs
	// deploys happen in the initramfs).
	tail := []string{"useradd", "--create-home", "--shell", "/bin/bash"}
	if u.Fullname != "" {
		tail = append(tail, "--comment", u.Fullname)
	}
	if len(u.Groups) > 0 {
		tail = append(tail, "--groups", strings.Join(u.Groups, ","))
	}
	tail = append(tail, u.Username)

	if composefs {
		// tail[0] is the literal "useradd" command name, needed only by the
		// ostree branch's `chroot <root> useradd …`. Here we invoke the
		// useradd binary directly, so drop it — otherwise it becomes a second
		// positional login name alongside the username and shadow-utils exits 2
		// ("invalid command syntax", dakota GH matrix 20260724T1705).
		if err := runner.Run("useradd", append([]string{"--root", root}, tail[1:]...)...); err != nil {
			return fmt.Errorf("useradd (--root %s): %w", root, err)
		}
	} else {
		if err := runner.Run("chroot", append([]string{root}, tail...)...); err != nil {
			return fmt.Errorf("useradd (chroot %s): %w", root, err)
		}
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
	// Same ostree-chroot vs composefs---root split as useradd above.
	if u.Password != "" {
		input := fmt.Sprintf("%s:%s\n", u.Username, u.Password)
		// A pre-hashed crypt(3) string ("$id$salt$hash", e.g. wootc's vault
		// $6$ SHA-512) must be written verbatim with -e. Without it chpasswd
		// (a) treats the hash as a PLAINTEXT password — the account's real
		// password becomes the literal hash text — and (b) invokes the
		// hashing stack (PAM/crypt config), which exits 1 on EL10 targets.
		flag := []string{}
		if strings.HasPrefix(u.Password, "$") {
			flag = []string{"-e"}
		}
		var cpErr error
		if composefs {
			cpErr = runner.RunWithStdin(bytes.NewBufferString(input), "chpasswd",
				append([]string{"--root", root}, flag...)...)
		} else {
			cpErr = runner.RunWithStdin(bytes.NewBufferString(input), "chroot",
				append([]string{root, "chpasswd"}, flag...)...)
		}
		if cpErr != nil {
			return fmt.Errorf("chpasswd (%s): %w", root, cpErr)
		}
	}

	fmt.Printf("  created user %q in installed system\n", u.Username)
	return nil
}
