package probe

import (
	"reflect"
	"strings"
	"testing"
)

const bootcStatusLive = `{"apiVersion":"org.containers.bootc/v1","kind":"BootcHost",
"status":{"booted":{"image":{"image":{"image":"ghcr.io/example/os:stable","transport":"registry"}}}}}`

func TestLive(t *testing.T) {
	tests := []struct {
		name    string
		sandbox map[string]string // what the probe's own root holds
		host    map[string]string // host-only paths, reachable via the runner
		bootc   string            // `bootc status --json` output; "" fails
		want    Live
	}{
		{
			name:    "ostree live marker",
			sandbox: map[string]string{"run/ostree-live": "", "proc/cmdline": "quiet\n"},
			bootc:   bootcStatusLive,
			want:    Live{IsLive: true, LiveImage: "ghcr.io/example/os:stable", DetectedBy: "/run/ostree-live"},
		},
		{
			name:    "dmsquash cmdline",
			sandbox: map[string]string{"proc/cmdline": "BOOT_IMAGE=/images/pxeboot/vmlinuz root=live:CDLABEL=Live rd.live.image quiet\n"},
			bootc:   bootcStatusLive,
			want:    Live{IsLive: true, LiveImage: "ghcr.io/example/os:stable", DetectedBy: "cmdline:root=live:"},
		},
		{
			name:    "rd.live.image alone",
			sandbox: map[string]string{"proc/cmdline": "rd.live.image rd.live.overlay.overlayfs=1\n"},
			bootc:   bootcStatusLive,
			want:    Live{IsLive: true, LiveImage: "ghcr.io/example/os:stable", DetectedBy: "cmdline:rd.live.image"},
		},
		{
			name:    "an installed ostree host is not live",
			sandbox: map[string]string{"run/ostree-booted": "", "proc/cmdline": "root=UUID=abcd ostree=/ostree/boot.1/default/0\n"},
			bootc:   bootcStatusLive,
			want:    Live{},
		},
		{
			name:    "similar-looking cmdline tokens are not live",
			sandbox: map[string]string{"proc/cmdline": "rd.live.imagex notroot=live:x\n"},
			want:    Live{},
		},
		{
			name:    "builder flag file",
			sandbox: map[string]string{"etc/bootc-installer/live-iso-mode": "", "proc/cmdline": "quiet\n"},
			bootc:   bootcStatusLive,
			want:    Live{IsLive: true, LiveImage: "ghcr.io/example/os:stable", DetectedBy: "/etc/bootc-installer/live-iso-mode"},
		},
		{
			name:    "live but bootc cannot say",
			sandbox: map[string]string{"run/ostree-live": ""},
			want:    Live{IsLive: true, DetectedBy: "/run/ostree-live"},
		},
		{
			name:    "live but nothing booted",
			sandbox: map[string]string{"run/ostree-live": ""},
			bootc:   `{"status":{"booted":null}}`,
			want:    Live{IsLive: true, DetectedBy: "/run/ostree-live"},
		},
		{
			name:    "flatpak: host /run/ostree-live is asked of the host",
			sandbox: map[string]string{flatpakMarker: "", "proc/cmdline": "quiet\n"},
			host:    map[string]string{"run/ostree-live": ""},
			bootc:   bootcStatusLive,
			want:    Live{IsLive: true, LiveImage: "ghcr.io/example/os:stable", DetectedBy: "/run/ostree-live"},
		},
		{
			name:    "flatpak: cmdline is the kernel's and readable in the sandbox",
			sandbox: map[string]string{flatpakMarker: "", "proc/cmdline": "rd.live.image\n"},
			bootc:   bootcStatusLive,
			want:    Live{IsLive: true, LiveImage: "ghcr.io/example/os:stable", DetectedBy: "cmdline:rd.live.image"},
		},
		{
			name:    "flatpak: host flag file under /run/host/etc",
			sandbox: map[string]string{flatpakMarker: "", "proc/cmdline": "quiet\n", "run/host/etc/bootc-installer/live-iso-mode": ""},
			bootc:   bootcStatusLive,
			want:    Live{IsLive: true, LiveImage: "ghcr.io/example/os:stable", DetectedBy: "/etc/bootc-installer/live-iso-mode"},
		},
		{
			name:    "flatpak: the runtime's own /etc does not count",
			sandbox: map[string]string{flatpakMarker: "", "proc/cmdline": "quiet\n", "etc/bootc-installer/live-iso-mode": ""},
			want:    Live{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := &fakeHost{canned: map[string]string{}}
			if tt.host != nil {
				host.root = fakeRoot(t, tt.host)
			}
			if tt.bootc != "" {
				host.canned["bootc status --json"] = tt.bootc
			}
			p := newProber(t, tt.sandbox, host, nil)
			if got := p.Live(); got != tt.want {
				t.Errorf("Live() = %+v, want %+v", got, tt.want)
			}
			if !tt.want.IsLive {
				for _, c := range host.calls {
					if strings.HasPrefix(c, "bootc") {
						t.Errorf("ran %q on a non-live host", c)
					}
				}
			}
		})
	}
}

const storeIndex = `[{"id":"aaa","names":["ghcr.io/example/os:stable"]},{"id":"bbb","names":["ghcr.io/example/os:beta","localhost/os:beta"]}]`
const superISOIndex = `[{"id":"ccc","names":["ghcr.io/example/os:stable","ghcr.io/example/other:latest"]}]`

func TestOffline(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		sandbox map[string]string
		host    map[string]string
		want    Offline
	}{
		{
			name: "nothing on this machine",
			want: Offline{Stores: []string{}, Images: []string{}},
		},
		{
			name: "host: env, listing, default and SuperISO stores, de-duplicated",
			env:  "/srv/env-store:/srv/missing:relative/store:/srv/env-store/",
			sandbox: map[string]string{
				"srv/env-store/vfs-images/images.json":                             `[{"names":["quay.io/env/img:1"]}]`,
				"etc/tuna-installer/offline-stores":                                "# stores on this ISO\n/srv/listed\n\n/srv/env-store\n",
				"srv/listed/":                                                      "",
				"usr/share/tuna-installer/oci-store/overlay-images/images.json":    storeIndex,
				"var/lib/superiso-store/overlay-images/images.json":                superISOIndex,
				"usr/share/tuna-installer/oci-store/overlay-images/unrelated.json": "{}",
			},
			want: Offline{
				Stores: []string{"/srv/env-store", "/srv/listed", "/usr/share/tuna-installer/oci-store", "/var/lib/superiso-store"},
				Images: []string{"quay.io/env/img:1", "ghcr.io/example/os:stable", "ghcr.io/example/os:beta", "localhost/os:beta", "ghcr.io/example/other:latest"},
			},
		},
		{
			name:    "a file where a store should be is not a store",
			sandbox: map[string]string{"usr/share/tuna-installer/oci-store": "not a dir"},
			want:    Offline{Stores: []string{}, Images: []string{}},
		},
		{
			name:    "unreadable index: store listed, no images",
			sandbox: map[string]string{"usr/share/tuna-installer/oci-store/overlay-images/images.json": "{broken"},
			want:    Offline{Stores: []string{"/usr/share/tuna-installer/oci-store"}, Images: []string{}},
		},
		{
			name: "flatpak: host /etc and /usr under /run/host, the rest asked of the host",
			sandbox: map[string]string{
				flatpakMarker: "",
				"run/host/etc/tuna-installer/offline-stores":                             "/run/media/live/store\n",
				"run/host/usr/share/tuna-installer/oci-store/overlay-images/images.json": storeIndex,
				// The runtime's own /usr and /etc are not the host's.
				"usr/share/tuna-installer/oci-store/overlay-images/images.json": `[{"names":["runtime/should-not-appear:1"]}]`,
				"etc/tuna-installer/offline-stores":                             "/runtime/store\n",
				"runtime/store/":                                                "",
			},
			host: map[string]string{
				"run/media/live/store/overlay-images/images.json":   `[{"names":["ghcr.io/example/media:1"]}]`,
				"var/lib/superiso-store/overlay-images/images.json": superISOIndex,
			},
			want: Offline{
				Stores: []string{"/run/media/live/store", "/usr/share/tuna-installer/oci-store", "/var/lib/superiso-store"},
				Images: []string{"ghcr.io/example/media:1", "ghcr.io/example/os:stable", "ghcr.io/example/os:beta", "localhost/os:beta", "ghcr.io/example/other:latest"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := &fakeHost{}
			if tt.host != nil {
				host.root = fakeRoot(t, tt.host)
			}
			p := newProber(t, tt.sandbox, host, func(k string) string {
				if k == OfflineStoresEnv {
					return tt.env
				}
				return ""
			})
			if got := p.Offline(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Offline() =\n %+v\nwant\n %+v", got, tt.want)
			}
		})
	}
}

// Outside a sandbox the probe never shells out for a file test or read.
func TestOfflineNoHostCommandsOutsideFlatpak(t *testing.T) {
	host := &fakeHost{}
	p := &Prober{Root: fakeRoot(t, map[string]string{"usr/share/tuna-installer/oci-store/": ""}), Run: host.run, Getenv: noEnv}
	p.Offline()
	if len(host.calls) != 0 {
		t.Errorf("ran %v", host.calls)
	}
}
