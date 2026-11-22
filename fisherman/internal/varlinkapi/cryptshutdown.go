package varlinkapi

import (
	"context"
	"errors"

	"github.com/tuna-os/fisherman/internal/varlink"
)

func cryptErr(name string) *varlink.Error {
	return &varlink.Error{Name: IfaceCryptEnroll + "." + name, Params: map[string]any{}}
}

// mapCryptErr turns an Executor error into the wire error.
func mapCryptErr(err error) error {
	switch {
	case errors.Is(err, ErrPasswordRequired):
		return cryptErr("PasswordRequired")
	case errors.Is(err, ErrPasswordIncorrect):
		return cryptErr("PasswordIncorrect")
	case errors.Is(err, ErrVolumeForeign):
		return cryptErr("VolumeUnderForeignManagement")
	case errors.Is(err, ErrFidoDeviceNotFound):
		return cryptErr("FidoDeviceNotFound")
	case errors.Is(err, ErrFidoTimeout):
		return cryptErr("FidoActionTimeout")
	case errors.Is(err, ErrNoDevice):
		return systemErr(errnoENODEV, "ENODEV", "no such LUKS2 volume")
	}
	return err
}

func (s *Service) registerCryptEnroll(srv *varlink.Server) {
	h := func(name string, f varlink.Handler) { srv.Handle(IfaceCryptEnroll+"."+name, f) }

	h("MakeRecoveryKey", func(ctx context.Context, _ *varlink.Call, w varlink.ReplyWriter) error {
		if err := s.checkAuth(ctx, IfaceCryptEnroll+".MakeRecoveryKey"); err != nil {
			return err
		}
		k, err := s.exec.MakeRecoveryKey(ctx)
		if err != nil {
			return mapCryptErr(err)
		}
		return w.Reply(map[string]any{"recoveryKey": k}, false)
	})

	h("Enroll", func(ctx context.Context, c *varlink.Call, w varlink.ReplyWriter) error {
		var p EnrollParams
		if err := c.Decode(&p); err != nil || p.Node == "" || p.Mechanism == "" {
			return invalidParam("node")
		}
		if err := s.checkAuth(ctx, IfaceCryptEnroll+".Enroll"); err != nil {
			return err
		}
		r, err := s.exec.Enroll(ctx, p)
		if err != nil {
			return mapCryptErr(err)
		}
		s.log.Info("cryptenroll", "node", p.Node, "mechanism", p.Mechanism)
		return w.Reply(r, false)
	})

	h("ListSlots", func(ctx context.Context, c *varlink.Call, w varlink.ReplyWriter) error {
		var p struct {
			Node string `json:"node"`
		}
		if err := c.Decode(&p); err != nil || p.Node == "" {
			return invalidParam("node")
		}
		if !c.More {
			return &varlink.Error{Name: varlink.ErrExpectedMore}
		}
		slots, err := s.exec.ListSlots(ctx, p.Node)
		if err != nil {
			return mapCryptErr(err)
		}
		if len(slots) == 0 {
			return w.Reply(struct{}{}, false)
		}
		for i, sl := range slots {
			if err := w.Reply(sl, i < len(slots)-1); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) registerShutdown(srv *varlink.Server) {
	for _, kind := range []string{"PowerOff", "Reboot", "Halt", "KExec", "SoftReboot"} {
		srv.Handle(IfaceShutdown+"."+kind, func(ctx context.Context, c *varlink.Call, w varlink.ReplyWriter) error {
			var p ShutdownParams
			if err := c.Decode(&p); err != nil {
				return invalidParam("skipInhibitors")
			}
			if err := s.checkAuth(ctx, IfaceShutdown+"."+kind); err != nil {
				return err
			}
			err := s.exec.Shutdown(ctx, kind, p)
			var bi *BlockedByInhibitor
			switch {
			case err == nil:
				s.log.Info("shutdown requested", "kind", kind)
				return w.Reply(struct{}{}, false)
			case errors.Is(err, ErrAlreadyInProgress):
				return &varlink.Error{Name: IfaceShutdown + ".AlreadyInProgress", Params: map[string]any{}}
			case errors.As(err, &bi):
				return &varlink.Error{Name: IfaceShutdown + ".BlockedByInhibitor",
					Params: map[string]any{"who": bi.Who, "why": bi.Why}}
			}
			return err
		})
	}
}
