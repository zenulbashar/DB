package proxy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Postgres wire-protocol startup phase (v3). The gateway parses ONLY the
// pre-authentication handshake — never query traffic (ADR-007: route, hold,
// count; no protocol rewriting).

const (
	codeSSLRequest    = 80877103
	codeGSSENCRequest = 80877104
	codeCancelRequest = 80877102
	protocolV3        = 196608

	// maxStartupLen bounds the first packet; the startup message carries only
	// short key/value parameters, so 16 KiB is generous and abuse-resistant.
	maxStartupLen = 16 * 1024
)

var (
	ErrTooLarge  = errors.New("startup packet too large")
	ErrMalformed = errors.New("malformed startup packet")
	ErrProtocol  = errors.New("unsupported protocol version")
)

type InitialKind int

const (
	KindSSLRequest InitialKind = iota
	KindGSSENC
	KindCancel
	KindStartup
)

// Initial is the first client packet: its raw bytes (forwarded verbatim to
// the backend) plus what the gateway needs for routing.
type Initial struct {
	Kind   InitialKind
	Raw    []byte
	Params map[string]string // startup parameters (user, database, options, …)
}

// ReadInitial reads exactly one length-prefixed startup-phase packet.
func ReadInitial(r io.Reader) (*Initial, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(lenBuf[:])
	if length < 8 || length > maxStartupLen {
		return nil, ErrTooLarge
	}
	body := make([]byte, length-4)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	raw := append(lenBuf[:], body...)
	code := binary.BigEndian.Uint32(body[:4])

	init := &Initial{Raw: raw}
	switch code {
	case codeSSLRequest:
		init.Kind = KindSSLRequest
	case codeGSSENCRequest:
		init.Kind = KindGSSENC
	case codeCancelRequest:
		init.Kind = KindCancel
	case protocolV3:
		init.Kind = KindStartup
		params, err := parseParams(body[4:])
		if err != nil {
			return nil, err
		}
		init.Params = params
	default:
		return nil, fmt.Errorf("%w: %d", ErrProtocol, code)
	}
	return init, nil
}

// parseParams decodes the null-terminated key/value list of a StartupMessage.
func parseParams(b []byte) (map[string]string, error) {
	params := map[string]string{}
	for len(b) > 0 && b[0] != 0 {
		kEnd := indexNull(b)
		if kEnd < 0 {
			return nil, ErrMalformed
		}
		key := string(b[:kEnd])
		b = b[kEnd+1:]
		vEnd := indexNull(b)
		if vEnd < 0 {
			return nil, ErrMalformed
		}
		params[key] = string(b[:vEnd])
		b = b[vEnd+1:]
	}
	return params, nil
}

func indexNull(b []byte) int {
	for i, c := range b {
		if c == 0 {
			return i
		}
	}
	return -1
}

// EndpointFromSNI maps an SNI hostname to an endpoint ID:
// ep-01abc….syd1.db.nimbus.app → ep_01abc… . Returns "" when the name is not
// in an endpoint namespace (DATABASE_ARCHITECTURE §5).
func EndpointFromSNI(serverName string) string {
	label, _, ok := strings.Cut(serverName, ".")
	if !ok || !strings.HasPrefix(label, "ep-") {
		return ""
	}
	return "ep_" + label[3:]
}

// EndpointFromOptions extracts `endpoint=<id>` from the libpq options
// parameter — the SNI-less fallback (same escape hatch Neon documents).
func EndpointFromOptions(params map[string]string) string {
	for _, tok := range strings.Fields(params["options"]) {
		tok = strings.TrimPrefix(tok, "-c")
		if v, ok := strings.CutPrefix(tok, "endpoint="); ok {
			return v
		}
	}
	return ""
}

// StripEndpointOption returns startup bytes with the routing-only
// `endpoint=` token removed from the options parameter. The backend Postgres
// would reject it as an unknown server argument, so fallback-routed
// connections get their startup message rewritten (SNI-routed ones are
// forwarded verbatim).
func StripEndpointOption(init *Initial) []byte {
	if init.Kind != KindStartup {
		return init.Raw
	}
	params := make(map[string]string, len(init.Params))
	for k, v := range init.Params {
		params[k] = v
	}
	toks := strings.Fields(params["options"])
	var kept []string
	for i := 0; i < len(toks); i++ {
		tok := toks[i]
		// libpq accepts the endpoint token three ways: bare "endpoint=X",
		// combined "-cendpoint=X", and the space-separated "-c endpoint=X"
		// pair. The original code dropped only the value of the last form and
		// left a dangling "-c" that the backend rejects (audit finding); this
		// handles the pair as a unit.
		if tok == "-c" && i+1 < len(toks) {
			if strings.HasPrefix(toks[i+1], "endpoint=") {
				i++ // drop both "-c" and its "endpoint=" value
				continue
			}
			kept = append(kept, tok, toks[i+1])
			i++
			continue
		}
		if strings.HasPrefix(strings.TrimPrefix(tok, "-c"), "endpoint=") {
			continue
		}
		kept = append(kept, tok)
	}
	if len(kept) == 0 {
		delete(params, "options")
	} else {
		params["options"] = strings.Join(kept, " ")
	}
	return EncodeStartup(params)
}

// EncodeStartup builds a protocol-v3 StartupMessage from parameters.
func EncodeStartup(params map[string]string) []byte {
	var body []byte
	body = binary.BigEndian.AppendUint32(body, protocolV3)
	for k, v := range params {
		body = append(body, k...)
		body = append(body, 0)
		body = append(body, v...)
		body = append(body, 0)
	}
	body = append(body, 0)
	out := binary.BigEndian.AppendUint32(nil, uint32(4+len(body)))
	return append(out, body...)
}

// ErrorResponse encodes a Postgres v3 ErrorResponse so clients fail with a
// readable message instead of a dropped socket.
func ErrorResponse(sqlstate, message string) []byte {
	fields := []struct {
		code byte
		val  string
	}{
		{'S', "FATAL"}, {'V', "FATAL"}, {'C', sqlstate}, {'M', message},
	}
	var body []byte
	for _, f := range fields {
		body = append(body, f.code)
		body = append(body, f.val...)
		body = append(body, 0)
	}
	body = append(body, 0)
	msg := make([]byte, 5, 5+len(body))
	msg[0] = 'E'
	binary.BigEndian.PutUint32(msg[1:5], uint32(4+len(body)))
	return append(msg, body...)
}
