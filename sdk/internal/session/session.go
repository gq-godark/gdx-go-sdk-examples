// Package session owns the post-handshake Noise transport lifecycle.
package session

import (
	"errors"
	"os"
	"strings"
	"sync"

	"github.com/gq-godark/gdx-go-sdk/internal/bound"
	"github.com/gq-godark/gdx-go-sdk/internal/noise"
)

// ErrNotEstablished is returned by Encrypt / Decrypt before Establish().
var ErrNotEstablished = errors.New("session not established")

// stampedNoncePush aligns push decryption to the server-stamped envelope nonce
// instead of a strictly-sequential Noise receive counter. The edge relay may
// drop frames for unsubscribed channels, advancing the server send counter and
// desyncing a sequential client (GCM tag mismatches / ACK timeouts on hosted
// testnet). Default on; set GDX_STAMPED_NONCE_PUSH=false for the legacy path.
var stampedNoncePush = strings.ToLower(os.Getenv("GDX_STAMPED_NONCE_PUSH")) != "false"

// StampedNoncePush reports whether push decryption realigns to the
// server-stamped envelope nonce (default) versus the legacy sequential gate.
// The client uses this to decide whether to buffer/reorder pushes by nonce.
func StampedNoncePush() bool { return stampedNoncePush }

// CryptoSession holds one Noise XK transport.
//
// All methods are safe to call from multiple goroutines.
type CryptoSession struct {
	mu          sync.Mutex
	transport   *noise.Transport
	connID      uint64
	established bool
	sendNonce   uint32
}

// IsEstablished reports whether the Noise XK handshake has completed.
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

// NextNonce returns the value the next call to EncryptOrder would consume,
// without advancing the counter. Useful for AAD construction.
func (s *CryptoSession) NextNonce() uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sendNonce
}

// Establish attaches a completed Noise transport to the authenticated conn_id.
func (s *CryptoSession) Establish(value any, connID uint64) error {
	transport, ok := value.(*noise.Transport)
	if !ok {
		return errors.New("ECDH session.setup is not supported; encrypted REST trading requires a Noise WebSocket session")
	}
	if transport == nil || connID == 0 {
		return errors.New("Noise transport and non-zero conn_id are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transport, s.connID, s.established, s.sendNonce = transport, connID, true, 0
	return nil
}

// GenerateKeypair is retained only so older REST call sites fail explicitly:
// Noise XK needs a live WebSocket relay and cannot be established over REST.
func (s *CryptoSession) GenerateKeypair() (string, error) {
	return "", errors.New("ECDH session.setup is not supported; encrypted REST trading requires a Noise WebSocket session")
}

// EncryptOrder seals an order payload. Returns (nonce_counter, ciphertext).
// `aad` is typically the protobuf-encoded `OrderHeader`.
func (s *CryptoSession) EncryptOrder(aad, plaintext []byte) (uint32, []byte, error) {
	s.mu.Lock()
	if !s.established || s.transport == nil {
		s.mu.Unlock()
		return 0, nil, ErrNotEstablished
	}
	transport := s.transport
	nonce := s.sendNonce
	if nonce == ^uint32(0) {
		s.mu.Unlock()
		return 0, nil, errors.New("send nonce counter exceeded u32 max")
	}
	s.sendNonce++
	s.mu.Unlock()
	ct, err := bound.Encrypt(transport, aad, plaintext)
	if err != nil {
		return 0, nil, err
	}
	return nonce, ct, nil
}

// DecryptPush opens an encrypted push from the sequencer. `aad` is typically
// the protobuf-encoded `ResponseHeader`.
func (s *CryptoSession) DecryptPush(nonceCounter uint32, aad, ciphertext []byte) ([]byte, error) {
	s.mu.Lock()
	if !s.established || s.transport == nil {
		s.mu.Unlock()
		return nil, ErrNotEstablished
	}
	transport := s.transport
	s.mu.Unlock()
	if stampedNoncePush {
		// Align the receive counter to the server-stamped nonce, then decrypt.
		// DecryptAt advances the counter to nonceCounter+1 on success.
		return bound.DecryptAt(transport, uint64(nonceCounter), aad, ciphertext)
	}
	if uint64(nonceCounter) != transport.RecvNonce() {
		return nil, errors.New("Noise receive nonce out of sequence")
	}
	return bound.Decrypt(transport, aad, ciphertext)
}

// RecvNonce returns the next Noise receive counter for ordered push handling.
func (s *CryptoSession) RecvNonce() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.transport == nil {
		return 0
	}
	return s.transport.RecvNonce()
}

// Reset clears all session state. Call between reconnects or on rekey.
func (s *CryptoSession) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transport = nil
	s.connID = 0
	s.established = false
	s.sendNonce = 0
}
