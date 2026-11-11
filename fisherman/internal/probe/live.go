package probe

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/tuna-os/fisherman/internal/install"
)

// Live-media signals. /run/ostree-booted is deliberately NOT one: every
// ostree or bootc host has it, installed or live. bootc-installer's GNOME
// frontend treated it as live, which hid the image picker on installed
// systems.
const (
	// ostreeLiveMarker is written by ostree-based live images.
	ostreeLiveMarker = "/run/ostree-live"
	// liveModeFlag is the explicit opt-in an ISO builder can ship (the GNOME
	// frontend's convention). It is a host /etc path.
	liveModeFlag = "/etc/bootc-installer/live-iso-mode"
)

// Live says whether the host booted from live media, and what it runs.
type Live struct {
	IsLive bool `json:"is_live"`
	// LiveImage is the booted bootc image ref, from `bootc status`. Empty
	// when not live, or when bootc cannot say. Non-empty means a recipe may
	// omit `image`: bootc installs the running container.
	LiveImage string `json:"live_image"`
	// DetectedBy names the signal that said "live": "/run/ostree-live",
	// "cmdline:rd.live.image", "cmdline:root=live:", "cmdline:root=tbox:",
	// "cmdline:tacklebox.live.squashimg" or
	// "/etc/bootc-installer/live-iso-mode". Empty when not live.
	DetectedBy string `json:"detected_by,omitempty"`
}

// Live detects live media. /proc/cmdline is the kernel's and readable from a
// sandbox; /run/ostree-live is the host's /run and is asked of the host from
// inside a Flatpak, where the sandbox has its own /run.
func (p *Prober) Live() Live {
	var l Live
	cmdSig := p.cmdlineSignal()
	switch {
	case p.hostTest("-e", ostreeLiveMarker):
		l.DetectedBy = ostreeLiveMarker
	case cmdSig != "":
		l.DetectedBy = cmdSig
	case p.hostTest("-e", liveModeFlag):
		l.DetectedBy = liveModeFlag
	}
	l.IsLive = l.DetectedBy != ""
	if l.IsLive {
		l.LiveImage = p.bootedImage()
		// A flattened tacklebox live root runs no booted container, so
		// `bootc status` has nothing to say. The media input names the
		// same embedded immutable image; prefer the booted ref whenever
		// bootc knows one.
		if l.LiveImage == "" && isTboxSignal(cmdSig) {
			if mi, err := p.MediaInput(); err == nil && mi != nil {
				l.LiveImage = mi.Image
			}
		}
	}
	return l
}

// cmdlineSignal returns "cmdline:<token>" for a live-boot argument:
// rd.live.image (dmsquash-live), a root=live: source, a root=tbox: source
// or a tacklebox.live.squashimg= image on tacklebox live media.
func (p *Prober) cmdlineSignal() string {
	cmdline, err := p.readKernelFile("/proc/cmdline")
	if err != nil {
		return ""
	}
	for _, tok := range strings.Fields(cmdline) {
		switch {
		case tok == "rd.live.image" || strings.HasPrefix(tok, "rd.live.image="):
			return "cmdline:rd.live.image"
		case strings.HasPrefix(tok, "root=live:"):
			return "cmdline:root=live:"
		case strings.HasPrefix(tok, "root=tbox:"):
			return "cmdline:root=tbox:"
		case tok == "tacklebox.live.squashimg" || strings.HasPrefix(tok, "tacklebox.live.squashimg="):
			return "cmdline:tacklebox.live.squashimg"
		}
	}
	return ""
}

// isTboxSignal reports whether a cmdline signal comes from tacklebox live
// media (a flattened live root with an embedded immutable image).
func isTboxSignal(sig string) bool {
	return sig == "cmdline:root=tbox:" || sig == "cmdline:tacklebox.live.squashimg"
}

// bootedImage is .status.booted.image.image.image from `bootc status
// --json`, the ref every frontend read for the live image.
func (p *Prober) bootedImage() string {
	return p.hostStatus().image
}

// bootcHostStatus is what fisherman reads from the host's `bootc status
// --json`. Only these fields are taken; the rest of bootc's JSON is not part
// of any fisherman contract.
type bootcHostStatus struct {
	// ok: bootc ran and printed a JSON object.
	ok bool
	// missing: there is no bootc command on the host.
	missing bool
	// failed: bootc exists, but `bootc status --json` failed.
	failed bool
	// booted: the object names a booted deployment with an image.
	booted bool
	image  string
	// digest is .status.booted.image.imageDigest when it is a sha256 digest.
	digest string
	// arch is .status.booted.image.architecture when it is a plain token.
	arch string
	// backend is "ostree" or "composefs" when the booted entry says which.
	backend string
	// deployments are the boot entries that carry an image, in the order
	// booted, staged, rollback, otherDeployments (the bootc store
	// inventory, tuna-os/fisherman#384).
	deployments []bootcDeployment
}

// bootcDeployment is one boot entry's image as `bootc status --json`
// reports it. Values are raw; the inventory validates them.
type bootcDeployment struct {
	role   string
	image  string
	digest string
	arch   string
	// transport is .image.image.transport ("registry",
	// "containers-storage", ...); the source reuse (#384) needs it to name
	// the image to bootc.
	transport string
	// backend is "ostree" or "composefs" for this entry.
	backend string
	// The ostree deployment: commit checksum, stateroot and serial, from
	// .ostree. Empty/-1 when the entry is not an ostree deployment.
	checksum  string
	stateroot string
	serial    int
}

// statusEntry is the part of a bootc BootEntry fisherman reads.
type statusEntry struct {
	Image *struct {
		Image struct {
			Image     string `json:"image"`
			Transport string `json:"transport"`
		} `json:"image"`
		ImageDigest  string `json:"imageDigest"`
		Architecture string `json:"architecture"`
	} `json:"image"`
	Ostree    json.RawMessage `json:"ostree"`
	Composefs json.RawMessage `json:"composefs"`
}

// hostStatus runs `bootc status --json` once per Prober and keeps the answer:
// Live and Capabilities both need it, and it is the same host.
func (p *Prober) hostStatus() bootcHostStatus {
	if p.status != nil {
		return *p.status
	}
	st := parseBootcStatus(p.run("bootc", "status", "--json"))
	p.status = &st
	return st
}

func parseBootcStatus(out []byte, err error) bootcHostStatus {
	var st bootcHostStatus
	if err != nil {
		st.missing = commandMissing(err)
		st.failed = !st.missing
		return st
	}
	var status struct {
		Status struct {
			Booted           *statusEntry  `json:"booted"`
			Staged           *statusEntry  `json:"staged"`
			Rollback         *statusEntry  `json:"rollback"`
			OtherDeployments []statusEntry `json:"otherDeployments"`
		} `json:"status"`
	}
	if json.Unmarshal(out, &status) != nil {
		return st
	}
	st.ok = true
	add := func(role string, e *statusEntry) {
		if e != nil && e.Image != nil && len(st.deployments) < maxDeployments {
			d := bootcDeployment{role: role, image: e.Image.Image.Image, digest: e.Image.ImageDigest,
				arch: e.Image.Architecture, transport: e.Image.Image.Transport, serial: -1}
			switch {
			case present(e.Composefs):
				d.backend = BackendComposefs
			case present(e.Ostree):
				d.backend = BackendOstree
				var o struct {
					Checksum     string `json:"checksum"`
					DeploySerial *int   `json:"deploySerial"`
					Stateroot    string `json:"stateroot"`
				}
				if json.Unmarshal(e.Ostree, &o) == nil {
					d.checksum, d.stateroot = o.Checksum, o.Stateroot
					if o.DeploySerial != nil {
						d.serial = *o.DeploySerial
					}
				}
			}
			st.deployments = append(st.deployments, d)
		}
	}
	add(RoleBooted, status.Status.Booted)
	add(RoleStaged, status.Status.Staged)
	add(RoleRollback, status.Status.Rollback)
	for i := range status.Status.OtherDeployments {
		add(RoleOtherDeployment, &status.Status.OtherDeployments[i])
	}
	b := status.Status.Booted
	if b == nil || b.Image == nil {
		return st
	}
	st.booted = true
	st.image = b.Image.Image.Image
	if validDigest(b.Image.ImageDigest) {
		st.digest = b.Image.ImageDigest
	}
	if tokenRe.MatchString(b.Image.Architecture) {
		st.arch = b.Image.Architecture
	}
	switch {
	case present(b.Composefs):
		st.backend = BackendComposefs
	case present(b.Ostree):
		st.backend = BackendOstree
	}
	return st
}

// present reports whether a JSON value exists and is not null.
func present(raw json.RawMessage) bool {
	return len(raw) != 0 && string(raw) != "null"
}

// Offline OCI media input, the v1 contract between the tacklebox ISO builder
// and fisherman. The alias keeps one canonical type in install (which probe
// already imports, so the reverse would be a cycle); ParseMediaInput and
// Validate are re-exported here for callers that only know probe.
const (
	// MediaInputPath is the host path the ISO builder writes
	// media-input.json to. A host /etc path, so Flatpak-aware like
	// offlineStoresFile.
	MediaInputPath = "/etc/bootc-installer/media-input.json"
)

// MediaInput describes the immutable embedded image on offline live media.
// See install.MediaInput for the field contract.
type MediaInput = install.MediaInput

// ParseMediaInput decodes media-input.json; see install.ParseMediaInput.
func ParseMediaInput(data []byte) (*MediaInput, error) {
	return install.ParseMediaInput(data)
}

// MediaInput reads the host's media-input.json. Inside a Flatpak the host's
// /etc is read under /run/host, like every other host file. A missing file
// is ordinary (not live media, or media without an embedded image) and
// returns (nil, nil).
func (p *Prober) MediaInput() (*MediaInput, error) {
	data, err := p.hostReadFile(MediaInputPath)
	if err != nil {
		return nil, nil //nolint:nilerr // best-effort: absent/unreadable media input → no binding, install proceeds as before
	}
	return ParseMediaInput(data)
}

// Offline-store discovery, the union of what the frontends looked at.
const (
	// OfflineStoresEnv is a colon-separated list of store roots.
	OfflineStoresEnv = "TUNA_OFFLINE_STORES"
	// offlineStoresFile lists store roots, one per line, # comments.
	offlineStoresFile = "/etc/tuna-installer/offline-stores"
	// offlineStoreDefault is the conventional store on tuna-installer media.
	offlineStoreDefault = "/usr/share/tuna-installer/oci-store"
)

// Offline lists the offline image stores on the host and the images in them.
type Offline struct {
	// Stores are host paths of containers-storage roots that exist, in
	// discovery order: $TUNA_OFFLINE_STORES, /etc/tuna-installer/offline-stores,
	// /usr/share/tuna-installer/oci-store, /var/lib/superiso-store.
	Stores []string `json:"stores"`
	// Images are the image names found across Stores, de-duplicated, in
	// store order.
	Images []string `json:"images"`
}

// Offline discovers stores on the HOST. Inside a Flatpak the host's /etc and
// /usr are read under /run/host; other host paths are asked of the host.
func (p *Prober) Offline() Offline {
	var candidates []string
	if env := p.getenv(OfflineStoresEnv); env != "" {
		candidates = append(candidates, strings.Split(env, ":")...)
	}
	if listing, err := p.hostReadFile(offlineStoresFile); err == nil {
		for _, line := range strings.Split(string(listing), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				candidates = append(candidates, line)
			}
		}
	}
	candidates = append(candidates, offlineStoreDefault, install.LiveMediaImageStore)

	o := Offline{Stores: []string{}, Images: []string{}}
	seenStore := map[string]bool{}
	seenImage := map[string]bool{}
	for _, s := range candidates {
		s = filepath.Clean(strings.TrimSpace(s))
		if s == "." || !filepath.IsAbs(s) || seenStore[s] {
			continue
		}
		seenStore[s] = true
		if !p.hostTest("-d", s) {
			continue
		}
		o.Stores = append(o.Stores, s)
		for _, name := range p.storeImages(s) {
			if !seenImage[name] {
				seenImage[name] = true
				o.Images = append(o.Images, name)
			}
		}
	}
	return o
}

// storeImageIndexes are containers-storage's per-driver image indexes.
var storeImageIndexes = []string{"overlay-images/images.json", "vfs-images/images.json"}

// storeImages reads image names straight from a containers-storage root's
// image index. The frontends ran `podman images --root <store>`, which needs
// podman on the host and takes the store's lock; reading the index is
// read-only and needs neither.
func (p *Prober) storeImages(store string) []string {
	var names []string
	for _, idx := range storeImageIndexes {
		b, err := p.hostReadFile(filepath.Join(store, idx))
		if err != nil {
			continue
		}
		var imgs []struct {
			Names []string `json:"names"`
		}
		if json.Unmarshal(b, &imgs) != nil {
			continue
		}
		for _, img := range imgs {
			names = append(names, img.Names...)
		}
	}
	return names
}
