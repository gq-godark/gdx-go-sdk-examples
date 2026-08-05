// Package noise implements the initiator side of Noise_XK_25519_AESGCM_SHA256.
package noise

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

const (
	Pattern = "Noise_XK_25519_AESGCM_SHA256"
	hashLen = 32
	keyLen  = 32
	tagLen  = 16
)

// TagLen is the AES-GCM authentication tag size for Noise transport messages.
func TagLen() int { return tagLen }

var prologueDomain = []byte("gdx-noise-xk/v1\x00")

func PrologueForUser(userUUID []byte) ([]byte, error) {
	if len(userUUID) != 16 {
		return nil, fmt.Errorf("user UUID must be 16 bytes, got %d", len(userUUID))
	}
	return append(append([]byte{}, prologueDomain...), userUUID...), nil
}

func ParsePinnedStaticPublicKeyHex(value string) ([]byte, error) {
	if len(value) >= 2 && value[:2] == "0x" {
		value = value[2:]
	}
	if len(value) != keyLen*2 {
		return nil, errors.New("Noise static public key must be 64 hex chars")
	}
	key, err := hex.DecodeString(value)
	if err != nil || len(key) != keyLen {
		return nil, errors.New("Noise static public key must be 64 hex chars")
	}
	return key, nil
}

type cipherState struct {
	key   []byte
	nonce uint64
}

func (c *cipherState) initializeKey(key []byte) {
	c.key = append(c.key[:0], key...)
	c.nonce = 0
}

func (c *cipherState) cryptNonce() []byte {
	nonce := make([]byte, 12)
	binary.BigEndian.PutUint64(nonce[4:], c.nonce)
	return nonce
}

func (c *cipherState) encrypt(ad, plaintext []byte) ([]byte, error) {
	if c.key == nil {
		return append([]byte{}, plaintext...), nil
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	out := aead.Seal(nil, c.cryptNonce(), plaintext, ad)
	c.nonce++
	return out, nil
}

func (c *cipherState) decrypt(ad, ciphertext []byte) ([]byte, error) {
	if c.key == nil {
		return append([]byte{}, ciphertext...), nil
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	out, err := aead.Open(nil, c.cryptNonce(), ciphertext, ad)
	if err != nil {
		return nil, err
	}
	c.nonce++
	return out, nil
}

type symmetricState struct {
	ck []byte
	h  []byte
	cs cipherState
}

func newSymmetricState() *symmetricState {
	name := []byte(Pattern)
	h := append(append([]byte{}, name...), make([]byte, hashLen-len(name))...)
	return &symmetricState{ck: append([]byte{}, h...), h: h}
}

func (s *symmetricState) mixHash(data []byte) {
	h := sha256.New()
	_, _ = h.Write(s.h)
	_, _ = h.Write(data)
	s.h = h.Sum(nil)
}

func hkdf(ck, ikm []byte, outputs int) [][]byte {
	mac := hmac.New(sha256.New, ck)
	_, _ = mac.Write(ikm)
	temp := mac.Sum(nil)
	out := make([][]byte, outputs)
	var previous []byte
	for i := range out {
		mac = hmac.New(sha256.New, temp)
		_, _ = mac.Write(previous)
		_, _ = mac.Write([]byte{byte(i + 1)})
		out[i] = mac.Sum(nil)
		previous = out[i]
	}
	return out
}

func (s *symmetricState) mixKey(ikm []byte) {
	out := hkdf(s.ck, ikm, 2)
	s.ck = out[0]
	s.cs.initializeKey(out[1])
}

func (s *symmetricState) encryptAndHash(plaintext []byte) ([]byte, error) {
	out, err := s.cs.encrypt(s.h, plaintext)
	if err == nil {
		s.mixHash(out)
	}
	return out, err
}

func (s *symmetricState) decryptAndHash(ciphertext []byte) ([]byte, error) {
	out, err := s.cs.decrypt(s.h, ciphertext)
	if err == nil {
		s.mixHash(ciphertext)
	}
	return out, err
}

func (s *symmetricState) split() (*cipherState, *cipherState) {
	out := hkdf(s.ck, nil, 2)
	send, recv := &cipherState{}, &cipherState{}
	send.initializeKey(out[0])
	recv.initializeKey(out[1])
	return send, recv
}

func dh(private *ecdh.PrivateKey, public []byte) ([]byte, error) {
	remote, err := ecdh.X25519().NewPublicKey(public)
	if err != nil {
		return nil, err
	}
	shared, err := private.ECDH(remote)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(shared, make([]byte, keyLen)) {
		return nil, errors.New("Noise X25519 shared secret is all zeros")
	}
	return shared, nil
}

// Transport is the post-handshake Noise transport. It is not goroutine safe.
type Transport struct {
	send cipherState
	recv cipherState
}

func (t *Transport) Encrypt(plaintext []byte) ([]byte, error) { return t.send.encrypt(nil, plaintext) }
func (t *Transport) Decrypt(ciphertext []byte) ([]byte, error) {
	return t.recv.decrypt(nil, ciphertext)
}
func (t *Transport) RecvNonce() uint64 { return t.recv.nonce }

// SetRecvNonce forces the receiving cipher's AEAD counter to n. The sequencer
// stamps the true Noise send counter on every push, and the edge is a blind
// relay that may drop frames for channels this client is not subscribed to
// (advancing the server counter). Aligning the receive counter to the
// server-stamped value before each decrypt keeps AEAD correct across those
// gaps while remaining exact for a gap-free stream.
func (t *Transport) SetRecvNonce(n uint64) { t.recv.nonce = n }

// DecryptAt sets the receiving counter to n and then decrypts one frame, so the
// GCM IV matches the server-stamped nonce regardless of any relayed gaps.
func (t *Transport) DecryptAt(n uint64, ciphertext []byte) ([]byte, error) {
	t.recv.nonce = n
	return t.recv.decrypt(nil, ciphertext)
}

// HandshakeInitiator drives the three-message XK handshake.
type HandshakeInitiator struct {
	ss        *symmetricState
	static    *ecdh.PrivateKey
	remote    []byte
	ephemeral *ecdh.PrivateKey
	turn      int
	finished  bool
}

func NewHandshakeInitiator(remoteStatic, prologue []byte) (*HandshakeInitiator, error) {
	if len(remoteStatic) != keyLen {
		return nil, errors.New("remote static public key must be 32 bytes")
	}
	static, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate Noise static key: %w", err)
	}
	ss := newSymmetricState()
	ss.mixHash(prologue)
	ss.mixHash(remoteStatic) // XK pre-message: <- s
	return &HandshakeInitiator{ss: ss, static: static, remote: append([]byte{}, remoteStatic...)}, nil
}

func (h *HandshakeInitiator) WriteMessage(payload []byte) ([]byte, error) {
	switch h.turn {
	case 0:
		e, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		h.ephemeral = e
		ep := e.PublicKey().Bytes()
		h.ss.mixHash(ep)
		shared, err := dh(e, h.remote)
		if err != nil {
			return nil, err
		}
		h.ss.mixKey(shared)
		body, err := h.ss.encryptAndHash(payload)
		if err != nil {
			return nil, err
		}
		h.turn = 1
		return append(ep, body...), nil
	case 2:
		if h.ephemeral == nil {
			return nil, errors.New("Noise handshake missing remote ephemeral")
		}
		encStatic, err := h.ss.encryptAndHash(h.static.PublicKey().Bytes())
		if err != nil {
			return nil, err
		}
		shared, err := dh(h.static, h.remote)
		if err != nil {
			return nil, err
		}
		h.ss.mixKey(shared)
		body, err := h.ss.encryptAndHash(payload)
		if err != nil {
			return nil, err
		}
		h.turn, h.finished = 3, true
		return append(encStatic, body...), nil
	default:
		return nil, errors.New("Noise handshake: not initiator write turn")
	}
}

func (h *HandshakeInitiator) ReadMessage(message []byte) ([]byte, error) {
	if h.turn != 1 || h.ephemeral == nil {
		return nil, errors.New("Noise handshake: not initiator read turn")
	}
	if len(message) < keyLen {
		return nil, io.ErrUnexpectedEOF
	}
	remoteEphemeral := message[:keyLen]
	h.remote = remoteEphemeral
	h.ss.mixHash(remoteEphemeral)
	shared, err := dh(h.ephemeral, remoteEphemeral)
	if err != nil {
		return nil, err
	}
	h.ss.mixKey(shared)
	body, err := h.ss.decryptAndHash(message[keyLen:])
	if err != nil {
		return nil, err
	}
	h.turn = 2
	return body, nil
}

func (h *HandshakeInitiator) IntoTransport() (*Transport, error) {
	if !h.finished {
		return nil, errors.New("Noise handshake not finished")
	}
	send, recv := h.ss.split()
	return &Transport{send: *send, recv: *recv}, nil
}
