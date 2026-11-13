// Package ipc implements low-level node-to-node messaging over TCP and UDP.
//
// Both transports carry the same versioned JSON Envelope:
//
//	TCP: [4-byte big-endian length][JSON envelope]   (stream needs framing)
//	UDP: one JSON envelope per datagram               (datagram is the frame)
package ipc

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// ProtocolVersion is bumped on any incompatible envelope change. Receivers
// reject versions they don't understand instead of guessing.
const ProtocolVersion = 1

// Limits. MaxUDPPayload stays under the 1280-byte IPv6 minimum MTU minus
// headers, so datagrams are never fragmented (fragments get dropped).
const (
	MaxTCPFrame   = 64 << 10 // 64 KiB
	MaxUDPPayload = 1200
	MaxBodyBytes  = 32 << 10
)

// Message types.
const (
	TypePing      = "ping"
	TypePong      = "pong"
	TypeEcho      = "echo"
	TypeEchoReply = "echo_reply"
	TypeHeartbeat = "heartbeat"
	TypeAck       = "ack"
	TypeError     = "error"
)

var knownTypes = map[string]bool{
	TypePing: true, TypePong: true, TypeEcho: true, TypeEchoReply: true,
	TypeHeartbeat: true, TypeAck: true, TypeError: true,
}

var (
	idPattern   = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	fromPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
)

// Errors callers can test for.
var (
	ErrMalformed = errors.New("malformed message")
	ErrTooLarge  = errors.New("message too large")
	ErrRemote    = errors.New("remote error")
)

// Envelope is the wire message. ID correlates a reply with its request.
type Envelope struct {
	V    int             `json:"v"`
	Type string          `json:"type"`
	ID   string          `json:"id"`
	From string          `json:"from"`
	TS   int64           `json:"ts"` // sender's Unix milliseconds (informational only)
	Body json.RawMessage `json:"body,omitempty"`
}

// ErrorBody is carried by TypeError envelopes.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// NewID returns a random correlation ID.
func NewID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Validate checks an envelope received from the network.
func (e Envelope) Validate() error {
	switch {
	case e.V != ProtocolVersion:
		return fmt.Errorf("%w: unsupported version %d", ErrMalformed, e.V)
	case !knownTypes[e.Type]:
		return fmt.Errorf("%w: unknown type %q", ErrMalformed, e.Type)
	case !idPattern.MatchString(e.ID):
		return fmt.Errorf("%w: bad id", ErrMalformed)
	case !fromPattern.MatchString(e.From):
		return fmt.Errorf("%w: bad sender", ErrMalformed)
	case len(e.Body) > MaxBodyBytes:
		return fmt.Errorf("%w: body %d bytes", ErrTooLarge, len(e.Body))
	}
	return nil
}

// decode parses and validates bytes from the wire. Unknown fields are
// rejected, matching the REST layer's strictness.
func decode(b []byte) (Envelope, error) {
	var e Envelope
	if err := strictUnmarshal(b, &e); err != nil {
		return Envelope{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	return e, e.Validate()
}

func encode(e Envelope) ([]byte, error) { return json.Marshal(e) }

// Reply builds a response to req with the same correlation ID.
func Reply(req Envelope, from, typ string, body any) (Envelope, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{V: ProtocolVersion, Type: typ, ID: req.ID, From: from, TS: time.Now().UnixMilli(), Body: raw}, nil
}

// errorReply never fails; it is used on paths where we're already failing.
func errorReply(id, from, code, msg string) Envelope {
	if !idPattern.MatchString(id) {
		id = "unknown"
	}
	raw, _ := json.Marshal(ErrorBody{Code: code, Message: msg})
	return Envelope{V: ProtocolVersion, Type: TypeError, ID: id, From: from, Body: raw}
}

// remoteErr converts a TypeError reply into a Go error.
func remoteErr(e Envelope) error {
	var eb ErrorBody
	_ = json.Unmarshal(e.Body, &eb)
	return fmt.Errorf("%w: %s: %s", ErrRemote, eb.Code, eb.Message)
}
