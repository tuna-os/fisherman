// Package receipt is the installed-system identity receipt (#385).
//
// An install that exits zero has written a disk. It has not shown that the
// disk boots, or that the system that boots is the image the user chose.
// This package keeps those two claims apart:
//
//   - At install time fisherman writes a non-secret installation receipt
//     into the installed system's /var (Receipt, Write). It records what
//     the install expects to boot: the deployment it created, the platform
//     manifest digest when one is known, the platform and the update
//     reference.
//   - It also stages a oneshot systemd unit in the deployment's /etc. At the
//     first boot of the installed system the unit runs the image's own
//     `bootc status --format json --format-version 1` and stores the output
//     next to the receipt (the collection). It changes nothing else.
//   - Evaluate compares a receipt with bootc status, either the collection
//     or a status read live on the installed system (`fisherman verify`),
//     and returns a versioned Result: verified, mismatch, unsupported,
//     unavailable or not-yet-run, with a stable reason code.
//
// Nothing here uploads, switches images, changes the boot order, rolls back
// or reboots. Schemas: docs/RECEIPT.md.
package receipt

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tuna-os/fisherman/internal/deploy"
)

// Schemas and their versions. A version changes when a field is removed or
// renamed or its meaning changes. New fields and new reason codes are
// additions and do not change it; a reader ignores what it does not know.
const (
	ReceiptSchema     = "fisherman.install-receipt"
	ReceiptVersion    = 1
	CollectionSchema  = "fisherman.boot-collection"
	CollectionVersion = 1
	ResultSchema      = "fisherman.boot-verification"
	ResultVersion     = 1
)

// Paths in the installed system, as the booted system sees them.
const (
	// ReceiptPath is the installation receipt. In /var, not /etc: an image
	// with a transient /etc would lose it at the first boot.
	ReceiptPath = "/var/lib/fisherman/install-receipt.json"
	// CollectionDir holds what the first-boot unit collected, and the
	// result `fisherman verify --write` stores.
	CollectionDir  = "/var/lib/fisherman/boot-receipt"
	CollectionFile = "collection.json"
	StatusFile     = "bootc-status.json"
	VersionFile    = "bootc-version.txt"
	ResultFile     = "verification.json"
	// UnitName is the first-boot collector.
	UnitName = "fisherman-boot-receipt.service"
	// ScriptPath is the collector the unit runs with /bin/sh. It is read,
	// not executed, so its SELinux label (etc_t) does not matter.
	ScriptPath = "/etc/fisherman/boot-receipt.sh"
)

// Backends, as bootc status reports them.
const (
	BackendOstree    = "ostree"
	BackendComposefs = "composefs"
)

// Where Expected.PlatformManifestDigest came from.
const (
	// DigestFromInstalledDeployment: the ostree.manifest-digest metadata of
	// the commit fisherman's install deployed (ostree backend).
	DigestFromInstalledDeployment = "installed_deployment"
	// DigestFromMediaInput: the platform manifest digest of verified live
	// media (media-input.json), which fisherman checked before installing.
	DigestFromMediaInput = "media_input"
)

// Why Expected.PlatformManifestDigest is "".
const (
	// DigestNotRecorded: neither the installed deployment nor verified
	// media named a platform manifest digest. A digest the source
	// reference pins may be an index digest and is never used.
	DigestNotRecorded = "platform_digest_not_recorded"
)

// Receipt is the installation receipt. It holds no passphrase, password,
// user name, hostname, machine-id, registry credential or disk serial.
type Receipt struct {
	Schema           string `json:"schema"`
	Version          int    `json:"version"`
	InstallID        string `json:"install_id"`
	InstalledAt      string `json:"installed_at"`
	FishermanVersion string `json:"fisherman_version"`
	// Backend is the deployment backend fisherman installed: "ostree" or
	// "composefs".
	Backend    string     `json:"backend"`
	Deployment Deployment `json:"deployment"`
	Source     Source     `json:"source"`
	Expected   Expected   `json:"expected"`
	// UpdateRef is the reference the installed system follows for updates,
	// as bootc records it ("" when fisherman does not know it).
	UpdateRef string `json:"update_ref"`
}

// Deployment is the deployment fisherman's install created.
type Deployment struct {
	Stateroot string `json:"stateroot"`
	// ID is the ostree commit checksum (ostree) or the composefs image
	// verity digest (composefs). bootc status reports the same value for
	// the booted entry.
	ID string `json:"id"`
}

// Source is the installation source, for the reader. The verifier never
// compares a booted digest with it.
type Source struct {
	// Ref is the source reference; "" when it is not a plain image
	// reference (it then is not recorded at all).
	Ref       string `json:"ref"`
	Transport string `json:"transport"`
	// ReferenceDigest is the digest the reference pins (name@sha256:...).
	// It can be an index digest, so it is information, not an expectation.
	ReferenceDigest string `json:"reference_digest"`
}

// Expected is what the installed system should boot.
type Expected struct {
	// PlatformManifestDigest is the platform-specific image manifest
	// digest. bootc status reports this digest (imageDigest), never an
	// index digest.
	PlatformManifestDigest string `json:"platform_manifest_digest"`
	DigestFrom             string `json:"digest_from"`
	DigestReason           string `json:"digest_reason,omitempty"`
	// IndexDigest is the digest of the index the platform manifest was
	// selected from, when verified media names one. Never compared with a
	// booted digest.
	IndexDigest string `json:"index_digest"`
	// Platform is "os/arch[/variant]" in OCI terms ("linux/amd64").
	Platform string `json:"platform"`
}

// Facts is what the install knows when it writes the receipt.
type Facts struct {
	InstallID        string
	InstalledAt      time.Time
	FishermanVersion string
	Deployment       *deploy.Deployment
	// SourceRef is the reference handed to bootc (--source-imgref), or ""
	// when bootc installed the root it runs in.
	SourceRef string
	// UpdateRef is the reference the installed system follows.
	UpdateRef string
	// InstalledDigest is the manifest digest of the installed ostree
	// commit (OstreeManifestDigest), or "".
	InstalledDigest string
	// Media* come from verified media-input.json.
	MediaPlatformDigest string
	MediaIndexDigest    string
	MediaPlatform       string
	// GOARCH is the architecture fisherman runs on (runtime.GOARCH).
	GOARCH string
}

// NewInstallID returns a random 128-bit id, as 32 hex characters.
func NewInstallID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand does not fail on Linux; a fixed value would make
		// two installs indistinguishable, so say so instead.
		return ""
	}
	return hex.EncodeToString(b)
}

var (
	hexIDRe     = regexp.MustCompile(`^[0-9a-f]{32}$`)
	digestRe    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	ostreeCsRe  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	verityRe    = regexp.MustCompile(`^[0-9a-f]{64}([0-9a-f]{64})?$`)
	refRe       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+=-]{0,511}$`)
	tokenRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)
	platformRe  = regexp.MustCompile(`^[a-z0-9]{1,16}/[a-z0-9_]{1,16}(/[a-z0-9]{1,8})?$`)
	stateRootRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

func validDigest(s string) bool { return digestRe.MatchString(s) }

// New builds the receipt. Every value is checked against a narrow pattern;
// one that does not match is left out, never copied as is.
func New(f Facts) (Receipt, error) {
	if !hexIDRe.MatchString(f.InstallID) {
		return Receipt{}, errors.New("receipt: no install id")
	}
	if f.Deployment == nil {
		return Receipt{}, errors.New("receipt: no deployment")
	}
	r := Receipt{
		Schema:           ReceiptSchema,
		Version:          ReceiptVersion,
		InstallID:        f.InstallID,
		InstalledAt:      f.InstalledAt.UTC().Format(time.RFC3339),
		FishermanVersion: sanitizeVersion(f.FishermanVersion),
		UpdateRef:        sanitizeRef(f.UpdateRef),
	}
	d := f.Deployment
	if stateRootRe.MatchString(d.Stateroot) {
		r.Deployment.Stateroot = d.Stateroot
	}
	switch d.Layout {
	case deploy.ComposeFs:
		r.Backend = BackendComposefs
		if id := filepath.Base(d.Root); verityRe.MatchString(id) {
			r.Deployment.ID = id
		}
	case deploy.Ostree:
		r.Backend = BackendOstree
		// <stateroot>/deploy/<checksum>.<serial>
		base := filepath.Base(d.Root)
		if i := strings.LastIndexByte(base, '.'); i > 0 && ostreeCsRe.MatchString(base[:i]) {
			r.Deployment.ID = base[:i]
		}
	}

	if ref := sanitizeRef(f.SourceRef); ref != "" {
		r.Source.Ref = ref
		r.Source.Transport, _ = splitTransport(ref)
		if _, d, ok := strings.Cut(ref, "@"); ok && validDigest(d) {
			r.Source.ReferenceDigest = d
		}
	}

	switch {
	case validDigest(f.InstalledDigest):
		r.Expected.PlatformManifestDigest, r.Expected.DigestFrom = f.InstalledDigest, DigestFromInstalledDeployment
	case validDigest(f.MediaPlatformDigest):
		r.Expected.PlatformManifestDigest, r.Expected.DigestFrom = f.MediaPlatformDigest, DigestFromMediaInput
	default:
		r.Expected.DigestReason = DigestNotRecorded
	}
	if validDigest(f.MediaIndexDigest) && f.MediaIndexDigest != r.Expected.PlatformManifestDigest {
		r.Expected.IndexDigest = f.MediaIndexDigest
	}
	switch {
	case platformRe.MatchString(f.MediaPlatform):
		r.Expected.Platform = f.MediaPlatform
	case platformRe.MatchString("linux/" + f.GOARCH):
		r.Expected.Platform = "linux/" + f.GOARCH
	}
	return r, nil
}

// sanitizeRef returns ref when it is a plain image reference, and "" for
// anything else: a reference with user information before the registry
// (user:secret@host/...) is never recorded.
func sanitizeRef(ref string) string {
	if !refRe.MatchString(ref) || strings.Contains(ref, "..") {
		return ""
	}
	_, name := splitTransport(ref)
	if at := strings.IndexByte(name, '@'); at >= 0 && !validDigest(name[at+1:]) {
		return ""
	}
	return ref
}

var versionRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._+~:-]{0,63}$`)

func sanitizeVersion(v string) string {
	v = strings.TrimSpace(v)
	if !versionRe.MatchString(v) {
		return ""
	}
	return v
}

// Transports, as bootc names them.
const (
	TransportRegistry          = "registry"
	TransportContainersStorage = "containers-storage"
	TransportOCI               = "oci"
	TransportOCIArchive        = "oci-archive"
)

// splitTransport splits a reference the way the install hands it to bootc.
func splitTransport(ref string) (transport, name string) {
	for _, t := range []string{TransportContainersStorage, TransportOCIArchive, TransportOCI} {
		if strings.HasPrefix(ref, t+":") {
			return t, strings.TrimPrefix(ref, t+":")
		}
	}
	if strings.HasPrefix(ref, "registry:") {
		return TransportRegistry, strings.TrimPrefix(ref, "registry:")
	}
	return TransportRegistry, strings.TrimPrefix(ref, "docker://")
}

// OstreeManifestDigest reads the manifest digest bootc recorded in the
// installed ostree commit (metadata key ostree.manifest-digest). out runs a
// read-only command. It returns "" for a composefs deployment, a missing
// ostree tool, or anything that is not one sha256 digest.
func OstreeManifestDigest(d *deploy.Deployment, out func(name string, args ...string) ([]byte, error)) string {
	if d == nil || d.Layout != deploy.Ostree {
		return ""
	}
	base := filepath.Base(d.Root)
	i := strings.LastIndexByte(base, '.')
	if i <= 0 || !ostreeCsRe.MatchString(base[:i]) {
		return ""
	}
	b, err := out("ostree", "show", "--repo="+filepath.Join(d.Sysroot, "ostree", "repo"),
		"--print-metadata-key=ostree.manifest-digest", base[:i])
	if err != nil {
		return ""
	}
	// GVariant text: 'sha256:...'
	s := strings.Trim(strings.TrimSpace(string(b)), `'"`)
	if !validDigest(s) {
		return ""
	}
	return s
}

// ErrTransientEtc means the image asks for a transient /etc: the receipt
// is written (it is in /var), but the first-boot unit would be lost at the
// first boot, so it is not staged.
var ErrTransientEtc = errors.New("the image's /etc is transient; the first-boot collector is not staged")

// Write stores the receipt in the installed system's /var and stages the
// first-boot collector in the deployment's /etc. d comes from the
// deployment resolver (post.ResolveDeployment). On ErrTransientEtc the
// receipt is written and the unit is not.
func Write(d *deploy.Deployment, r Receipt) error {
	if d == nil {
		return errors.New("receipt: no deployment")
	}
	if !hexIDRe.MatchString(r.InstallID) {
		return errors.New("receipt: no install id")
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFile(d.Var, strings.TrimPrefix(ReceiptPath, "/var/"), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write receipt: %w", err)
	}
	if d.EtcIsTransient() {
		return ErrTransientEtc
	}
	if err := writeFile(d.Etc, strings.TrimPrefix(ScriptPath, "/etc/"), []byte(CollectorScript(r.InstallID)), 0o644); err != nil {
		return fmt.Errorf("write collector: %w", err)
	}
	if err := writeFile(d.Etc, "systemd/system/"+UnitName, []byte(CollectorUnit()), 0o644); err != nil {
		return fmt.Errorf("write unit: %w", err)
	}
	// Enable through a wants link: systemctl cannot enable offline.
	wants := filepath.Join(d.Etc, "systemd", "system", "multi-user.target.wants")
	if err := mkdirLabeled(d.Etc, "systemd/system/multi-user.target.wants"); err != nil {
		return fmt.Errorf("enable unit: %w", err)
	}
	link := filepath.Join(wants, UnitName)
	_ = os.Remove(link)
	if err := os.Symlink("/etc/systemd/system/"+UnitName, link); err != nil {
		return fmt.Errorf("enable unit: %w", err)
	}
	deploy.InheritLabel(link)
	return nil
}

// mkdirLabeled creates base/rel one directory at a time, so each new
// directory gets the label of its parent.
func mkdirLabeled(base, rel string) error {
	cur := base
	for _, part := range strings.Split(rel, "/") {
		cur = filepath.Join(cur, part)
		if fi, err := os.Lstat(cur); err == nil {
			if !fi.IsDir() {
				return fmt.Errorf("%s is not a directory", cur)
			}
			continue
		}
		if err := os.Mkdir(cur, 0o755); err != nil {
			return err
		}
		deploy.InheritLabel(cur)
	}
	return nil
}

// writeFile writes base/rel atomically (temporary file, rename), never
// through a symlink that was already there.
func writeFile(base, rel string, data []byte, mode os.FileMode) error {
	path := filepath.Join(base, rel)
	if err := mkdirLabeled(base, filepath.Dir(rel)); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr, os.Chmod(tmp.Name(), mode)); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	deploy.InheritLabel(path)
	return nil
}

// CollectorUnit is the first-boot unit. It runs once: the collection it
// writes is its own stop condition.
func CollectorUnit() string {
	return `# Written by fisherman. See docs/RECEIPT.md in tuna-os/fisherman.
# Records what the installed system booted, once. It reads bootc status and
# changes nothing else: no image switch, no boot order, no rollback, no
# reboot, no network. Remove this file to stop it.
[Unit]
Description=Record the booted bootc image for the installation receipt
ConditionPathExists=` + ReceiptPath + `
ConditionPathExists=!` + CollectionDir + "/" + CollectionFile + `
After=local-fs.target basic.target

[Service]
Type=oneshot
ExecStart=/bin/sh ` + ScriptPath + `
TimeoutStartSec=120

[Install]
WantedBy=multi-user.target
`
}

// CollectorScript is the collector. installID must be 32 hex characters
// (it is the only value fisherman puts in it). Every other value it writes
// to collection.json is a number, or is checked against a fixed set of
// characters first, so the output is valid JSON whatever the system says.
func CollectorScript(installID string) string {
	if !hexIDRe.MatchString(installID) {
		installID = ""
	}
	return `#!/bin/sh
# Written by fisherman. See docs/RECEIPT.md in tuna-os/fisherman.
# Stores the output of the image's own bootc status next to the installation
# receipt. Read-only for everything but ` + CollectionDir + `.
set -u
umask 022
dir=` + CollectionDir + `
mkdir -p "$dir" || exit 0
tmp="$dir/.collect.$$"
rm -rf "$tmp"
mkdir "$tmp" || exit 0
rc=127
if command -v bootc >/dev/null 2>&1; then
	bootc status --format json --format-version 1 >"$tmp/` + StatusFile + `" 2>/dev/null
	rc=$?
	bootc --version 2>/dev/null | head -n 1 >"$tmp/` + VersionFile + `"
fi
boot_id=$(cat /proc/sys/kernel/random/boot_id 2>/dev/null || true)
case "$boot_id" in *[!0-9a-f-]*) boot_id= ;; esac
now=$(date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || true)
case "$now" in *[!0-9TZ:-]*) now= ;; esac
printf '{"schema":"` + CollectionSchema + `","version":` + fmt.Sprint(CollectionVersion) + `,"install_id":"%s","collected_at":"%s","boot_id":"%s","bootc_exit":%d,"collector":"` + UnitName + `"}\n' \
	"` + installID + `" "$now" "$boot_id" "$rc" >"$tmp/` + CollectionFile + `"
for f in ` + StatusFile + ` ` + VersionFile + `; do
	if [ -e "$tmp/$f" ]; then mv -f "$tmp/$f" "$dir/$f"; else rm -f "$dir/$f"; fi
done
# Last, so its presence means the collection is complete.
mv -f "$tmp/` + CollectionFile + `" "$dir/` + CollectionFile + `"
rmdir "$tmp" 2>/dev/null || true
exit 0
`
}
