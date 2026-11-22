package varlink

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"time"
)

// Client is a minimal Varlink client for tests and the fisherman CLI. It is
// not safe for concurrent use; open one per call stream.
type Client struct {
	conn net.Conn
	br   *bufio.Reader
}

// Dial connects to a Varlink UNIX socket.
func Dial(path string) (*Client, error) {
	conn, err := net.DialTimeout("unix", path, 5*time.Second)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, br: bufio.NewReader(conn)}, nil
}

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

// SetDeadline bounds every following read and write.
func (c *Client) SetDeadline(t time.Time) error { return c.conn.SetDeadline(t) }

// SendRaw writes one pre-rendered call frame (a NUL is appended).
func (c *Client) SendRaw(b []byte) error {
	_, err := c.conn.Write(append(append([]byte{}, b...), 0))
	return err
}

// ReadFrame reads one raw reply frame, without the NUL.
func (c *Client) ReadFrame() ([]byte, error) { return readFrame(c.br) }

func (c *Client) send(method string, params any, more bool) error {
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		raw = b
	}
	b, err := json.Marshal(Call{Method: method, Parameters: raw, More: more})
	if err != nil {
		return err
	}
	return c.SendRaw(b)
}

type replyIn struct {
	Parameters json.RawMessage `json:"parameters"`
	Continues  bool            `json:"continues"`
	Error      string          `json:"error"`
}

// next reads one reply. A Varlink error reply comes back as *Error.
func (c *Client) next(out any) (continues bool, err error) {
	b, err := c.ReadFrame()
	if err != nil {
		return false, err
	}
	var r replyIn
	if err := json.Unmarshal(b, &r); err != nil {
		return false, err
	}
	if r.Error != "" {
		var p any
		_ = json.Unmarshal(r.Parameters, &p)
		return false, &Error{Name: r.Error, Params: p}
	}
	if out != nil && len(r.Parameters) > 0 {
		if err := json.Unmarshal(r.Parameters, out); err != nil {
			return false, err
		}
	}
	return r.Continues, nil
}

// Call makes a single-reply call.
func (c *Client) Call(method string, params, out any) error {
	if err := c.send(method, params, false); err != nil {
		return err
	}
	cont, err := c.next(out)
	if err == nil && cont {
		return errors.New("varlink: unexpected continues on a non-more call")
	}
	return err
}

// Stream is the reply stream of a call made with "more".
type Stream struct {
	c    *Client
	done bool
}

// CallMore starts a streaming call.
func (c *Client) CallMore(method string, params any) (*Stream, error) {
	if err := c.send(method, params, true); err != nil {
		return nil, err
	}
	return &Stream{c: c}, nil
}

// Next reads the next reply into out. ok is false after the final reply has
// been delivered. A Varlink error reply is returned as *Error and ends the
// stream.
func (s *Stream) Next(out any) (ok bool, err error) {
	if s.done {
		return false, nil
	}
	cont, err := s.c.next(out)
	if err != nil || !cont {
		s.done = true
	}
	return err == nil, err
}
