// Package session owns the ECDH session lifecycle with the gdx-edge sequencer.
//
// Wire flow:
//
//  1. Client: GenerateKeypair() -> base64 public key sent to `session.setup`.
//  2. Server: returns its public key + session_id.
//  3. Client: Establish(serverPub, sessionID) derives the AES key via HKDF.
//  4. From here, EncryptOrder / DecryptPush use the session key + a 96-bit
//     nonce built from session_id || nonce_counter.
//
// Reset() must be called between reconnects so the nonce counter and stale
// keypair don't leak across sessions.
package session

import (
	"crypto/ecdh"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"

	gdxcrypto "github.com/gq-godark/gdx-go-sdk/internal/crypto"
)

// ErrNotEstablished is returned by Encrypt / Decrypt before Establish().
var ErrNotEstablished = errors.New("session not established")

// ErrMissingKeypair is returned by Establish() before GenerateKeypair().
var ErrMissingKeypair = errors.New("must call GenerateKeypair() before Establish()")

// CryptoSession holds a single ECDH session with the sequencer.
//
// All methods are safe to call from multiple goroutines.
type CryptoSession struct {
	mu          sync.Mutex
	privateKey  *ecdh.PrivateKey
	localPublic []byte
	sessionKey  []byte
	sessionID   uint64
	hasSession  bool
	established bool
	nonce       gdxcrypto.NonceTracker
}

// IsEstablished reports whether the ECDH handshake has completed.
func (s *CryptoSession) IsEstablished() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.established
}

// SessionID returns the server-assigned session id (only valid once
// IsEstablished returns true).
func (s *CryptoSession) SessionID() (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasSession {
		return 0, false
	}
	return s.sessionID, true
}

// NextNonce returns the value the next call to EncryptOrder would consume,
// without advancing the counter. Useful for AAD construction.
func (s *CryptoSession) NextNonce() uint32 {
	return s.nonce.PeekNext()
}

// GenerateKeypair creates a fresh X25519 keypair for this session and returns
// the base64-encoded public key string the client sends in `session.setup`.
func (s *CryptoSession) GenerateKeypair() (string, error) {
	priv, pub, err := gdxcrypto.GenerateEphemeralKeypair()
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.privateKey = priv
	s.localPublic = pub
	s.sessionKey = nil
	s.hasSession = false
	s.established = false
	s.nonce.Reset()
	return base64.StdEncoding.EncodeToString(pub), nil
}

// Establish completes the ECDH handshake. `sequencerPubkeyB64` is the
// base64-encoded server X25519 public key from the `session.setup` response;
// `sessionID` is the server-assigned session id from the same response.
//
// On success the private key is dropped so a stolen *CryptoSession can no
// longer revive past sessions.
func (s *CryptoSession) Establish(sequencerPubkeyB64 string, sessionID uint64) error {
	remotePub, err := base64.StdEncoding.DecodeString(sequencerPubkeyB64)
	if err != nil {
		return fmt.Errorf("decode sequencer public key: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.privateKey == nil || s.localPublic == nil {
		return ErrMissingKeypair
	}

	key, err := gdxcrypto.DeriveSessionKey(s.privateKey, s.localPublic, remotePub)
	if err != nil {
		return err
	}

	s.sessionKey = key
	s.sessionID = sessionID
	s.hasSession = true
	s.established = true
	s.privateKey = nil
	s.nonce.Reset()
	return nil
}

// EncryptOrder seals an order payload. Returns (nonce_counter, ciphertext).
// `aad` is typically the protobuf-encoded `OrderHeader`.
func (s *CryptoSession) EncryptOrder(aad, plaintext []byte) (uint32, []byte, error) {
	s.mu.Lock()
	if !s.established {
		s.mu.Unlock()
		return 0, nil, ErrNotEstablished
	}
	key := s.sessionKey
	sid := s.sessionID
	s.mu.Unlock()

	counter, err := s.nonce.Advance()
	if err != nil {
		return 0, nil, err
	}
	ct, err := gdxcrypto.Encrypt(key, sid, counter, aad, plaintext)
	if err != nil {
		return 0, nil, err
	}
	return counter, ct, nil
}

// DecryptPush opens an encrypted push from the sequencer. `aad` is typically
// the protobuf-encoded `ResponseHeader`.
func (s *CryptoSession) DecryptPush(nonceCounter uint32, aad, ciphertext []byte) ([]byte, error) {
	s.mu.Lock()
	if !s.established {
		s.mu.Unlock()
		return nil, ErrNotEstablished
	}
	key := s.sessionKey
	sid := s.sessionID
	s.mu.Unlock()

	pt, err := gdxcrypto.Decrypt(key, sid, nonceCounter, aad, ciphertext)
	if err != nil {
		return nil, err
	}
	s.nonce.CommitRecv(nonceCounter)
	return pt, nil
}

// Reset clears all session state. Call between reconnects or on rekey.
func (s *CryptoSession) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.privateKey = nil
	s.localPublic = nil
	s.sessionKey = nil
	s.sessionID = 0
	s.hasSession = false
	s.established = false
	s.nonce.Reset()
}
