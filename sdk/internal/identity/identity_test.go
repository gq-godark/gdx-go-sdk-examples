package identity

import (
	"testing"
)

func TestRoundTrip(t *testing.T) {
	want := "1d3e7e87-8c0c-4f4f-8a5a-1a2b3c4d5e6f"
	b, err := ToBytes(want)
	if err != nil {
		t.Fatalf("ToBytes: %v", err)
	}
	if len(b) != UserUUIDLen {
		t.Fatalf("wire len = %d, want %d", len(b), UserUUIDLen)
	}
	got, err := FromBytes(b)
	if err != nil {
		t.Fatalf("FromBytes: %v", err)
	}
	if got != want {
		t.Fatalf("round-trip: got %q, want %q", got, want)
	}
}

func TestToBytesInvalid(t *testing.T) {
	if _, err := ToBytes("not-a-uuid"); err == nil {
		t.Fatal("expected error for invalid UUID")
	}
}

func TestFromBytesWrongLen(t *testing.T) {
	if _, err := FromBytes([]byte{1, 2, 3}); err == nil {
		t.Fatal("expected ErrInvalidUserUUIDLen for short input")
	}
}
