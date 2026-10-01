// Package identity converts public identity strings to their protobuf bytes.
//
// Current encrypted trading uses a 32-byte Solana account. UUID helpers remain
// for legacy metadata returned by older edges.
package identity

import (
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/google/uuid"
)

const (
	// UserUUIDLen is the legacy RFC 4122 wire size.
	UserUUIDLen = 16
	// AccountLen is the current Solana account wire size.
	AccountLen = 32
)

// ErrInvalidUserUUIDLen is returned when a wire-decoded UUID is the wrong length.
var ErrInvalidUserUUIDLen = errors.New("user_uuid must be 16 bytes")

// ErrInvalidAccountLen is returned when an account does not decode to 32 bytes.
var ErrInvalidAccountLen = errors.New("account must be a 32-byte Solana public key")

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

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

// AccountToBytes converts a canonical Solana base58 public key to 32 bytes.
// The edge's optional "acct_" display prefix is accepted for convenience.
func AccountToBytes(s string) ([]byte, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "acct_"))
	if s == "" {
		return nil, ErrInvalidAccountLen
	}
	n := new(big.Int)
	radix := big.NewInt(58)
	for _, r := range s {
		digit := strings.IndexRune(base58Alphabet, r)
		if digit < 0 {
			return nil, fmt.Errorf("invalid base58 account character %q", r)
		}
		n.Mul(n, radix)
		n.Add(n, big.NewInt(int64(digit)))
	}
	raw := n.Bytes()
	leadingZeroes := 0
	for leadingZeroes < len(s) && s[leadingZeroes] == '1' {
		leadingZeroes++
	}
	if leadingZeroes+len(raw) > AccountLen {
		return nil, ErrInvalidAccountLen
	}
	out := make([]byte, AccountLen)
	copy(out[AccountLen-len(raw):], raw)
	if leadingZeroes > AccountLen-len(raw) {
		return nil, ErrInvalidAccountLen
	}
	return out, nil
}

// AccountFromBytes converts a 32-byte Solana public key to canonical base58.
func AccountFromBytes(b []byte) (string, error) {
	if len(b) != AccountLen {
		return "", ErrInvalidAccountLen
	}
	leadingZeroes := 0
	for leadingZeroes < len(b) && b[leadingZeroes] == 0 {
		leadingZeroes++
	}
	n := new(big.Int).SetBytes(b)
	radix := big.NewInt(58)
	zero := new(big.Int)
	var encoded []byte
	for n.Cmp(zero) > 0 {
		q, rem := new(big.Int), new(big.Int)
		q.QuoRem(n, radix, rem)
		encoded = append(encoded, base58Alphabet[rem.Int64()])
		n = q
	}
	out := make([]byte, 0, leadingZeroes+len(encoded))
	for i := 0; i < leadingZeroes; i++ {
		out = append(out, '1')
	}
	for i := len(encoded) - 1; i >= 0; i-- {
		out = append(out, encoded[i])
	}
	return string(out), nil
}
