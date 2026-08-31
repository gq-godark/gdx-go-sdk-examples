package transport

import (
	"reflect"
	"testing"
)

func TestNormalize_LoginSuccess(t *testing.T) {
	in := Message{
		"id":   "abc",
		"op":   "login",
		"code": float64(0),
		"data": map[string]any{
			"user_uuid":  "u",
			"session_id": float64(1),
			"conn_id":    float64(7),
		},
	}
	got := normalizeInboundMessage(in)
	if got["type"] != "auth_result" {
		t.Fatalf("type = %v, want auth_result", got["type"])
	}
	if got["success"] != true {
		t.Fatalf("success = %v", got["success"])
	}
	if got["user_uuid"] != "u" {
		t.Errorf("user_uuid = %v", got["user_uuid"])
	}
	if got["conn_id"] != float64(7) {
		t.Errorf("conn_id = %v", got["conn_id"])
	}
}

func TestNormalize_LoginFailure(t *testing.T) {
	in := Message{
		"id":      "abc",
		"op":      "login",
		"code":    float64(1),
		"message": "bad key",
	}
	got := normalizeInboundMessage(in)
	if got["type"] != "auth_result" || got["success"] != false {
		t.Fatalf("login-fail got %+v", got)
	}
	if got["error"] != "bad key" {
		t.Errorf("error = %v", got["error"])
	}
}

func TestNormalize_NoiseHandshakeReply(t *testing.T) {
	in := Message{
		"id":   "x",
		"op":   "noise.handshake",
		"code": float64(0),
		"data": map[string]any{
			"conn_id":     float64(42),
			"message":     "AAAA",
			"established": false,
		},
	}
	got := normalizeInboundMessage(in)
	if got["type"] != "noise_handshake_reply" {
		t.Fatalf("type = %v", got["type"])
	}
	if got["conn_id"] != float64(42) || got["message"] != "AAAA" || got["established"] != false {
		t.Errorf("noise reply = %+v", got)
	}
}

func TestNormalize_SubscribeAck(t *testing.T) {
	in := Message{
		"id":   "y",
		"op":   "subscribe",
		"code": float64(0),
		"data": map[string]any{"channel": "orders"},
	}
	got := normalizeInboundMessage(in)
	if got["event"] != "subscribe" || got["channel"] != "orders" {
		t.Fatalf("subscribe ack = %+v", got)
	}
}

func TestNormalize_Pong(t *testing.T) {
	in := Message{"op": "pong", "code": float64(0)}
	got := normalizeInboundMessage(in)
	if got["type"] != "pong" {
		t.Fatalf("pong = %+v", got)
	}
}

func TestNormalize_EncryptedAck(t *testing.T) {
	in := Message{
		"op":   "order.place",
		"code": float64(0),
		"data": map[string]any{
			"message_type":   "ack",
			"encrypted_body": "ZGF0YQ==",
			"nonce":          float64(7),
			"fencing_epoch":  float64(1),
		},
	}
	got := normalizeInboundMessage(in)
	if got["type"] != "encrypted_push" {
		t.Fatalf("type = %v", got["type"])
	}
	if got["message_type"] != "ack" {
		t.Errorf("message_type = %v", got["message_type"])
	}
	if got["encrypted_body"] != "ZGF0YQ==" {
		t.Errorf("body = %v", got["encrypted_body"])
	}
}

func TestNormalize_EncryptedBatchPreservesResponseAADFields(t *testing.T) {
	in := Message{
		"op":   "order.mass_quote",
		"code": float64(0),
		"data": map[string]any{
			"message_type":   "mass_quote_ack",
			"ciphertext":     "ZGF0YQ==",
			"nonce":          float64(7),
			"fencing_epoch":  float64(1),
			"correlation_id": "1339673755198158349044581307228491536",
			"session_seq":    float64(42),
		},
	}
	got := normalizeInboundMessage(in)
	if got["type"] != "encrypted_push" || got["message_type"] != "mass_quote_ack" {
		t.Fatalf("batch response = %+v", got)
	}
	if got["correlation_id"] != "1339673755198158349044581307228491536" {
		t.Errorf("correlation_id = %v", got["correlation_id"])
	}
	if got["session_seq"] != float64(42) {
		t.Errorf("session_seq = %v", got["session_seq"])
	}
}

func TestNormalize_BatchErrorResolvesCommand(t *testing.T) {
	got := normalizeInboundMessage(Message{
		"op":      "order.batch_cancel",
		"code":    float64(503),
		"message": "system temporarily unavailable, retry",
	})
	if got["type"] != "error" || got["message"] != "system temporarily unavailable, retry" {
		t.Fatalf("batch error = %+v", got)
	}
}

func TestNormalize_PassThroughLegacy(t *testing.T) {
	in := Message{"type": "auth_result", "success": true}
	got := normalizeInboundMessage(in)
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("legacy frame mutated: %+v", got)
	}
}

func TestEdgeURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"wss://api.example.com", "wss://api.example.com/ws/v1"},
		{"wss://api.example.com/", "wss://api.example.com/ws/v1"},
		{"wss://api.example.com/ws/v1", "wss://api.example.com/ws/v1"},
		{"http://localhost:8080", "http://localhost:8080/ws/v1"},
	}
	for _, c := range cases {
		if got := EdgeURL(c.in); got != c.want {
			t.Errorf("EdgeURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
