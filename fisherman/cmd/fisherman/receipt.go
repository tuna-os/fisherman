package main

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/tuna-os/fisherman/internal/install"
	"github.com/tuna-os/fisherman/internal/post"
	"github.com/tuna-os/fisherman/internal/receipt"
	"github.com/tuna-os/fisherman/internal/runner"
)

// receiptOutput runs the read-only ostree query of the receipt; tests
// replace it.
var receiptOutput = runner.Output

// receiptNow is the clock; tests fix it.
var receiptNow = time.Now

// receiptUpdateRef is the reference bootc records for updates: the target
// reference when the install passed one, else the source, without its
// transport (install.bootcViaContainer hands bootc the bare reference, so
// the installed system follows the registry); else, for an install of the
// live root, the image the live host booted.
func receiptUpdateRef(sourceImgref, targetImgref, liveImage string) string {
	switch {
	case targetImgref != "":
		return bareRef(targetImgref)
	case sourceImgref != "":
		return bareRef(sourceImgref)
	}
	return liveImage
}

// bareRef strips a transport prefix the way install's bareImageRef does
// ("docker://x", "containers-storage:x" -> "x"; "host:5000/x" is kept).
func bareRef(image string) string {
	if i := strings.Index(image, "://"); i >= 0 {
		return image[i+3:]
	}
	if i := strings.Index(image, ":"); i > 0 && !strings.ContainsAny(image[:i], "/.") {
		return image[i+1:]
	}
	return image
}

// stageReceipt writes the installation receipt and stages the first-boot
// collector in the installed system at target (docs/RECEIPT.md), and
// returns the line to log. It never fails the install: the receipt is
// evidence for later, not part of a bootable system.
func stageReceipt(target, sourceImgref, updateRef string, media *install.MediaInput) string {
	d, err := post.ResolveDeployment(target)
	if err != nil {
		return fmt.Sprintf("Warning: installation receipt not written: %v", err)
	}
	f := receipt.Facts{
		InstallID:        receipt.NewInstallID(),
		InstalledAt:      receiptNow(),
		FishermanVersion: version,
		Deployment:       d,
		SourceRef:        sourceImgref,
		UpdateRef:        updateRef,
		InstalledDigest:  receipt.OstreeManifestDigest(d, receiptOutput),
		GOARCH:           runtime.GOARCH,
	}
	if media != nil {
		f.MediaPlatformDigest = media.PlatformManifestDigest
		f.MediaIndexDigest = media.IndexDigest
		f.MediaPlatform = media.Platform
	}
	rc, err := receipt.New(f)
	if err != nil {
		return fmt.Sprintf("Warning: installation receipt not written: %v", err)
	}
	switch err := receipt.Write(d, rc); {
	case errors.Is(err, receipt.ErrTransientEtc):
		return fmt.Sprintf("Installation receipt written to %s; %v (run `fisherman verify --json` on the installed system)", receipt.ReceiptPath, err)
	case err != nil:
		return fmt.Sprintf("Warning: installation receipt not written: %v", err)
	}
	return fmt.Sprintf("Installation receipt written to %s; the first boot records the booted image (boot not verified yet)", receipt.ReceiptPath)
}
