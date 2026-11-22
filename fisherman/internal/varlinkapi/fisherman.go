package varlinkapi

import (
	"context"

	"github.com/tuna-os/fisherman/internal/varlink"
)

func invalidParam(name string) *varlink.Error {
	return &varlink.Error{Name: varlink.ErrInvalidParameter, Params: map[string]string{"parameter": name}}
}

func fishermanErr(name string, params any) *varlink.Error {
	return &varlink.Error{Name: IfaceFisherman + "." + name, Params: params}
}

func (s *Service) registerFisherman(srv *varlink.Server) {
	h := func(name string, f varlink.Handler) { srv.Handle(IfaceFisherman+"."+name, f) }

	h("Requirements", func(ctx context.Context, _ *varlink.Call, w varlink.ReplyWriter) error {
		r, err := s.exec.Requirements(ctx)
		if err != nil {
			return err
		}
		return w.Reply(r, false)
	})

	h("Probe", func(ctx context.Context, _ *varlink.Call, w varlink.ReplyWriter) error {
		r, err := s.exec.Probe(ctx)
		if err != nil {
			return err
		}
		return w.Reply(r, false)
	})

	h("Validate", func(ctx context.Context, c *varlink.Call, w varlink.ReplyWriter) error {
		var p struct {
			Config  Config `json:"config"`
			Partial *bool  `json:"partial"`
		}
		if err := c.Decode(&p); err != nil {
			return invalidParam("config")
		}
		errs, _, err := s.validate(ctx, p.Config, p.Partial != nil && *p.Partial)
		if err != nil {
			return err
		}
		return w.Reply(map[string]any{"valid": len(errs) == 0, "errors": errs}, false)
	})

	h("ConfigureTarget", func(_ context.Context, c *varlink.Call, w varlink.ReplyWriter) error {
		var p struct {
			User      *User   `json:"user"`
			Hostname  *string `json:"hostname"`
			Locale    *string `json:"locale"`
			Keymap    *string `json:"keymap"`
			TimeZone  *string `json:"timeZone"`
			Autologin *bool   `json:"autologin"`
		}
		if err := c.Decode(&p); err != nil {
			return invalidParam("user")
		}
		errs := s.checkIdentity(p.Hostname, p.User)
		if p.Locale != nil && !localeRe.MatchString(*p.Locale) {
			errs = append(errs, ValidationError{"locale", CodeLocaleInvalid, "locale must look like en_US.UTF-8"})
		}
		if p.Keymap != nil && !localeRe.MatchString(*p.Keymap) {
			errs = append(errs, ValidationError{"keymap", CodeKeymapInvalid, "keymap must look like us or de-nodeadkeys"})
		}
		if p.TimeZone != nil && (!tzRe.MatchString(*p.TimeZone)) {
			errs = append(errs, ValidationError{"timeZone", CodeTimeZoneInvalid, "time zone must look like Europe/Berlin"})
		}
		if len(errs) > 0 {
			return w.Reply(map[string]any{"configured": false, "errors": errs}, false)
		}
		s.mu.Lock()
		if s.st == stateInstalling {
			s.mu.Unlock()
			return fishermanErr("InstallInProgress", nil)
		}
		if p.User != nil {
			s.target.User = p.User
		}
		if p.Hostname != nil {
			s.target.Hostname = *p.Hostname
		}
		if p.Locale != nil {
			s.target.Locale = *p.Locale
		}
		if p.Keymap != nil {
			s.target.Keymap = *p.Keymap
		}
		if p.TimeZone != nil {
			s.target.TimeZone = *p.TimeZone
		}
		if p.Autologin != nil {
			s.target.Autologin = *p.Autologin
		}
		s.mu.Unlock()
		s.log.Info("target configured")
		return w.Reply(map[string]any{"configured": true, "errors": []ValidationError{}}, false)
	})

	h("Install", func(ctx context.Context, c *varlink.Call, w varlink.ReplyWriter) error {
		if !c.More {
			return &varlink.Error{Name: varlink.ErrExpectedMore}
		}
		var p struct {
			Config    Config `json:"config"`
			Confirmed bool   `json:"confirmed"`
		}
		if err := c.Decode(&p); err != nil {
			return invalidParam("config")
		}
		if !p.Confirmed {
			return fishermanErr("NotConfirmed", nil)
		}
		errs, disk, err := s.validate(ctx, p.Config, false)
		if err != nil {
			return err
		}
		if len(errs) > 0 || disk == nil {
			return fishermanErr("InvalidConfig", map[string]any{"errors": errs})
		}
		ictx, finish, err := s.begin(ctx)
		switch err {
		case nil:
		case errInstalling:
			return fishermanErr("InstallInProgress", nil)
		default:
			return fishermanErr("AlreadyInstalled", nil)
		}
		final := s.runInstall(ictx, s.planFor(p.Config, *disk), func(ev Event) {
			_ = w.Reply(ev, true) // a write error means the client left; ictx is cancelled
		})
		finish(final.Event == EvComplete)
		return w.Reply(final, false)
	})

	h("Handoff", func(ctx context.Context, c *varlink.Call, w varlink.ReplyWriter) error {
		var p struct {
			Mode *string `json:"mode"`
		}
		if err := c.Decode(&p); err != nil {
			return invalidParam("mode")
		}
		mode := "reboot"
		if p.Mode != nil {
			mode = *p.Mode
		}
		switch mode {
		case "migrate", "soft_reboot", "reboot":
		default:
			return invalidParam("mode")
		}
		s.mu.Lock()
		installed := s.st == stateInstalled
		s.mu.Unlock()
		if !installed {
			return fishermanErr("NotInstalled", nil)
		}
		keeps, err := s.exec.Handoff(ctx, mode)
		if err != nil {
			return err
		}
		s.log.Info("handoff", "mode", mode)
		return w.Reply(map[string]any{"mode": mode, "started": true, "sessionKeepsRunning": keeps}, false)
	})

	h("Cancel", func(ctx context.Context, _ *varlink.Call, w varlink.ReplyWriter) error {
		s.mu.Lock()
		cancel, done := s.cancel, s.done
		s.mu.Unlock()
		if cancel == nil {
			return w.Reply(map[string]any{"wasInstalling": false}, false)
		}
		s.log.Info("cancel requested")
		cancel()
		select {
		case <-done: // the executor finished its cleanup
		case <-ctx.Done():
		}
		return w.Reply(map[string]any{"wasInstalling": true}, false)
	})
}
