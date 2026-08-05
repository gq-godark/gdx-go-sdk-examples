package noise

import (
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// HandshakeResponder drives the responder side of the XK handshake (tests + mock edge).
type HandshakeResponder struct {
	ss        *symmetricState
	static    *ecdh.PrivateKey
	ephemeral *ecdh.PrivateKey
	remoteEp  []byte
	turn      int
	finished  bool
}

// NewHandshakeResponder creates a responder with the given local static keypair and prologue.
func NewHandshakeResponder(static *ecdh.PrivateKey, prologue []byte) (*HandshakeResponder, error) {
	if static == nil {
		return nil, errors.New("static key required")
	}
	ss := newSymmetricState()
	ss.mixHash(prologue)
	ss.mixHash(static.PublicKey().Bytes()) // XK pre-message: <- s
	return &HandshakeResponder{ss: ss, static: static}, nil
}

// GenerateStaticKeyPair returns a fresh X25519 static keypair for mock sequencers.
func GenerateStaticKeyPair() (*ecdh.PrivateKey, []byte, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate static key: %w", err)
	}
	return priv, priv.PublicKey().Bytes(), nil
}

func (h *HandshakeResponder) ReadMessage(message []byte) ([]byte, error) {
	switch h.turn {
	case 0:
		if len(message) < keyLen {
			return nil, io.ErrUnexpectedEOF
		}
		h.remoteEp = message[:keyLen]
		rest := message[keyLen:]
		h.ss.mixHash(h.remoteEp)
		shared, err := dh(h.static, h.remoteEp)
		if err != nil {
			return nil, err
		}
		h.ss.mixKey(shared)
		payload, err := h.ss.decryptAndHash(rest)
		if err != nil {
			return nil, err
		}
		h.turn = 1
		return payload, nil
	case 2:
		if h.ephemeral == nil || h.remoteEp == nil {
			return nil, errors.New("Noise handshake: missing ephemerals")
		}
		encStaticLen := keyLen
		if h.ss.cs.key != nil {
			encStaticLen = keyLen + tagLen
		}
		if len(message) < encStaticLen {
			return nil, io.ErrUnexpectedEOF
		}
		encStatic := message[:encStaticLen]
		rest := message[encStaticLen:]
		remoteStatic, err := h.ss.decryptAndHash(encStatic)
		if err != nil {
			return nil, err
		}
		shared, err := dh(h.ephemeral, remoteStatic)
		if err != nil {
			return nil, err
		}
		h.ss.mixKey(shared)
		payload, err := h.ss.decryptAndHash(rest)
		if err != nil {
			return nil, err
		}
		h.turn, h.finished = 3, true
		return payload, nil
	default:
		return nil, errors.New("Noise handshake: not responder read turn")
	}
}

func (h *HandshakeResponder) WriteMessage(payload []byte) ([]byte, error) {
	if h.turn != 1 {
		return nil, errors.New("Noise handshake: not responder write turn")
	}
	if h.remoteEp == nil {
		return nil, errors.New("Noise handshake: missing remote ephemeral")
	}
	e, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	h.ephemeral = e
	ep := e.PublicKey().Bytes()
	h.ss.mixHash(ep)
	shared, err := dh(e, h.remoteEp)
	if err != nil {
		return nil, err
	}
	h.ss.mixKey(shared)
	body, err := h.ss.encryptAndHash(payload)
	if err != nil {
		return nil, err
	}
	h.turn = 2
	return append(ep, body...), nil
}

func (h *HandshakeResponder) IsFinished() bool { return h.finished }

func (h *HandshakeResponder) IntoTransport() (*Transport, error) {
	if !h.finished {
		return nil, errors.New("Noise handshake not finished")
	}
	send, recv := h.ss.split()
	// Responder: initiator send = cs1, recv = cs2 — we invert.
	return &Transport{send: *recv, recv: *send}, nil
}
