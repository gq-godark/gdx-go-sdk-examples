// Package crypto implements the wire-level crypto primitives the gdx-edge
// encrypted trading protocol relies on:
//
//   - X25519 ECDH key exchange (ephemeral on the client side)
//   - HKDF-SHA256 derivation of a 32-byte AES session key
//   - AES-256-GCM encryption with a 96-bit nonce of
//     `session_id(u64 BE) || nonce_counter(u32 BE)`
//
// This is a byte-for-byte port of the python and rust reference
// implementations - any change here is a wire-protocol change.
package crypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"sync"

	"golang.org/x/crypto/hkdf"
)

func newSHA256() hash.Hash { return sha256.New() }

// HKDFInfo is the HKDF `info` parameter shared with the sequencer. Must match
// the python / rust SDKs byte-for-byte.
var HKDFInfo = []byte("gdx-e2e-session-key-v1")

// GCMTagLen is the AES-GCM authentication tag length the wire format expects.
const GCMTagLen = 16

// SessionKeyLen is the size of the AES-256 session key derived by HKDF.
const SessionKeyLen = 32

// PublicKeyLen is the wire size of an X25519 public key.
const PublicKeyLen = 32

// MaxNonceCounter is the maximum value a u32 nonce counter can take before
// the session must be rotated to avoid nonce reuse under the same session key.
const MaxNonceCounter = uint32(0xFFFFFFFF)

// ErrWeakPublicKey is returned when the ECDH shared secret is all zeros,
// indicating the peer sent a degenerate (small-order) point.
var ErrWeakPublicKey = errors.New("ECDH shared secret is all zeros")

// ErrNonceCounterExhausted is returned by NonceTracker.Advance when the local
// send counter has wrapped past the u32 maximum.
var ErrNonceCounterExhausted = errors.New("send nonce counter exceeded u32 max")

// GenerateEphemeralKeypair generates a fresh X25519 keypair for one session.
// Returns the private key (kept on the client) and the 32-byte public key
// bytes (sent to the sequencer in `session.setup`).
func GenerateEphemeralKeypair() (*ecdh.PrivateKey, []byte, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate X25519 keypair: %w", err)
	}
	return priv, priv.PublicKey().Bytes(), nil
}

// DeriveSessionKey performs ECDH(local_priv, remote_pub) and runs HKDF-SHA256
// to derive a 32-byte AES key.
//
// HKDF salt is the byte-lexicographic min(local_pub, remote_pub) concatenated
// with the max - this matches the wire encoding used by every other SDK.
func DeriveSessionKey(localPriv *ecdh.PrivateKey, localPub, remotePub []byte) ([]byte, error) {
	if len(remotePub) != PublicKeyLen {
		return nil, fmt.Errorf("remote public key must be %d bytes, got %d", PublicKeyLen, len(remotePub))
	}

	remoteKey, err := ecdh.X25519().NewPublicKey(remotePub)
	if err != nil {
		return nil, fmt.Errorf("parse remote X25519 public key: %w", err)
	}

	shared, err := localPriv.ECDH(remoteKey)
	if err != nil {
		return nil, fmt.Errorf("X25519 ECDH: %w", err)
	}
	if bytes.Equal(shared, make([]byte, PublicKeyLen)) {
		return nil, ErrWeakPublicKey
	}

	var salt []byte
	if bytes.Compare(localPub, remotePub) <= 0 {
		salt = append(append(make([]byte, 0, PublicKeyLen*2), localPub...), remotePub...)
	} else {
		salt = append(append(make([]byte, 0, PublicKeyLen*2), remotePub...), localPub...)
	}

	r := hkdf.New(newSHA256, shared, salt, HKDFInfo)
	out := make([]byte, SessionKeyLen)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, fmt.Errorf("HKDF expand: %w", err)
	}
	return out, nil
}

// BuildGCMNonce assembles the 12-byte GCM nonce as
// `session_id(u64 BE) || nonce_counter(u32 BE)`.
func BuildGCMNonce(sessionID uint64, nonceCounter uint32) []byte {
	nonce := make([]byte, 12)
	binary.BigEndian.PutUint64(nonce[0:8], sessionID)
	binary.BigEndian.PutUint32(nonce[8:12], nonceCounter)
	return nonce
}

// Encrypt AES-256-GCM-encrypts `plaintext` under `key` with `aad` as the
// additional authenticated data. Returns ciphertext || 16-byte tag.
func Encrypt(key []byte, sessionID uint64, nonceCounter uint32, aad, plaintext []byte) ([]byte, error) {
	gcm, err := newAESGCM(key)
	if err != nil {
		return nil, err
	}
	return gcm.Seal(nil, BuildGCMNonce(sessionID, nonceCounter), plaintext, aad), nil
}

// Decrypt AES-256-GCM-decrypts `ciphertext` (which must include the 16-byte
// tag) under `key` with `aad`. Returns an error on AEAD failure.
func Decrypt(key []byte, sessionID uint64, nonceCounter uint32, aad, ciphertext []byte) ([]byte, error) {
	gcm, err := newAESGCM(key)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, BuildGCMNonce(sessionID, nonceCounter), ciphertext, aad)
}

func newAESGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != SessionKeyLen {
		return nil, fmt.Errorf("aes-gcm key must be %d bytes, got %d", SessionKeyLen, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes.NewCipher: %w", err)
	}
	return cipher.NewGCM(block)
}

// NonceTracker is a goroutine-safe monotonic counter for the send side plus
// a last-seen tracker for the receive side (used for replay detection by
// callers that care).
type NonceTracker struct {
	mu       sync.Mutex
	sendCtr  uint32
	lastRecv uint32
	hasRecv  bool
}

// PeekNext returns the value the next call to Advance would emit, without
// advancing the counter.
func (n *NonceTracker) PeekNext() uint32 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.sendCtr
}

// Advance returns the current send-counter value and increments it. Returns
// ErrNonceCounterExhausted if the counter has reached the u32 max - the
// session must be rotated at that point.
func (n *NonceTracker) Advance() (uint32, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.sendCtr == MaxNonceCounter {
		return 0, ErrNonceCounterExhausted
	}
	v := n.sendCtr
	n.sendCtr++
	return v, nil
}

// CommitRecv records the highest counter value the receive side has accepted.
// Currently informational; the wire protocol does not require strict
// monotonicity on the receive side because the sequencer multiplexes streams.
func (n *NonceTracker) CommitRecv(received uint32) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.lastRecv = received
	n.hasRecv = true
}

// Reset zeroes both the send and receive counters. Used on reconnect / rekey.
func (n *NonceTracker) Reset() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sendCtr = 0
	n.lastRecv = 0
	n.hasRecv = false
}
