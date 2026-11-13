package diagnostics

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/fisherman/internal/probe"
)

// Sentinel secrets. Each is planted in at least one input; the bundle must
// hold no byte sequence of any of them (TestSentinelsNeverInBundle). They
// all contain "SENTINEL" (or, for the modhex recovery key and the WireGuard
// key, a unique run) so a leak of any part is found.
var (
	sLUKS        = "SENTINELluks7f3a91Qz"
	sPassword    = "SENTINELpw91c2Xk"
	sHash        = "$6$SENTINELsalt$SENTINELhash0Zq9"
	sRecoveryKey = "vvvvhhhh-tttthhhh-ccccdddd-eeeeffff-gggghhhh-iiiijjjj-kkkkllll-nnnnrrrr"
	sTailscale   = "tskey-auth-SENTINELkCNTRL-Zq0123456789"
	sGitHub      = "ghp_SENTINELaaaabbbbcccc0000"
	sRegistry    = "SENTINELreguser:SENTINELregtoken"
	sWifi        = "SENTINELwifi55aa"
	sSSHKey      = "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmUSENTINELkeybody\n-----END OPENSSH PRIVATE KEY-----"
	sWireGuard   = "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk="
	sUnknown     = "SENTINELunknown0Zq"
	sEnv         = "SENTINELenv0Zq"
	sHost        = "sentinelhq7x"
	sUser        = "sentinelusr"
	sDisk        = "/dev/SENTINELnvme0q"
	sModel       = "SENTINEL Model 9000"
	sStore       = "/SENTINELstore0q"
)

// sentinelSecrets are the values that must never appear, with
// identifiers included or not.
var sentinelSecrets = []string{sLUKS, sPassword, sHash, sRecoveryKey, sTailscale, sGitHub,
	sRegistry, sWifi, sSSHKey, sWireGuard, sUnknown, sEnv, sStore}

// sentinelIdents must not appear while identifiers are masked.
var sentinelIdents = []string{sHost, sUser, sDisk, sModel}

func sentinelRecipe(t *testing.T) []byte {
	t.Helper()
	r := map[string]any{
		"disk":                  sDisk,
		"filesystem":            "btrfs",
		"encryption":            map[string]any{"type": "tpm2-luks-passphrase", "passphrase": sLUKS},
		"image":                 "docker://" + sRegistry + "@ghcr.io/example/os:latest",
		"targetImgref":          "ghcr.io/example/os:latest",
		"hostname":              sHost,
		"user":                  map[string]any{"username": sUser, "fullname": "SENTINEL Full Name", "password": sPassword, "groups": []string{"wheel", "SENTINEL bad group"}, "sshKey": sUnknown},
		"customMounts":          []any{map[string]any{"partition": sDisk + "-part1", "target": "/", "fstype": "xfs", "secret": sUnknown}},
		"varDisk":               map[string]any{"disk": sDisk + "-var", "keepExisting": true},
		"tailscaleKey":          sTailscale,
		"wifiPassword":          sWifi,
		"env":                   map[string]any{"REGISTRY_AUTH": sEnv},
		"brewTap":               "SENTINEL/tap",
		"distroID":              "sentineldistro",
		"locale":                "SENTINEL_locale",
		"timezone":              "Europe/Berlin",
		"keyboard":              map[string]any{"layout": "de", "variant": "SENTINEL variant"},
		"additionalImageStores": []string{sStore},
		"flatpaks":              []string{"org.SENTINEL.App"},
		"extras":                map[string]any{"printServices": true},
		"override":              []string{"SENTINEL_override"},
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sentinelHashRecipe is sentinelRecipe with a crypt hash as the password.
func sentinelHashRecipe(t *testing.T) []byte {
	return bytes.Replace(sentinelRecipe(t), []byte(sPassword), []byte(sHash), 1)
}

// sentinelProgress is the log of the install of a recipe whose user
// password is password.
func sentinelProgress(password string) []byte {
	lines := []string{
		`{"type":"step","step":1,"total_steps":9,"step_name":"Partitioning","step_id":"partition","overall_pct":1}`,
		`{"type":"info","message":"luksFormat with ` + sLUKS + `"}`,
		`{"type":"substep","message":"tailscale up --authkey ` + sTailscale + `"}`,
		`{"type":"recovery_key","key":"` + sRecoveryKey + `"}`,
		`{"type":"info","message":"recovery key ` + sRecoveryKey + `"}`,
		`{"type":"step","step":2,"total_steps":9,"step_name":"x","step_id":"SENTINEL step","token":"` + sGitHub + `"}`,
		`{"type":"secret_event","value":"` + sUnknown + `"}`,
		`not json at all ` + sWifi,
		`{"type":"info","message":"cut ` + password[:10],
		`[fisherman] stderr line with ` + sGitHub,
		`{"type":"receipt","version":1,"written":true,"install_id":"SENTINEL-install-id","path":"/SENTINEL/path","first_boot_check":true}`,
		`{"type":"error","message":"skopeo copy --creds ` + sRegistry + ` psk=` + sWifi + ` key ` + sWireGuard +
			` hash ` + sHash + ` pass ` + strings.ReplaceAll(sSSHKey, "\n", `\n`) + ` on ` + sDisk + ` for ` + sHost + ` user ` + sUser +
			` token: ` + sGitHub + ` luks ` + sLUKS[:12] + `…","code":"bootc_install_failed","field":"SENTINEL field!","step_id":"bootc_install",` +
			`"disk_changed":true,"cleanup":{"status":"failed","message":"umount ` + sDisk + ` busy; ` + password + `"},"extra":"` + sUnknown + `"}`,
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

func sentinelProbe() *probe.Result {
	r := &probe.Result{ProtocolVersion: 1}
	r.Disks = []probe.Disk{{Path: sDisk, SizeBytes: 512 << 30, Model: sModel, Vendor: sModel, Transport: "nvme", Eligible: true}}
	r.Offline.Stores = []string{sStore}
	r.Offline.Images = []string{"ghcr.io/SENTINEL/" + sUnknown}
	r.Live.LiveImage = "docker://" + sRegistry + "@registry.example/live:1"
	r.Bootc.Version = "1.4.0"
	r.Bootc.Capabilities = map[string]probe.Capability{"SENTINEL cap": {Status: "ok"}, "install_to_filesystem": {Status: sUnknown}}
	r.Capabilities.Version = 1
	r.Capabilities.Source.Store = sStore
	r.Capabilities.Source.Ref = "containers-storage:" + sStore
	r.Capabilities.InstallDefaults.Observed = true
	r.BootcStore.Reason = sUnknown
	r.DMI.Product = "Model " + sTailscale
	return r
}

func sentinelRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("etc/os-release", "ID=fedora\nVERSION_ID=43\nPRETTY_NAME=\""+sUnknown+"\"\nHOME_URL=https://"+sRegistry+"@x\nVARIANT_ID=\""+sEnv+" bad!\"\n")
	write("proc/sys/kernel/osrelease", "6.18.1-200.fc43.x86_64\n")
	write("proc/self/mountinfo", strings.Join([]string{
		"22 1 0:21 / / rw,relatime - xfs /dev/vda3 rw",
		"90 22 253:0 / /mnt/fisherman-target rw,relatime - xfs /dev/mapper/fisherman-root rw",
		"91 90 259:2 / /mnt/fisherman-target/boot ro,relatime - ext4 " + sDisk + "-part2 rw",
		"92 22 0:50 / /var/fisherman-tmp rw - tmpfs " + sUnknown + " rw",
		"93 22 0:51 / /home/" + sUser + "/" + sWifi + " rw - fuse sshfs#" + sRegistry + " rw",
	}, "\n")+"\n")
	write("sys/block/dm-0/dm/name", "fisherman-root\n")
	write("sys/block/dm-1/dm/name", "luks-"+sUnknown+"\n")
	return root
}

// windows returns every 8-byte window of s (the whole s when shorter),
// skipping windows that are only punctuation or line structure.
func windows(s string) []string {
	if len(s) <= 8 {
		return []string{s}
	}
	var out []string
	for i := 0; i+8 <= len(s); i++ {
		w := s[i : i+8]
		if strings.ContainsAny(w, "\n") || strings.Count(w, "-") > 4 || strings.Trim(w, "- ") == "" {
			continue
		}
		out = append(out, w)
	}
	return out
}

// knownBenign are windows of sentinels that the bundle holds for other
// reasons (the PEM armour words; the "/var/lib/" prefix of a path).
var knownBenign = []string{}

func assertNoSentinel(t *testing.T, out []byte, values []string) {
	t.Helper()
	low := bytes.ToLower(out)
	if bytes.Contains(low, []byte("sentinel")) {
		i := bytes.Index(low, []byte("sentinel"))
		lo, hi := max(0, i-80), min(len(out), i+80)
		t.Errorf("bundle holds %q:\n...%s...", "sentinel", out[lo:hi])
	}
	for _, v := range values {
		for _, w := range windows(v) {
			benign := false
			for _, b := range knownBenign {
				if strings.Contains(b, w) || strings.Contains(w, b) {
					benign = true
				}
			}
			if !benign && bytes.Contains(out, []byte(w)) {
				i := bytes.Index(out, []byte(w))
				lo, hi := max(0, i-80), min(len(out), i+80)
				t.Errorf("bundle holds %q of a planted secret:\n...%s...", w, out[lo:hi])
			}
		}
	}
}

// TestSentinelsNeverInBundle plants known secrets in every input (recipe,
// including unknown fields; progress log, including malformed, mixed and
// unknown events; probe report; os-release; mountinfo; device-mapper
// names) and fails if any part of one reaches the bundle.
func TestSentinelsNeverInBundle(t *testing.T) {
	root := sentinelRoot(t)
	for _, ids := range []bool{false, true} {
		for name, c := range map[string]struct {
			recipe   []byte
			password string
		}{"plain": {sentinelRecipe(t), sPassword}, "hash": {sentinelHashRecipe(t), sHash}} {
			in := Inputs{
				Versions: Versions{Fisherman: "v0.2.0-50", Go: "go1.24.7", OS: "linux", Arch: "amd64"},
				Recipe:   c.recipe, Progress: sentinelProgress(c.password), Probe: sentinelProbe(),
				Root: root, IncludeIdentifiers: ids, Now: time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC),
			}
			out, err := Marshal(Build(in))
			if err != nil {
				t.Fatal(err)
			}
			values := sentinelSecrets
			if !ids {
				values = append(append([]string{}, sentinelSecrets...), sentinelIdents...)
			}
			t.Run(name, func(t *testing.T) {
				if ids {
					// Identifiers are kept on request, but the secrets of the
					// windows test must not overlap with them.
					assertNoSentinelExcept(t, out, values)
					return
				}
				assertNoSentinel(t, out, values)
			})
		}
	}
}

// assertNoSentinelExcept is assertNoSentinel for a bundle that keeps
// identifiers: the identifier sentinels may appear, the secrets not.
func assertNoSentinelExcept(t *testing.T, out []byte, values []string) {
	t.Helper()
	scrubbed := out
	for _, id := range sentinelIdents {
		scrubbed = bytes.ReplaceAll(scrubbed, []byte(id), []byte("ID"))
	}
	// Masked device paths derived from sDisk ("-part1", "-var") too.
	assertNoSentinel(t, scrubbed, values)
}
