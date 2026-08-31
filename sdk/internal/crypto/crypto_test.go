package crypto

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestECDHRoundTrip(t *testing.T) {
	alicePriv, alicePub, err := GenerateEphemeralKeypair()
	if err != nil {
		t.Fatalf("alice keypair: %v", err)
	}
	bobPriv, bobPub, err := GenerateEphemeralKeypair()
	if err != nil {
		t.Fatalf("bob keypair: %v", err)
	}

	aliceKey, err := DeriveSessionKey(alicePriv, alicePub, bobPub)
	if err != nil {
		t.Fatalf("alice derive: %v", err)
	}
	bobKey, err := DeriveSessionKey(bobPriv, bobPub, alicePub)
	if err != nil {
		t.Fatalf("bob derive: %v", err)
	}

	if !bytes.Equal(aliceKey, bobKey) {
		t.Fatalf("derived keys differ:\n  alice = %x\n  bob   = %x", aliceKey, bobKey)
	}
	if len(aliceKey) != SessionKeyLen {
		t.Fatalf("derived key len = %d, want %d", len(aliceKey), SessionKeyLen)
	}
}

func TestDeriveSessionKeyRejectsWrongRemoteLen(t *testing.T) {
	priv, pub, err := GenerateEphemeralKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	if _, err := DeriveSessionKey(priv, pub, []byte{1, 2, 3}); err == nil {
		t.Fatal("expected error for short remote key")
	}
}

func TestBuildGCMNonceLayout(t *testing.T) {
	got := BuildGCMNonce(0x0102030405060708, 0x090A0B0C)
	want := []byte{
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0A, 0x0B, 0x0C,
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("nonce mismatch:\n  got  %x\n  want %x", got, want)
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, SessionKeyLen)
	aad := []byte("aad-bytes")
	pt := []byte("hello sequencer")

	ct, err := Encrypt(key, 0xDEADBEEFCAFEBABE, 7, aad, pt)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if len(ct) != len(pt)+GCMTagLen {
		t.Fatalf("ciphertext len = %d, want plaintext(%d) + tag(%d)", len(ct), len(pt), GCMTagLen)
	}

	got, err := Decrypt(key, 0xDEADBEEFCAFEBABE, 7, aad, ct)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, pt) {
		t.Fatalf("round-trip plaintext mismatch: got %q want %q", got, pt)
	}
}

func TestDecryptRejectsTamperedAAD(t *testing.T) {
	key := bytes.Repeat([]byte{0xAB}, SessionKeyLen)
	ct, err := Encrypt(key, 1, 1, []byte("good-aad"), []byte("payload"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := Decrypt(key, 1, 1, []byte("bad-aad"), ct); err == nil {
		t.Fatal("expected AEAD failure on AAD mismatch")
	}
}

func TestDecryptRejectsTamperedCiphertext(t *testing.T) {
	key := bytes.Repeat([]byte{0xAB}, SessionKeyLen)
	ct, err := Encrypt(key, 1, 1, nil, []byte("payload"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	ct[0] ^= 0x01
	if _, err := Decrypt(key, 1, 1, nil, ct); err == nil {
		t.Fatal("expected AEAD failure on tampered ciphertext")
	}
}

func TestNonceTrackerAdvance(t *testing.T) {
	var n NonceTracker
	for i := uint32(0); i < 5; i++ {
		v, err := n.Advance()
		if err != nil {
			t.Fatalf("Advance(%d): %v", i, err)
		}
		if v != i {
			t.Fatalf("Advance returned %d, want %d", v, i)
		}
	}
	if peek := n.PeekNext(); peek != 5 {
		t.Fatalf("PeekNext = %d, want 5", peek)
	}
}

func TestNonceTrackerReset(t *testing.T) {
	var n NonceTracker
	if _, err := n.Advance(); err != nil {
		t.Fatal(err)
	}
	n.CommitRecv(42)
	n.Reset()
	if peek := n.PeekNext(); peek != 0 {
		t.Fatalf("PeekNext after Reset = %d, want 0", peek)
	}
}

func TestNonceTrackerExhaustion(t *testing.T) {
	// Cheap-but-correct: poke the internal counter to the boundary so we
	// don't have to call Advance() 2^32 times. The internal field name is
	// stable within the package (we own it).
	var n NonceTracker
	for i := 0; i < 1; i++ {
		_, _ = n.Advance()
	}
	// Force the tracker to the max value via the only public mutation API:
	// drain manually using internal access (same package).
	n.sendCtr = MaxNonceCounter
	_, err := n.Advance()
	if !errors.Is(err, ErrNonceCounterExhausted) {
		t.Fatalf("expected ErrNonceCounterExhausted, got %v", err)
	}
}

// Defensive check: encrypting twice with the same (session, counter) must
// produce identical ciphertext (deterministic given the AEAD inputs). This
// catches accidental randomness leaks in BuildGCMNonce.
func TestEncryptIsDeterministicGivenNonceInputs(t *testing.T) {
	key := bytes.Repeat([]byte{0x11}, SessionKeyLen)
	aad := []byte{0x01, 0x02}
	pt := []byte("p")
	ct1, _ := Encrypt(key, 99, 100, aad, pt)
	ct2, _ := Encrypt(key, 99, 100, aad, pt)
	if !bytes.Equal(ct1, ct2) {
		t.Fatalf("encrypt is non-deterministic: %x != %x", ct1, ct2)
	}
}

// Smoke check: BuildGCMNonce(_, 0xFFFFFFFF) still yields a 12-byte nonce.
func TestBuildGCMNonceMaxCounter(t *testing.T) {
	n := BuildGCMNonce(1, MaxNonceCounter)
	if len(n) != 12 {
		t.Fatalf("nonce len = %d, want 12", len(n))
	}
	if got := binary.BigEndian.Uint32(n[8:]); got != MaxNonceCounter {
		t.Fatalf("counter slice = %#x, want %#x", got, MaxNonceCounter)
	}
}
