package godark

import (
	"encoding/base64"
	"testing"
)

func jwtWithSub(sub string) string {
	payload := `{"sub":"` + sub + `","scope":"trade"}`
	body := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return "eyJhbGciOiJIUzI1NiJ9." + body + ".sig"
}

func TestUserUUIDFromAccessTokenJWT(t *testing.T) {
	sub := "3b026a17-fd27-4a7d-bb93-048e30e4900e"
	got, ok := userUUIDFromAccessTokenJWT(jwtWithSub(sub))
	if !ok || got != sub {
		t.Fatalf("got (%q, %v), want (%q, true)", got, ok, sub)
	}
}

func TestUserUUIDFromAccessTokenJWT_RejectsMalformed(t *testing.T) {
	if _, ok := userUUIDFromAccessTokenJWT("not-a-jwt"); ok {
		t.Fatal("expected false for malformed token")
	}
	if _, ok := userUUIDFromAccessTokenJWT(jwtWithSub("not-a-uuid")); ok {
		t.Fatal("expected false for non-uuid sub")
	}
}
