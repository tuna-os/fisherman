package post

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/tuna-os/fisherman/internal/recipe"
	"github.com/tuna-os/fisherman/internal/runner"
)

var intentInFlatpak = runner.InFlatpak

// CheckTargetFlatpakIntentContext runs before partitioning. The target-only
// consumer uses local filesystem operations and therefore requires a native
// host process; it must not mix sandbox paths with host Flatpak commands.
func CheckTargetFlatpakIntentContext(intent *recipe.FlatpakIntent) error {
	if intent != nil && intent.Target.Mode == "replace" && intentInFlatpak() {
		return fmt.Errorf("explicit target Flatpak selection requires native host fisherman; sandbox execution is not supported")
	}
	return nil
}

// ApplyTargetFlatpakIntent installs only the explicit supported selection into
// a fresh target repository. Legacy host-wide tar copying is never called.
// Missing apps are downloaded; this initial consumer makes no offline/cache
// reuse promise. A failed request is fatal to the caller, not a success warning.
func ApplyTargetFlatpakIntent(target, varPath string, intent *recipe.FlatpakIntent) error {
	if err := CheckTargetFlatpakIntentContext(intent); err != nil {
		return err
	}
	if err := intent.Validate(); err != nil {
		return err
	}
	if intent == nil || intent.Target.Mode != "replace" {
		return fmt.Errorf("target replace intent required")
	}
	if !filepath.IsAbs(target) || filepath.Clean(target) == "/" {
		return fmt.Errorf("unsafe target root")
	}
	var etcDir string
	var err error
	if isComposeFsNative(target) {
		etcDir, err = ComposeFsDeployEtcDirFn(target)
		if varPath == "" {
			varPath = "state/os/default/var"
		}
	} else {
		var deploy string
		deploy, err = DeploymentDirFn(target)
		etcDir = filepath.Join(deploy, "etc")
		if varPath == "" {
			varPath = "ostree/deploy/default/var"
		}
	}
	if err != nil {
		return err
	}
	if filepath.IsAbs(varPath) || filepath.Clean(varPath) == ".." || strings.HasPrefix(filepath.Clean(varPath), "../") {
		return fmt.Errorf("unsafe flatpak var path")
	}
	dst := filepath.Join(target, varPath, "lib", "flatpak")
	unit := filepath.Join(etcDir, "systemd", "system", "flatpak-preinstall.service")
	state := filepath.Join(etcDir, "fisherman", "flatpak-intent.json")
	for _, path := range []string{dst, filepath.Dir(unit), state} {
		if err := intentPathWithin(target, path); err != nil {
			return err
		}
	}
	if _, err := os.Lstat(state); err == nil {
		return fmt.Errorf("refusing to replace existing applied Flatpak intent state")
	} else if !os.IsNotExist(err) {
		return err
	}
	// Refuse conflicting policy before downloading or publishing anything.
	if old, e := os.Readlink(unit); e == nil && old == "/dev/null" {
	} else if _, e := os.Lstat(unit); e == nil {
		return fmt.Errorf("refusing to replace target preinstall unit override")
	} else if !os.IsNotExist(e) {
		return e
	}
	// Never mutate an existing installation under a replace request. In
	// particular, [] cannot silently leave inherited apps and claim success.
	if err := requireEmptyIntentRepository(dst); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(dst), ".fisherman-flatpak-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage) // Only this invocation's freshly created staging directory.
	fp := func(args ...string) error {
		argv := append([]string{"FLATPAK_SYSTEM_DIR=" + stage, "flatpak", "--system"}, args...)
		if err := Exec.Command("env", argv...).Run(); err != nil {
			return fmt.Errorf("flatpak %s: %w", strings.Join(args, " "), err)
		}
		return nil
	}
	if len(intent.Target.Apps) > 0 {
		if err := fp("remote-add", "flathub", "https://dl.flathub.org/repo/flathub.flatpakrepo"); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, a := range intent.Target.Apps {
			ref := "app/" + a.ID + "/" + a.Arch + "/" + a.Branch
			if seen[ref] {
				continue
			}
			seen[ref] = true
			if err := fp("install", "--noninteractive", "-y", "flathub", ref); err != nil {
				return err
			}
			// Run the real deployment lookup; a zero exit from a mocked/download
			// command alone is not enough to call this deployed in production.
			if err := fp("info", ref); err != nil {
				return err
			}
		}
	}
	// Recheck after external commands, before publishing into the target.
	for _, path := range []string{dst, filepath.Dir(unit), state} {
		if err := intentPathWithin(target, path); err != nil {
			return err
		}
	}
	if _, err := os.Lstat(state); err == nil {
		return fmt.Errorf("applied Flatpak intent state appeared during installation")
	} else if !os.IsNotExist(err) {
		return err
	}
	// Check again after commands. Rmdir, unlike os.Remove, cannot delete a
	// regular file that appears after this check.
	if err := requireEmptyIntentRepository(dst); err != nil {
		return err
	}
	// MkdirTemp is private while building; the published system installation
	// must be traversable by ordinary users.
	if err := os.Chmod(stage, 0755); err != nil {
		return err
	}
	if err := syscall.Rmdir(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(stage, dst); err != nil {
		return err
	}
	// Stop the image's independent first-boot defaults from undoing replace.
	// Inherit does not enter this function, so its service policy is untouched.
	if err := os.MkdirAll(filepath.Dir(unit), 0755); err != nil {
		return err
	}
	if old, e := os.Readlink(unit); e == nil && old == "/dev/null" { /* already masked */
	} else {
		if _, e := os.Lstat(unit); e == nil {
			return fmt.Errorf("refusing to replace target preinstall unit override")
		} else if !os.IsNotExist(e) {
			return e
		}
		if err := os.Symlink("/dev/null", unit); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(state), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(struct {
		Version      int                     `json:"version"`
		AppliedScope string                  `json:"appliedScope"`
		Selection    recipe.FlatpakSelection `json:"selection"`
	}{1, "target", intent.Target}, "", "  ")
	if err != nil {
		return err
	}
	return publishIntentState(state, append(data, '\n'))
}

func requireEmptyIntentRepository(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("target Flatpak repository is not a directory")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("explicit Flatpak selection requires an empty target repository")
	}
	return nil
}

// Publish complete state atomically without overwriting any concurrent file or
// symlink. Link, unlike Rename, fails if the destination already exists.
func publishIntentState(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".flatpak-intent-state-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0644); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Link(tmp, path)
}

// Reject traversal and ALL existing symlinks below the target root, including
// dangling links. EvalSymlinks returning ENOENT does not mean a path is absent.
// The target root itself may be a mounted or canonicalized path.
func intentPathWithin(target, path string) error {
	root, err := filepath.EvalSymlinks(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(target, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return fmt.Errorf("intent path escapes target")
	}
	current := root
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("intent path contains symlink: %s", current)
		}
	}
	return nil
}
