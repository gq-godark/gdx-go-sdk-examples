package godark_test

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	godark "github.com/gq-godark/gdx-go-sdk"
	gdxcrypto "github.com/gq-godark/gdx-go-sdk/internal/crypto"
	sequencerpb "github.com/gq-godark/gdx-go-sdk/proto/gdx/sequencer/v1"
)

const (
	restMockUserUUID = "00000000-0000-0000-0000-000000000123"
	// Solana base58 sentinel used in shielded-pool / auth-me mocks; the
	// shape mirrors a real owner pubkey (32 bytes -> ~44 base58 chars).
	restMockWallet = "9xQeWvG816bUx9EPjHmaT23yvVM2ZWbrrpZb9PusVFin"
)

// restMockEdge mocks the REST flow: auth/token -> session/setup -> encrypted
// orders. It exercises the same crypto primitives the real sequencer would,
// so a passing test proves the SDK roundtrips the wire format correctly.
type restMockEdge struct {
	t          *testing.T
	srv        *httptest.Server
	bearer     string
	serverPriv *ecdh.PrivateKey
	serverPub  []byte
	sessionKey []byte
	sessionID  uint64

	mu           sync.Mutex
	pushNonceCtr uint32
	gotPlace     int
	gotCancel    int
	gotBalance   int
	gotMe        int
	gotLeverage  int
	gotUpdLev    int
	levSettings  []map[string]any
}

func newRestMockEdge(t *testing.T) *restMockEdge {
	t.Helper()
	m := &restMockEdge{
		t:         t,
		bearer:    "test-bearer-token",
		sessionID: 0xCAFEBABE,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/token", m.handleAuthToken)
	mux.HandleFunc("/api/v1/auth/token/revoke", m.handleRevoke)
	mux.HandleFunc("/api/v1/session/setup", m.handleSessionSetup)
	mux.HandleFunc("/api/v1/orders", m.handleOrdersCollection)
	mux.HandleFunc("/api/v1/orders/", m.handleOrderItem)
	mux.HandleFunc("/api/v1/shielded-pool/balances/", m.handleShieldedPoolBalances)
	mux.HandleFunc("/api/v1/auth/me", m.handleAuthMe)
	mux.HandleFunc("/api/v1/leverage", m.handleLeverage)
	m.srv = httptest.NewServer(mux)
	return m
}

func (m *restMockEdge) close() { m.srv.Close() }

func (m *restMockEdge) baseURL() string { return m.srv.URL }

func (m *restMockEdge) writeOK(w http.ResponseWriter, data map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code": 0,
		"data": data,
	})
}

func (m *restMockEdge) writeErr(w http.ResponseWriter, status, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":    code,
		"message": msg,
	})
}

func (m *restMockEdge) handleLeverage(w http.ResponseWriter, r *http.Request) {
	if !m.authOK(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		m.mu.Lock()
		m.gotLeverage++
		settings := append([]map[string]any(nil), m.levSettings...)
		m.mu.Unlock()
		m.writeOK(w, map[string]any{"settings": settings})
	case http.MethodPost:
		m.mu.Lock()
		m.gotUpdLev++
		m.mu.Unlock()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			m.writeErr(w, 400, 1400, "read body: "+err.Error())
			return
		}
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			m.writeErr(w, 400, 1400, "bad json")
			return
		}
		header, _ := req["header"].(map[string]any)
		if header == nil {
			m.writeErr(w, 400, 1400, "missing header")
			return
		}
		if header["request_type"] != "update_leverage" {
			m.writeErr(w, 400, 1400, "bad request_type")
			return
		}
		if header["leverage"] == nil {
			m.writeErr(w, 400, 1400, "missing header.leverage")
			return
		}
		corrID := m.corrIDFromReq(req)
		encrypted := m.buildEncryptedAck(0, corrID)
		m.writeOK(w, encrypted)
	default:
		w.WriteHeader(405)
	}
}

func (m *restMockEdge) handleAuthToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		m.writeErr(w, 400, 1400, "bad json")
		return
	}
	if tok, _ := body["token"].(string); tok == "bad-token" {
		m.writeErr(w, 401, 1401, "invalid api key")
		return
	}
	if grant, _ := body["grant_type"].(string); grant == "client_credentials" {
		if body["passphrase"] == nil || body["passphrase"] == "" {
			m.writeErr(w, 400, 1400, "passphrase is required")
			return
		}
	}
	m.writeOK(w, map[string]any{
		"access_token": m.bearer,
		"user_uuid":    restMockUserUUID,
	})
}

func (m *restMockEdge) handleRevoke(w http.ResponseWriter, _ *http.Request) {
	m.writeOK(w, map[string]any{"revoked": true})
}

func (m *restMockEdge) handleShieldedPoolBalances(w http.ResponseWriter, r *http.Request) {
	if !m.authOK(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	owner := strings.TrimPrefix(r.URL.Path, "/api/v1/shielded-pool/balances/")
	if owner == "" {
		m.writeErr(w, 400, 1400, "missing owner")
		return
	}
	m.mu.Lock()
	m.gotBalance++
	m.mu.Unlock()

	// Raw payload mirrors the real edge: NOT wrapped in the docs
	// envelope, camelCase keys, all u64 raw amounts decimal-encoded as
	// strings (so they roundtrip JSON cleanly). walletUsdtUi is a real
	// JSON number / null.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"walletUsdtRaw":      "12345000",
		"walletUsdtUi":       12.345,
		"pendingDepositsRaw": "1000000",
		"shieldedBalanceRaw": "5000000",
	})
}

func (m *restMockEdge) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	if !m.authOK(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	m.mu.Lock()
	m.gotMe++
	m.mu.Unlock()
	// /auth/me also returns raw JSON (no docs envelope), snake_case keys.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":              restMockUserUUID,
		"dynamic_user_id": "dyn-mock",
		"email":           "trader@example.com",
		"wallet_address":  restMockWallet,
		"referral_code":   "FRIEND123",
		"tier":            "VIP1",
	})
}

func (m *restMockEdge) handleSessionSetup(w http.ResponseWriter, r *http.Request) {
	if !m.authOK(w, r) {
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		m.writeErr(w, 400, 1400, "bad json")
		return
	}
	clientPubB64, _ := body["client_ecdh_pubkey"].(string)
	clientPub, err := base64.StdEncoding.DecodeString(clientPubB64)
	if err != nil {
		m.writeErr(w, 400, 1400, "bad client pubkey")
		return
	}

	priv, pub, err := gdxcrypto.GenerateEphemeralKeypair()
	if err != nil {
		m.writeErr(w, 500, 1500, "server keypair: "+err.Error())
		return
	}
	key, err := gdxcrypto.DeriveSessionKey(priv, pub, clientPub)
	if err != nil {
		m.writeErr(w, 500, 1500, "derive: "+err.Error())
		return
	}
	m.serverPriv = priv
	m.serverPub = pub
	m.sessionKey = key
	m.writeOK(w, map[string]any{
		"sequencer_ecdh_pubkey": base64.StdEncoding.EncodeToString(pub),
		"session_id":            m.sessionID,
	})
}

func (m *restMockEdge) handleOrdersCollection(w http.ResponseWriter, r *http.Request) {
	if !m.authOK(w, r) {
		return
	}
	switch r.Method {
	case http.MethodPost:
		m.handlePlace(w, r)
	case http.MethodGet:
		m.handleLookupByCOID(w, r)
	default:
		w.WriteHeader(405)
	}
}

func (m *restMockEdge) handleOrderItem(w http.ResponseWriter, r *http.Request) {
	if !m.authOK(w, r) {
		return
	}
	switch r.Method {
	case http.MethodDelete:
		m.handleCancel(w, r)
	case http.MethodPatch:
		m.handleModify(w, r)
	case http.MethodGet:
		m.handleGet(w, r)
	default:
		w.WriteHeader(405)
	}
}

func (m *restMockEdge) handlePlace(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	m.gotPlace++
	m.mu.Unlock()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		m.writeErr(w, 400, 1400, "read body: "+err.Error())
		return
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		m.writeErr(w, 400, 1400, "bad json")
		return
	}
	corrID := m.corrIDFromReq(req)
	encrypted := m.buildEncryptedAck(7777, corrID)
	m.writeOK(w, encrypted)
}

func (m *restMockEdge) handleCancel(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	m.gotCancel++
	m.mu.Unlock()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		m.writeErr(w, 400, 1400, "read body: "+err.Error())
		return
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		m.writeErr(w, 400, 1400, "bad json")
		return
	}
	corrID := m.corrIDFromReq(req)
	encrypted := m.buildEncryptedAck(7777, corrID)
	m.writeOK(w, encrypted)
}

func (m *restMockEdge) handleModify(w http.ResponseWriter, _ *http.Request) {
	// Not exercised by the test but kept in shape with python/rust.
	m.writeOK(w, map[string]any{"success": true, "order_id": "1", "sequence": "1"})
}

func (m *restMockEdge) handleGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/orders/")
	m.writeOK(w, map[string]any{
		"order_id": id,
		"status":   "FILLED",
	})
}

func (m *restMockEdge) handleLookupByCOID(w http.ResponseWriter, r *http.Request) {
	coid := r.URL.Query().Get("client_order_id")
	if coid == "" {
		m.writeErr(w, 400, 1400, "missing client_order_id")
		return
	}
	m.writeOK(w, map[string]any{
		"client_order_id": coid,
		"order_id":        "9999",
	})
}

func (m *restMockEdge) authOK(w http.ResponseWriter, r *http.Request) bool {
	hdr := r.Header.Get("Authorization")
	if hdr != "Bearer "+m.bearer {
		m.writeErr(w, 401, 1401, "missing or bad bearer")
		return false
	}
	return true
}

func (m *restMockEdge) corrIDFromReq(req map[string]any) []byte {
	header, _ := req["header"].(map[string]any)
	s, _ := header["correlation_id"].(string)
	b, err := identityToBytes(s)
	if err != nil {
		return make([]byte, 16)
	}
	return b
}

func (m *restMockEdge) buildEncryptedAck(orderID uint64, corrID []byte) map[string]any {
	// Direct AckMessage (NodeResponse wrapper removed in hotpath-edge-frames).
	ack := &sequencerpb.AckMessage{
		Sequence:      42,
		OrderId:       orderID,
		CorrelationId: corrID,
		AckOutcome: &sequencerpb.AckOutcomeWire{
			Kind: sequencerpb.AckOutcomeKind_ACK_OUTCOME_KIND_APPLIED,
		},
	}
	body, err := proto.Marshal(ack)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	m.mu.Lock()
	pushNonce := m.pushNonceCtr
	m.pushNonceCtr++
	m.mu.Unlock()

	aad, err := godark.BuildResponseHeaderAAD(
		uuidStrToBytes(restMockUserUUID),
		"ack",
		uint32(len(body)+gdxcrypto.GCMTagLen),
		uint64(pushNonce),
		0,
		corrID,
		42,
	)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	ct, err := gdxcrypto.Encrypt(m.sessionKey, m.sessionID, pushNonce, aad, body)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	return map[string]any{
		"encrypted":      true,
		"message_type":   "ack",
		"header":         map[string]any{"message_type": "ack"},
		"ciphertext":     base64.StdEncoding.EncodeToString(ct),
		"nonce":          pushNonce,
		"fencing_epoch":  0,
		"correlation_id": new(big.Int).SetBytes(corrID).String(),
		"session_seq":    42,
	}
}

// -----------------------------------------------------------------------
// Tests
// -----------------------------------------------------------------------

func TestRestClient_KeyPairConnectSendsPassphrase(t *testing.T) {
	m := newRestMockEdge(t)
	defer m.close()

	client, err := godark.NewRestClient(godark.RestClientConfig{
		APIKeyID:   "gdk_test",
		APISecret:  "secret-with:colons",
		Passphrase: "my-pass",
		BaseURL:    m.baseURL(),
	})
	if err != nil {
		t.Fatalf("NewRestClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Disconnect(ctx)

	if got := client.UserUUID(); got != restMockUserUUID {
		t.Fatalf("UserUUID = %q, want %q", got, restMockUserUUID)
	}
}

func TestRestClient_PlaceCancelRoundTrip(t *testing.T) {
	t.Skip("encrypted REST trading was removed; order flow requires a Noise XK WebSocket session")

	m := newRestMockEdge(t)
	defer m.close()

	client, err := godark.NewRestClient(godark.RestClientConfig{
		APIKey:  "good-token",
		BaseURL: m.baseURL(),
	})
	if err != nil {
		t.Fatalf("NewRestClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Disconnect(ctx)

	if got := client.UserUUID(); got != restMockUserUUID {
		t.Fatalf("UserUUID = %q, want %q", got, restMockUserUUID)
	}
	if !client.IsSessionEstablished() {
		t.Fatalf("session not established after Connect")
	}

	ack, err := client.PlaceOrder(ctx, godark.PlaceOrderRestRequest{
		PlaceOrderRequest: godark.PlaceOrderRequest{
			Symbol:    "BTC-USDC-PERP",
			Side:      godark.SideSell,
			OrderType: godark.OrderTypeLimit,
			Price:     999_999,
			Quantity:  0.01,
		},
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if !ack.Success || ack.OrderID != "7777" {
		t.Fatalf("PlaceOrder ack: %+v", ack)
	}

	cancelAck, err := client.CancelOrder(ctx, ack.OrderID, "BTC-USDC-PERP")
	if err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if !cancelAck.Success {
		t.Fatalf("CancelOrder ack: %+v", cancelAck)
	}

	if m.gotPlace != 1 || m.gotCancel != 1 {
		t.Fatalf("server counters: place=%d cancel=%d", m.gotPlace, m.gotCancel)
	}
}

func TestRestClient_AuthFailure(t *testing.T) {
	m := newRestMockEdge(t)
	defer m.close()

	client, err := godark.NewRestClient(godark.RestClientConfig{
		APIKey:  "bad-token",
		BaseURL: m.baseURL(),
	})
	if err != nil {
		t.Fatalf("NewRestClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = client.Connect(ctx)
	if err == nil {
		t.Fatalf("Connect: expected AuthenticationError, got nil")
	}
	var ae *godark.AuthenticationError
	if !errors.As(err, &ae) {
		t.Fatalf("Connect: expected *AuthenticationError, got %T (%v)", err, err)
	}
}

func TestRestClient_AwaitTerminalStatus(t *testing.T) {
	m := newRestMockEdge(t)
	defer m.close()

	client, err := godark.NewRestClient(godark.RestClientConfig{
		APIKey:  "good-token",
		BaseURL: m.baseURL(),
	})
	if err != nil {
		t.Fatalf("NewRestClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Disconnect(ctx)

	// Mock always reports FILLED, so this should return on the first poll.
	row, err := client.AwaitTerminalStatus(ctx, "12345", 2*time.Second, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("AwaitTerminalStatus: %v", err)
	}
	if got, _ := row["status"].(string); got != "FILLED" {
		t.Fatalf("status: got %q", got)
	}
}

func TestRestClient_CancelByClientID(t *testing.T) {
	t.Skip("encrypted REST trading was removed; order flow requires a Noise XK WebSocket session")

	m := newRestMockEdge(t)
	defer m.close()

	client, err := godark.NewRestClient(godark.RestClientConfig{
		APIKey:  "good-token",
		BaseURL: m.baseURL(),
	})
	if err != nil {
		t.Fatalf("NewRestClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Disconnect(ctx)

	// Cancel by client-order-id without ever having placed it locally:
	// the SDK should fall back to GET /orders?client_order_id= -> "9999"
	// (the mock's canned answer), then DELETE /orders/9999.
	ack, err := client.CancelOrderByClientID(ctx, "my-coid", "BTC-USDC-PERP")
	if err != nil {
		t.Fatalf("CancelOrderByClientID: %v", err)
	}
	if !ack.Success {
		t.Fatalf("CancelOrderByClientID ack: %+v", ack)
	}
	if m.gotCancel != 1 {
		t.Fatalf("server saw %d cancels, want 1", m.gotCancel)
	}
}

func TestRestClient_GetBalance(t *testing.T) {
	m := newRestMockEdge(t)
	defer m.close()

	client, err := godark.NewRestClient(godark.RestClientConfig{
		APIKey:  "good-token",
		BaseURL: m.baseURL(),
	})
	if err != nil {
		t.Fatalf("NewRestClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Disconnect(ctx)

	bal, err := client.GetBalance(ctx, restMockWallet)
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if bal.WalletUSDTRaw != 12_345_000 {
		t.Fatalf("WalletUSDTRaw: got %d, want 12345000", bal.WalletUSDTRaw)
	}
	if bal.PendingDepositsRaw != 1_000_000 {
		t.Fatalf("PendingDepositsRaw: got %d, want 1000000", bal.PendingDepositsRaw)
	}
	if bal.ShieldedBalanceRaw != 5_000_000 {
		t.Fatalf("ShieldedBalanceRaw: got %d, want 5000000", bal.ShieldedBalanceRaw)
	}
	if bal.WalletUSDTUI != 12.345 {
		t.Fatalf("WalletUSDTUI: got %v, want 12.345", bal.WalletUSDTUI)
	}
	if m.gotBalance != 1 {
		t.Fatalf("server saw %d balance calls, want 1", m.gotBalance)
	}
}

func TestRestClient_GetBalance_NotConnected(t *testing.T) {
	m := newRestMockEdge(t)
	defer m.close()

	client, err := godark.NewRestClient(godark.RestClientConfig{
		APIKey:  "good-token",
		BaseURL: m.baseURL(),
	})
	if err != nil {
		t.Fatalf("NewRestClient: %v", err)
	}
	// Note: no Connect() call. GetBalance must refuse to make the HTTP
	// call without a bearer; this is the same pre-condition the other
	// authenticated read methods (GetOrder, GetOrderByClientID) enforce.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := client.GetBalance(ctx, restMockWallet); err == nil {
		t.Fatalf("GetBalance: expected error when not connected, got nil")
	}
	if m.gotBalance != 0 {
		t.Fatalf("server saw %d balance calls, want 0 (must not be hit without bearer)", m.gotBalance)
	}
}

func TestRestClient_GetBalance_MissingOwner(t *testing.T) {
	m := newRestMockEdge(t)
	defer m.close()

	client, err := godark.NewRestClient(godark.RestClientConfig{
		APIKey:  "good-token",
		BaseURL: m.baseURL(),
	})
	if err != nil {
		t.Fatalf("NewRestClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Disconnect(ctx)

	// Empty / whitespace owner must fail fast on the client without
	// burning a server round-trip (the edge would 404 anyway, but the
	// SDK should give a descriptive error pointing at GetMyBalance).
	if _, err := client.GetBalance(ctx, "   "); err == nil {
		t.Fatalf("GetBalance(\"\"): expected error, got nil")
	}
	if m.gotBalance != 0 {
		t.Fatalf("server saw %d balance calls, want 0", m.gotBalance)
	}
}

func TestRestClient_GetBalance_HTTPError(t *testing.T) {
	// Mock the shielded-pool endpoint returning a 403 with a free-text
	// body (NOT the docs envelope -- /shielded-pool routes return raw
	// payloads). The SDK must surface the status + body, not silently
	// zero-value the Balance.
	m := newRestMockEdge(t)
	defer m.close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/auth/token":
			m.handleAuthToken(w, r)
		case r.URL.Path == "/api/v1/session/setup":
			m.handleSessionSetup(w, r)
		case strings.HasPrefix(r.URL.Path, "/api/v1/shielded-pool/balances/"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":"vault not provisioned"}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	client, err := godark.NewRestClient(godark.RestClientConfig{
		APIKey:  "good-token",
		BaseURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("NewRestClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Disconnect(ctx)

	bal, err := client.GetBalance(ctx, restMockWallet)
	if err == nil {
		t.Fatalf("GetBalance: expected HTTP error, got %+v", bal)
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "vault not provisioned") {
		t.Fatalf("GetBalance error should surface status+body; got: %v", err)
	}
}

func TestRestClient_GetMe(t *testing.T) {
	m := newRestMockEdge(t)
	defer m.close()

	client, err := godark.NewRestClient(godark.RestClientConfig{
		APIKey:  "good-token",
		BaseURL: m.baseURL(),
	})
	if err != nil {
		t.Fatalf("NewRestClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Disconnect(ctx)

	me, err := client.GetMe(ctx)
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	if me.ID != restMockUserUUID {
		t.Fatalf("Me.ID = %q, want %q", me.ID, restMockUserUUID)
	}
	if me.WalletAddress != restMockWallet {
		t.Fatalf("Me.WalletAddress = %q, want %q", me.WalletAddress, restMockWallet)
	}
	if me.Tier != "VIP1" {
		t.Fatalf("Me.Tier = %q, want %q", me.Tier, "VIP1")
	}
	if m.gotMe != 1 {
		t.Fatalf("server saw %d /me calls, want 1", m.gotMe)
	}
}

func TestRestClient_LeverageRoundTrip(t *testing.T) {
	t.Skip("encrypted REST trading was removed; leverage updates require a Noise XK WebSocket session")

	m := newRestMockEdge(t)
	defer m.close()

	m.levSettings = []map[string]any{{"symbol_id": 1, "leverage": 3}}

	client, err := godark.NewRestClient(godark.RestClientConfig{
		APIKey:  "good-token",
		BaseURL: m.baseURL(),
	})
	if err != nil {
		t.Fatalf("NewRestClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Disconnect(ctx)

	settings, err := client.GetLeverage(ctx)
	if err != nil {
		t.Fatalf("GetLeverage: %v", err)
	}
	if len(settings.Settings) != 1 || settings.Settings[0].Leverage != 3 {
		t.Fatalf("GetLeverage: %+v", settings)
	}
	if m.gotLeverage != 1 {
		t.Fatalf("server saw %d GET /leverage, want 1", m.gotLeverage)
	}

	ack, err := client.UpdateLeverage(ctx, "BTC-USDC-PERP", 5)
	if err != nil {
		t.Fatalf("UpdateLeverage: %v", err)
	}
	if !ack.Success {
		t.Fatalf("UpdateLeverage ack: %+v", ack)
	}
	if m.gotUpdLev != 1 {
		t.Fatalf("server saw %d POST /leverage, want 1", m.gotUpdLev)
	}
}

func TestRestClient_GetMyBalance(t *testing.T) {
	m := newRestMockEdge(t)
	defer m.close()

	client, err := godark.NewRestClient(godark.RestClientConfig{
		APIKey:  "good-token",
		BaseURL: m.baseURL(),
	})
	if err != nil {
		t.Fatalf("NewRestClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Disconnect(ctx)

	// First call: no cached wallet, so the SDK does /me then /balances.
	bal, err := client.GetMyBalance(ctx)
	if err != nil {
		t.Fatalf("GetMyBalance (cold): %v", err)
	}
	if bal.WalletUSDTRaw != 12_345_000 {
		t.Fatalf("WalletUSDTRaw: got %d, want 12345000", bal.WalletUSDTRaw)
	}
	if m.gotMe != 1 {
		t.Fatalf("first GetMyBalance: gotMe=%d, want 1", m.gotMe)
	}

	// Second call: wallet is cached, so /me must NOT be re-hit; only
	// /balances should grow.
	if _, err := client.GetMyBalance(ctx); err != nil {
		t.Fatalf("GetMyBalance (warm): %v", err)
	}
	if m.gotMe != 1 {
		t.Fatalf("warm GetMyBalance: gotMe=%d, want 1 (must reuse cached wallet)", m.gotMe)
	}
	if m.gotBalance != 2 {
		t.Fatalf("warm GetMyBalance: gotBalance=%d, want 2", m.gotBalance)
	}
}
