package post_test

// Host Wi-Fi profiles and Bluetooth pairings, copied into sysroots shaped
// like real installs (tuna-os/fisherman#407, #411).

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tuna-os/fisherman/internal/deploy/deploytest"
	"github.com/tuna-os/fisherman/internal/post"
)

func hostDirs(t *testing.T) (bt, nm string) {
	t.Helper()
	host := t.TempDir()
	bt = filepath.Join(host, "bluetooth")
	nm = filepath.Join(host, "system-connections")
	restore := post.SetHostSecretDirs(bt, nm)
	t.Cleanup(restore)
	return bt, nm
}

func TestCopyBluetoothPairings_Fixtures(t *testing.T) {
	for name, fx := range fixtures(t) {
		t.Run(name, func(t *testing.T) {
			realFileCommands(t)
			bt, _ := hostDirs(t)
			dev := filepath.Join(bt, "AA:BB:CC:DD:EE:FF", "11:22:33:44:55:66")
			if err := os.MkdirAll(dev, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dev, "info"), []byte("[LinkKey]\nKey=00\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			n, err := post.CopyBluetoothPairings(fx.Path)
			if err != nil || n != 1 {
				t.Fatalf("CopyBluetoothPairings = %d, %v; want 1 adapter", n, err)
			}
			// In the /var the booted system mounts (#407: it went to
			// <target>/var/lib/bluetooth on ostree).
			got := filepath.Join(fx.Var, "lib", "bluetooth", "AA:BB:CC:DD:EE:FF", "11:22:33:44:55:66", "info")
			if fi, err := os.Stat(got); err != nil || fi.Mode().Perm() != 0o600 {
				t.Errorf("pairing in the booted /var: %v", err)
			}
			if fi, err := os.Stat(filepath.Join(fx.Var, "lib", "bluetooth")); err != nil || fi.Mode().Perm() != 0o700 {
				t.Errorf("bluetooth dir: %v, want mode 700", err)
			}
			assertNoStrays(t, fx)
		})
	}
}

func TestCopyWiFiConnections_Fixtures(t *testing.T) {
	for name, fx := range fixtures(t) {
		t.Run(name, func(t *testing.T) {
			realFileCommands(t)
			_, nm := hostDirs(t)
			if err := os.MkdirAll(nm, 0o755); err != nil {
				t.Fatal(err)
			}
			for f, mode := range map[string]os.FileMode{"home.nmconnection": 0o600, "work.nmconnection": 0o600, "README": 0o644} {
				if err := os.WriteFile(filepath.Join(nm, f), []byte("psk=secret\n"), mode); err != nil {
					t.Fatal(err)
				}
			}
			n, err := post.CopyWiFiConnections(fx.Path)
			if err != nil || n != 2 {
				t.Fatalf("CopyWiFiConnections = %d, %v; want 2 profiles", n, err)
			}
			dst := filepath.Join(fx.Etc, "NetworkManager", "system-connections")
			for _, f := range []string{"home.nmconnection", "work.nmconnection"} {
				if fi, err := os.Stat(filepath.Join(dst, f)); err != nil || fi.Mode().Perm() != 0o600 {
					t.Errorf("%s in the deployment etc: %v", f, err)
				}
			}
			if _, err := os.Stat(filepath.Join(dst, "README")); err == nil {
				t.Error("copied a file that is not a profile")
			}
			assertNoStrays(t, fx)
		})
	}
}

// Nothing to copy is zero and no error, and creates nothing: the caller
// reports it as skipped, never as done.
func TestCopyHostSecrets_NothingOnTheHost(t *testing.T) {
	fx := deploytest.Ostree(t)
	realFileCommands(t)
	bt, nm := hostDirs(t)

	if n, err := post.CopyBluetoothPairings(fx.Path); n != 0 || err != nil {
		t.Errorf("no host dir: %d, %v", n, err)
	}
	if n, err := post.CopyWiFiConnections(fx.Path); n != 0 || err != nil {
		t.Errorf("no host dir: %d, %v", n, err)
	}
	// Present but empty (bluetoothd with no adapter; NM with no profile).
	for _, d := range []string{bt, nm} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(nm, "notes.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if n, err := post.CopyBluetoothPairings(fx.Path); n != 0 || err != nil {
		t.Errorf("empty host dir: %d, %v", n, err)
	}
	if n, err := post.CopyWiFiConnections(fx.Path); n != 0 || err != nil {
		t.Errorf("no profiles: %d, %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(fx.Var, "lib", "bluetooth")); err == nil {
		t.Error("created a bluetooth dir with nothing to copy")
	}
	if _, err := os.Stat(filepath.Join(fx.Etc, "NetworkManager")); err == nil {
		t.Error("created a NetworkManager dir with nothing to copy")
	}
	assertNoStrays(t, fx)
}
