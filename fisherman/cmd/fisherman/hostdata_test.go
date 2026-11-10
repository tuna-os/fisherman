package main

import (
	"strings"
	"testing"

	"github.com/tuna-os/fisherman/internal/recipe"
)

// tuna-os/fisherman#411: host Wi-Fi profiles and Bluetooth keys go to the
// target on live media, or when the recipe says so. Never by default from an
// installed host.
func TestCopyHostSecrets_Gate(t *testing.T) {
	tests := []struct {
		name          string
		net, bt       *bool
		live          bool
		wantNet       bool
		wantBT        bool
		wantLiveAsked bool
	}{
		{"installed host, fields absent: nothing", nil, nil, false, false, false, true},
		{"live media, fields absent: both", nil, nil, true, true, true, true},
		{"installed host, both true: both, live not asked", boolPtr(true), boolPtr(true), false, true, true, false},
		{"live media, both false: nothing, live not asked", boolPtr(false), boolPtr(false), true, false, false, false},
		{"installed host, network only", boolPtr(true), nil, false, true, false, true},
		{"live media, bluetooth opted out", nil, boolPtr(false), true, true, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotNet, gotBT bool
			oldN, oldB := copyWiFiConnections, copyBluetoothPairings
			copyWiFiConnections = func(string) (int, error) { gotNet = true; return 2, nil }
			copyBluetoothPairings = func(string) (int, error) { gotBT = true; return 0, nil }
			t.Cleanup(func() { copyWiFiConnections, copyBluetoothPairings = oldN, oldB })

			asked := 0
			r := &recipe.Recipe{CopyNetworkProfiles: tt.net, CopyBluetoothPairings: tt.bt}
			out, _ := captureOutput(t, func() {
				copyHostSecrets("/mnt/t", r, func() bool { asked++; return tt.live })
			})
			if gotNet != tt.wantNet || gotBT != tt.wantBT {
				t.Errorf("copied network=%v bluetooth=%v, want %v %v", gotNet, gotBT, tt.wantNet, tt.wantBT)
			}
			if (asked > 0) != tt.wantLiveAsked || asked > 1 {
				t.Errorf("live detection ran %d times, want asked=%v and at most once", asked, tt.wantLiveAsked)
			}
			// One line per kind, and it never claims a copy that did not happen.
			if tt.wantNet && !strings.Contains(out, "Copied 2 network profile(s) from the host") {
				t.Errorf("no copy line for network profiles:\n%s", out)
			}
			if !tt.wantNet && !strings.Contains(out, "Copying network profiles: skipped") {
				t.Errorf("no skip line for network profiles:\n%s", out)
			}
			if tt.wantBT && !strings.Contains(out, "skipped, the host has none") {
				t.Errorf("a copy of zero pairings must read as skipped:\n%s", out)
			}
			if !tt.wantBT && !strings.Contains(out, "Copying Bluetooth pairings: skipped") {
				t.Errorf("no skip line for Bluetooth:\n%s", out)
			}
		})
	}
}
