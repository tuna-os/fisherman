package main

import (
	"fmt"

	"github.com/tuna-os/fisherman/internal/post"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/recipe"
)

// Replaceable in tests.
var (
	copyWiFiConnections   = post.CopyWiFiConnections
	copyBluetoothPairings = post.CopyBluetoothPairings
)

// copyHostSecrets copies the host's NetworkManager profiles and Bluetooth
// pairings into the installed system when the recipe and the host allow it,
// and logs one line for each: what was copied, or why nothing was.
//
// Both hold secrets (Wi-Fi passwords, 802.1X credentials, Bluetooth link
// keys). On live media they are what the user just set up in the installer
// session, and copying them is the point. On any other host (CI, an
// installed machine that prepares a disk for someone else) they are the
// operator's, so the default there is not to copy
// (recipe.FollowLive, tuna-os/fisherman#411).
//
// isLive is asked at most once, and only when a field is absent.
func copyHostSecrets(target string, r *recipe.Recipe, isLive func() bool) {
	var liveKnown, live bool
	hostIsLive := func() bool {
		if !liveKnown {
			live, liveKnown = isLive(), true
		}
		return live
	}
	decide := func(field *bool, name string) (bool, string) {
		if field != nil {
			if *field {
				return true, ""
			}
			return false, fmt.Sprintf("the recipe sets %s to false", name)
		}
		if hostIsLive() {
			return true, ""
		}
		return false, fmt.Sprintf("this host is not live media (set %s to true to copy them)", name)
	}

	report := func(what string, n int, err error) {
		switch {
		case err != nil:
			progress.Info(fmt.Sprintf("Warning: could not copy %s: %v", what, err))
		case n == 0:
			progress.Info(fmt.Sprintf("Copying %s: skipped, the host has none", what))
		default:
			progress.Info(fmt.Sprintf("Copied %d %s from the host", n, what))
		}
	}

	if ok, why := decide(r.CopyBluetoothPairings, "copyBluetoothPairings"); ok {
		n, err := copyBluetoothPairings(target)
		report("Bluetooth adapter pairing set(s)", n, err)
	} else {
		progress.Info("Copying Bluetooth pairings: skipped, " + why)
	}

	if ok, why := decide(r.CopyNetworkProfiles, "copyNetworkProfiles"); ok {
		n, err := copyWiFiConnections(target)
		report("network profile(s)", n, err)
	} else {
		progress.Info("Copying network profiles: skipped, " + why)
	}
}
