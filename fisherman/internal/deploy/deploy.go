// Package deploy resolves where the BOOTED system's files live inside a
// freshly installed bootc target that is still mounted on the installer host.
//
// The mounted target is the physical sysroot, not the root the installed
// system boots into. A file written to <target>/etc or <target>/var is on the
// disk, but no booted system ever reads it. Each backend keeps the three
// trees a post-install step needs in a different place:
//
//	ostree             root  <target>/ostree/deploy/<stateroot>/deploy/<csum>.<n>
//	                   /etc  <root>/etc
//	                   /var  <target>/ostree/deploy/<stateroot>/var
//	composefs-native   root  <target>/state/deploy/<verity>   (holds /etc only)
//	                   /etc  <root>/etc
//	                   /var  <target>/state/os/default/var
//
// Resolve is the one place that knows this. Every post-install step that
// writes into the installed system goes through it.
package deploy

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Layout is the deployment backend of an installed target.
type Layout int

const (
	// Ostree is the ostree backend: the deployment is a full root checkout.
	Ostree Layout = iota
	// ComposeFs is bootc's composefs-native backend: /usr lives in the
	// composefs object store and is not a directory on the mounted target.
	ComposeFs
)

func (l Layout) String() string {
	if l == ComposeFs {
		return "composefs"
	}
	return "ostree"
}

// DefaultStateroot is the stateroot bootc installs into.
const DefaultStateroot = "default"

// ErrNoDeployment means the target holds no deployment of the detected
// layout: bootc has not installed into it, or the install failed.
var ErrNoDeployment = errors.New("no deployment found")

// Deployment says where the booted system's trees are on the mounted target.
type Deployment struct {
	// Sysroot is the mounted target (the physical sysroot).
	Sysroot string
	Layout  Layout
	// Stateroot is the ostree stateroot name ("default" for bootc installs).
	Stateroot string
	// Root is the deployment root. On ostree it is the full root checkout
	// (usr, etc, ...). On composefs it holds the deployment's etc only.
	Root string
	// Etc is the /etc the booted system sees.
	Etc string
	// Var is the /var the booted system sees.
	Var string
}

// Usr returns the deployment's /usr, and false when it is not a directory
// on the mounted target (composefs-native keeps it in the object store).
//
// On ostree the files under it are hard links into the repository: read
// them, never write them in place.
func (d *Deployment) Usr() (string, bool) {
	if d.Layout != Ostree {
		return "", false
	}
	usr := filepath.Join(d.Root, "usr")
	if fi, err := os.Stat(usr); err != nil || !fi.IsDir() {
		return "", false
	}
	return usr, true
}

// HasUsrFile reports whether one of the paths (relative to /usr, e.g.
// "bin/systemd-cryptenroll") exists in the installed image. known is false
// when /usr cannot be inspected offline; present is then meaningless.
func (d *Deployment) HasUsrFile(rel ...string) (present, known bool) {
	usr, ok := d.Usr()
	if !ok {
		return false, false
	}
	for _, r := range rel {
		if _, err := os.Lstat(filepath.Join(usr, r)); err == nil {
			return true, true
		}
	}
	return false, true
}

// HasUnit reports whether a systemd system unit exists in the installed
// image: under /usr/lib/systemd/system, or under the deployment's
// /etc/systemd/system. known is false when only /etc could be inspected and
// the unit is not there.
func (d *Deployment) HasUnit(unit string) (present, known bool) {
	if _, err := os.Lstat(filepath.Join(d.Etc, "systemd", "system", unit)); err == nil {
		return true, true
	}
	return d.HasUsrFile(filepath.Join("lib", "systemd", "system", unit))
}

// EtcIsTransient reports whether the image asks for a transient /etc
// (prepare-root.conf: [etc] transient=true). A write to Etc is then lost at
// the first boot. Only an ostree deployment can be inspected; composefs
// reports false.
func (d *Deployment) EtcIsTransient() bool {
	usr, ok := d.Usr()
	if !ok {
		return false
	}
	for _, p := range []string{
		filepath.Join(d.Etc, "ostree", "prepare-root.conf"),
		filepath.Join(usr, "lib", "ostree", "prepare-root.conf"),
	} {
		if v, found := iniValue(p, "etc", "transient"); found {
			return v == "true" || v == "1" || v == "yes"
		}
	}
	return false
}

// iniValue reads key from [section] of a keyfile.
func iniValue(path, section, key string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	in := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			in = strings.TrimSpace(line[1:len(line)-1]) == section
			continue
		}
		if !in {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == key {
			return strings.ToLower(strings.TrimSpace(v)), true
		}
	}
	return "", false
}

// Resolver finds a Deployment. The zero value inspects the filesystem; the
// fields let a caller substitute a probe (internal/post keeps its historical
// test hooks this way).
type Resolver struct {
	// IsComposeFs reports whether sysroot is composefs-native.
	// Default: IsComposeFs.
	IsComposeFs func(sysroot string) bool
	// OstreeDeployment returns the ostree deployment root.
	// Default: FindOstreeDeployment.
	OstreeDeployment func(sysroot string) (string, error)
	// ComposeFsEtc returns the composefs deployment's etc.
	// Default: FindComposeFsEtc.
	ComposeFsEtc func(sysroot string) (string, error)
}

// Resolve locates the deployment in the target mounted at sysroot.
func Resolve(sysroot string) (*Deployment, error) {
	return Resolver{}.Resolve(sysroot)
}

// Resolve locates the deployment in the target mounted at sysroot.
func (r Resolver) Resolve(sysroot string) (*Deployment, error) {
	isComposeFs := r.IsComposeFs
	if isComposeFs == nil {
		isComposeFs = IsComposeFs
	}
	if isComposeFs(sysroot) {
		find := r.ComposeFsEtc
		if find == nil {
			find = FindComposeFsEtc
		}
		etc, err := find(sysroot)
		if err != nil {
			return nil, fmt.Errorf("finding composefs deploy etc: %w", err)
		}
		return &Deployment{
			Sysroot:   sysroot,
			Layout:    ComposeFs,
			Stateroot: DefaultStateroot,
			Root:      filepath.Dir(etc),
			Etc:       etc,
			Var:       ComposeFsVar(sysroot),
		}, nil
	}

	find := r.OstreeDeployment
	if find == nil {
		find = FindOstreeDeployment
	}
	root, err := find(sysroot)
	if err != nil {
		return nil, fmt.Errorf("finding deployment dir: %w", err)
	}
	stateroot := ostreeStateroot(sysroot, root)
	return &Deployment{
		Sysroot:   sysroot,
		Layout:    Ostree,
		Stateroot: stateroot,
		Root:      root,
		Etc:       filepath.Join(root, "etc"),
		Var:       filepath.Join(sysroot, "ostree", "deploy", stateroot, "var"),
	}, nil
}

// ostreeStateroot reads the stateroot name out of a deployment path
// <sysroot>/ostree/deploy/<stateroot>/deploy/<csum>.<n>. A path of another
// shape yields DefaultStateroot.
func ostreeStateroot(sysroot, root string) string {
	rel, err := filepath.Rel(filepath.Join(sysroot, "ostree", "deploy"), root)
	if err != nil {
		return DefaultStateroot
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) == 3 && parts[1] == "deploy" && parts[0] != ".." && parts[0] != "" {
		return parts[0]
	}
	return DefaultStateroot
}

// IsComposeFs reports whether the target at sysroot is composefs-native.
//
// Detection is positive, by the composefs deploy base <sysroot>/state/deploy.
// The composefs-native backend also creates an /ostree directory, so "no
// /ostree" alone is not the signal; it is kept as the legacy fallback for a
// target that has neither.
func IsComposeFs(sysroot string) bool {
	if fi, err := os.Stat(filepath.Join(sysroot, "state", "deploy")); err == nil && fi.IsDir() {
		return true
	}
	_, err := os.Stat(filepath.Join(sysroot, "ostree"))
	return err != nil
}

// ComposeFsVar is the /var of a composefs-native target.
func ComposeFsVar(sysroot string) string {
	return filepath.Join(sysroot, "state", "os", DefaultStateroot, "var")
}

// FindOstreeDeployment walks <sysroot>/ostree/deploy/*/deploy/ for the
// deployment `bootc install to-filesystem` created. A fresh target has one.
// With more than one, the first in name order of the "default" stateroot
// wins, then of the other stateroots.
func FindOstreeDeployment(sysroot string) (string, error) {
	base := filepath.Join(sysroot, "ostree", "deploy")
	stateroots, err := os.ReadDir(base)
	if err != nil {
		return "", fmt.Errorf("%w under %s: %w", ErrNoDeployment, base, err)
	}
	names := make([]string, 0, len(stateroots))
	for _, st := range stateroots {
		if st.IsDir() {
			names = append(names, st.Name())
		}
	}
	sort.SliceStable(names, func(i, j int) bool {
		if (names[i] == DefaultStateroot) != (names[j] == DefaultStateroot) {
			return names[i] == DefaultStateroot
		}
		return names[i] < names[j]
	})
	for _, name := range names {
		dir := filepath.Join(base, name, "deploy")
		deployments, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, dep := range deployments {
			// <csum>.<n> is the checkout; <csum>.<n>.origin is a file.
			if dep.IsDir() {
				return filepath.Join(dir, dep.Name()), nil
			}
		}
	}
	return "", fmt.Errorf("%w under %s", ErrNoDeployment, base)
}

// FindComposeFsEtc finds the writable /etc of the composefs-native
// deployment: <sysroot>/state/deploy/<verity>/etc, where <verity> is the
// digest the boot entry passes as composefs=<verity>.
//
//  1. composefs=<verity> from a BLS entry under <sysroot>/boot/loader/entries
//     or <sysroot>/boot/efi/loader/entries, when that deployment exists.
//  2. Otherwise the newest directory under <sysroot>/state/deploy (a fresh
//     install has one).
func FindComposeFsEtc(sysroot string) (string, error) {
	for _, loaderDir := range []string{
		filepath.Join(sysroot, "boot", "loader", "entries"),
		filepath.Join(sysroot, "boot", "efi", "loader", "entries"),
	} {
		entries, err := filepath.Glob(filepath.Join(loaderDir, "*.conf"))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			data, err := os.ReadFile(entry)
			if err != nil {
				continue
			}
			for _, line := range strings.Split(string(data), "\n") {
				if !strings.HasPrefix(strings.TrimSpace(line), "options") {
					continue
				}
				for _, field := range strings.Fields(line) {
					hash, ok := strings.CutPrefix(field, "composefs=")
					// A "?" prefix marks an optional digest on the kernel
					// command line; the directory name has none.
					hash = strings.TrimPrefix(hash, "?")
					if !ok || hash == "" || strings.ContainsRune(hash, filepath.Separator) {
						continue
					}
					etc := filepath.Join(sysroot, "state", "deploy", hash, "etc")
					if _, err := os.Stat(etc); err == nil {
						return etc, nil
					}
				}
			}
		}
	}

	base := filepath.Join(sysroot, "state", "deploy")
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", fmt.Errorf("%w: reading composefs deploy base %s: %w", ErrNoDeployment, base, err)
	}
	newest := ""
	var newestMod int64
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if mod := info.ModTime().UnixNano(); newest == "" || mod > newestMod {
			newest, newestMod = e.Name(), mod
		}
	}
	if newest == "" {
		return "", fmt.Errorf("%w: no composefs deploy directory under %s", ErrNoDeployment, base)
	}
	return filepath.Join(base, newest, "etc"), nil
}
