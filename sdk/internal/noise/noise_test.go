package noise_test

import (
	"testing"

	"github.com/gq-godark/gdx-go-sdk/internal/noise"
)

func TestNoiseHandshakeRoundTrip(t *testing.T) {
	user := []byte{
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x2a,
	}
	prologue, err := noise.PrologueForUser(user)
	if err != nil {
		t.Fatal(err)
	}
	serverStatic, serverStaticPub, err := noise.GenerateStaticKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	responder, err := noise.NewHandshakeResponder(serverStatic, prologue)
	if err != nil {
		t.Fatal(err)
	}
	initiator, err := noise.NewHandshakeInitiator(serverStaticPub, prologue)
	if err != nil {
		t.Fatal(err)
	}

	msg1, err := initiator.WriteMessage(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := responder.ReadMessage(msg1); err != nil {
		t.Fatal(err)
	}
	msg2, err := responder.WriteMessage(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := initiator.ReadMessage(msg2); err != nil {
		t.Fatal(err)
	}
	msg3, err := initiator.WriteMessage(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := responder.ReadMessage(msg3); err != nil {
		t.Fatal(err)
	}
	clientTransport, err := initiator.IntoTransport()
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, err := responder.IntoTransport()
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("hello-noise")
	ct, err := clientTransport.Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	out, err := serverTransport.Decrypt(ct)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(plain) {
		t.Fatalf("decrypt: %q", out)
	}
}

// handshakePair completes an XK handshake and returns (client, server) transports.
func handshakePair(t *testing.T) (*noise.Transport, *noise.Transport) {
	t.Helper()
	user := []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x2a}
	prologue, err := noise.PrologueForUser(user)
	if err != nil {
		t.Fatal(err)
	}
	serverStatic, serverStaticPub, err := noise.GenerateStaticKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	responder, err := noise.NewHandshakeResponder(serverStatic, prologue)
	if err != nil {
		t.Fatal(err)
	}
	initiator, err := noise.NewHandshakeInitiator(serverStaticPub, prologue)
	if err != nil {
		t.Fatal(err)
	}
	msg1, _ := initiator.WriteMessage(nil)
	if _, err := responder.ReadMessage(msg1); err != nil {
		t.Fatal(err)
	}
	msg2, _ := responder.WriteMessage(nil)
	if _, err := initiator.ReadMessage(msg2); err != nil {
		t.Fatal(err)
	}
	msg3, _ := initiator.WriteMessage(nil)
	if _, err := responder.ReadMessage(msg3); err != nil {
		t.Fatal(err)
	}
	// Client sends, server receives (initiator's send == responder's recv).
	client, err := initiator.IntoTransport()
	if err != nil {
		t.Fatal(err)
	}
	server, err := responder.IntoTransport()
	if err != nil {
		t.Fatal(err)
	}
	return client, server
}

// TestDecryptAtToleratesRelayGap proves the nonce-realign fix: a frame the relay
// "drops" (advancing the sender counter) must still decrypt at its stamped nonce.
func TestDecryptAtToleratesRelayGap(t *testing.T) {
	sender, receiver := handshakePair(t)

	ct0, _ := sender.Encrypt([]byte("frame-0")) // send nonce 0
	_, _ = sender.Encrypt([]byte("frame-1"))    // send nonce 1 (dropped by relay)
	ct2, _ := sender.Encrypt([]byte("frame-2")) // send nonce 2

	out0, err := receiver.DecryptAt(0, ct0)
	if err != nil || string(out0) != "frame-0" {
		t.Fatalf("frame 0: out=%q err=%v", out0, err)
	}
	if receiver.RecvNonce() != 1 {
		t.Fatalf("recv nonce after frame 0 = %d, want 1", receiver.RecvNonce())
	}

	// Skip stamped nonce 1 entirely; frame 2 must still decrypt.
	out2, err := receiver.DecryptAt(2, ct2)
	if err != nil || string(out2) != "frame-2" {
		t.Fatalf("frame 2 after gap: out=%q err=%v", out2, err)
	}
	if receiver.RecvNonce() != 3 {
		t.Fatalf("recv nonce after frame 2 = %d, want 3", receiver.RecvNonce())
	}
}

// TestSequentialDecryptBreaksOnGap documents why the fix is needed: the plain
// sequential Decrypt cannot open a frame sealed at a skipped counter.
func TestSequentialDecryptBreaksOnGap(t *testing.T) {
	sender, receiver := handshakePair(t)
	_, _ = sender.Encrypt([]byte("frame-0")) // nonce 0, never delivered
	ct1, _ := sender.Encrypt([]byte("frame-1"))

	// receiver.recv.nonce is 0, but ct1 was sealed at 1 -> GCM tag mismatch.
	if _, err := receiver.Decrypt(ct1); err == nil {
		t.Fatal("expected sequential decrypt to fail across a gap")
	}
}
