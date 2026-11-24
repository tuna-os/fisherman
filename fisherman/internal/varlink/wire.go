// Package varlink is a small, dependency-free implementation of the Varlink
// wire protocol (https://varlink.org/): JSON objects, each terminated by a
// NUL byte, over a stream socket.
//
// It exists so that fishermand controls the exact bytes it writes. The
// reference client (hanthor/gnome-setup, built on zlink) is strict about the
// shape of replies, and the daemon must match systemd's own services.
package varlink

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// maxFrame bounds one message. A call or reply larger than this is a protocol
// error: nothing in this API comes close.
const maxFrame = 1 << 20

// Call is one decoded method call.
type Call struct {
	Method     string          `json:"method"`
	Parameters json.RawMessage `json:"parameters,omitempty"`
	More       bool            `json:"more,omitempty"`
	Oneway     bool            `json:"oneway,omitempty"`
	Upgrade    bool            `json:"upgrade,omitempty"`
}

// Decode unmarshals the call parameters into v. Absent or null parameters
// leave v untouched; unknown fields are ignored, as Varlink requires.
func (c *Call) Decode(v any) error {
	if len(c.Parameters) == 0 || string(c.Parameters) == "null" {
		return nil
	}
	return json.Unmarshal(c.Parameters, v)
}

// reply is the on-the-wire reply object.
type reply struct {
	Parameters any    `json:"parameters,omitempty"`
	Continues  bool   `json:"continues,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Error is a Varlink error reply. Name is the fully qualified error name,
// for example "io.systemd.SysInstall.NoCandidateDevices".
type Error struct {
	Name   string
	Params any
}

func (e *Error) Error() string {
	if e.Params == nil {
		return e.Name
	}
	b, _ := json.Marshal(e.Params)
	return e.Name + " " + string(b)
}

// Standard error names from the org.varlink.service interface.
const (
	ErrInterfaceNotFound = "org.varlink.service.InterfaceNotFound"
	ErrMethodNotFound    = "org.varlink.service.MethodNotFound"
	ErrInvalidParameter  = "org.varlink.service.InvalidParameter"
	ErrExpectedMore      = "org.varlink.service.ExpectedMore"
	ErrInternal          = "org.varlink.service.InternalError"
	ErrPermissionDenied  = "org.varlink.service.PermissionDenied"
)

type noParameters struct{}

// NoParameters, passed to ReplyWriter.Reply, sends a reply with no
// "parameters" key at all. systemd ends some streams that way (the final,
// empty reply of io.systemd.SysInstall.Run), and zlink clients report it as
// MissingParameters, which gnome-setup ignores on purpose.
var NoParameters any = noParameters{}

// marshalReply renders a reply frame, without the trailing NUL.
func marshalReply(params any, continues bool, errName string) ([]byte, error) {
	switch params.(type) {
	case nil:
		params = struct{}{}
	case noParameters:
		params = nil
	}
	return json.Marshal(reply{Parameters: params, Continues: continues, Error: errName})
}

// readFrame reads one NUL-terminated message, without the NUL.
func readFrame(r *bufio.Reader) ([]byte, error) {
	var out []byte
	for {
		chunk, err := r.ReadSlice(0)
		out = append(out, chunk...)
		if len(out) > maxFrame {
			return nil, fmt.Errorf("varlink: message exceeds %d bytes", maxFrame)
		}
		switch {
		case err == nil:
			return out[:len(out)-1], nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(out) > 0:
			return nil, io.ErrUnexpectedEOF
		default:
			return nil, err
		}
	}
}
