// Package reuse is the mutating half of the bootc source reuse
// (tuna-os/fisherman#384, phase 2): it exports an image the live host's
// bootc keeps in its ostree storage into containers-storage with `bootc
// image copy-to-storage`, under a name fisherman owns, verifies the copy,
// and removes it again.
//
// Only an install with an explicit recipe.SourceReuse reaches it; probe,
// validate and the default install never do. Every command goes through
// one allowlist (Allowed): the copy, its --help, an ostree rev-parse of the
// image's ref, a local skopeo inspect of the copy, the /var/tmp bind mount,
// and `podman rmi --ignore` of fisherman's own destination. No pull, no
// set-unified, no image cmd, no prune or GC, no credentials.
package reuse

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tuna-os/fisherman/internal/probe"
	"github.com/tuna-os/fisherman/internal/progress"
	"github.com/tuna-os/fisherman/internal/recipe"
)

// DestinationRepo is the repository every copy is named under; the tag is
// the item's short digest and a random install id, so a copy never takes a
// name something else uses.
const DestinationRepo = "localhost/fisherman-reuse"

// VarTmp is where bootc writes its temporary OCI layout (ostree-ext
// export opens /var/tmp itself, not $TMPDIR).
const VarTmp = "/var/tmp"

// DefaultStore is containers-storage's default graph root, where skopeo
// writes the copy.
const DefaultStore = "/var/lib/containers/storage"

// Margin is added to the estimate before it is compared with free space.
const Margin uint64 = 1 << 30

// lowSpace is the free space under which a failed copy is reported as
// disk exhaustion.
const lowSpace uint64 = 64 << 20

// Walk bounds for the estimate.
const (
	maxWalkEntries = 4_000_000
	walkTimeout    = 5 * time.Minute
)

var (
	destRe      = regexp.MustCompile(`^` + regexp.QuoteMeta(DestinationRepo) + `:[0-9a-f]{12}-[0-9a-f]{16}$`)
	sourceRefRe = regexp.MustCompile(`^(docker://|containers-storage:|oci:|oci-archive:)[A-Za-z0-9][A-Za-z0-9._:/@+=-]{0,511}$`)
	ostreeRefRe = regexp.MustCompile(`^ostree/container/image/[A-Za-z0-9_/-]+$`)
	checksumRe  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	digestRe    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// ErrNotAllowed is returned for a command outside the allowlist; it is
// never run.
var ErrNotAllowed = errors.New("command is not on the source reuse allowlist")

// Allowed reports whether name/args is a command this package may run.
// scratchVarTmp is the session's own directory bound over /var/tmp.
func Allowed(scratchVarTmp, name string, args []string) error {
	ok := false
	switch name {
	case "bootc":
		switch {
		case len(args) == 3 && args[0] == "image" && args[1] == "copy-to-storage" && args[2] == "--help":
			ok = true
		case len(args) == 6 && args[0] == "image" && args[1] == "copy-to-storage" &&
			args[2] == "--source" && sourceRefRe.MatchString(args[3]) && !strings.Contains(args[3], "..") &&
			args[4] == "--target" && destRe.MatchString(args[5]):
			ok = true
		}
	case "ostree":
		ok = len(args) == 3 && args[0] == "rev-parse" && args[1] == "--repo="+probe.OstreeRepo &&
			len(args[2]) <= 4096 && ostreeRefRe.MatchString(args[2])
	case "skopeo":
		ok = len(args) == 2 && args[0] == "inspect" && strings.HasPrefix(args[1], "containers-storage:") &&
			destRe.MatchString(strings.TrimPrefix(args[1], "containers-storage:"))
	case "podman":
		ok = len(args) == 3 && args[0] == "rmi" && args[1] == "--ignore" && destRe.MatchString(args[2])
	case "mount":
		ok = scratchVarTmp != "" && len(args) == 3 && args[0] == "--bind" && args[1] == scratchVarTmp && args[2] == VarTmp
	case "umount":
		ok = len(args) == 1 && args[0] == VarTmp
	}
	if !ok {
		return fmt.Errorf("%s: %w", name, ErrNotAllowed)
	}
	return nil
}

// Failure is a reuse problem with a stable recipe code.
type Failure struct {
	Code    string
	Reason  string
	Message string
}

func (f *Failure) Error() string { return f.Message }

func fail(code, reason, format string, args ...any) *Failure {
	return &Failure{Code: code, Reason: reason, Message: fmt.Sprintf(format, args...)}
}

// Env is every seam the session touches the host through.
type Env struct {
	// Run streams a command's output (runner.Run).
	Run func(name string, args ...string) error
	// Output returns a command's stdout (runner.Output).
	Output func(name string, args ...string) ([]byte, error)
	// Statfs returns the free bytes and the device of the filesystem
	// holding path.
	Statfs func(path string) (free uint64, dev uint64, err error)
	// Root is prefixed to host paths the session reads or writes directly
	// (the deployment walk, the scratch directory). "" is "/".
	Root string
	// Resolve re-reads the host (probe.Prober.ReuseSource) after the copy.
	Resolve func() probe.ReuseSource
	// Emit writes a source_reuse event.
	Emit func(progress.SourceReuseEvent)
	Now  func() time.Time
	// Sample is how often free space is sampled during the copy.
	Sample time.Duration
	// RandomID returns the random part of the destination tag (16 hex).
	RandomID func() string
}

// DefaultEnv is the production Env, minus Run/Output/Resolve, which the CLI
// supplies (they go through internal/runner and the probe).
func DefaultEnv() Env {
	return Env{
		Statfs:   statfs,
		Emit:     progress.SourceReuse,
		Now:      time.Now,
		Sample:   500 * time.Millisecond,
		RandomID: randomID,
	}
}

func statfs(path string) (uint64, uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	var fi syscall.Stat_t
	if err := syscall.Stat(path, &fi); err != nil {
		return 0, 0, err
	}
	return st.Bavail * uint64(st.Bsize), uint64(fi.Dev), nil //nolint:unconvert // Dev is uint64 on amd64, not on every GOARCH
}

func randomID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Result is a verified copy.
type Result struct {
	// SourceImgref is the install source: containers-storage:<destination>.
	SourceImgref      string
	Destination       string
	DestinationDigest string
	Candidate         probe.ReuseCandidate
}

// Session is one acquisition. Release undoes whatever it did, once, from
// any goroutine (the install's teardown, a cancel).
type Session struct {
	env      Env
	digest   string
	platform string
	scratch  string // host path of the session's scratch directory

	mu sync.Mutex
	// bindMu serialises everything that changes the /var/tmp bind: the
	// copy's mount, the copy's umount and Release's umount. bound changes
	// only under it (and mu), after the command it records has finished.
	// Lock order: bindMu, then mu.
	bindMu      sync.Mutex
	bound       bool
	destination string
	created     bool
	released    bool
	madeScratch bool
}

// New prepares a session. scratch is a host directory the session owns
// (created, and removed by Release), on disk-backed storage.
func New(env Env, digest, platform, scratch string) *Session {
	if env.Now == nil {
		env.Now = time.Now
	}
	if env.Sample <= 0 {
		env.Sample = 500 * time.Millisecond
	}
	if env.RandomID == nil {
		env.RandomID = randomID
	}
	if env.Emit == nil {
		env.Emit = func(progress.SourceReuseEvent) {}
	}
	return &Session{env: env, digest: digest, platform: probe.NormalizeArch(platform), scratch: scratch}
}

func (s *Session) host(path string) string {
	if s.env.Root == "" {
		return path
	}
	return filepath.Join(s.env.Root, path)
}

func (s *Session) varTmpDir() string { return filepath.Join(s.scratch, "var-tmp") }

func (s *Session) output(name string, args ...string) ([]byte, error) {
	if err := s.check(name, args); err != nil {
		return nil, err
	}
	return s.env.Output(name, args...)
}

func (s *Session) run(name string, args ...string) error {
	if err := s.check(name, args); err != nil {
		return err
	}
	return s.env.Run(name, args...)
}

func (s *Session) check(name string, args []string) error {
	if err := Allowed(s.varTmpDir(), name, args); err != nil {
		return err
	}
	s.mu.Lock()
	released := s.released
	s.mu.Unlock()
	// After Release only Release's own commands run (it calls env directly).
	if released {
		return fmt.Errorf("%s: source reuse session already released", name)
	}
	return nil
}

func (s *Session) event(phase string) progress.SourceReuseEvent {
	return progress.SourceReuseEvent{Phase: phase, Digest: s.digest, Platform: s.platform}
}

func (s *Session) failed(ev progress.SourceReuseEvent, f *Failure) *Failure {
	ev.Phase, ev.Code, ev.Reason = progress.ReuseFailed, f.Code, f.Reason
	s.env.Emit(ev)
	return f
}

// Acquire runs the copy for the first usable candidate and verifies it. On
// error the caller still calls Release (it is registered before Acquire).
func (s *Session) Acquire(candidates []probe.ReuseCandidate) (*Result, error) {
	base := s.event("")

	// 1. An older bootc has no copy-to-storage, or none with explicit
	//    source and destination: refuse rather than copy the booted image
	//    to localhost/bootc.
	help, err := s.output("bootc", "image", "copy-to-storage", "--help")
	switch {
	case err != nil && exitCode(err) == 2:
		return nil, s.failed(base, fail(recipe.CodeSourceReuseCommandUnsupported, "command_absent",
			"source reuse: this bootc has no `image copy-to-storage`"))
	case err != nil:
		return nil, s.failed(base, fail(recipe.CodeSourceReuseCommandUnsupported, "check_failed",
			"source reuse: `bootc image copy-to-storage --help` failed"))
	case !strings.Contains(string(help), "--source") || !strings.Contains(string(help), "--target"):
		return nil, s.failed(base, fail(recipe.CodeSourceReuseCommandUnsupported, "flags_absent",
			"source reuse: this bootc's `image copy-to-storage` takes no --source and --target, "+
				"so fisherman cannot name the image and the destination"))
	}

	// 2. The image ref the copy reads must point at the deployment's own
	//    commit: a staged or cached update moves it, and the copy would
	//    then export another image under the same name.
	cand, f := s.pickCandidate(candidates)
	if f != nil {
		return nil, s.failed(base, f)
	}
	base.Source = cand.SourceRef

	// 3. The estimate, from the deployment's checkout.
	start := s.env.Now()
	est, f := s.estimate(cand)
	if f != nil {
		return nil, s.failed(base, f)
	}
	est.EstimateMS = s.env.Now().Sub(start).Milliseconds()

	// 4. The scratch directory and the free space.
	// While it is bound over /var/tmp every program on the host uses it:
	// the same 1777 /var/tmp has, as install's overrideVarTmp does.
	if err := mkVarTmp(s.host(s.varTmpDir())); err != nil {
		return nil, s.failed(base, fail(recipe.CodeSourceReuseInsufficientSpace, "scratch_unavailable",
			"source reuse: cannot create the scratch directory %s: %v", s.varTmpDir(), err))
	}
	s.mu.Lock()
	s.madeScratch = true
	s.mu.Unlock()
	if f := s.checkSpace(&est); f != nil {
		base.Estimate = &est
		return nil, s.failed(base, f)
	}
	base.Estimate = &est

	// 5. A destination name nothing has.
	dest := fmt.Sprintf("%s:%s-%s", DestinationRepo, strings.TrimPrefix(s.digest, "sha256:")[:12], s.env.RandomID())
	if !destRe.MatchString(dest) {
		return nil, s.failed(base, fail(recipe.CodeSourceReuseDestinationExists, "invalid_name",
			"source reuse: could not form a destination name"))
	}
	if _, err := s.output("skopeo", "inspect", "containers-storage:"+dest); err == nil {
		return nil, s.failed(base, fail(recipe.CodeSourceReuseDestinationExists, "exists",
			"source reuse: containers-storage already has %s; fisherman does not overwrite a name it did not create", dest))
	}
	base.Destination = "containers-storage:" + dest
	planned := base
	planned.Phase = progress.ReusePlanned
	s.env.Emit(planned)

	// 6. The copy, with /var/tmp on the session's scratch.
	measured, f := s.copy(base, cand, dest, est)
	if f != nil {
		return nil, s.failed(base, f)
	}

	// 7. Verify: the source did not move, the deployment still has the
	//    digest, the copy is readable and of the platform.
	vstart := s.env.Now()
	destDigest, f := s.verify(cand, dest)
	if f != nil {
		return nil, s.failed(base, f)
	}
	measured.VerifyMS = s.env.Now().Sub(vstart).Milliseconds()
	verified := base
	verified.Phase, verified.DestinationDigest, verified.Measured = progress.ReuseVerified, destDigest, &measured
	s.env.Emit(verified)
	return &Result{SourceImgref: "containers-storage:" + dest, Destination: dest,
		DestinationDigest: destDigest, Candidate: cand}, nil
}

func mkVarTmp(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o777|os.ModeSticky)
}

func (s *Session) pickCandidate(candidates []probe.ReuseCandidate) (probe.ReuseCandidate, *Failure) {
	moved := false
	for _, c := range candidates {
		if c.Digest != s.digest || c.Platform != s.platform || !checksumRe.MatchString(c.Checksum) {
			continue
		}
		got, err := s.revParse(c)
		switch {
		case err != nil:
			continue
		case got != c.Checksum:
			moved = true
			continue
		}
		return c, nil
	}
	if moved {
		return probe.ReuseCandidate{}, fail(recipe.CodeSourceReuseSourceMoved, "ref_moved",
			"source reuse: the image's ostree ref no longer points at the deployment with digest %s "+
				"(a staged or cached update took the name); copy-to-storage would export another image", s.digest)
	}
	return probe.ReuseCandidate{}, fail(recipe.CodeSourceReuseSourceUnavailable, "ref_unreadable",
		"source reuse: no ostree image ref for digest %s could be read in %s", s.digest, probe.OstreeRepo)
}

func (s *Session) revParse(c probe.ReuseCandidate) (string, error) {
	out, err := s.output("ostree", "rev-parse", "--repo="+probe.OstreeRepo, c.OstreeRef)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// estimate walks the deployment's checkout. The copy writes every file
// once as an uncompressed tar (headers for the object and the hard-linked
// path, data once per inode), then unpacks it into containers-storage.
func (s *Session) estimate(c probe.ReuseCandidate) (progress.ReuseEstimate, *Failure) {
	var est progress.ReuseEstimate
	root := s.host(c.DeployPath())
	fi, err := os.Lstat(root)
	if err != nil {
		reason := "unreadable"
		if errors.Is(err, fs.ErrNotExist) {
			reason = "missing"
		}
		return est, fail(recipe.CodeSourceReuseEstimateFailed, reason,
			"source reuse: cannot read the deployment %s: %v", c.DeployPath(), err)
	}
	rootDev := devOf(fi)
	seen := map[[2]uint64]bool{}
	deadline := s.env.Now().Add(walkTimeout)
	var entries uint64
	var walkFail *Failure
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			walkFail = fail(recipe.CodeSourceReuseEstimateFailed, "inaccessible",
				"source reuse: cannot read %s in the deployment: %v", strings.TrimPrefix(path, s.env.Root), err)
			return walkFail
		}
		entries++
		if entries > maxWalkEntries || s.env.Now().After(deadline) {
			walkFail = fail(recipe.CodeSourceReuseEstimateFailed, "too_large",
				"source reuse: the deployment has too many entries to estimate")
			return walkFail
		}
		info, err := d.Info()
		if err != nil {
			walkFail = fail(recipe.CodeSourceReuseEstimateFailed, "inaccessible",
				"source reuse: cannot read %s in the deployment: %v", strings.TrimPrefix(path, s.env.Root), err)
			return walkFail
		}
		if d.IsDir() && path != root && devOf(info) != rootDev {
			return fs.SkipDir // another filesystem mounted inside
		}
		hdr := uint64(512)
		if len(path)-len(root) > 100 {
			hdr += 1024 // a PAX header for a long name
		}
		switch {
		case info.Mode().IsRegular():
			est.Files++
			// An object path and the hard-linked tree path: two headers.
			est.ScratchBytes += 2 * hdr
			key := [2]uint64{devOf(info), inoOf(info)}
			if !seen[key] {
				seen[key] = true
				size := uint64(info.Size())
				est.ScratchBytes += roundUp(size, 512)
				est.StoreBytes += roundUp(size, 4096)
			}
		case d.IsDir():
			est.ScratchBytes += hdr
			est.StoreBytes += 4096
		default:
			est.ScratchBytes += hdr
		}
		return nil
	})
	if walkFail != nil {
		return est, walkFail
	}
	if err != nil {
		return est, fail(recipe.CodeSourceReuseEstimateFailed, "inaccessible",
			"source reuse: cannot walk the deployment: %v", err)
	}
	est.ScratchBytes += 2 * 1024 // two tar end blocks
	return est, nil
}

func roundUp(n, to uint64) uint64 { return (n + to - 1) / to * to }

func devOf(fi fs.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev) //nolint:unconvert // not uint64 on every GOARCH
	}
	return 0
}

func inoOf(fi fs.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}

// storeProbePath is the nearest existing directory of the default store.
func (s *Session) storeProbePath() string {
	for p := DefaultStore; ; p = filepath.Dir(p) {
		if fi, err := os.Stat(s.host(p)); err == nil && fi.IsDir() {
			return s.host(p)
		}
		if p == "/" {
			return s.host("/")
		}
	}
}

func (s *Session) checkSpace(est *progress.ReuseEstimate) *Failure {
	scratchFree, scratchDev, err := s.env.Statfs(s.host(s.varTmpDir()))
	if err != nil {
		return fail(recipe.CodeSourceReuseInsufficientSpace, "statfs_failed",
			"source reuse: cannot read the free space of %s: %v", s.varTmpDir(), err)
	}
	storeFree, storeDev, err := s.env.Statfs(s.storeProbePath())
	if err != nil {
		return fail(recipe.CodeSourceReuseInsufficientSpace, "statfs_failed",
			"source reuse: cannot read the free space of %s: %v", DefaultStore, err)
	}
	est.ScratchFreeBytes, est.StoreFreeBytes = scratchFree, storeFree
	est.SameFilesystem = scratchDev == storeDev
	est.RequiredBytes = est.ScratchBytes + est.StoreBytes + Margin
	short := false
	if est.SameFilesystem {
		short = scratchFree < est.RequiredBytes
	} else {
		short = scratchFree < est.ScratchBytes+Margin/2 || storeFree < est.StoreBytes+Margin/2
	}
	if short {
		return fail(recipe.CodeSourceReuseInsufficientSpace, "insufficient_space",
			"source reuse: the copy needs about %s (%s of scratch, %s in containers-storage, %s margin), "+
				"and %s is free on the scratch and %s on the store filesystem",
			human(est.RequiredBytes), human(est.ScratchBytes), human(est.StoreBytes), human(Margin),
			human(scratchFree), human(storeFree))
	}
	return nil
}

func human(b uint64) string {
	return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
}

// copy binds the session's scratch over /var/tmp, runs copy-to-storage and
// undoes the bind. Free space is sampled meanwhile.
func (s *Session) copy(base progress.SourceReuseEvent, c probe.ReuseCandidate, dest string, est progress.ReuseEstimate) (progress.ReuseMeasured, *Failure) {
	var m progress.ReuseMeasured
	if err := s.bind(dest); err != nil {
		return m, fail(recipe.CodeSourceReuseTransferFailed, "bind_failed",
			"source reuse: cannot bind %s over %s: %v", s.varTmpDir(), VarTmp, err)
	}

	copying := base
	copying.Phase = progress.ReuseCopying
	s.env.Emit(copying)

	scratchMin, storeMin := est.ScratchFreeBytes, est.StoreFreeBytes
	var minMu sync.Mutex
	sample := func() {
		if free, _, err := s.env.Statfs(s.host(s.varTmpDir())); err == nil {
			minMu.Lock()
			scratchMin = min(scratchMin, free)
			minMu.Unlock()
		}
		if free, _, err := s.env.Statfs(s.storeProbePath()); err == nil {
			minMu.Lock()
			storeMin = min(storeMin, free)
			minMu.Unlock()
		}
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(s.env.Sample)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				sample()
			}
		}
	}()
	start := s.env.Now()
	copyErr := s.run("bootc", "image", "copy-to-storage", "--source", c.SourceRef, "--target", dest)
	m.CopyMS = s.env.Now().Sub(start).Milliseconds()
	close(stop)
	<-done
	sample()
	unbindErr := s.unbind()

	minMu.Lock()
	m.ScratchPeakBytes = sub(est.ScratchFreeBytes, scratchMin)
	m.StorePeakBytes = sub(est.StoreFreeBytes, storeMin)
	lowest := min(scratchMin, storeMin)
	minMu.Unlock()
	if free, _, err := s.env.Statfs(s.storeProbePath()); err == nil {
		m.StoreBytes = sub(est.StoreFreeBytes, free)
	}

	if copyErr != nil {
		if lowest < lowSpace || errors.Is(copyErr, syscall.ENOSPC) {
			return m, fail(recipe.CodeSourceReuseDiskExhausted, "disk_exhausted",
				"source reuse: the copy ran out of disk space (%s free at the lowest point): %v", human(lowest), copyErr)
		}
		return m, fail(recipe.CodeSourceReuseTransferFailed, "command_failed",
			"source reuse: bootc image copy-to-storage failed: %v", copyErr)
	}
	if unbindErr != nil {
		return m, fail(recipe.CodeSourceReuseTransferFailed, "unbind_failed",
			"source reuse: cannot release the %s bind: %v", VarTmp, unbindErr)
	}
	return m, nil
}

func sub(a, b uint64) uint64 {
	if b > a {
		return 0
	}
	return a - b
}

// bind mounts the session's scratch over /var/tmp. Under bindMu, so a
// Release that starts meanwhile waits for the mount and then undoes it; a
// session already released mounts nothing. From the bind on, Release also
// removes dest: the copy can create it at any point, and a cancel can end
// the copy at any point.
func (s *Session) bind(dest string) error {
	s.bindMu.Lock()
	defer s.bindMu.Unlock()
	if err := s.check("mount", []string{"--bind", s.varTmpDir(), VarTmp}); err != nil {
		return err
	}
	if err := s.env.Run("mount", "--bind", s.varTmpDir(), VarTmp); err != nil {
		return err
	}
	s.mu.Lock()
	s.bound = true
	s.destination, s.created = dest, true
	s.mu.Unlock()
	return nil
}

// unbindLocked undoes the session's own bind, if it is in place: one
// umount for one mount. A second umount of /var/tmp would unmount the
// host's own /var/tmp where that is a mount point, so bound goes false only
// after an umount that succeeded, and an umount runs only while bound is
// true. The caller holds bindMu. It runs even after Release has begun (the
// copy's unbind racing a cancel): whichever of the two gets bindMu first
// unbinds, and the other finds nothing to do (tuna-os/fisherman#383 VM run
// 43186222564, where the copy claimed the bind, Release skipped it and
// removed the scratch, and the bind stayed over a deleted directory).
func (s *Session) unbindLocked() error {
	s.mu.Lock()
	bound := s.bound
	s.mu.Unlock()
	if !bound {
		return nil
	}
	if err := Allowed(s.varTmpDir(), "umount", []string{VarTmp}); err != nil {
		return err
	}
	if err := s.env.Run("umount", VarTmp); err != nil {
		return err
	}
	s.mu.Lock()
	s.bound = false
	s.mu.Unlock()
	return nil
}

// unbind is the copy's own unbind.
func (s *Session) unbind() error {
	s.bindMu.Lock()
	defer s.bindMu.Unlock()
	return s.unbindLocked()
}

// releaseHook runs in Release once the session is marked released and
// before the bind is handled. Tests set it to order a cancel against the
// copy.
var releaseHook func()

// ociArch maps bootc's architecture spelling to the OCI one skopeo prints.
var ociArch = map[string]string{
	"x86_64":  "amd64",
	"aarch64": "arm64",
	"ppc64le": "ppc64le",
	"s390x":   "s390x",
	"riscv64": "riscv64",
}

func (s *Session) verify(c probe.ReuseCandidate, dest string) (string, *Failure) {
	got, err := s.revParse(c)
	switch {
	case err != nil:
		return "", fail(recipe.CodeSourceReuseIdentityMismatch, "source_unreadable_after_copy",
			"source reuse: the image's ostree ref cannot be read after the copy: %v", err)
	case got != c.Checksum:
		return "", fail(recipe.CodeSourceReuseIdentityMismatch, "source_moved_during_copy",
			"source reuse: the image's ostree ref moved while it was copied; the copy may be another image")
	}
	if s.env.Resolve != nil {
		still := false
		for _, rc := range s.env.Resolve().Candidates {
			still = still || (rc.Checksum == c.Checksum && rc.Digest == s.digest && rc.Platform == s.platform)
		}
		if !still {
			return "", fail(recipe.CodeSourceReuseIdentityMismatch, "deployment_changed",
				"source reuse: after the copy, no deployment has commit %s with digest %s", c.Checksum, s.digest)
		}
	}
	out, err := s.output("skopeo", "inspect", "containers-storage:"+dest)
	if err != nil {
		return "", fail(recipe.CodeSourceReuseVerifyFailed, "destination_unreadable",
			"source reuse: the copy %s cannot be inspected: %v", dest, err)
	}
	var info struct {
		Digest       string `json:"Digest"`
		Architecture string `json:"Architecture"`
		Os           string `json:"Os"`
	}
	if json.Unmarshal(out, &info) != nil {
		return "", fail(recipe.CodeSourceReuseVerifyFailed, "malformed_output",
			"source reuse: skopeo inspect of the copy printed something that is not its JSON")
	}
	if !digestRe.MatchString(info.Digest) {
		return "", fail(recipe.CodeSourceReuseVerifyFailed, "destination_digest_invalid",
			"source reuse: the copy has no sha256 manifest digest")
	}
	if want, ok := ociArch[s.platform]; !ok || info.Architecture != want || info.Os != "linux" {
		return "", fail(recipe.CodeSourceReuseIdentityMismatch, "platform_mismatch",
			"source reuse: the copy is %s/%s, the item is linux/%s", info.Os, info.Architecture, s.platform)
	}
	return info.Digest, nil
}

// Release undoes the session: the /var/tmp bind, the destination image
// (`podman rmi --ignore` of the one name fisherman created; layers another
// image uses stay) and the scratch directory. Safe to call more than once
// and from any goroutine; only the first call acts.
func (s *Session) Release() error {
	s.mu.Lock()
	if s.released {
		s.mu.Unlock()
		return nil
	}
	s.released = true
	s.mu.Unlock()
	if releaseHook != nil {
		releaseHook()
	}

	// Wait for a mount or umount in flight, then undo the bind if it is
	// still there. From here no new bind starts (check refuses it).
	var errs []error
	s.bindMu.Lock()
	unbindErr := s.unbindLocked()
	s.bindMu.Unlock()
	if unbindErr != nil {
		errs = append(errs, fmt.Errorf("unbinding %s: %w", VarTmp, unbindErr))
	}
	s.mu.Lock()
	created, dest, made := s.created, s.destination, s.madeScratch
	s.mu.Unlock()
	reason := ""
	if created {
		reason = "removed"
		if err := Allowed("", "podman", []string{"rmi", "--ignore", dest}); err != nil {
			errs = append(errs, err)
		} else if err := s.env.Run("podman", "rmi", "--ignore", dest); err != nil {
			reason = "remove_failed"
			errs = append(errs, fmt.Errorf("removing %s: %w", dest, err))
		}
	}
	if made && unbindErr != nil {
		// The bind is still over /var/tmp and the scratch is its source:
		// removing it would leave /var/tmp on a deleted directory, where
		// the next install cannot bind. Keep it, and say so.
		errs = append(errs, fmt.Errorf("kept %s: it is still bound over %s", s.scratch, VarTmp))
	} else if made {
		// The bind is gone, so this deletes only what the copy wrote.
		if err := os.RemoveAll(s.host(s.scratch)); err != nil {
			errs = append(errs, fmt.Errorf("removing %s: %w", s.scratch, err))
		}
	}
	if created {
		ev := s.event(progress.ReuseReleased)
		ev.Destination, ev.Reason = "containers-storage:"+dest, reason
		s.env.Emit(ev)
	}
	return errors.Join(errs...)
}

// exitCode is the exit status inside err, or -1.
func exitCode(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		return coded.ExitCode()
	}
	return -1
}
