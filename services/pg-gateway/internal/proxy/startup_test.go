package proxy

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func packet(code uint32, params ...string) []byte {
	var body bytes.Buffer
	_ = binary.Write(&body, binary.BigEndian, code)
	for i := 0; i+1 < len(params); i += 2 {
		body.WriteString(params[i])
		body.WriteByte(0)
		body.WriteString(params[i+1])
		body.WriteByte(0)
	}
	if len(params) > 0 {
		body.WriteByte(0)
	}
	out := make([]byte, 4)
	binary.BigEndian.PutUint32(out, uint32(4+body.Len()))
	return append(out, body.Bytes()...)
}

func TestReadInitialSSLRequest(t *testing.T) {
	init, err := ReadInitial(bytes.NewReader(packet(codeSSLRequest)))
	if err != nil {
		t.Fatal(err)
	}
	if init.Kind != KindSSLRequest {
		t.Fatalf("kind = %v, want SSLRequest", init.Kind)
	}
}

func TestReadInitialStartupParams(t *testing.T) {
	raw := packet(protocolV3, "user", "app", "database", "orders", "options", "endpoint=ep_abc")
	init, err := ReadInitial(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if init.Kind != KindStartup {
		t.Fatalf("kind = %v, want Startup", init.Kind)
	}
	if init.Params["user"] != "app" || init.Params["database"] != "orders" {
		t.Fatalf("params = %v", init.Params)
	}
	if !bytes.Equal(init.Raw, raw) {
		t.Fatal("Raw must round-trip verbatim for backend forwarding")
	}
}

func TestReadInitialRejectsOversized(t *testing.T) {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], maxStartupLen+1)
	if _, err := ReadInitial(bytes.NewReader(lenBuf[:])); err == nil {
		t.Fatal("oversized packet must be rejected")
	}
}

func TestReadInitialRejectsUnknownProtocol(t *testing.T) {
	if _, err := ReadInitial(bytes.NewReader(packet(12345))); err == nil {
		t.Fatal("unknown protocol must be rejected")
	}
}

func TestEndpointFromSNI(t *testing.T) {
	cases := map[string]string{
		"ep-01abc.syd1.db.nimbus.app": "ep_01abc",
		"api.db.nimbus.app":           "",
		"ep-x":                        "", // no domain suffix
		"":                            "",
		"epx.syd1.db.nimbus.app":      "",
	}
	for in, want := range cases {
		if got := EndpointFromSNI(in); got != want {
			t.Errorf("EndpointFromSNI(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEndpointFromOptions(t *testing.T) {
	cases := []struct {
		options string
		want    string
	}{
		{"endpoint=ep_abc", "ep_abc"},
		{"-c endpoint=ep_abc", "ep_abc"},
		{"-c search_path=public endpoint=ep_x", "ep_x"},
		{"", ""},
		{"-c statement_timeout=5000", ""},
	}
	for _, tc := range cases {
		got := EndpointFromOptions(map[string]string{"options": tc.options})
		if got != tc.want {
			t.Errorf("options %q → %q, want %q", tc.options, got, tc.want)
		}
	}
}

func TestStripEndpointOption(t *testing.T) {
	raw := packet(protocolV3, "user", "app", "options", "-c search_path=public endpoint=ep_x")
	init, err := ReadInitial(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	stripped, err := ReadInitial(bytes.NewReader(StripEndpointOption(init)))
	if err != nil {
		t.Fatalf("rewritten startup must stay parseable: %v", err)
	}
	if stripped.Params["user"] != "app" {
		t.Fatalf("params lost in rewrite: %v", stripped.Params)
	}
	if got := stripped.Params["options"]; got != "-c search_path=public" {
		t.Fatalf("options = %q, want endpoint token removed only", got)
	}

	// Options containing only the endpoint token vanish entirely.
	raw = packet(protocolV3, "user", "app", "options", "endpoint=ep_x")
	init, _ = ReadInitial(bytes.NewReader(raw))
	stripped, _ = ReadInitial(bytes.NewReader(StripEndpointOption(init)))
	if _, ok := stripped.Params["options"]; ok {
		t.Fatalf("empty options must be dropped, got %q", stripped.Params["options"])
	}
}

func TestErrorResponseShape(t *testing.T) {
	msg := ErrorResponse("08004", "nope")
	if msg[0] != 'E' {
		t.Fatal("must be an ErrorResponse")
	}
	if int(binary.BigEndian.Uint32(msg[1:5])) != len(msg)-1 {
		t.Fatal("length header mismatch")
	}
	if !bytes.Contains(msg, []byte("08004")) || !bytes.Contains(msg, []byte("nope")) {
		t.Fatal("missing fields")
	}
}
