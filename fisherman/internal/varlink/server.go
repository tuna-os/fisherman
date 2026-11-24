package varlink

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sort"
	"sync"

	"github.com/tuna-os/fisherman/internal/auth"
)

// ReplyWriter sends replies for one call.
type ReplyWriter interface {
	// Reply sends one reply. Pass continues=true for every reply of a
	// streaming call except the last. A handler that returns nil without a
	// final (continues=false) reply gets an empty one added.
	Reply(params any, continues bool) error
}

// Handler serves one method. Return *Error for a Varlink error reply; any
// other non-nil error becomes org.varlink.service.InternalError.
//
// ctx is cancelled when the client disconnects.
type Handler func(ctx context.Context, call *Call, w ReplyWriter) error

// ServiceInfo is the answer to org.varlink.service.GetInfo.
type ServiceInfo struct {
	Vendor  string `json:"vendor"`
	Product string `json:"product"`
	Version string `json:"version"`
	URL     string `json:"url"`
}

// Server dispatches Varlink calls to handlers.
//
// The server never logs call parameters: they carry passphrases. It logs the
// method name and the error name only.
type Server struct {
	Info   ServiceInfo
	Logger *slog.Logger

	mu         sync.RWMutex
	methods    map[string]Handler
	interfaces map[string]string // name -> IDL text
}

// NewServer returns an empty server with the org.varlink.service interface.
func NewServer(info ServiceInfo, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &Server{Info: info, Logger: logger,
		methods: map[string]Handler{}, interfaces: map[string]string{}}
	s.methods["org.varlink.service.GetInfo"] = s.getInfo
	s.methods["org.varlink.service.GetInterfaceDescription"] = s.getInterfaceDescription
	return s
}

// Handle registers a method by its fully qualified name.
func (s *Server) Handle(method string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.methods[method] = h
}

// AddInterface registers an interface description for introspection.
func (s *Server) AddInterface(name, idl string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interfaces[name] = idl
}

func (s *Server) getInfo(_ context.Context, _ *Call, w ReplyWriter) error {
	s.mu.RLock()
	names := []string{"org.varlink.service"}
	for n := range s.interfaces {
		names = append(names, n)
	}
	s.mu.RUnlock()
	sort.Strings(names)
	return w.Reply(map[string]any{
		"vendor": s.Info.Vendor, "product": s.Info.Product,
		"version": s.Info.Version, "url": s.Info.URL, "interfaces": names,
	}, false)
}

func (s *Server) getInterfaceDescription(_ context.Context, c *Call, w ReplyWriter) error {
	var p struct {
		Interface string `json:"interface"`
	}
	if err := c.Decode(&p); err != nil || p.Interface == "" {
		return &Error{ErrInvalidParameter, map[string]string{"parameter": "interface"}}
	}
	s.mu.RLock()
	idl, ok := s.interfaces[p.Interface]
	s.mu.RUnlock()
	if !ok {
		return &Error{ErrInterfaceNotFound, map[string]string{"interface": p.Interface}}
	}
	return w.Reply(map[string]string{"description": idl}, false)
}

// Serve accepts connections on l until ctx is cancelled or l fails. It closes
// l before returning.
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	var wg sync.WaitGroup
	defer wg.Wait()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { <-ctx.Done(); _ = l.Close() }()
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.serveConn(ctx, conn)
		}()
	}
}

type frame struct {
	b   []byte
	err error
}

func (s *Server) serveConn(parent context.Context, conn net.Conn) {
	defer conn.Close()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	// Attribute the peer while it is connected: handlers authorize
	// mutating calls against these credentials. Unattributable peers
	// (non-UNIX, SO_PEERCRED off Linux, or already gone) carry no
	// credentials, and a service that authorizes denies them.
	if creds, err := auth.PeerCreds(conn); err == nil {
		ctx = auth.WithCreds(ctx, creds)
	}

	// A reader goroutine notices a disconnect while a streaming handler is
	// busy, and cancels ctx so the handler can clean up.
	frames := make(chan frame, 16)
	go func() {
		defer close(frames)
		br := bufio.NewReader(conn)
		for {
			b, err := readFrame(br)
			if err != nil {
				cancel()
				frames <- frame{err: err}
				return
			}
			select {
			case frames <- frame{b: b}:
			case <-ctx.Done():
				return
			}
		}
	}()

	var wmu sync.Mutex
	for f := range frames {
		if f.err != nil {
			return
		}
		w := &connWriter{conn: conn, mu: &wmu}
		var call Call
		if err := json.Unmarshal(f.b, &call); err != nil || call.Method == "" {
			s.Logger.Debug("varlink: malformed call")
			return // not a Varlink peer: drop the connection
		}
		s.dispatch(ctx, &call, w)
		if w.err != nil {
			return
		}
	}
}

func (s *Server) dispatch(ctx context.Context, call *Call, w *connWriter) {
	s.mu.RLock()
	h, ok := s.methods[call.Method]
	s.mu.RUnlock()
	w.oneway = call.Oneway
	if !ok {
		s.Logger.Debug("varlink: method not found", "method", call.Method)
		w.fail(&Error{ErrMethodNotFound, map[string]string{"method": call.Method}})
		return
	}
	err := h(ctx, call, w)
	switch e := err.(type) {
	case nil:
		if !w.finished {
			w.err = w.Reply(struct{}{}, false)
		}
	case *Error:
		s.Logger.Debug("varlink: call failed", "method", call.Method, "error", e.Name)
		w.fail(e)
	default:
		if ctx.Err() != nil {
			w.err = ctx.Err() // client went away; nothing to send
			return
		}
		s.Logger.Warn("varlink: handler error", "method", call.Method, "err", fmt.Sprint(err))
		w.fail(&Error{ErrInternal, map[string]string{"method": call.Method}})
	}
}

type connWriter struct {
	conn     net.Conn
	mu       *sync.Mutex
	oneway   bool
	finished bool
	err      error
}

func (w *connWriter) write(b []byte) error {
	if w.oneway {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err := w.conn.Write(append(b, 0))
	return err
}

func (w *connWriter) Reply(params any, continues bool) error {
	if w.finished {
		return errors.New("varlink: reply after final reply")
	}
	b, err := marshalReply(params, continues, "")
	if err != nil {
		return err
	}
	if !continues {
		w.finished = true
	}
	if err := w.write(b); err != nil {
		w.err = err
		return err
	}
	return nil
}

func (w *connWriter) fail(e *Error) {
	if w.finished {
		return
	}
	w.finished = true
	b, err := marshalReply(e.Params, false, e.Name)
	if err == nil {
		err = w.write(b)
	}
	w.err = err
}
