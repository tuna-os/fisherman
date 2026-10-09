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
	// "cmdline:rd.live.image", "cmdline:root=live:" or
	// "/etc/bootc-installer/live-iso-mode". Empty when not live.
	DetectedBy string `json:"detected_by,omitempty"`
}

// Live detects live media. /proc/cmdline is the kernel's and readable from a
// sandbox; /run/ostree-live is the host's /run and is asked of the host from
// inside a Flatpak, where the sandbox has its own /run.
func (p *Prober) Live() Live {
	var l Live
	switch {
	case p.hostTest("-e", ostreeLiveMarker):
		l.DetectedBy = ostreeLiveMarker
	case p.cmdlineSignal() != "":
		l.DetectedBy = p.cmdlineSignal()
	case p.hostTest("-e", liveModeFlag):
		l.DetectedBy = liveModeFlag
	}
	l.IsLive = l.DetectedBy != ""
	if l.IsLive {
		l.LiveImage = p.bootedImage()
	}
	return l
}

// cmdlineSignal returns "cmdline:<token>" for a dracut live-boot argument:
// rd.live.image (dmsquash-live) or a root=live: source.
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
		}
	}
	return ""
}

// bootedImage is .status.booted.image.image.image from `bootc status
// --json`, the ref every frontend read for the live image.
func (p *Prober) bootedImage() string {
	out, err := p.run("bootc", "status", "--json")
	if err != nil {
		return ""
	}
	var status struct {
		Status struct {
			Booted *struct {
				Image *struct {
					Image struct {
						Image string `json:"image"`
					} `json:"image"`
				} `json:"image"`
			} `json:"booted"`
		} `json:"status"`
	}
	if json.Unmarshal(out, &status) != nil || status.Status.Booted == nil || status.Status.Booted.Image == nil {
		return ""
	}
	return status.Status.Booted.Image.Image.Image
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
