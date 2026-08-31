package godark

import (
	"strings"
	"testing"
)

func TestResolvePassphrase_PrefersConstructor(t *testing.T) {
	t.Setenv("GDX_PASSPHRASE", "from-env")
	got, err := resolvePassphrase("explicit")
	if err != nil {
		t.Fatalf("resolvePassphrase: %v", err)
	}
	if got != "explicit" {
		t.Fatalf("got %q, want explicit", got)
	}
}

func TestResolvePassphrase_FromGDXEnv(t *testing.T) {
	t.Setenv("GODARK_PASSPHRASE", "")
	t.Setenv("GDX_PASSPHRASE", "gdx-pw")
	got, err := resolvePassphrase("")
	if err != nil {
		t.Fatalf("resolvePassphrase: %v", err)
	}
	if got != "gdx-pw" {
		t.Fatalf("got %q, want gdx-pw", got)
	}
}

func TestResolvePassphrase_FromGODARKEnv(t *testing.T) {
	t.Setenv("GODARK_PASSPHRASE", "godark-pw")
	t.Setenv("GDX_PASSPHRASE", "gdx-pw")
	got, err := resolvePassphrase("")
	if err != nil {
		t.Fatalf("resolvePassphrase: %v", err)
	}
	if got != "godark-pw" {
		t.Fatalf("got %q, want godark-pw", got)
	}
}

func TestResolvePassphrase_EmptyReturnsError(t *testing.T) {
	t.Setenv("GODARK_PASSPHRASE", "   ")
	t.Setenv("GDX_PASSPHRASE", "")
	_, err := resolvePassphrase("")
	if err == nil {
		t.Fatal("expected error for missing passphrase")
	}
	_, err = resolvePassphrase("  ")
	if err == nil {
		t.Fatal("expected error for whitespace-only passphrase")
	}
}

func TestNewClient_RejectsKeyPairWithoutPassphrase(t *testing.T) {
	t.Setenv("GODARK_PASSPHRASE", "")
	t.Setenv("GDX_PASSPHRASE", "")
	_, err := NewClient(ClientConfig{
		APIKeyID:  "gdk_xx",
		APISecret: "secret",
		BaseURL:   "wss://localhost:1",
	})
	if err == nil {
		t.Fatal("expected error when passphrase missing for key pair")
	}
	if !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestNewClient_RejectsPassphraseWithLegacyAPIKey(t *testing.T) {
	_, err := NewClient(ClientConfig{
		APIKey:     "legacy-token",
		Passphrase: "pw",
		BaseURL:    "wss://localhost:1",
	})
	if err == nil {
		t.Fatal("expected error when Passphrase set with legacy APIKey")
	}
}

func TestNewRestClient_RejectsKeyPairWithoutPassphrase(t *testing.T) {
	t.Setenv("GODARK_PASSPHRASE", "")
	t.Setenv("GDX_PASSPHRASE", "")
	_, err := NewRestClient(RestClientConfig{
		APIKeyID:  "gdk_xx",
		APISecret: "secret",
	})
	if err == nil {
		t.Fatal("expected error when passphrase missing for key pair")
	}
}
