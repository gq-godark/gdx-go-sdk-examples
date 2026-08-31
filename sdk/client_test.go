package godark

import (
	"errors"
	"strings"
	"testing"
)

func TestNewClient_RequiresCredential(t *testing.T) {
	_, err := NewClient(ClientConfig{})
	if err == nil {
		t.Fatal("expected error when no credentials provided")
	}
	if !strings.Contains(err.Error(), "APIKey") {
		t.Fatalf("error message should mention APIKey, got %q", err.Error())
	}
}

func TestNewClient_RejectsMixedCredentials(t *testing.T) {
	_, err := NewClient(ClientConfig{
		APIKey:    "legacy",
		APIKeyID:  "id",
		APISecret: "secret",
	})
	if err == nil {
		t.Fatal("expected error when both APIKey and APIKeyID+APISecret set")
	}
}

func TestNewClient_RequiresBothKeyParts(t *testing.T) {
	_, err := NewClient(ClientConfig{APIKeyID: "id"})
	if err == nil {
		t.Fatal("expected error when APISecret missing")
	}
	_, err = NewClient(ClientConfig{APISecret: "secret"})
	if err == nil {
		t.Fatal("expected error when APIKeyID missing")
	}
}

func TestNewClient_AcceptsLegacyAPIKey(t *testing.T) {
	c, err := NewClient(ClientConfig{APIKey: "legacy-token", BaseURL: "wss://localhost:1"})
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	if c.authToken != "legacy-token" {
		t.Errorf("authToken = %q, want %q", c.authToken, "legacy-token")
	}
}

func TestNewClient_AcceptsKeyPair(t *testing.T) {
	c, err := NewClient(ClientConfig{
		APIKeyID:   "gdk_xx",
		APISecret:  "sssss",
		Passphrase: "my-pass",
		BaseURL:    "wss://localhost:1",
	})
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	if c.authToken != "gdk_xx:sssss:my-pass" {
		t.Errorf("authToken = %q", c.authToken)
	}
}

func TestNewClient_AcceptsKeyPairPassphraseFromEnv(t *testing.T) {
	t.Setenv("GODARK_PASSPHRASE", "env-pass")
	c, err := NewClient(ClientConfig{
		APIKeyID:  "gdk_xx",
		APISecret: "sssss",
		BaseURL:   "wss://localhost:1",
	})
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	if c.authToken != "gdk_xx:sssss:env-pass" {
		t.Errorf("authToken = %q", c.authToken)
	}
}

func TestNewClient_AppliesSymbolMapOverride(t *testing.T) {
	custom := map[string]int64{"CUSTOM-PERP": 999}
	c, _ := NewClient(ClientConfig{
		APIKey:    "x",
		BaseURL:   "wss://localhost:1",
		SymbolMap: custom,
	})
	if c.symbolMap["CUSTOM-PERP"] != 999 {
		t.Errorf("symbol override not applied: %v", c.symbolMap)
	}
}

func TestResolveSymbol_KnownAndUnknown(t *testing.T) {
	c, _ := NewClient(ClientConfig{APIKey: "x", BaseURL: "wss://x:1"})
	if id, err := c.resolveSymbol("BTC-USDC-PERP"); err != nil || id != 1 {
		t.Errorf("BTC: id=%d err=%v; want 1 / nil", id, err)
	}
	if _, err := c.resolveSymbol("XXX-XXX"); err == nil {
		t.Errorf("expected error for unknown symbol")
	}
}

func TestCoerceNumericErrorCode(t *testing.T) {
	cases := []struct {
		in   any
		want *int32
	}{
		{nil, nil},
		{2007, ptr32(2007)},
		{int64(2008), ptr32(2008)},
		{float64(2009), ptr32(2009)},
		{"2010", ptr32(2010)},
		{"  2011  ", ptr32(2011)},
		{"NOT_NUMERIC", nil},
		{[]byte("nope"), nil},
	}
	for _, c := range cases {
		got := coerceNumericErrorCode(c.in)
		switch {
		case got == nil && c.want == nil:
			// ok
		case got == nil || c.want == nil:
			t.Errorf("coerce(%v) = %v, want %v", c.in, ptrOrNil(got), ptrOrNil(c.want))
		case *got != *c.want:
			t.Errorf("coerce(%v) = %d, want %d", c.in, *got, *c.want)
		}
	}
}

func TestEnsureReady_NotConnected(t *testing.T) {
	c, _ := NewClient(ClientConfig{APIKey: "x", BaseURL: "wss://x:1"})
	err := c.ensureReady()
	if err == nil {
		t.Fatal("expected ensureReady error before Connect()")
	}
	var ce *ConnectionError
	if !errors.As(err, &ce) {
		t.Errorf("expected *ConnectionError, got %T", err)
	}
}

func TestUserUUIDBytesFallsBackToZero(t *testing.T) {
	c, _ := NewClient(ClientConfig{APIKey: "x", BaseURL: "wss://x:1"})
	b := c.userUUIDBytes()
	if len(b) != 16 {
		t.Fatalf("len = %d, want 16", len(b))
	}
	for _, x := range b {
		if x != 0 {
			t.Fatalf("expected all zero bytes, got %x", b)
		}
	}
}

func clearEdgeAndNoiseEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"GDX_NOISE_STATIC_PUBLIC_KEY",
		"GDX_NOISE_STATIC_PUBKEY",
		"GODARK_NOISE_STATIC_PUBLIC_KEY",
		"GDX_HPKE_STATIC_PUBLIC_KEY",
		"GDX_HPKE_STATIC_PUBKEY",
		"GODARK_HPKE_STATIC_PUBLIC_KEY",
		"VITE_GDX_HPKE_STATIC_PUBKEY",
		"GODARK_EDGE_URL",
		"GDX_EDGE_URL",
	} {
		t.Setenv(key, "")
	}
}

func TestNewClient_DefaultEnvironmentTestnetUsesBakedHpkePin(t *testing.T) {
	clearEdgeAndNoiseEnv(t)
	c, err := NewClient(ClientConfig{APIKey: "x"})
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	if c.baseURL != defaultEdgeBaseURL {
		t.Errorf("baseURL = %q, want %q", c.baseURL, defaultEdgeBaseURL)
	}
	if c.noiseStaticKey != testnetHpkeStaticPublicKeyHex {
		t.Errorf("noiseStaticKey = %q, want baked testnet pin", c.noiseStaticKey)
	}
}

func TestNewClient_DevnetUsesSeparateURLAndBakedHpkePin(t *testing.T) {
	clearEdgeAndNoiseEnv(t)
	c, err := NewClient(ClientConfig{
		APIKey:      "x",
		Environment: EnvironmentDevnet,
	})
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	if c.baseURL != defaultDevnetEdgeBaseURL {
		t.Errorf("baseURL = %q, want %q", c.baseURL, defaultDevnetEdgeBaseURL)
	}
	if c.noiseStaticKey != devnetHpkeStaticPublicKeyHex {
		t.Errorf("noiseStaticKey = %q, want baked devnet pin", c.noiseStaticKey)
	}
}

func TestNewClient_LocalnetHasNoBakedNoisePin(t *testing.T) {
	clearEdgeAndNoiseEnv(t)
	c, err := NewClient(ClientConfig{
		APIKey:      "x",
		Environment: EnvironmentLocalnet,
	})
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	if c.baseURL != "ws://127.0.0.1:4000" {
		t.Errorf("baseURL = %q, want ws://127.0.0.1:4000", c.baseURL)
	}
	if c.noiseStaticKey != "" {
		t.Errorf("noiseStaticKey = %q, want empty for localnet", c.noiseStaticKey)
	}
}

func TestNewClient_ExplicitNoiseOverridesEnvironment(t *testing.T) {
	clearEdgeAndNoiseEnv(t)
	pin := strings.Repeat("11", 32)
	c, err := NewClient(ClientConfig{
		APIKey:                  "x",
		Environment:             EnvironmentTestnet,
		NoiseStaticPublicKeyHex: pin,
	})
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	if c.noiseStaticKey != pin {
		t.Errorf("noiseStaticKey = %q, want explicit override", c.noiseStaticKey)
	}
}

func TestResolveHpkeStaticPublicKey_EnvOverridesEnvironmentPreset(t *testing.T) {
	clearEdgeAndNoiseEnv(t)
	envPin := strings.Repeat("22", 32)
	t.Setenv("GDX_HPKE_STATIC_PUBLIC_KEY", envPin)
	got := resolveHpkeStaticPublicKey("", EnvironmentTestnet)
	if got != envPin {
		t.Errorf("resolveHpkeStaticPublicKey = %q, want env override %q", got, envPin)
	}
	got = resolveHpkeStaticPublicKey("", EnvironmentDevnet)
	if got != envPin {
		t.Errorf("resolveHpkeStaticPublicKey(devnet) = %q, want env override", got)
	}
	// Without env, presets win.
	t.Setenv("GDX_HPKE_STATIC_PUBLIC_KEY", "")
	if got := resolveHpkeStaticPublicKey("", EnvironmentTestnet); got != testnetHpkeStaticPublicKeyHex {
		t.Errorf("testnet preset = %q, want %q", got, testnetHpkeStaticPublicKeyHex)
	}
	if got := resolveHpkeStaticPublicKey("", EnvironmentLocalnet); got != "" {
		t.Errorf("localnet preset = %q, want empty", got)
	}
}

func ptr32(v int32) *int32 { return &v }
func ptrOrNil(p *int32) any {
	if p == nil {
		return nil
	}
	return *p
}
