package godark

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"testing"
)

func clearRestHpkeEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"GDX_HPKE_STATIC_PUBLIC_KEY",
		"GDX_HPKE_STATIC_PUBKEY",
		"GODARK_HPKE_STATIC_PUBLIC_KEY",
		"VITE_GDX_HPKE_STATIC_PUBKEY",
		"GODARK_NOISE_STATIC_PUBLIC_KEY",
		"GDX_NOISE_STATIC_PUBLIC_KEY",
		"GDX_NOISE_STATIC_PUBKEY",
		"GODARK_REST_URL",
		"GDX_REST_URL",
		"GODARK_EDGE_URL",
		"GDX_EDGE_URL",
	} {
		t.Setenv(key, "")
	}
}

func validX25519PubHex(t *testing.T) string {
	t.Helper()
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return hex.EncodeToString(priv.PublicKey().Bytes())
}

func TestInferEnvironmentFromRestURL(t *testing.T) {
	cases := []struct {
		url  string
		want Environment
	}{
		{"https://api.devnet.godark-dex.com", EnvironmentDevnet},
		{"http://18.143.165.149:13300", EnvironmentDevnet},
		{"https://api.godark-dex.com", EnvironmentTestnet},
		{"http://127.0.0.1:4000", EnvironmentLocalnet},
		{"http://localhost:8080", EnvironmentLocalnet},
		{"http://foo.localhost", EnvironmentLocalnet},
		{"https://unknown.example.com", EnvironmentTestnet},
	}
	for _, tc := range cases {
		if got := inferEnvironmentFromRestURL(tc.url); got != tc.want {
			t.Errorf("inferEnvironmentFromRestURL(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

func TestNewRestClient_InfersDevnetPinFromURL(t *testing.T) {
	clearRestHpkeEnv(t)
	c, err := NewRestClient(RestClientConfig{
		APIKey:  "k",
		BaseURL: "https://api.devnet.godark-dex.com",
	})
	if err != nil {
		t.Fatalf("NewRestClient: %v", err)
	}
	if c.hpkePinHex != restDevnetHpkeStaticPublicKeyHex {
		t.Fatalf("hpkePinHex = %q, want baked devnet pin", c.hpkePinHex)
	}
}

func TestNewRestClient_InfersTestnetPinFromURL(t *testing.T) {
	clearRestHpkeEnv(t)
	c, err := NewRestClient(RestClientConfig{
		APIKey:  "k",
		BaseURL: "https://api.godark-dex.com",
	})
	if err != nil {
		t.Fatalf("NewRestClient: %v", err)
	}
	if c.hpkePinHex != restTestnetHpkeStaticPublicKeyHex {
		t.Fatalf("hpkePinHex = %q, want baked testnet pin", c.hpkePinHex)
	}
}

func TestNewRestClient_EnvOverridesBakedPin(t *testing.T) {
	clearRestHpkeEnv(t)
	override := validX25519PubHex(t)
	t.Setenv("GDX_HPKE_STATIC_PUBLIC_KEY", override)
	c, err := NewRestClient(RestClientConfig{
		APIKey:  "k",
		BaseURL: "https://api.devnet.godark-dex.com",
	})
	if err != nil {
		t.Fatalf("NewRestClient: %v", err)
	}
	if c.hpkePinHex != override {
		t.Fatalf("hpkePinHex = %q, want env override %q", c.hpkePinHex, override)
	}
}

func TestNewRestClient_ExplicitOverridesEnvAndBaked(t *testing.T) {
	clearRestHpkeEnv(t)
	t.Setenv("GDX_HPKE_STATIC_PUBLIC_KEY", validX25519PubHex(t))
	explicit := validX25519PubHex(t)
	c, err := NewRestClient(RestClientConfig{
		APIKey:                 "k",
		BaseURL:                "https://api.devnet.godark-dex.com",
		HpkeStaticPublicKeyHex: explicit,
	})
	if err != nil {
		t.Fatalf("NewRestClient: %v", err)
	}
	if c.hpkePinHex != explicit {
		t.Fatalf("hpkePinHex = %q, want explicit %q", c.hpkePinHex, explicit)
	}
}

func TestNewRestClient_LocalnetHasNoBakedPin(t *testing.T) {
	clearRestHpkeEnv(t)
	c, err := NewRestClient(RestClientConfig{
		APIKey:  "k",
		BaseURL: "http://127.0.0.1:4000",
	})
	if err != nil {
		t.Fatalf("NewRestClient: %v", err)
	}
	if c.hpkePinHex != "" {
		t.Fatalf("hpkePinHex = %q, want empty for localnet", c.hpkePinHex)
	}
}

func TestNewRestClient_ExplicitEnvironmentOverridesURLInference(t *testing.T) {
	clearRestHpkeEnv(t)
	c, err := NewRestClient(RestClientConfig{
		APIKey:      "k",
		BaseURL:     "https://api.godark-dex.com",
		Environment: EnvironmentDevnet,
	})
	if err != nil {
		t.Fatalf("NewRestClient: %v", err)
	}
	if c.hpkePinHex != restDevnetHpkeStaticPublicKeyHex {
		t.Fatalf("hpkePinHex = %q, want baked devnet pin from Environment", c.hpkePinHex)
	}
}

func TestResolveRestHpkePin(t *testing.T) {
	clearRestHpkeEnv(t)
	if got := resolveRestHpkePin("", EnvironmentTestnet); got != restTestnetHpkeStaticPublicKeyHex {
		t.Errorf("testnet baked = %q", got)
	}
	if got := resolveRestHpkePin("", EnvironmentDevnet); got != restDevnetHpkeStaticPublicKeyHex {
		t.Errorf("devnet baked = %q", got)
	}
	if got := resolveRestHpkePin("", EnvironmentLocalnet); got != "" {
		t.Errorf("localnet = %q, want empty", got)
	}
}
