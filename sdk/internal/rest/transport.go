// Package rest is the HTTP transport for the GoDark REST API at /api/v1.
//
// Mirrors the python and rust reference implementations: every endpoint
// returns a docs envelope `{ code, data, message?, request_id? }` which the
// wrapper unwraps; non-zero `code` becomes a typed [EnvelopeError]. All
// authenticated endpoints take a bearer token issued by `/auth/token`.
package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// EnvelopeError represents a docs envelope with code != 0.
type EnvelopeError struct {
	Code      int
	Message   string
	RequestID string
}

func (e *EnvelopeError) Error() string {
	return fmt.Sprintf("rest envelope error code=%d message=%q", e.Code, e.Message)
}

// Transport is a thin net/http wrapper that asserts the docs envelope shape.
type Transport struct {
	base    string
	client  *http.Client
	timeout time.Duration
}

// New returns a Transport rooted at base (e.g. "https://api.godark-dex.com").
// Pass nil for client to use a default 30s-timeout http.Client.
func New(base string, client *http.Client) *Transport {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Transport{
		base:    strings.TrimRight(base, "/"),
		client:  client,
		timeout: 30 * time.Second,
	}
}

// BaseURL returns the configured root URL (no trailing slash).
func (t *Transport) BaseURL() string { return t.base }

// SetClient swaps the underlying http.Client (useful for tests that need a
// transport with a custom transport.RoundTripper or shorter timeout).
func (t *Transport) SetClient(c *http.Client) { t.client = c }

// -----------------------------------------------------------------------
// Generic verbs
// -----------------------------------------------------------------------

func (t *Transport) doJSON(ctx context.Context, method, path string, bearer string, body any, query url.Values) (map[string]any, error) {
	raw, err := t.doJSONRaw(ctx, method, path, bearer, body, query)
	if err != nil {
		return nil, err
	}
	return unwrap(raw)
}

// doJSONRaw issues a request and returns the parsed top-level JSON
// object as-is, WITHOUT unwrapping the docs `{code, data, ...}`
// envelope. Use this only for endpoints that intentionally return their
// payload at the response root (e.g. legacy / non-docs APIs like
// shielded-pool, auth/me). Status >= 400 still surfaces as an error.
func (t *Transport) doJSONRaw(ctx context.Context, method, path string, bearer string, body any, query url.Values) (map[string]any, error) {
	v, err := t.doJSONValue(ctx, method, path, bearer, body, query)
	if err != nil {
		return nil, err
	}
	out, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s %s: expected JSON object at root", method, path)
	}
	return out, nil
}

// doJSONValue is like doJSONRaw but accepts any JSON root (object or array).
func (t *Transport) doJSONValue(ctx context.Context, method, path string, bearer string, body any, query url.Values) (any, error) {
	u := t.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, u, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("%s %s: server status %d: %s", method, u, resp.StatusCode, string(respBody))
	}
	if resp.StatusCode >= 400 {
		if len(respBody) == 0 {
			return nil, fmt.Errorf("%s %s: status %d (empty body)", method, u, resp.StatusCode)
		}
		return nil, fmt.Errorf("%s %s: status %d: %s", method, u, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var out any
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("%s %s: bad JSON (status %d): %w", method, u, resp.StatusCode, err)
	}
	return out, nil
}

func unwrap(env map[string]any) (map[string]any, error) {
	code := envCode(env)
	if code != 0 {
		msg, _ := env["message"].(string)
		reqID, _ := env["request_id"].(string)
		return nil, &EnvelopeError{Code: code, Message: msg, RequestID: reqID}
	}
	data, ok := env["data"].(map[string]any)
	if !ok {
		reqID, _ := env["request_id"].(string)
		return nil, &EnvelopeError{Code: 1500, Message: "missing data object", RequestID: reqID}
	}
	return data, nil
}

func envCode(env map[string]any) int {
	switch v := env["code"].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return 1
}

// -----------------------------------------------------------------------
// Endpoints
// -----------------------------------------------------------------------

// TimePublic calls `GET /api/v1/time`.
func (t *Transport) TimePublic(ctx context.Context) (map[string]any, error) {
	return t.doJSON(ctx, http.MethodGet, "/api/v1/time", "", nil, nil)
}

// AuthTokenClientCredentials performs the docs RFC-6749 client-credentials
// grant against `/api/v1/auth/token`.
func (t *Transport) AuthTokenClientCredentials(ctx context.Context, clientID, clientSecret, passphrase string) (map[string]any, error) {
	body := map[string]any{
		"grant_type":    "client_credentials",
		"client_id":     clientID,
		"client_secret": clientSecret,
	}
	if passphrase != "" {
		body["passphrase"] = passphrase
	}
	return t.doJSON(ctx, http.MethodPost, "/api/v1/auth/token", "", body, nil)
}

// AuthTokenLegacy issues `POST /api/v1/auth/token` with the legacy single
// opaque token (the python SDK's `api_key` constructor).
func (t *Transport) AuthTokenLegacy(ctx context.Context, token string) (map[string]any, error) {
	return t.doJSON(ctx, http.MethodPost, "/api/v1/auth/token", "", map[string]any{"token": token}, nil)
}

// SessionSetup is deprecated: ECDH REST session setup is retired (Noise XK is
// WS-only). GodarkRestClient never calls this; kept for transport unit tests.
func (t *Transport) SessionSetup(ctx context.Context, bearer, clientECDHPubKey string) (map[string]any, error) {
	return t.doJSON(ctx, http.MethodPost, "/api/v1/session/setup", bearer, map[string]any{
		"client_ecdh_pubkey": clientECDHPubKey,
	}, nil)
}

// PostOrdersEncrypted issues `POST /api/v1/orders` with an already-encrypted
// body. The header + ciphertext layout is constructed by the SDK.
func (t *Transport) PostOrdersEncrypted(ctx context.Context, bearer string, body map[string]any) (map[string]any, error) {
	return t.doJSON(ctx, http.MethodPost, "/api/v1/orders", bearer, body, nil)
}

// GetLeverage issues `GET /api/v1/leverage`.
func (t *Transport) GetLeverage(ctx context.Context, bearer string) (map[string]any, error) {
	return t.doJSON(ctx, http.MethodGet, "/api/v1/leverage", bearer, nil, nil)
}

// PostLeverageEncrypted issues `POST /api/v1/leverage` with an already-
// encrypted update-leverage body.
func (t *Transport) PostLeverageEncrypted(ctx context.Context, bearer string, body map[string]any) (map[string]any, error) {
	return t.doJSON(ctx, http.MethodPost, "/api/v1/leverage", bearer, body, nil)
}

// PostEncrypted issues an authenticated encrypted POST to an arbitrary
// `/api/v1/...` path (e.g. openOrders snapshot RPCs, massQuote).
func (t *Transport) PostEncrypted(ctx context.Context, bearer, path string, body map[string]any) (map[string]any, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return t.doJSON(ctx, http.MethodPost, path, bearer, body, nil)
}

// DeleteOrderEncrypted issues `DELETE /api/v1/orders/{order_id}` with an
// encrypted body.
func (t *Transport) DeleteOrderEncrypted(ctx context.Context, bearer, orderID string, body map[string]any) (map[string]any, error) {
	return t.doJSON(ctx, http.MethodDelete, "/api/v1/orders/"+url.PathEscape(orderID), bearer, body, nil)
}

// PatchOrderEncrypted issues `PATCH /api/v1/orders/{order_id}` with an
// encrypted modify body.
func (t *Transport) PatchOrderEncrypted(ctx context.Context, bearer, orderID string, body map[string]any) (map[string]any, error) {
	return t.doJSON(ctx, http.MethodPatch, "/api/v1/orders/"+url.PathEscape(orderID), bearer, body, nil)
}

// GetOrder issues `GET /api/v1/orders/{order_id}`.
func (t *Transport) GetOrder(ctx context.Context, bearer, orderID string) (map[string]any, error) {
	return t.doJSON(ctx, http.MethodGet, "/api/v1/orders/"+url.PathEscape(orderID), bearer, nil, nil)
}

// GetOrderByClientOrderID issues `GET /api/v1/orders?client_order_id=`.
func (t *Transport) GetOrderByClientOrderID(ctx context.Context, bearer, clientOrderID string) (map[string]any, error) {
	q := url.Values{}
	q.Set("client_order_id", clientOrderID)
	return t.doJSON(ctx, http.MethodGet, "/api/v1/orders", bearer, nil, q)
}

// RegisterClientOrderMapping pushes the `(coid, order_id)` mapping the SDK
// learned post-decrypt back to the edge so future coid lookups resolve.
func (t *Transport) RegisterClientOrderMapping(ctx context.Context, bearer, clientOrderID, orderID string) (map[string]any, error) {
	return t.doJSON(ctx, http.MethodPost, "/api/v1/orders/_register_coid", bearer, map[string]any{
		"client_order_id": clientOrderID,
		"order_id":        orderID,
	}, nil)
}

// RevokeToken issues `POST /api/v1/auth/token/revoke`. Best-effort: errors
// are typically silenced by callers.
func (t *Transport) RevokeToken(ctx context.Context, bearer string) (map[string]any, error) {
	return t.doJSON(ctx, http.MethodPost, "/api/v1/auth/token/revoke", bearer, nil, nil)
}

// GetShieldedPoolBalances issues
// `GET /api/v1/shielded-pool/balances/{owner}`. `owner` is the user's
// Solana base58 wallet pubkey (the same value `GetMe` returns as
// `wallet_address`). The edge returns its payload at the JSON root
// (no docs envelope), with all u64 raw amounts decimal-encoded as
// strings:
//
//	{
//	  "walletUsdtRaw":        "12345",
//	  "walletUsdtUi":          12.345,
//	  "pendingDepositsRaw":   "1000",
//	  "shieldedBalanceRaw":   "5000"
//	}
//
// Note: hitting this endpoint also nudges the edge's BalanceWatchService
// to start streaming shielded-balance pushes for (user, owner).
func (t *Transport) GetShieldedPoolBalances(ctx context.Context, bearer, owner string) (map[string]any, error) {
	return t.doJSONRaw(ctx, http.MethodGet, "/api/v1/shielded-pool/balances/"+url.PathEscape(owner), bearer, nil, nil)
}

// GetAuthMe issues `GET /api/v1/auth/me` and returns the authenticated
// user's profile (raw JSON root, snake_case keys -- not envelope-wrapped).
func (t *Transport) GetAuthMe(ctx context.Context, bearer string) (map[string]any, error) {
	return t.doJSONRaw(ctx, http.MethodGet, "/api/v1/auth/me", bearer, nil, nil)
}

func asObjectSlice(v any, path string) ([]map[string]any, error) {
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("GET %s: expected JSON array", path)
	}
	out := make([]map[string]any, 0, len(arr))
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("GET %s: element %d is not an object", path, i)
		}
		out = append(out, m)
	}
	return out, nil
}

// GetFundingRates issues `GET /api/v1/market-data/funding-rates` (public, raw array).
func (t *Transport) GetFundingRates(ctx context.Context) ([]map[string]any, error) {
	const path = "/api/v1/market-data/funding-rates"
	v, err := t.doJSONValue(ctx, http.MethodGet, path, "", nil, nil)
	if err != nil {
		return nil, err
	}
	return asObjectSlice(v, path)
}

// GetOpenInterest issues `GET /api/v1/market-data/open-interest` (public, raw array).
func (t *Transport) GetOpenInterest(ctx context.Context) ([]map[string]any, error) {
	const path = "/api/v1/market-data/open-interest"
	v, err := t.doJSONValue(ctx, http.MethodGet, path, "", nil, nil)
	if err != nil {
		return nil, err
	}
	return asObjectSlice(v, path)
}

// GetVolume issues `GET /api/v1/market-data/volume` (public, raw object).
func (t *Transport) GetVolume(ctx context.Context) (map[string]any, error) {
	return t.doJSONRaw(ctx, http.MethodGet, "/api/v1/market-data/volume", "", nil, nil)
}

// IsEnvelopeError reports whether err is (or wraps) an *EnvelopeError.
func IsEnvelopeError(err error) bool {
	var e *EnvelopeError
	return errors.As(err, &e)
}
