package hpke

import (
	"testing"

	"github.com/google/uuid"
)

func TestSetupOpenRoundtripAndSeal(t *testing.T) {
	seq, err := GenerateStaticKeyPair()
	if err != nil {
		t.Fatalf("GenerateStaticKeyPair: %v", err)
	}
	user := uuid.MustParse("00000000-0000-0000-0000-000000000007")
	info := InfoForConn(user[:], 42)
	enc, client, err := SetupSession(seq.PublicKey(), info)
	if err != nil {
		t.Fatalf("SetupSession: %v", err)
	}
	if len(enc) != EncappedKeyLen {
		t.Fatalf("encapped len = %d, want %d", len(enc), EncappedKeyLen)
	}
	server, err := OpenSession(seq, enc, info)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}

	nonce := NonceFromU64(1)
	ct, err := client.SealC2S(nonce[:], []byte("aad"), []byte("place"))
	if err != nil {
		t.Fatalf("SealC2S: %v", err)
	}
	pt, err := server.OpenC2S(nonce[:], []byte("aad"), ct)
	if err != nil {
		t.Fatalf("OpenC2S: %v", err)
	}
	if string(pt) != "place" {
		t.Fatalf("roundtrip c2s: got %q", pt)
	}

	nonce2 := NonceFromU64(2)
	ct2, err := server.SealS2C(nonce2[:], []byte("rh"), []byte("ack"))
	if err != nil {
		t.Fatalf("SealS2C: %v", err)
	}
	pt2, err := client.OpenS2C(nonce2[:], []byte("rh"), ct2)
	if err != nil {
		t.Fatalf("OpenS2C: %v", err)
	}
	if string(pt2) != "ack" {
		t.Fatalf("roundtrip s2c: got %q", pt2)
	}
}
