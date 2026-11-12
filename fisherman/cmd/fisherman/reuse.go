package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/tuna-os/fisherman/internal/install"
	"github.com/tuna-os/fisherman/internal/post"
	"github.com/tuna-os/fisherman/internal/probe"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/receipt"
	"github.com/tuna-os/fisherman/internal/recipe"
	"github.com/tuna-os/fisherman/internal/reuse"
	"github.com/tuna-os/fisherman/internal/runner"
)

// Source reuse (tuna-os/fisherman#384, phase 2): the CLI side. The host
// checks run in full validate and the install (reuseProblems); only the
// install runs the copy (acquireSourceReuse). docs/SOURCE-REUSE.md.

// reuseScratch is the copy's scratch directory: under the install's own
// disk-backed scratch (/var/fisherman-tmp), never /run.
const reuseScratch = "/var/fisherman-tmp/source-reuse"

// hostReuseSource resolves a sourceReuse against this host, read-only
// (probe.Prober.ReuseSource). Tests replace it.
var hostReuseSource = func(digest, platform string) probe.ReuseSource {
	return newProber().ReuseSource(digest, platform)
}

// reuseProblems is the host half of the sourceReuse rules. It runs only
// for a recipe that has sourceReuse and whose static sourceReuse rules
// passed (soFar has no source_reuse_* problem), and it only reads: `bootc
// status --json`, the live-media signals and bootc's store index file.
func reuseProblems(r *recipe.Recipe, soFar []recipe.Problem) []recipe.Problem {
	if r.SourceReuse == nil {
		return nil
	}
	for _, p := range soFar {
		if strings.HasPrefix(p.Code, "source_reuse_") {
			return nil
		}
	}
	if required, err := hostMediaRequired(); err == nil && required {
		return []recipe.Problem{reuseProblem(recipe.CodeSourceReuseLiveMedia,
			"sourceReuse is not qualified on live media: bootc refuses copy-to-storage on a read-only sysroot")}
	}
	res := hostReuseSource(r.SourceReuse.Digest, r.SourceReuse.Platform)
	if p, bad := reuseResolutionProblem(r.SourceReuse, res); bad {
		return []recipe.Problem{p}
	}
	return nil
}

func reuseProblem(code, msg string) recipe.Problem {
	return recipe.Problem{Field: "sourceReuse", Code: code, Message: msg}
}

// reuseResolutionProblem maps probe's answer to a validate code.
func reuseResolutionProblem(s *recipe.SourceReuse, res probe.ReuseSource) (recipe.Problem, bool) {
	switch res.Reason {
	case probe.ReuseEligible:
		if res.Eligible && len(res.Candidates) > 0 {
			return recipe.Problem{}, false
		}
	case probe.ReuseHostNotBootc:
		return reuseProblem(recipe.CodeSourceReuseHostNotBootc, fmt.Sprintf(
			"sourceReuse needs a bootc-booted host: bootc_store is unavailable here (%s)", res.HostReason)), true
	case probe.ReuseLiveMedia:
		return reuseProblem(recipe.CodeSourceReuseLiveMedia,
			"sourceReuse is not qualified on live media: bootc refuses copy-to-storage on a read-only sysroot"), true
	case probe.ReuseBackendNotQualified:
		return reuseProblem(recipe.CodeSourceReuseBackendNotQualified,
			"sourceReuse is qualified on an ostree-backend host only; this host's booted entry is not ostree"), true
	case probe.ReuseItemNotFound:
		return reuseProblem(recipe.CodeSourceReuseItemNotFound, fmt.Sprintf(
			"no ostree deployment of this host has the digest %s (bootc_store.items[] with storage \"ostree\")", s.Digest)), true
	case probe.ReusePlatformMismatch:
		return reuseProblem(recipe.CodeSourceReusePlatformMismatch, fmt.Sprintf(
			"the item with digest %s is not a %s image for this machine", s.Digest, s.Platform)), true
	}
	return reuseProblem(recipe.CodeSourceReuseItemUnverified, fmt.Sprintf(
		"the item with digest %s cannot be named to bootc: its reference, platform, transport or ostree commit is unknown", s.Digest)), true
}

// reuseSandboxed reports whether the install runs in a Flatpak sandbox;
// tests replace it.
var reuseSandboxed = runner.InFlatpak

// newReuseSession builds the copy session; tests replace it.
var newReuseSession = func(r *recipe.Recipe) reuseSession {
	env := reuse.DefaultEnv()
	env.Run = runner.Run
	env.Output = runner.Output
	env.Resolve = func() probe.ReuseSource {
		return newProber().ReuseSource(r.SourceReuse.Digest, r.SourceReuse.Platform)
	}
	return reuse.New(env, r.SourceReuse.Digest, r.SourceReuse.Platform, reuseScratch)
}

type reuseSession interface {
	Acquire([]probe.ReuseCandidate) (*reuse.Result, error)
	Release() error
}

// acquireSourceReuse runs the copy, before any disk step. The session's
// Release is registered first, so every exit (a failure, a cancel, the
// end of the install) removes the copy, the /var/tmp bind and the scratch.
// A recipe without sourceReuse gets nil and nothing runs.
func acquireSourceReuse(r *recipe.Recipe, register func(func() error)) (*reuse.Result, *recipe.Problem) {
	if r.SourceReuse == nil {
		return nil, nil
	}
	// The copy walks /sysroot, makes its scratch and binds /var/tmp on the
	// host itself: fisherman must run there, as a frontend runs it
	// (flatpak-spawn --host pkexec fisherman), not inside a sandbox.
	if reuseSandboxed() {
		p := reuseProblem(recipe.CodeSourceReuseSandboxed,
			"sourceReuse needs fisherman to run natively on the host, not inside a Flatpak sandbox")
		return nil, &p
	}
	res := hostReuseSource(r.SourceReuse.Digest, r.SourceReuse.Platform)
	if p, bad := reuseResolutionProblem(r.SourceReuse, res); bad {
		return nil, &p
	}
	progress.Info(fmt.Sprintf("Source reuse: copying %s from this host's bootc storage (%s)",
		r.SourceReuse.Digest, res.Candidates[0].SourceRef))
	sess := newReuseSession(r)
	register(sess.Release)
	result, err := sess.Acquire(res.Candidates)
	if err != nil {
		var f *reuse.Failure
		if errors.As(err, &f) {
			p := reuseProblem(f.Code, f.Message)
			return nil, &p
		}
		p := reuseProblem(recipe.CodeSourceReuseTransferFailed, "source reuse: "+err.Error())
		return nil, &p
	}
	progress.Info(fmt.Sprintf("Source reuse: verified %s as %s (manifest %s); the installed system follows %s",
		r.SourceReuse.Digest, result.Destination, result.DestinationDigest, r.TargetImgref))
	return result, nil
}

// installedDigest reads the installed ostree commit's manifest digest; tests
// replace it.
var installedDigest = func(target string) string {
	d, err := post.ResolveDeployment(target)
	if err != nil {
		return ""
	}
	return receipt.OstreeManifestDigest(d, receiptOutput)
}

// checkReusedInstall compares what bootc installed with the verified copy.
// A different digest is fatal; one that cannot be read is reported and left
// to the receipt check at the first boot.
func checkReusedInstall(target string, res *reuse.Result) (string, *recipe.Problem) {
	got := installedDigest(target)
	switch got {
	case "":
		return "Warning: source reuse: the installed image's manifest digest could not be read; the first boot's receipt check compares it", nil
	case res.DestinationDigest:
		return "Source reuse: the installed image is the verified copy (" + got + ")", nil
	}
	p := reuseProblem(recipe.CodeSourceReuseInstalledDigestDiffs, fmt.Sprintf(
		"source reuse: bootc installed %s, not the verified copy %s", got, res.DestinationDigest))
	return "", &p
}

// reuseInstallOptions makes the install from a verified copy run bootc
// from the copy's own container, with the host's containers-storage
// mounted, as an install from a pulled registry image does. The host's
// bootc cannot install it: on a booted ostree host /ostree/repo holds
// several commits, and bootc's source detection for --source-imgref needs
// exactly one (`ostree rev-parse --single`, "Multiple commit objects
// found"). The copy's name is never the update reference: --target-imgref
// stays the recipe's targetImgref.
func reuseInstallOptions(opts *install.Options) {
	opts.RunInSourceContainer = true
	opts.AdditionalImageStores = nil
}
