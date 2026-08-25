// Package hpke implements HPKE Base (RFC 9180) for trading E2E.
//
// Suite: DHKEM(X25519, HKDF-SHA256) + HKDF-SHA256 + AES-256-GCM.
// After setup, peers export directional keys and seal each message with an
// explicit 96-bit nonce (0u32_be ‖ counter_be).
package hpke

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/cloudflare/circl/hpke"
	"github.com/cloudflare/circl/kem"
)

const (
	KeyLen          = 32
	EncappedKeyLen  = 32
	TagLen          = 16
	WireVersion     = 2
	InfoDomain      = "gdx-hpke/v1\000"
	InfoDomainREST  = "gdx-hpke/v1/rest\000"
	ExportC2S       = "gdx-hpke/v1 c2s"
	ExportS2C       = "gdx-hpke/v1 s2c"
)

var suite = hpke.NewSuite(
	hpke.KEM_X25519_HKDF_SHA256,
	hpke.KDF_HKDF_SHA256,
	hpke.AEAD_AES256GCM,
)

// InfoForConn builds gdx-hpke/v1\0 ‖ user_uuid ‖ conn_id_be.
func InfoForConn(userUUID []byte, connID uint64) []byte {
	info := make([]byte, 0, len(InfoDomain)+16+8)
	info = append(info, InfoDomain...)
	info = append(info, userUUID...)
	var connBE [8]byte
	putU64BE(connBE[:], connID)
	info = append(info, connBE[:]...)
	return info
}

// InfoForRESTRequest builds gdx-hpke/v1/rest\0 ‖ user_uuid ‖ request_id_be.
func InfoForRESTRequest(userUUID []byte, requestID uint64) []byte {
	info := make([]byte, 0, len(InfoDomainREST)+16+8)
	info = append(info, InfoDomainREST...)
	info = append(info, userUUID...)
	var reqBE [8]byte
	putU64BE(reqBE[:], requestID)
	info = append(info, reqBE[:]...)
	return info
}

// NonceFromU64 packs a monotonic u64 into a 96-bit GCM nonce: 0u32_be ‖ counter_be.
func NonceFromU64(counter uint64) [12]byte {
	var out [12]byte
	putU64BE(out[4:], counter)
	return out
}

func putU64BE(dst []byte, v uint64) {
	for i := len(dst) - 1; i >= 0; i-- {
		dst[i] = byte(v)
		v >>= 8
	}
}

func seal(key []byte, nonce []byte, aad, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return aead.Seal(nil, nonce, plaintext, aad), nil
}

func open(key []byte, nonce []byte, aad, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, nonce, ciphertext, aad)
}

// SealedSession holds application keys after HPKE export.
type SealedSession struct {
	kC2S [KeyLen]byte
	kS2C [KeyLen]byte
}

func sealedFromExported(kC2S, kS2C []byte) (*SealedSession, error) {
	if len(kC2S) != KeyLen || len(kS2C) != KeyLen {
		return nil, errors.New("HPKE export key length mismatch")
	}
	var s SealedSession
	copy(s.kC2S[:], kC2S)
	copy(s.kS2C[:], kS2C)
	return &s, nil
}

func (s *SealedSession) SealC2S(nonce []byte, aad, plaintext []byte) ([]byte, error) {
	ct, err := seal(s.kC2S[:], nonce, aad, plaintext)
	if err != nil {
		return nil, fmt.Errorf("AES-GCM seal failed: %w", err)
	}
	return ct, nil
}

func (s *SealedSession) OpenC2S(nonce []byte, aad, ciphertext []byte) ([]byte, error) {
	pt, err := open(s.kC2S[:], nonce, aad, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("AES-GCM open failed: %w", err)
	}
	return pt, nil
}

func (s *SealedSession) SealS2C(nonce []byte, aad, plaintext []byte) ([]byte, error) {
	ct, err := seal(s.kS2C[:], nonce, aad, plaintext)
	if err != nil {
		return nil, fmt.Errorf("AES-GCM seal failed: %w", err)
	}
	return ct, nil
}

func (s *SealedSession) OpenS2C(nonce []byte, aad, ciphertext []byte) ([]byte, error) {
	pt, err := open(s.kS2C[:], nonce, aad, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("AES-GCM open failed: %w", err)
	}
	return pt, nil
}

// StaticKeyPair is the sequencer static recipient keypair (tests / mock edge).
type StaticKeyPair struct {
	private [KeyLen]byte
	public  [KeyLen]byte
}

// GenerateStaticKeyPair creates a fresh X25519 HPKE keypair.
func GenerateStaticKeyPair() (*StaticKeyPair, error) {
	scheme := hpke.KEM_X25519_HKDF_SHA256.Scheme()
	_, sk, err := scheme.GenerateKeyPair()
	if err != nil {
		return nil, fmt.Errorf("HPKE keypair: %w", err)
	}
	pk := sk.Public()
	skBytes, err := sk.MarshalBinary()
	if err != nil {
		return nil, err
	}
	pkBytes, err := pk.MarshalBinary()
	if err != nil {
		return nil, err
	}
	var pair StaticKeyPair
	copy(pair.private[:], skBytes)
	copy(pair.public[:], pkBytes)
	return &pair, nil
}

// PublicKey returns the 32-byte X25519 public key.
func (p *StaticKeyPair) PublicKey() []byte {
	return p.public[:]
}

func (p *StaticKeyPair) kemPrivate() (kem.PrivateKey, error) {
	scheme := hpke.KEM_X25519_HKDF_SHA256.Scheme()
	sk, err := scheme.UnmarshalBinaryPrivateKey(p.private[:])
	if err != nil {
		return nil, fmt.Errorf("HPKE private key: %w", err)
	}
	return sk, nil
}

// SetupSession is the client (initiator): encapsulate to sequencer pubkey.
func SetupSession(recipientPublic []byte, info []byte) ([]byte, *SealedSession, error) {
	if len(recipientPublic) != KeyLen {
		return nil, nil, fmt.Errorf("HPKE public key must be %d bytes, got %d", KeyLen, len(recipientPublic))
	}
	scheme := hpke.KEM_X25519_HKDF_SHA256.Scheme()
	pk, err := scheme.UnmarshalBinaryPublicKey(recipientPublic)
	if err != nil {
		return nil, nil, fmt.Errorf("HPKE public key: %w", err)
	}
	sender, err := suite.NewSender(pk, info)
	if err != nil {
		return nil, nil, err
	}
	enc, ctx, err := sender.Setup(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("HPKE setup_sender: %w", err)
	}
	if len(enc) != EncappedKeyLen {
		return nil, nil, fmt.Errorf("encapped key must be %d bytes, got %d", EncappedKeyLen, len(enc))
	}
	sealed, err := sealedFromExported(ctx.Export([]byte(ExportC2S), KeyLen), ctx.Export([]byte(ExportS2C), KeyLen))
	if err != nil {
		return nil, nil, err
	}
	return enc, sealed, nil
}

// OpenSession is the sequencer (recipient): open encapped key with static private key.
func OpenSession(recipient *StaticKeyPair, encappedKey, info []byte) (*SealedSession, error) {
	if len(encappedKey) != EncappedKeyLen {
		return nil, fmt.Errorf("encapped key must be %d bytes, got %d", EncappedKeyLen, len(encappedKey))
	}
	sk, err := recipient.kemPrivate()
	if err != nil {
		return nil, err
	}
	receiver, err := suite.NewReceiver(sk, info)
	if err != nil {
		return nil, err
	}
	ctx, err := receiver.Setup(encappedKey)
	if err != nil {
		return nil, fmt.Errorf("HPKE setup_receiver: %w", err)
	}
	return sealedFromExported(ctx.Export([]byte(ExportC2S), KeyLen), ctx.Export([]byte(ExportS2C), KeyLen))
}

// ParsePinnedStaticPublicKey parses a 64-hex-char pinned sequencer public key.
func ParsePinnedStaticPublicKey(hexStr string) ([]byte, error) {
	hexStr = stringsTrimSpace(hexStr)
	if len(hexStr) >= 2 && (hexStr[0:2] == "0x" || hexStr[0:2] == "0X") {
		hexStr = hexStr[2:]
	}
	bytes, err := hex.DecodeString(hexStr)
	if err != nil {
		return nil, fmt.Errorf("HPKE static public key must be hex: %w", err)
	}
	if len(bytes) != KeyLen {
		return nil, fmt.Errorf("HPKE static public key must be %d bytes, got %d", KeyLen, len(bytes))
	}
	return bytes, nil
}

func stringsTrimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
