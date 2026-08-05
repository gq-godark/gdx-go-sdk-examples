// Package bound authenticates cleartext routing headers inside Noise payloads.
package bound

import (
	"bytes"
	"crypto/sha256"
	"errors"

	"github.com/gq-godark/gdx-go-sdk/internal/noise"
)

const HashLen = sha256.Size

func Encrypt(transport *noise.Transport, aad, plaintext []byte) ([]byte, error) {
	hash := sha256.Sum256(aad)
	framed := make([]byte, 0, HashLen+len(plaintext))
	framed = append(framed, hash[:]...)
	framed = append(framed, plaintext...)
	return transport.Encrypt(framed)
}

func Decrypt(transport *noise.Transport, aad, ciphertext []byte) ([]byte, error) {
	framed, err := transport.Decrypt(ciphertext)
	if err != nil {
		return nil, err
	}
	return verify(framed, aad)
}

// DecryptAt opens a bound frame at the server-stamped Noise nonce, tolerating
// relay-dropped frames that would otherwise desync a strictly-sequential
// receive counter.
func DecryptAt(transport *noise.Transport, stampedNonce uint64, aad, ciphertext []byte) ([]byte, error) {
	framed, err := transport.DecryptAt(stampedNonce, ciphertext)
	if err != nil {
		return nil, err
	}
	return verify(framed, aad)
}

func verify(framed, aad []byte) ([]byte, error) {
	if len(framed) < HashLen {
		return nil, errors.New("bound ciphertext too short")
	}
	expected := sha256.Sum256(aad)
	if !bytes.Equal(framed[:HashLen], expected[:]) {
		return nil, errors.New("bound AAD mismatch")
	}
	return framed[HashLen:], nil
}
