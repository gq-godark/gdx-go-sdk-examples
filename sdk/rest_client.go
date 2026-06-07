package godark

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	gdxcrypto "github.com/gq-godark/gdx-go-sdk/internal/crypto"
	"github.com/gq-godark/gdx-go-sdk/internal/identity"
	"github.com/gq-godark/gdx-go-sdk/internal/rest"
	"github.com/gq-godark/gdx-go-sdk/internal/session"
)

const defaultRestBaseURL = "https://api.godark-dex.com"

// RestClientConfig is the constructor input for NewRestClient. Either APIKey
// (legacy single opaque key) OR APIKeyID + APISecret (key-pair) must be set.
type RestClientConfig struct {
	APIKey     string
	APIKeyID   string
	APISecret  string
	Passphrase string

	// BaseURL is the REST origin (e.g. "https://api.godark-dex.com"). If
	// empty, the SDK tries `GODARK_REST_URL` / `GDX_REST_URL`, then derives
	// from `GODARK_EDGE_URL` (translating `wss://host/ws/v1` ->
	// `https://host`), then falls back to the default production URL.
	BaseURL string

	// UserUUID is a fallback when the auth response omits `user_uuid` (some
	// local edges do this). Also read from `GODARK_USER_UUID` / `GDX_USER_UUID`.
	UserUUID string

	// SymbolMap overrides the embedded default symbol map.
	SymbolMap map[string]int64

	// HTTPClient is forwarded to the REST transport. Pass nil for a 30s
	// default.
	HTTPClient *http.Client
}

// GodarkRestClient is the REST trading client. Same crypto contract as
// GodarkClient: orders are AES-256-GCM-encrypted under a per-session key
// derived via X25519 ECDH; the edge stays stateless and only routes the
// encrypted envelope.
type GodarkRestClient struct {
	legacyToken string
	apiKeyID    string
	apiSecret   string
	passphrase  string
	baseURL     string
	symbolMap  map[string]int64
	http       *rest.Transport
	session    *session.CryptoSession
	fallback   string

	mu             sync.RWMutex
	bearer         string
	userUUID       string
	walletAddr     string
	localCOIDIndex map[string]string
}

// NewRestClient validates the config and returns an unconnected client.
// Call Connect to obtain a bearer token and complete ECDH session setup.
func NewRestClient(cfg RestClientConfig) (*GodarkRestClient, error) {
	creds, err := resolveRestCredentials(RestClientConfig{
		APIKey:     cfg.APIKey,
		APIKeyID:   cfg.APIKeyID,
		APISecret:  cfg.APISecret,
		Passphrase: cfg.Passphrase,
	})
	if err != nil {
		return nil, err
	}
	base := resolveRestBaseURL(cfg.BaseURL)
	symMap := cfg.SymbolMap
	if symMap == nil {
		symMap = DefaultSymbolMap()
	}
	return &GodarkRestClient{
		legacyToken:    creds.legacyToken,
		apiKeyID:       creds.apiKeyID,
		apiSecret:      creds.apiSecret,
		passphrase:     creds.passphrase,
		baseURL:        base,
		symbolMap:      symMap,
		http:           rest.New(base, cfg.HTTPClient),
		session:        &session.CryptoSession{},
		fallback:       resolveUserUUID(cfg.UserUUID),
		localCOIDIndex: make(map[string]string),
	}, nil
}

type restCredentials struct {
	legacyToken string
	apiKeyID    string
	apiSecret   string
	passphrase  string
}

func resolveRestCredentials(cfg RestClientConfig) (restCredentials, error) {
	if cfg.APIKeyID != "" || cfg.APISecret != "" {
		if cfg.APIKeyID == "" || cfg.APISecret == "" {
			return restCredentials{}, errors.New("APIKeyID and APISecret must be provided together")
		}
		if cfg.APIKey != "" {
			return restCredentials{}, errors.New("use either APIKey or (APIKeyID + APISecret), not both")
		}
		passphrase, err := resolvePassphrase(cfg.Passphrase)
		if err != nil {
			return restCredentials{}, err
		}
		return restCredentials{
			apiKeyID:   cfg.APIKeyID,
			apiSecret:  cfg.APISecret,
			passphrase: passphrase,
		}, nil
	}
	if cfg.APIKey != "" {
		if strings.TrimSpace(cfg.Passphrase) != "" {
			return restCredentials{}, errors.New("Passphrase must not be set when using legacy APIKey")
		}
		return restCredentials{legacyToken: cfg.APIKey}, nil
	}
	return restCredentials{}, errors.New("provide APIKey or both APIKeyID + APISecret")
}

// IsSessionEstablished reports whether ECDH session setup completed.
func (c *GodarkRestClient) IsSessionEstablished() bool {
	return c.session.IsEstablished()
}

// BaseURL returns the resolved REST origin.
func (c *GodarkRestClient) BaseURL() string { return c.baseURL }

// UserUUID returns the authenticated user's canonical UUID. Empty until
// Connect succeeds.
func (c *GodarkRestClient) UserUUID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.userUUID
}

// BearerToken returns the active bearer token (or "" if not connected).
func (c *GodarkRestClient) BearerToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.bearer
}

// Connect performs `auth/token` then `session/setup` (ECDH). After this
// returns nil, encrypted orders can flow.
func (c *GodarkRestClient) Connect(ctx context.Context) error {
	var (
		authData map[string]any
		err      error
	)
	if c.apiKeyID != "" {
		authData, err = c.http.AuthTokenClientCredentials(ctx, c.apiKeyID, c.apiSecret, c.passphrase)
	} else {
		authData, err = c.http.AuthTokenLegacy(ctx, c.legacyToken)
	}
	if err != nil {
		return newAuthenticationError(err.Error())
	}

	bearer, _ := authData["access_token"].(string)
	if bearer == "" {
		bearer, _ = authData["token"].(string)
	}
	if bearer == "" {
		return newAuthenticationError("auth/token missing access_token/token")
	}

	uid, _ := authData["user_uuid"].(string)
	if uid == "" {
		uid = c.fallback
	}
	if uid == "" {
		return newAuthenticationError(
			"REST auth succeeded but user_uuid missing in response and no fallback " +
				"provided via constructor or GODARK_USER_UUID / GDX_USER_UUID env vars",
		)
	}

	c.mu.Lock()
	c.bearer = bearer
	c.userUUID = uid
	c.mu.Unlock()

	pubB64, err := c.session.GenerateKeypair()
	if err != nil {
		return newSessionError(fmt.Sprintf("generate keypair: %v", err))
	}
	sessData, err := c.http.SessionSetup(ctx, bearer, pubB64)
	if err != nil {
		return newSessionError(err.Error())
	}
	seqPub, _ := sessData["sequencer_ecdh_pubkey"].(string)
	if seqPub == "" {
		seqPub, _ = sessData["server_ecdh_pubkey"].(string)
	}
	sidRaw := sessData["session_id"]
	var sid uint64
	switch v := sidRaw.(type) {
	case float64:
		sid = uint64(v)
	case int:
		sid = uint64(v)
	case int64:
		sid = uint64(v)
	case uint64:
		sid = v
	case string:
		parsed, perr := strconv.ParseUint(v, 10, 64)
		if perr != nil {
			return newSessionError(fmt.Sprintf("invalid session_id %q: %v", v, perr))
		}
		sid = parsed
	default:
		return newSessionError(fmt.Sprintf("missing or invalid session_id type %T", sidRaw))
	}
	if err := c.session.Establish(seqPub, sid); err != nil {
		return newSessionError(err.Error())
	}
	return nil
}

// Disconnect revokes the bearer and resets the local ECDH session.
// Revocation is best-effort: errors are silently ignored.
func (c *GodarkRestClient) Disconnect(ctx context.Context) error {
	c.mu.RLock()
	bearer := c.bearer
	c.mu.RUnlock()
	if bearer != "" {
		_, _ = c.http.RevokeToken(ctx, bearer)
	}
	c.mu.Lock()
	c.bearer = ""
	c.userUUID = ""
	c.walletAddr = ""
	c.localCOIDIndex = make(map[string]string)
	c.mu.Unlock()
	c.session.Reset()
	return nil
}

// -----------------------------------------------------------------------
// Trading
// -----------------------------------------------------------------------

// PlaceOrderRestRequest extends PlaceOrderRequest with an optional
// ClientOrderID used by the REST routing index. The cleartext field is
// additive: the encrypted body still carries the canonical order id once
// the sequencer assigns one.
type PlaceOrderRestRequest struct {
	PlaceOrderRequest
	ClientOrderID string
}

// PlaceOrder sends an encrypted place via `POST /api/v1/orders`.
func (c *GodarkRestClient) PlaceOrder(ctx context.Context, req PlaceOrderRestRequest) (*OrderAck, error) {
	if err := c.ensureReady(); err != nil {
		return nil, err
	}
	if req.Symbol == "" || req.Side == "" || req.OrderType == "" {
		return nil, errors.New("PlaceOrder: Symbol, Side, and OrderType are required")
	}
	symbolID, err := c.resolveSymbol(req.Symbol)
	if err != nil {
		return nil, err
	}
	tif := req.TimeInForce
	if tif == "" {
		tif = TimeInForceGTC
	}

	corrID := newCorrelationID()
	var pricePtr *float64
	if req.Price != 0 {
		p := req.Price
		pricePtr = &p
	}

	plaintext, err := BuildPlaceOrderRequest(
		uint64(symbolID),
		req.Side, req.OrderType,
		req.Quantity,
		c.userUUIDBytes(),
		pricePtr,
		tif,
		req.AON,
		req.MinFillSize,
		req.ExpiryTime,
		corrID,
		uint64(time.Now().UnixNano()),
	)
	if err != nil {
		return nil, err
	}

	ack, err := c.sendEncrypted(ctx, "place", uint64(symbolID), plaintext, corrID, req.ClientOrderID, "POST", "")
	if err != nil {
		return nil, err
	}

	// Best-effort: register the (client_order_id -> order_id) mapping post-
	// decrypt. Failures must NOT invalidate the placed order; just log them
	// by surfacing the order ack untouched. Matches python/rust.
	if req.ClientOrderID != "" && ack.Success && ack.OrderID != "" {
		c.mu.Lock()
		c.localCOIDIndex[req.ClientOrderID] = ack.OrderID
		bearer := c.bearer
		c.mu.Unlock()
		if bearer != "" {
			_, _ = c.http.RegisterClientOrderMapping(ctx, bearer, req.ClientOrderID, ack.OrderID)
		}
	}
	return ack, nil
}

// CancelOrder sends an encrypted cancel via `DELETE /api/v1/orders/{id}`.
func (c *GodarkRestClient) CancelOrder(ctx context.Context, orderID, symbol string) (*OrderAck, error) {
	if err := c.ensureReady(); err != nil {
		return nil, err
	}
	if symbol == "" {
		symbol = "BTC-USDC-PERP"
	}
	symbolID, err := c.resolveSymbol(symbol)
	if err != nil {
		return nil, err
	}
	oid, err := strconv.ParseUint(orderID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("CancelOrder: invalid order_id %q: %w", orderID, err)
	}
	corrID := newCorrelationID()
	plaintext, err := BuildCancelOrderRequest(oid, c.userUUIDBytes(), uint64(symbolID), corrID)
	if err != nil {
		return nil, err
	}
	return c.sendEncrypted(ctx, "cancel", uint64(symbolID), plaintext, corrID, "", "DELETE", orderID)
}

// CancelOrderByClientID resolves a client_order_id to a real order_id (local
// index first, then `GET /api/v1/orders?client_order_id=`) and cancels.
func (c *GodarkRestClient) CancelOrderByClientID(ctx context.Context, clientOrderID, symbol string) (*OrderAck, error) {
	c.mu.RLock()
	realID := c.localCOIDIndex[clientOrderID]
	bearer := c.bearer
	c.mu.RUnlock()
	if realID == "" {
		if bearer == "" {
			return nil, newConnectionError("not connected")
		}
		row, err := c.http.GetOrderByClientOrderID(ctx, bearer, clientOrderID)
		if err != nil {
			return nil, fmt.Errorf("resolve client_order_id %q: %w", clientOrderID, err)
		}
		realID = resolveOrderIDFromLookup(row)
		if realID == "" {
			return nil, newOrderError(fmt.Sprintf("unknown client_order_id: %s", clientOrderID), "")
		}
		c.mu.Lock()
		c.localCOIDIndex[clientOrderID] = realID
		c.mu.Unlock()
	}
	return c.CancelOrder(ctx, realID, symbol)
}

// ModifyOrder sends an encrypted modify via `PATCH /api/v1/orders/{id}`.
func (c *GodarkRestClient) ModifyOrder(ctx context.Context, orderID, symbol string, newPrice, newQuantity *float64) (*OrderAck, error) {
	if err := c.ensureReady(); err != nil {
		return nil, err
	}
	if newPrice == nil && newQuantity == nil {
		return nil, errors.New("ModifyOrder: at least one of newPrice / newQuantity required")
	}
	if symbol == "" {
		symbol = "BTC-USDC-PERP"
	}
	symbolID, err := c.resolveSymbol(symbol)
	if err != nil {
		return nil, err
	}
	oid, err := strconv.ParseUint(orderID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("ModifyOrder: invalid order_id %q: %w", orderID, err)
	}
	corrID := newCorrelationID()
	plaintext, err := BuildModifyOrderRequest(oid, c.userUUIDBytes(), uint64(symbolID), newPrice, newQuantity, corrID)
	if err != nil {
		return nil, err
	}
	return c.sendEncrypted(ctx, "modify", uint64(symbolID), plaintext, corrID, "", "PATCH", orderID)
}

// GetOrder fetches a single order row.
func (c *GodarkRestClient) GetOrder(ctx context.Context, orderID string) (map[string]any, error) {
	c.mu.RLock()
	bearer := c.bearer
	c.mu.RUnlock()
	if bearer == "" {
		return nil, newConnectionError("not connected")
	}
	return c.http.GetOrder(ctx, bearer, orderID)
}

// GetOrderByClientID fetches the order row indexed by client_order_id.
func (c *GodarkRestClient) GetOrderByClientID(ctx context.Context, clientOrderID string) (map[string]any, error) {
	c.mu.RLock()
	bearer := c.bearer
	c.mu.RUnlock()
	if bearer == "" {
		return nil, newConnectionError("not connected")
	}
	return c.http.GetOrderByClientOrderID(ctx, bearer, clientOrderID)
}

// GetMe fetches the authenticated user's profile via
// `GET /api/v1/auth/me`. The result is cached on the client; subsequent
// calls within a connected session return the cached value.
//
// The most useful field is WalletAddress -- the Solana base58 owner
// pubkey the SDK passes to GetBalance and other on-chain endpoints.
func (c *GodarkRestClient) GetMe(ctx context.Context) (*MeProfile, error) {
	c.mu.RLock()
	bearer := c.bearer
	cachedWallet := c.walletAddr
	c.mu.RUnlock()
	if bearer == "" {
		return nil, newConnectionError("not connected")
	}
	data, err := c.http.GetAuthMe(ctx, bearer)
	if err != nil {
		return nil, err
	}
	me := &MeProfile{
		ID:            stringValue(data["id"]),
		DynamicUserID: stringValue(data["dynamic_user_id"]),
		Email:         stringValue(data["email"]),
		WalletAddress: stringValue(data["wallet_address"]),
		ReferralCode:  stringValue(data["referral_code"]),
		Tier:          stringValue(data["tier"]),
	}
	if me.WalletAddress != "" && me.WalletAddress != cachedWallet {
		c.mu.Lock()
		c.walletAddr = me.WalletAddress
		c.mu.Unlock()
	}
	return me, nil
}

// GetBalance fetches the on-chain balance snapshot for `owner` (the
// Solana base58 wallet pubkey) via
// `GET /api/v1/shielded-pool/balances/{owner}`. The response splits the
// USDT position into wallet (SPL ATA), in-flight shield deposits, and
// the sequencer-tracked shielded balance.
//
// Calling this also nudges the edge's BalanceWatchService to start
// streaming shielded-balance pushes for (authenticated user, owner).
func (c *GodarkRestClient) GetBalance(ctx context.Context, owner string) (*Balance, error) {
	c.mu.RLock()
	bearer := c.bearer
	c.mu.RUnlock()
	if bearer == "" {
		return nil, newConnectionError("not connected")
	}
	if strings.TrimSpace(owner) == "" {
		return nil, errors.New("GetBalance: owner pubkey is required (use GetMyBalance to auto-resolve from /auth/me)")
	}
	data, err := c.http.GetShieldedPoolBalances(ctx, bearer, owner)
	if err != nil {
		return nil, err
	}
	return &Balance{
		WalletUSDTRaw:      coerceUint64(data["walletUsdtRaw"]),
		PendingDepositsRaw: coerceUint64(data["pendingDepositsRaw"]),
		ShieldedBalanceRaw: coerceUint64(data["shieldedBalanceRaw"]),
		WalletUSDTUI:       coerceFloat64(data["walletUsdtUi"]),
	}, nil
}

// GetMyBalance is the convenience pairing of GetMe + GetBalance: it
// resolves the user's owner pubkey from `/auth/me` (cached after the
// first hit) and then fetches the shielded-pool balance snapshot.
func (c *GodarkRestClient) GetMyBalance(ctx context.Context) (*Balance, error) {
	c.mu.RLock()
	owner := c.walletAddr
	c.mu.RUnlock()
	if owner == "" {
		me, err := c.GetMe(ctx)
		if err != nil {
			return nil, err
		}
		if me.WalletAddress == "" {
			return nil, errors.New("GetMyBalance: /auth/me returned empty wallet_address")
		}
		owner = me.WalletAddress
	}
	return c.GetBalance(ctx, owner)
}

// GetLeverage fetches per-symbol leverage settings via `GET /api/v1/leverage`.
func (c *GodarkRestClient) GetLeverage(ctx context.Context) (*LeverageSettings, error) {
	c.mu.RLock()
	bearer := c.bearer
	c.mu.RUnlock()
	if bearer == "" {
		return nil, newConnectionError("not connected")
	}
	raw, err := c.http.GetLeverage(ctx, bearer)
	if err != nil {
		return nil, err
	}
	rows, _ := raw["settings"].([]any)
	out := &LeverageSettings{Settings: make([]LeverageSetting, 0, len(rows))}
	for _, row := range rows {
		m, ok := row.(map[string]any)
		if !ok {
			continue
		}
		out.Settings = append(out.Settings, LeverageSetting{
			SymbolID: int64(coerceUint64(m["symbol_id"])),
			Leverage: int32(coerceUint64(m["leverage"])),
		})
	}
	return out, nil
}

// UpdateLeverage sends an encrypted leverage update via `POST /api/v1/leverage`.
// The JSON header must include `leverage` so the edge can update its DB cache
// and fan out WS pushes on success.
func (c *GodarkRestClient) UpdateLeverage(ctx context.Context, symbol string, leverage int) (*OrderAck, error) {
	if err := c.ensureReady(); err != nil {
		return nil, err
	}
	symbolID, err := c.resolveSymbol(symbol)
	if err != nil {
		return nil, err
	}
	lev := leverage
	if lev < 1 {
		lev = 1
	}

	corrID := newCorrelationID()
	plaintext, err := BuildUpdateLeverageRequest(c.userUUIDBytes(), uint64(symbolID), lev, corrID)
	if err != nil {
		return nil, err
	}

	bodyLength := uint32(len(plaintext) + gdxcrypto.GCMTagLen)
	nonceCounter := c.session.NextNonce()

	aad, err := BuildOrderHeaderAAD(c.userUUIDBytes(), uint64(symbolID), "update_leverage", uint64(nonceCounter), bodyLength, corrID)
	if err != nil {
		return nil, err
	}
	actualNonce, ciphertext, err := c.session.EncryptOrder(aad, plaintext)
	if err != nil {
		return nil, newEncryptionError(fmt.Sprintf("encrypt leverage update: %v", err))
	}
	bodyB64 := base64.StdEncoding.EncodeToString(ciphertext)

	corrIDStr := ""
	if s, err := identity.FromBytes(corrID); err == nil {
		corrIDStr = s
	}
	headerObj := map[string]any{
		"symbol_id":      symbolID,
		"request_type":   "update_leverage",
		"nonce":          actualNonce,
		"body_length":    bodyLength,
		"correlation_id": corrIDStr,
		"leverage":       lev,
	}
	payload := map[string]any{
		"header":     headerObj,
		"ciphertext": bodyB64,
	}

	c.mu.RLock()
	bearer := c.bearer
	c.mu.RUnlock()

	raw, err := c.http.PostLeverageEncrypted(ctx, bearer, payload)
	if err != nil {
		return nil, err
	}
	return c.parseAck(raw)
}

// AwaitTerminalStatus polls GetOrder until the order reaches one of
// `FILLED`, `CANCELLED`, `REJECTED`, or times out.
func (c *GodarkRestClient) AwaitTerminalStatus(ctx context.Context, orderID string, timeout, pollInterval time.Duration) (map[string]any, error) {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	if pollInterval <= 0 {
		pollInterval = 250 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	terminal := map[string]struct{}{
		"FILLED": {}, "CANCELLED": {}, "REJECTED": {},
	}
	for time.Now().Before(deadline) {
		row, err := c.GetOrder(ctx, orderID)
		if err != nil {
			return nil, err
		}
		st, _ := row["status"].(string)
		if _, ok := terminal[strings.ToUpper(st)]; ok {
			return row, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
	return nil, newTimeoutError(fmt.Sprintf("order %s did not reach terminal status within %s", orderID, timeout))
}

// -----------------------------------------------------------------------
// Internals
// -----------------------------------------------------------------------

func (c *GodarkRestClient) sendEncrypted(ctx context.Context, requestType string, symbolID uint64, plaintext, correlationID []byte, clientOrderID string, httpMethod, pathOrderID string) (*OrderAck, error) {
	bodyLength := uint32(len(plaintext) + gdxcrypto.GCMTagLen)
	nonceCounter := c.session.NextNonce()

	aad, err := BuildOrderHeaderAAD(c.userUUIDBytes(), symbolID, requestType, uint64(nonceCounter), bodyLength, correlationID)
	if err != nil {
		return nil, err
	}
	actualNonce, ciphertext, err := c.session.EncryptOrder(aad, plaintext)
	if err != nil {
		return nil, newEncryptionError(fmt.Sprintf("encrypt order: %v", err))
	}
	bodyB64 := base64.StdEncoding.EncodeToString(ciphertext)

	corrIDStr := ""
	if s, err := identity.FromBytes(correlationID); err == nil {
		corrIDStr = s
	}
	headerObj := map[string]any{
		"symbol_id":      symbolID,
		"request_type":   requestType,
		"nonce":          actualNonce,
		"body_length":    bodyLength,
		"correlation_id": corrIDStr,
	}
	payload := map[string]any{
		"header":     headerObj,
		"ciphertext": bodyB64,
	}
	if clientOrderID != "" {
		payload["client_order_id"] = clientOrderID
	}

	c.mu.RLock()
	bearer := c.bearer
	c.mu.RUnlock()

	var raw map[string]any
	switch httpMethod {
	case "POST":
		raw, err = c.http.PostOrdersEncrypted(ctx, bearer, payload)
	case "DELETE":
		raw, err = c.http.DeleteOrderEncrypted(ctx, bearer, pathOrderID, payload)
	case "PATCH":
		raw, err = c.http.PatchOrderEncrypted(ctx, bearer, pathOrderID, payload)
	default:
		return nil, fmt.Errorf("unsupported HTTP method: %q", httpMethod)
	}
	if err != nil {
		return nil, err
	}
	return c.parseAck(raw)
}

func (c *GodarkRestClient) parseAck(raw map[string]any) (*OrderAck, error) {
	// Encrypted ack path: same shape as WS encrypted_push frames.
	if hasEncrypted(raw) {
		return c.decryptRestAck(raw)
	}
	if v, ok := raw["success"].(bool); ok && !v {
		ec, _ := raw["error_code"].(string)
		reason, _ := raw["error"].(string)
		if reason == "" {
			reason = "order rejected"
		}
		return nil, MakeOrderErrorFromJSON(reason, ec)
	}
	return &OrderAck{
		OrderID:  stringValue(raw["order_id"]),
		Success:  true,
		Sequence: stringValue(raw["sequence"]),
	}, nil
}

func (c *GodarkRestClient) decryptRestAck(msg map[string]any) (*OrderAck, error) {
	ctB64, _ := msg["encrypted_body"].(string)
	if ctB64 == "" {
		ctB64, _ = msg["ciphertext"].(string)
	}
	ct, err := base64.StdEncoding.DecodeString(ctB64)
	if err != nil {
		return nil, newEncryptionError(fmt.Sprintf("decode ack: %v", err))
	}
	nonce := coerceUint32(msg["nonce"])
	fencingEpoch := coerceUint64(msg["fencing_epoch"])
	messageType, _ := msg["message_type"].(string)
	if messageType == "" {
		messageType = "ack"
	}
	aad, err := BuildResponseHeaderAAD(c.userUUIDBytes(), messageType, uint32(len(ct)), uint64(nonce), fencingEpoch)
	if err != nil {
		return nil, err
	}
	pt, err := c.session.DecryptPush(nonce, aad, ct)
	if err != nil {
		return nil, newEncryptionError(fmt.Sprintf("decrypt ack: %v", err))
	}
	ack, isAck, err := ParseNodeResponseAck(pt)
	if err != nil {
		return nil, err
	}
	if !isAck {
		return nil, newOrderError("expected ack inside REST encrypted body", "")
	}
	if !ack.Success {
		if ack.ErrorCode != nil {
			code := int32(*ack.ErrorCode)
			return nil, MakeOrderErrorFromCode(&code)
		}
		return nil, MakeOrderErrorFromJSON("order rejected", "")
	}
	return &OrderAck{
		OrderID:  strconv.FormatUint(ack.OrderID, 10),
		Success:  true,
		Sequence: strconv.FormatUint(ack.Sequence, 10),
	}, nil
}

func hasEncrypted(raw map[string]any) bool {
	if v, ok := raw["encrypted"].(bool); ok && v {
		return true
	}
	if s, _ := raw["encrypted_body"].(string); s != "" {
		return true
	}
	if s, _ := raw["ciphertext"].(string); s != "" {
		// Differentiate from the WS payload shape: if the response also
		// carries a top-level `header`, we treat it as the encrypted REST
		// ack envelope.
		if _, hasHeader := raw["header"]; hasHeader {
			return true
		}
		if _, hasMsgType := raw["message_type"]; hasMsgType {
			return true
		}
	}
	return false
}

func resolveOrderIDFromLookup(row map[string]any) string {
	if id, _ := row["order_id"].(string); id != "" {
		return id
	}
	if id, _ := row["orderId"].(string); id != "" {
		return id
	}
	return ""
}

func (c *GodarkRestClient) ensureReady() error {
	c.mu.RLock()
	bearer := c.bearer
	uid := c.userUUID
	c.mu.RUnlock()
	if bearer == "" {
		return newConnectionError("not connected: call Connect first")
	}
	if uid == "" {
		return newConnectionError("not authenticated")
	}
	if !c.session.IsEstablished() {
		return newSessionError("ECDH session not established")
	}
	return nil
}

func (c *GodarkRestClient) userUUIDBytes() []byte {
	c.mu.RLock()
	uid := c.userUUID
	c.mu.RUnlock()
	if uid == "" {
		return make([]byte, identity.UserUUIDLen)
	}
	b, err := identity.ToBytes(uid)
	if err != nil {
		return make([]byte, identity.UserUUIDLen)
	}
	return b
}

func (c *GodarkRestClient) resolveSymbol(symbol string) (int64, error) {
	id, ok := c.symbolMap[symbol]
	if !ok {
		known := make([]string, 0, len(c.symbolMap))
		for k := range c.symbolMap {
			known = append(known, k)
		}
		return 0, fmt.Errorf("unknown symbol %q (known: %v)", symbol, known)
	}
	return id, nil
}

func resolveRestBaseURL(explicit string) string {
	if v := strings.TrimSpace(explicit); v != "" {
		return strings.TrimRight(v, "/")
	}
	for _, key := range []string{"GODARK_REST_URL", "GDX_REST_URL"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return strings.TrimRight(v, "/")
		}
	}
	ws := strings.TrimSpace(os.Getenv("GODARK_EDGE_URL"))
	if ws == "" {
		ws = strings.TrimSpace(os.Getenv("GDX_EDGE_URL"))
	}
	if ws != "" {
		return wsOriginToHTTPRest(ws)
	}
	return defaultRestBaseURL
}

func wsOriginToHTTPRest(wsURL string) string {
	u := strings.TrimRight(strings.TrimSpace(wsURL), "/")
	if strings.HasSuffix(u, "/ws/v1") {
		u = strings.TrimSuffix(u, "/ws/v1")
	} else if strings.HasSuffix(u, "/ws") {
		u = strings.TrimSuffix(u, "/ws")
	}
	switch {
	case strings.HasPrefix(u, "ws://"):
		return "http://" + strings.TrimPrefix(u, "ws://")
	case strings.HasPrefix(u, "wss://"):
		return "https://" + strings.TrimPrefix(u, "wss://")
	}
	return u
}

// Make sure url import isn't dead-stripped (helps in builds where the only
// reference is inside an unused branch). Compiler would have flagged it
// otherwise; this assertion is a no-op at runtime.
var _ = url.PathEscape
