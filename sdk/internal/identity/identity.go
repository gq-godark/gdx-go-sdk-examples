// Package identity converts between canonical UUID strings (8-4-4-4-12 hex)
// and the 16-byte big-endian RFC 4122 wire encoding the sequencer expects.
//
// The wire format carries `bytes user_uuid` (RFC 4122, big-endian, 16 B). The
// public SDK surface exposes the canonical hex form because that's what users
// see in dashboards; conversion happens at the wire boundary.
package identity

import (
	"errors"

	"github.com/google/uuid"
)

const (
	// UserUUIDLen is the wire size of a `bytes user_uuid` field (RFC 4122).
	UserUUIDLen = 16
)

// ErrInvalidUserUUIDLen is returned when a wire-decoded UUID is the wrong length.
var ErrInvalidUserUUIDLen = errors.New("user_uuid must be 16 bytes")

// ToBytes converts an 8-4-4-4-12 hex UUID string to its 16-byte wire form.
func ToBytes(s string) ([]byte, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return nil, err
	}
	b := make([]byte, UserUUIDLen)
	copy(b, u[:])
	return b, nil
}

// FromBytes converts a 16-byte wire-form UUID to its canonical hex string.
func FromBytes(b []byte) (string, error) {
	if len(b) != UserUUIDLen {
		return "", ErrInvalidUserUUIDLen
	}
	var u uuid.UUID
	copy(u[:], b)
	return u.String(), nil
}
