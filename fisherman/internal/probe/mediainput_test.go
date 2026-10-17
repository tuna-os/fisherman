package probe

import (
	"strings"
	"testing"
)

const testMediaInputJSON = `{"schemaVersion":1,"kind":"tacklebox-live","image":"ghcr.io/example/os:stable",` +
	`"platformManifestDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",` +
	`"ociLayoutPath":"/run/media/layout","targetImgref":"ghcr.io/example/os:stable","platform":"linux/amd64"}`

func TestMediaInput(t *testing.T) {
	tests := []struct {
		name    string
		sandbox map[string]string
		host    map[string]string
		wantNil bool
		wantErr bool
	}{
		{
			name:    "no media input on this machine",
			sandbox: map[string]string{},
			wantNil: true,
		},
		{
			name:    "valid media input",
			sandbox: map[string]string{"etc/bootc-installer/media-input.json": testMediaInputJSON},
		},
		{
			name:    "malformed media input is an error, not a silent skip",
			sandbox: map[string]string{"etc/bootc-installer/media-input.json": "{broken"},
			wantErr: true,
		},
		{
			name:    "empty media input is an error",
			sandbox: map[string]string{"etc/bootc-installer/media-input.json": "   \n"},
			wantErr: true,
		},
		{
			name: "flatpak: host media input under /run/host/etc",
			sandbox: map[string]string{
				flatpakMarker: "",
				"run/host/etc/bootc-installer/media-input.json": testMediaInputJSON,
			},
		},
		{
			name: "flatpak: the runtime's own /etc does not count",
			sandbox: map[string]string{
				flatpakMarker:                          "",
				"etc/bootc-installer/media-input.json": testMediaInputJSON,
			},
			wantNil: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := &fakeHost{}
			if tt.host != nil {
				host.root = fakeRoot(t, tt.host)
			}
			p := newProber(t, tt.sandbox, host, nil)
			got, err := p.MediaInput()
			if tt.wantErr {
				if err == nil {
					t.Fatal("malformed media input accepted")
				}
				return
			}
			if err != nil {
				t.Fatalf("MediaInput() error: %v", err)
			}
			if tt.wantNil {
				if got != nil {
					t.Fatalf("MediaInput() = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("MediaInput() = nil, want parsed input")
			}
			if got.Image != "ghcr.io/example/os:stable" || got.OCILayoutPath != "/run/media/layout" ||
				got.TargetImgref != "ghcr.io/example/os:stable" ||
				got.PlatformManifestDigest != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
				t.Fatalf("fields lost in decode: %+v", got)
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("probe media input fails install validation: %v", err)
			}
		})
	}
}

func TestIsTboxSignal(t *testing.T) {
	for _, tt := range []struct {
		signal string
		want   bool
	}{
		{"cmdline:root=tbox:", true},
		{"cmdline:tacklebox.live.squashimg", true},
		{"cmdline:root=live:", false},
		{"cmdline:rd.live.image", false},
		{"/run/ostree-live", false},
		{"", false},
	} {
		if got := isTboxSignal(tt.signal); got != tt.want {
			t.Errorf("isTboxSignal(%q) = %v, want %v", tt.signal, got, tt.want)
		}
	}
}

// A flattened tacklebox live root runs no booted container, so `bootc
// status` has nothing to say; the probe falls back to the media input for
// the live image, which is what a live install then deploys.
func TestLiveTboxFallsBackToMediaInput(t *testing.T) {
	for _, tt := range []struct {
		name     string
		cmdline  string
		detected string
		wantLive bool
	}{
		{"tbox root", "BOOT_IMAGE=vmlinuz root=tbox:/dev/sr0 quiet\n", "cmdline:root=tbox:", true},
		{"tacklebox squashimg", "tacklebox.live.squashimg=/live.img quiet\n", "cmdline:tacklebox.live.squashimg", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			host := &fakeHost{}
			p := newProber(t, map[string]string{
				"proc/cmdline":                         tt.cmdline,
				"etc/bootc-installer/media-input.json": testMediaInputJSON,
			}, host, nil)
			got := p.Live()
			if !got.IsLive || got.DetectedBy != tt.detected {
				t.Fatalf("Live() = %+v, want live via %q", got, tt.detected)
			}
			if got.LiveImage != "ghcr.io/example/os:stable" {
				t.Fatalf("LiveImage = %q, want the embedded media image", got.LiveImage)
			}
			for _, c := range host.calls {
				if strings.HasPrefix(c, "bootc") {
					continue
				}
				t.Errorf("unexpected host command %q", c)
			}
		})
	}
}
