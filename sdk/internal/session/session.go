// Package session owns the post-handshake HPKE sealed session lifecycle.
package session

import (
	"errors"
	"sync"

	"github.com/gq-godark/gdx-go-sdk/internal/hpke"
)

// ErrNotEstablished is returned by Encrypt / Decrypt before Setup completes.
var ErrNotEstablished = errors.New("session not established")

// GCMTagLen is the AES-GCM authentication tag length on the wire.
const GCMTagLen = hpke.TagLen

// CryptoSession holds one HPKE sealed transport session.
//
// All methods are safe to call from multiple goroutines.
type CryptoSession struct {
	mu          sync.Mutex
	sealed      *hpke.SealedSession
	connID      uint64
	established bool
	sendCounter uint64
}

// IsEstablished reports whether the HPKE session has completed setup.
func (s *CryptoSession) IsEstablished() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.established
}

// SessionID returns the server-assigned connection id (only valid once
// IsEstablished returns true).
func (s *CryptoSession) SessionID() (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.established {
		return 0, false
	}
	return s.connID, true
}

// NextNonce returns the value the next EncryptOrder would consume, without
// advancing the counter.
func (s *CryptoSession) NextNonce() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.established {
		return 1
	}
	return s.sendCounter
}

// BodyLengthForPlaintext returns the wire body_length for a plaintext payload.
func BodyLengthForPlaintext(plaintextLen int) (uint32, error) {
	n := plaintextLen + hpke.TagLen
	if n < 0 || n > int(^uint32(0)) {
		return 0, errors.New("encrypted body too large")
	}
	return uint32(n), nil
}

// Setup performs HPKE Base setup against the pinned sequencer public key.
// Returns the encapped key bytes to send in HpkeSetup.
func (s *CryptoSession) Setup(recipientPublic, userUUID []byte, connID uint64) ([]byte, error) {
	if connID == 0 {
		return nil, errors.New("HPKE conn_id must be non-zero")
	}
	if len(userUUID) != 16 {
		return nil, errors.New("user UUID must be 16 bytes")
	}
	if len(recipientPublic) != hpke.KeyLen {
		return nil, errors.New("HPKE public key must be 32 bytes")
	}
	info := hpke.InfoForConn(userUUID, connID)
	enc, sealed, err := hpke.SetupSession(recipientPublic, info)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.sealed = sealed
	s.connID = connID
	s.established = true
	s.sendCounter = 1
	s.mu.Unlock()
	return enc, nil
}

// GenerateKeypair is retained so older REST call sites fail explicitly.
func (s *CryptoSession) GenerateKeypair() (string, error) {
	return "", errors.New("ECDH session.setup is not supported; encrypted REST trading requires HPKE over WebSocket")
}

// EncryptOrder seals an order payload. Returns (nonce_counter, ciphertext).
// aad is typically the protobuf-encoded OrderHeader.
func (s *CryptoSession) EncryptOrder(aad, plaintext []byte) (uint64, []byte, error) {
	s.mu.Lock()
	if !s.established || s.sealed == nil {
		s.mu.Unlock()
		return 0, nil, ErrNotEstablished
	}
	sealed := s.sealed
	nonce := s.sendCounter
	if nonce == ^uint64(0) {
		s.mu.Unlock()
		return 0, nil, errors.New("send nonce counter overflow")
	}
	s.sendCounter++
	s.mu.Unlock()

	nonceBytes := hpke.NonceFromU64(nonce)
	ct, err := sealed.SealC2S(nonceBytes[:], aad, plaintext)
	if err != nil {
		return 0, nil, err
	}
	return nonce, ct, nil
}

// DecryptPush opens an encrypted push from the sequencer. aad is typically
// the protobuf-encoded ResponseHeader. nonce is the server-stamped counter.
func (s *CryptoSession) DecryptPush(nonce uint64, aad, ciphertext []byte) ([]byte, error) {
	s.mu.Lock()
	if !s.established || s.sealed == nil {
		s.mu.Unlock()
		return nil, ErrNotEstablished
	}
	sealed := s.sealed
	s.mu.Unlock()

	nonceBytes := hpke.NonceFromU64(nonce)
	return sealed.OpenS2C(nonceBytes[:], aad, ciphertext)
}

// Reset clears all session state. Call between reconnects or on rekey.
func (s *CryptoSession) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sealed = nil
	s.connID = 0
	s.established = false
	s.sendCounter = 1
}
