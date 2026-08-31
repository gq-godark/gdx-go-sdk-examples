package session

import (
	"errors"
	"testing"

	"github.com/gq-godark/gdx-go-sdk/internal/hpke"
)

func TestEncryptOrderBeforeEstablish(t *testing.T) {
	var s CryptoSession
	if _, _, err := s.EncryptOrder(nil, []byte("x")); !errors.Is(err, ErrNotEstablished) {
		t.Fatalf("expected ErrNotEstablished, got %v", err)
	}
}

func TestDecryptPushBeforeEstablish(t *testing.T) {
	var s CryptoSession
	if _, err := s.DecryptPush(0, nil, []byte("x")); !errors.Is(err, ErrNotEstablished) {
		t.Fatalf("expected ErrNotEstablished, got %v", err)
	}
}

func TestSetupNotEstablishedUntilPeerConfirm(t *testing.T) {
	seq, err := hpke.GenerateStaticKeyPair()
	if err != nil {
		t.Fatalf("GenerateStaticKeyPair: %v", err)
	}
	user := bytes16(0x42)
	var s CryptoSession
	if _, err := s.Setup(seq.PublicKey(), user, 7); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if s.IsEstablished() {
		t.Fatal("session must not be established before peer confirm")
	}
	if err := s.Establish(); err != nil {
		t.Fatalf("establish: %v", err)
	}
	if !s.IsEstablished() {
		t.Fatal("session must be established after confirm")
	}
}

func TestAbortSetupClearsPendingState(t *testing.T) {
	seq, err := hpke.GenerateStaticKeyPair()
	if err != nil {
		t.Fatalf("GenerateStaticKeyPair: %v", err)
	}
	user := bytes16(0x42)
	var s CryptoSession
	if _, err := s.Setup(seq.PublicKey(), user, 7); err != nil {
		t.Fatalf("setup: %v", err)
	}
	s.AbortSetup()
	if s.IsEstablished() {
		t.Fatal("abort must leave session unestablished")
	}
	if _, _, err := s.EncryptOrder(nil, []byte("x")); !errors.Is(err, ErrNotEstablished) {
		t.Fatalf("expected ErrNotEstablished after abort, got %v", err)
	}
}

func TestReplayPushRejected(t *testing.T) {
	seq, err := hpke.GenerateStaticKeyPair()
	if err != nil {
		t.Fatalf("GenerateStaticKeyPair: %v", err)
	}
	user := bytes16(0x42)
	info := hpke.InfoForConn(user, 7)
	var client CryptoSession
	enc, err := client.Setup(seq.PublicKey(), user, 7)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := client.Establish(); err != nil {
		t.Fatalf("establish: %v", err)
	}
	server, err := hpke.OpenSession(seq, enc, info)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	aad := []byte("resp-header")
	nonce := hpke.NonceFromU64(1)
	ct, err := server.SealS2C(nonce[:], aad, []byte("payload"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := client.DecryptPush(1, aad, ct); err != nil {
		t.Fatalf("first decrypt: %v", err)
	}
	if _, err := client.DecryptPush(1, aad, ct); err == nil {
		t.Fatal("expected replay rejection")
	}
}

func bytes16(v byte) []byte {
	b := make([]byte, 16)
	for i := range b {
		b[i] = v
	}
	return b
}
