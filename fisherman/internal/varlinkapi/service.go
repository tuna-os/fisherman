package varlinkapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"sync"

	"github.com/tuna-os/fisherman/internal/auth"
	"github.com/tuna-os/fisherman/internal/recipe"
	"github.com/tuna-os/fisherman/internal/varlink"
)

type state int

const (
	stateIdle state = iota
	stateInstalling
	stateInstalled
)

// Service is the daemon's state machine. It owns the order of calls, input
// validation, the step stream and the wire format; the Executor owns the
// machine.
//
// Logging: the service logs method names, disk paths and step ids. It never
// logs request parameters, and Secret redacts itself, so a passphrase or a
// recovery key cannot reach a log through this package.
type Service struct {
	exec Executor
	log  *slog.Logger
	// checker approves mutating calls through the polkit authority. Nil
	// allows everything: the mock daemon and the in-process tests use no
	// checker, the real daemon wires PkcheckChecker.
	checker auth.Checker

	mu     sync.Mutex
	st     state
	target TargetConfig
	cancel context.CancelFunc
	done   chan struct{}
}

// NewService returns a service over exec. A nil logger discards logs.
func NewService(exec Executor, log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Service{exec: exec, log: log}
}

// SetChecker makes mutating calls ask checker first. A nil checker allows
// everything. With a checker, a caller without peer credentials is denied:
// the authority has nobody to judge.
func (s *Service) SetChecker(c auth.Checker) { s.checker = c }

// checkAuth approves action for the peer in ctx. It returns a Varlink
// error on denial and nil otherwise; the action logged is the method name
// only, never a device, a slot or a credential.
func (s *Service) checkAuth(ctx context.Context, action string) error {
	if s.checker == nil {
		return nil
	}
	creds, ok := auth.Of(ctx)
	if !ok {
		s.log.Info("denied: peer has no credentials", "action", action)
		return &varlink.Error{Name: varlink.ErrPermissionDenied}
	}
	if err := s.checker.Check(ctx, creds, action); err != nil {
		s.log.Info("polkit denied", "action", action, "uid", creds.UID)
		return &varlink.Error{Name: varlink.ErrPermissionDenied}
	}
	return nil
}

// Errors returned by begin.
var (
	errInstalling       = errors.New("an install is already running")
	errAlreadyInstalled = errors.New("the system is already installed")
)

// begin moves idle to installing. The returned context is cancelled by
// Cancel only; callers pass a detached context so a dropped connection
// never cancels the install. finish must be called exactly once.
func (s *Service) begin(parent context.Context) (context.Context, func(ok bool), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.st {
	case stateInstalling:
		return nil, nil, errInstalling
	case stateInstalled:
		return nil, nil, errAlreadyInstalled
	case stateIdle:
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	s.st, s.cancel, s.done = stateInstalling, cancel, done
	return ctx, func(ok bool) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if ok {
			s.st = stateInstalled
		} else {
			s.st = stateIdle
		}
		s.cancel, s.done = nil, nil
		cancel()
		close(done)
	}, nil
}

// runInstall runs the executor and returns the terminal event. Progress
// events go to emit as they happen. A nil final means the client went away
// (the install was cancelled and cleaned up, and nobody is listening).
func (s *Service) runInstall(ctx context.Context, plan InstallPlan, emit func(Event)) Event {
	rep := newReporter(plan, emit)
	s.log.Info("install started", "disk", plan.Disk.Path, "erase", plan.Erase,
		"encryption", plan.Encryption.Type)
	err := s.exec.Install(ctx, plan, rep)
	var ie *InstallError
	switch {
	case err == nil:
		rep.finish()
		s.log.Info("install complete", "disk", plan.Disk.Path)
		return Event{Event: EvComplete, OverallPct: 100, Message: ptr("Installation complete")}
	case errors.As(err, &ie):
		s.log.Warn("install failed", "disk", plan.Disk.Path, "step", ie.Step, "reason", ie.Reason, "message", ie.Message)
		step := ie.Step
		if step == "" {
			step = rep.current()
		}
		ev := rep.event(EvFailed, step, rep.last())
		ev.Reason, ev.Message, ev.NextAction = ptr(ie.Reason), ptr(ie.Message), ptr(ie.NextAction)
		return ev
	case errors.Is(err, context.Canceled):
		s.log.Info("install cancelled", "disk", plan.Disk.Path)
		ev := rep.event(EvCancelled, "", rep.last())
		ev.Message = ptr("Installation cancelled and cleaned up")
		return ev
	default:
		s.log.Error("install failed", "disk", plan.Disk.Path, "reason", ReasonInternal)
		ev := rep.event(EvFailed, rep.current(), rep.last())
		ev.Reason, ev.Message, ev.NextAction = ptr(ReasonInternal), ptr(err.Error()), ptr(ActionReportBug)
		return ev
	}
}

// ---- validation -----------------------------------------------------------

// recipeEncryption maps the API's enum to the recipe's names, so one rule
// set (internal/recipe) serves the CLI and the daemon.
var recipeEncryption = map[string]string{
	"none":                 "none",
	"luks_passphrase":      "luks-passphrase",
	"tpm2_luks":            "tpm2-luks",
	"tpm2_luks_passphrase": "tpm2-luks-passphrase",
}

var (
	localeRe = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,64}$`)
	tzRe     = regexp.MustCompile(`^[A-Za-z0-9_+-]+(/[A-Za-z0-9_+-]+){0,2}$`)
)

func fromProblems(ps []recipe.Problem) []ValidationError {
	out := make([]ValidationError, 0, len(ps))
	for _, p := range ps {
		out = append(out, ValidationError{Field: p.Field, Code: p.Code, Message: p.Message})
	}
	return out
}

// Validation codes owned by the daemon. The recipe rules keep their own.
const (
	CodeDiskRequired        = "disk_required"
	CodeDiskNotFound        = "disk_not_found"
	CodeDiskNotEligible     = "disk_not_eligible"
	CodeDiskTooSmall        = "disk_too_small"
	CodeInstallModeRequired = "install_mode_required"
	CodeInstallModeInvalid  = "install_mode_invalid"
	CodeInsufficientFree    = "insufficient_free_space"
	CodeConflictingLabel    = "conflicting_disk_label"
	CodeEncryptionRequired  = "encryption_required"
	CodeLocaleInvalid       = "locale_invalid"
	CodeKeymapInvalid       = "keymap_invalid"
	CodeTimeZoneInvalid     = "timezone_invalid"
	CodeScaleInvalid        = "scale_invalid"
)

// validate checks cfg. partial checks only what is present and reads no
// machine state; the full check also needs the disk and TPM facts.
func (s *Service) validate(ctx context.Context, cfg Config, partial bool) ([]ValidationError, *Disk, error) {
	errs := []ValidationError{}
	var disk *Disk
	var probed *ProbeResult
	if cfg.Disk != nil || !partial {
		p, err := s.exec.Probe(ctx)
		if err != nil {
			return nil, nil, err
		}
		probed = &p
	}

	// disk and installMode
	switch {
	case cfg.Disk == nil || *cfg.Disk == "":
		if !partial {
			errs = append(errs, ValidationError{"disk", CodeDiskRequired, "choose a disk to install to"})
		}
	default:
		for i := range probed.Disks {
			if probed.Disks[i].Path == *cfg.Disk {
				disk = &probed.Disks[i]
			}
		}
		switch {
		case disk == nil:
			errs = append(errs, ValidationError{"disk", CodeDiskNotFound, "disk " + *cfg.Disk + " does not exist"})
		case !disk.Eligible:
			reason := "not eligible"
			if disk.ExcludedReason != nil {
				reason = *disk.ExcludedReason
			}
			errs = append(errs, ValidationError{"disk", CodeDiskNotEligible, "disk " + *cfg.Disk + " cannot be installed to: " + reason})
			disk = nil
		}
	}
	if cfg.InstallMode != nil && *cfg.InstallMode != "erase" && *cfg.InstallMode != "free_space" {
		errs = append(errs, ValidationError{"installMode", CodeInstallModeInvalid, `installMode must be "erase" or "free_space"`})
	} else if disk != nil {
		errs = append(errs, s.checkFit(disk, cfg.InstallMode, partial)...)
	}

	// encryption
	if cfg.Encryption == nil {
		if !partial {
			errs = append(errs, ValidationError{"encryption", CodeEncryptionRequired, "choose an encryption type"})
		}
	} else {
		enc := recipe.Encryption{Type: recipeEncryption[cfg.Encryption.Type]}
		if enc.Type == "" {
			enc.Type = cfg.Encryption.Type // let the recipe rule report it
		}
		if cfg.Encryption.Passphrase != nil {
			enc.Passphrase = cfg.Encryption.Passphrase.Reveal()
		}
		errs = append(errs, fromProblems(recipe.CheckEncryption(enc))...)
		if c, ok := recipe.LookupEncryption(enc.Type); ok && c.NeedsTPM2 && !partial && probed != nil && !probed.TPM.Usable {
			errs = append(errs, ValidationError{"encryption.type", recipe.CodeEncryptionTPMUnavailable,
				"this encryption type needs a TPM 2.0, and this machine has none usable"})
		}
	}

	errs = append(errs, s.checkIdentity(cfg.Hostname, cfg.User)...)
	return errs, disk, nil
}

func (s *Service) checkFit(d *Disk, mode *string, partial bool) []ValidationError {
	switch {
	case d.SizeBytes < d.MinimalSizeBytes:
		return []ValidationError{{"disk", CodeDiskTooSmall, "disk " + d.Path + " is smaller than the minimum of " + sizeLabel(d.MinimalSizeBytes)}}
	case mode == nil && !partial && d.ExistingData.HasPartitionTable:
		return []ValidationError{{"installMode", CodeInstallModeRequired, "disk " + d.Path + " holds data: choose erase or free_space"}}
	case mode != nil && *mode == "free_space":
		if d.ForeignLabel {
			return []ValidationError{{"installMode", CodeConflictingLabel, "disk " + d.Path + " has a partition table that cannot share the disk: erase it or choose another"}}
		}
		if d.FreeBytes < d.MinimalSizeBytes {
			return []ValidationError{{"installMode", CodeInsufficientFree, "disk " + d.Path + " has " + sizeLabel(d.FreeBytes) + " free, needs " + sizeLabel(d.MinimalSizeBytes)}}
		}
	}
	return nil
}

func (s *Service) checkIdentity(hostname *string, user *User) []ValidationError {
	var errs []ValidationError
	if hostname != nil {
		errs = append(errs, fromProblems(recipe.CheckHostname(*hostname))...)
	}
	if user != nil {
		errs = append(errs, fromProblems(recipe.CheckUsername(user.Username))...)
		if user.Password != nil {
			errs = append(errs, fromProblems(recipe.CheckPassword(user.Password.Reveal()))...)
		}
	}
	return errs
}

// planFor builds an InstallPlan from a validated config and the stored
// target settings.
func (s *Service) planFor(cfg Config, disk Disk) InstallPlan {
	s.mu.Lock()
	target := s.target
	s.mu.Unlock()
	if cfg.User != nil {
		target.User = cfg.User
	}
	if cfg.Hostname != nil {
		target.Hostname = *cfg.Hostname
	}
	plan := InstallPlan{Disk: disk, Target: target}
	plan.Erase = cfg.InstallMode == nil || *cfg.InstallMode == "erase"
	if cfg.Encryption != nil {
		plan.Encryption = *cfg.Encryption
	}
	return plan
}

// Register adds every method and interface description to srv.
func (s *Service) Register(srv *varlink.Server) {
	s.registerFisherman(srv)
	s.registerSysInstall(srv)
	s.registerCryptEnroll(srv)
	s.registerShutdown(srv)
	for name, idl := range interfaceIDL {
		srv.AddInterface(name, idl)
	}
}

// RegisterInterface adds one interface and its description to srv. The
// real daemon serves exactly one interface per socket, like systemd; the
// mock daemon serves every interface on every socket through Register.
func (s *Service) RegisterInterface(srv *varlink.Server, iface string) {
	switch iface {
	case IfaceFisherman:
		s.registerFisherman(srv)
	case IfaceSysInstall:
		s.registerSysInstall(srv)
	case IfaceCryptEnroll:
		s.registerCryptEnroll(srv)
	case IfaceShutdown:
		s.registerShutdown(srv)
	default:
		return
	}
	srv.AddInterface(iface, interfaceIDL[iface])
}

func (s *Service) isInstalling() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st == stateInstalling
}
