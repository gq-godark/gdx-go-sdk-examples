package rest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEnvelopeError_NonZeroCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":       1401,
			"message":    "invalid api key",
			"request_id": "req-123",
		})
	}))
	defer srv.Close()

	tr := New(srv.URL, nil)
	_, err := tr.TimePublic(context.Background())
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	var env *EnvelopeError
	if !errors.As(err, &env) {
		t.Fatalf("expected *EnvelopeError, got %T (%v)", err, err)
	}
	if env.Code != 1401 || env.Message != "invalid api key" || env.RequestID != "req-123" {
		t.Fatalf("EnvelopeError fields wrong: %+v", env)
	}
	if !IsEnvelopeError(err) {
		t.Fatalf("IsEnvelopeError false-negative")
	}
}

func TestEnvelopeOK_UnwrapsData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"now_ns": 1_700_000_000_000_000_000},
		})
	}))
	defer srv.Close()

	tr := New(srv.URL, nil)
	data, err := tr.TimePublic(context.Background())
	if err != nil {
		t.Fatalf("TimePublic: %v", err)
	}
	if data["now_ns"] == nil {
		t.Fatalf("expected now_ns in unwrapped data, got %+v", data)
	}
}

func TestAuthTokenClientCredentials_IncludesPassphrase(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/token" {
			w.WriteHeader(404)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"access_token": "tok"},
		})
	}))
	defer srv.Close()

	tr := New(srv.URL, nil)
	_, err := tr.AuthTokenClientCredentials(context.Background(), "gdk_id", "secret", "my-pass")
	if err != nil {
		t.Fatalf("AuthTokenClientCredentials: %v", err)
	}
	if gotBody["passphrase"] != "my-pass" {
		t.Fatalf("passphrase in body = %v, want my-pass", gotBody["passphrase"])
	}
	if gotBody["client_id"] != "gdk_id" || gotBody["client_secret"] != "secret" {
		t.Fatalf("unexpected body: %+v", gotBody)
	}
}

func TestEnvelopeOK_MissingDataIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0})
	}))
	defer srv.Close()

	tr := New(srv.URL, nil)
	_, err := tr.TimePublic(context.Background())
	if err == nil {
		t.Fatalf("expected error for code:0 with no data")
	}
}

func TestGetLeverage_SendsBearerNoBody(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if r.ContentLength > 0 {
			t.Errorf("GET /leverage should have no body, got ContentLength=%d", r.ContentLength)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"settings": []map[string]any{
					{"symbol_id": 1, "leverage": 5},
				},
			},
		})
	}))
	defer srv.Close()

	tr := New(srv.URL, nil)
	data, err := tr.GetLeverage(context.Background(), "my-bearer")
	if err != nil {
		t.Fatalf("GetLeverage: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/api/v1/leverage" {
		t.Fatalf("request = %s %s, want GET /api/v1/leverage", gotMethod, gotPath)
	}
	if gotAuth != "Bearer my-bearer" {
		t.Fatalf("Authorization = %q, want Bearer my-bearer", gotAuth)
	}
	settings, _ := data["settings"].([]any)
	if len(settings) != 1 {
		t.Fatalf("settings = %+v, want 1 row", data["settings"])
	}
}

func TestPostLeverageEncrypted_SendsJSONWithHeaderLeverage(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/leverage" {
			w.WriteHeader(404)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"success": true},
		})
	}))
	defer srv.Close()

	tr := New(srv.URL, nil)
	_, err := tr.PostLeverageEncrypted(context.Background(), "tok", map[string]any{
		"header": map[string]any{
			"symbol_id":      1,
			"request_type":   "update_leverage",
			"nonce":          42,
			"body_length":    128,
			"correlation_id": "00000000-0000-0000-0000-000000000001",
			"leverage":       5,
		},
		"ciphertext": "abc123",
	})
	if err != nil {
		t.Fatalf("PostLeverageEncrypted: %v", err)
	}
	header, _ := gotBody["header"].(map[string]any)
	if header == nil {
		t.Fatalf("missing header in body: %+v", gotBody)
	}
	if header["request_type"] != "update_leverage" {
		t.Fatalf("request_type = %v, want update_leverage", header["request_type"])
	}
	if header["leverage"] != float64(5) {
		t.Fatalf("header.leverage = %v, want 5", header["leverage"])
	}
	if gotBody["ciphertext"] != "abc123" {
		t.Fatalf("ciphertext = %v, want abc123", gotBody["ciphertext"])
	}
}

func TestMarketDataPublicEndpoints_RawJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected Authorization header")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/market-data/funding-rates":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"symbol_id": 1, "current_rate": "0.00001"}})
		case "/api/v1/market-data/open-interest":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"symbol_id": 1, "open_interest": "10"}})
		case "/api/v1/market-data/volume":
			_ = json.NewEncoder(w).Encode(map[string]any{"total_volume_24h": "1", "symbols": []any{}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	tr := New(srv.URL, nil)
	rates, err := tr.GetFundingRates(context.Background())
	if err != nil {
		t.Fatalf("GetFundingRates: %v", err)
	}
	if rates[0]["symbol_id"] != float64(1) {
		t.Fatalf("funding symbol_id = %v", rates[0]["symbol_id"])
	}
	oi, err := tr.GetOpenInterest(context.Background())
	if err != nil {
		t.Fatalf("GetOpenInterest: %v", err)
	}
	if oi[0]["open_interest"] != "10" {
		t.Fatalf("open_interest = %v", oi[0]["open_interest"])
	}
	vol, err := tr.GetVolume(context.Background())
	if err != nil {
		t.Fatalf("GetVolume: %v", err)
	}
	if vol["total_volume_24h"] != "1" {
		t.Fatalf("volume = %v", vol["total_volume_24h"])
	}
}
