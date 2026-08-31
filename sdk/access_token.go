package godark

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
)

// userUUIDFromAccessTokenJWT parses the internal user UUID from a compact access
// JWT's sub claim. Signature is not verified — callers should only use tokens
// returned by the edge auth/token response.
func userUUIDFromAccessTokenJWT(token string) (string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", false
	}
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return "", false
	}
	if _, err := uuid.Parse(sub); err != nil {
		return "", false
	}
	return sub, true
}
