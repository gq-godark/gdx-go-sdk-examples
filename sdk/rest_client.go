package godark

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	gdxcrypto "github.com/gq-godark/gdx-go-sdk/internal/crypto"
	"github.com/gq-godark/gdx-go-sdk/internal/hpke"
	"github.com/gq-godark/gdx-go-sdk/internal/identity"
	"github.com/gq-godark/gdx-go-sdk/internal/rest"
)

const defaultRestBaseURL = "https://api.godark-dex.com"

// REST-local HPKE sequencer pins (Track A). Do not fill client.go
// testnetHpkeStaticPublicKeyHex / devnetHpkeStaticPublicKeyHex here — those
// are reserved for the separate WS pin PR.
const (
	restTestnetHpkeStaticPublicKeyHex = "a9fdd7f26c0de36d82811e9fe1df2509960cd5b25eef037355e209b9222bea7d"
	restDevnetHpkeStaticPublicKeyHex  = "a6807e2f6cd04b54cc19be2fd4faea2a1239f1e2896912d91222678ab54cdd45"
)

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

	// HpkeStaticPublicKeyHex pins the sequencer's 32-byte X25519 HPKE key for
	// one-shot REST encryption. Preference: this field → env vars (same list
	// as resolveHpkeStaticPublicKey) → REST-local baked pin for Environment
	// (or URL-inferred environment). Localnet has no baked pin.
	HpkeStaticPublicKeyHex string

	// Environment selects the deployment for HPKE pin baking when
	// HpkeStaticPublicKeyHex and env vars are unset. If empty, inferred from
	// the resolved BaseURL via inferEnvironmentFromRestURL.
	Environment Environment

	// SymbolMap overrides the embedded default symbol map.
	SymbolMap map[string]int64

	// HTTPClient is forwarded to the REST transport. Pass nil for a 30s
	// default.
	HTTPClient *http.Client
}

// GodarkRestClient exposes REST account, market, and encrypted trading
// endpoints. Each encrypted request uses one-shot HPKE (encapped_key +
// request_id); there is no persistent REST session.
type GodarkRestClient struct {
	legacyToken string
	apiKeyID    string
	apiSecret   string
	passphrase  string
	baseURL     string
	symbolMap   map[string]int64
	http        *rest.Transport
	hpkePinHex  string
	fallback    string

	mu             sync.RWMutex
	bearer         string
	userUUID       string
	tokenScope     string
	walletAddr     string
	localCOIDIndex map[string]string
	nextRequestID  atomic.Uint64
}

// NewRestClient validates the config and returns an unconnected client.
// Call Connect to obtain a bearer token before encrypted trading RPCs.
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
	env := cfg.Environment
	if strings.TrimSpace(string(env)) == "" {
		env = inferEnvironmentFromRestURL(base)
	} else {
		env = env.normalize()
	}
	symMap := cfg.SymbolMap
	if symMap == nil {
		symMap = DefaultSymbolMap()
	}
	c := &GodarkRestClient{
		legacyToken:    creds.legacyToken,
		apiKeyID:       creds.apiKeyID,
		apiSecret:      creds.apiSecret,
		passphrase:     creds.passphrase,
		baseURL:        base,
		symbolMap:      symMap,
		http:           rest.New(base, cfg.HTTPClient),
		hpkePinHex:     resolveRestHpkePin(cfg.HpkeStaticPublicKeyHex, env),
		fallback:       resolveUserUUID(cfg.UserUUID),
		localCOIDIndex: make(map[string]string),
	}
	c.nextRequestID.Store(1)
	return c, nil
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

// IsSessionEstablished is always false: REST uses one-shot HPKE per request.
func (c *GodarkRestClient) IsSessionEstablished() bool {
	return false
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

// TokenScope returns the OAuth scope string from the last auth/token response.
func (c *GodarkRestClient) TokenScope() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tokenScope
}

// Connect authenticates REST requests via POST /api/v1/auth/token. User identity
// is resolved from user_uuid in the auth response, constructor/env fallback, or
// the JWT sub claim.
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
	if strings.TrimSpace(uid) == "" {
		uid = c.fallback
	}
	if uid == "" {
		if parsed, ok := userUUIDFromAccessTokenJWT(bearer); ok {
			uid = parsed
		}
	}
	if uid == "" {
		return newAuthenticationError(
			"REST auth succeeded but user_uuid missing in response, JWT sub, and no fallback " +
				"provided via constructor or GODARK_USER_UUID / GDX_USER_UUID env vars",
		)
	}

	scope, _ := authData["scope"].(string)

	c.mu.Lock()
	c.bearer = bearer
	c.userUUID = uid
	c.tokenScope = scope
	c.mu.Unlock()

	return nil
}

// Disconnect revokes the bearer and resets local state.
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
	c.tokenScope = ""
	c.walletAddr = ""
	c.localCOIDIndex = make(map[string]string)
	c.mu.Unlock()
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

// GetFundingRates calls `GET /api/v1/market-data/funding-rates` (public; Connect not required).
func (c *GodarkRestClient) GetFundingRates(ctx context.Context) ([]map[string]any, error) {
	return c.http.GetFundingRates(ctx)
}

// GetOpenInterest calls `GET /api/v1/market-data/open-interest` (public; Connect not required).
func (c *GodarkRestClient) GetOpenInterest(ctx context.Context) ([]map[string]any, error) {
	return c.http.GetOpenInterest(ctx)
}

// GetVolume calls `GET /api/v1/market-data/volume` (public; Connect not required).
func (c *GodarkRestClient) GetVolume(ctx context.Context) (map[string]any, error) {
	return c.http.GetVolume(ctx)
}

// GetOpenOrders returns live open orders via encrypted POST /api/v1/openOrders.
func (c *GodarkRestClient) GetOpenOrders(ctx context.Context) (*OpenOrdersSnapshot, error) {
	variant, err := c.snapshotRPC(ctx, "get_open_orders", BuildGetOpenOrdersRequest, "/api/v1/openOrders")
	if err != nil {
		return nil, err
	}
	if variant.Kind != "open_orders_snapshot" || variant.OpenOrders == nil {
		return nil, newOrderError(fmt.Sprintf("expected open_orders_snapshot, got %s", variant.Kind), "")
	}
	return variant.OpenOrders, nil
}

// GetPositions returns live positions via encrypted POST /api/v1/positions.
func (c *GodarkRestClient) GetPositions(ctx context.Context) (*PositionsSnapshot, error) {
	variant, err := c.snapshotRPC(ctx, "get_positions", BuildGetPositionsRequest, "/api/v1/positions")
	if err != nil {
		return nil, err
	}
	if variant.Kind != "positions_snapshot" || variant.Positions == nil {
		return nil, newOrderError(fmt.Sprintf("expected positions_snapshot, got %s", variant.Kind), "")
	}
	return variant.Positions, nil
}

// GetAccount returns live account margin via encrypted POST /api/v1/account.
func (c *GodarkRestClient) GetAccount(ctx context.Context) (*AccountMarginUpdate, error) {
	variant, err := c.snapshotRPC(ctx, "get_account", BuildGetAccountRequest, "/api/v1/account")
	if err != nil {
		return nil, err
	}
	if variant.Kind != "account_margin_update" || variant.AccountMargin == nil {
		return nil, newOrderError(fmt.Sprintf("expected account_margin_update, got %s", variant.Kind), "")
	}
	return variant.AccountMargin, nil
}

// MassQuote performs bulk cancel-replace via encrypted POST /api/v1/orders/massQuote.
func (c *GodarkRestClient) MassQuote(ctx context.Context, symbol string, legs []MassQuoteLegInput, postOnly *bool) (*MassQuoteAck, error) {
	if err := c.ensureReady(); err != nil {
		return nil, err
	}
	symbolID, err := c.resolveSymbol(symbol)
	if err != nil {
		return nil, err
	}
	corrID := newCorrelationID()
	plaintext, err := BuildMassQuoteRequest(uint64(symbolID), c.userUUIDBytes(), legs, corrID, postOnly)
	if err != nil {
		return nil, err
	}
	sealed, raw, err := c.sendEncryptedEnvelope(ctx, "mass_quote", uint64(symbolID), plaintext, corrID, restEncRoute{kind: "post_path", postPath: "/api/v1/orders/massQuote"}, "", nil)
	if err != nil {
		return nil, err
	}
	return c.parseMassQuoteREST(raw, sealed)
}

// BatchCancel cancels up to 20 resting orders via encrypted POST /api/v1/orders.
func (c *GodarkRestClient) BatchCancel(ctx context.Context, symbol string, orderIDs []uint64) (*BatchCancelAck, error) {
	if err := c.ensureReady(); err != nil {
		return nil, err
	}
	symbolID, err := c.resolveSymbol(symbol)
	if err != nil {
		return nil, err
	}
	corrID := newCorrelationID()
	plaintext, err := BuildBatchCancelRequest(uint64(symbolID), c.userUUIDBytes(), orderIDs, corrID)
	if err != nil {
		return nil, err
	}
	sealed, raw, err := c.sendEncryptedEnvelope(ctx, "batch_cancel", uint64(symbolID), plaintext, corrID, restEncRoute{kind: "post_orders"}, "", nil)
	if err != nil {
		return nil, err
	}
	return c.parseBatchCancelREST(raw, sealed)
}

// BatchModify amends up to 20 resting orders via encrypted POST /api/v1/orders.
func (c *GodarkRestClient) BatchModify(ctx context.Context, symbol string, legs []BatchModifyLegInput) (*BatchModifyAck, error) {
	if err := c.ensureReady(); err != nil {
		return nil, err
	}
	symbolID, err := c.resolveSymbol(symbol)
	if err != nil {
		return nil, err
	}
	corrID := newCorrelationID()
	plaintext, err := BuildBatchModifyRequest(uint64(symbolID), c.userUUIDBytes(), legs, corrID)
	if err != nil {
		return nil, err
	}
	sealed, raw, err := c.sendEncryptedEnvelope(ctx, "batch_modify", uint64(symbolID), plaintext, corrID, restEncRoute{kind: "post_orders"}, "", nil)
	if err != nil {
		return nil, err
	}
	return c.parseBatchModifyREST(raw, sealed)
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

	sealed, raw, err := c.sendEncryptedEnvelope(ctx, "update_leverage", uint64(symbolID), plaintext, corrID, restEncRoute{kind: "post_leverage"}, "", &lev)
	if err != nil {
		return nil, err
	}
	return c.parseAck(raw, sealed)
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

type restEncRoute struct {
	kind     string // post_orders | post_leverage | post_path | delete | patch
	postPath string
	orderID  string
}

func correlationIDHeaderHex(correlationID []byte) string {
	if len(correlationID) != 16 {
		if len(correlationID) == 0 {
			return ""
		}
		return hex.EncodeToString(correlationID)
	}
	for _, b := range correlationID {
		if b != 0 {
			return hex.EncodeToString(correlationID)
		}
	}
	return ""
}

func (c *GodarkRestClient) pinnedRecipient() ([]byte, error) {
	c.mu.RLock()
	pin := c.hpkePinHex
	c.mu.RUnlock()
	if strings.TrimSpace(pin) == "" {
		return nil, newSessionError("HPKE static public key unset; set HpkeStaticPublicKeyHex or GDX_HPKE_STATIC_PUBLIC_KEY")
	}
	return hpke.ParsePinnedStaticPublicKey(pin)
}

func (c *GodarkRestClient) setupRESTSession(userUUID []byte) (uint64, []byte, *hpke.SealedSession, error) {
	recipient, err := c.pinnedRecipient()
	if err != nil {
		return 0, nil, nil, err
	}
	requestID := c.nextRequestID.Add(1) - 1
	info := hpke.InfoForRESTRequest(userUUID, requestID)
	encapped, sealed, err := hpke.SetupSession(recipient, info)
	if err != nil {
		return 0, nil, nil, newEncryptionError(fmt.Sprintf("HPKE setup: %v", err))
	}
	return requestID, encapped, sealed, nil
}

func (c *GodarkRestClient) sendEncryptedEnvelope(
	ctx context.Context,
	requestType string,
	symbolID uint64,
	plaintext, correlationID []byte,
	route restEncRoute,
	clientOrderID string,
	headerLeverage *int,
) (*hpke.SealedSession, map[string]any, error) {
	userUUID := c.userUUIDBytes()
	requestID, encapped, sealed, err := c.setupRESTSession(userUUID)
	if err != nil {
		return nil, nil, err
	}

	const nonce = uint64(0)
	bodyLength := uint32(len(plaintext) + gdxcrypto.GCMTagLen)
	aad, err := BuildOrderHeaderAAD(userUUID, symbolID, requestType, nonce, bodyLength, correlationID)
	if err != nil {
		return nil, nil, err
	}
	nonceBytes := hpke.NonceFromU64(nonce)
	ciphertext, err := sealed.SealC2S(nonceBytes[:], aad, plaintext)
	if err != nil {
		return nil, nil, newEncryptionError(fmt.Sprintf("encrypt request: %v", err))
	}

	headerObj := map[string]any{
		"symbol_id":      symbolID,
		"request_type":   requestType,
		"nonce":          nonce,
		"body_length":    bodyLength,
		"correlation_id": correlationIDHeaderHex(correlationID),
	}
	if headerLeverage != nil {
		headerObj["leverage"] = *headerLeverage
	}
	payload := map[string]any{
		"header":         headerObj,
		"encrypted_body": base64.StdEncoding.EncodeToString(ciphertext),
		"encapped_key":   base64.StdEncoding.EncodeToString(encapped),
		"request_id":     requestID,
	}
	if clientOrderID != "" {
		payload["client_order_id"] = clientOrderID
	}

	c.mu.RLock()
	bearer := c.bearer
	c.mu.RUnlock()

	var raw map[string]any
	switch route.kind {
	case "post_orders":
		raw, err = c.http.PostOrdersEncrypted(ctx, bearer, payload)
	case "post_leverage":
		raw, err = c.http.PostLeverageEncrypted(ctx, bearer, payload)
	case "post_path":
		if route.postPath == "" {
			return nil, nil, errors.New("post_path route requires postPath")
		}
		raw, err = c.http.PostEncrypted(ctx, bearer, route.postPath, payload)
	case "delete":
		if route.orderID == "" {
			return nil, nil, errors.New("delete route requires orderID")
		}
		raw, err = c.http.DeleteOrderEncrypted(ctx, bearer, route.orderID, payload)
	case "patch":
		if route.orderID == "" {
			return nil, nil, errors.New("patch route requires orderID")
		}
		raw, err = c.http.PatchOrderEncrypted(ctx, bearer, route.orderID, payload)
	default:
		return nil, nil, fmt.Errorf("unsupported encrypted route: %q", route.kind)
	}
	if err != nil {
		return nil, nil, err
	}
	return sealed, raw, nil
}

func (c *GodarkRestClient) sendEncrypted(ctx context.Context, requestType string, symbolID uint64, plaintext, correlationID []byte, clientOrderID string, httpMethod, pathOrderID string) (*OrderAck, error) {
	route := restEncRoute{kind: "post_orders"}
	switch httpMethod {
	case "POST":
		route.kind = "post_orders"
	case "DELETE":
		route.kind = "delete"
		route.orderID = pathOrderID
	case "PATCH":
		route.kind = "patch"
		route.orderID = pathOrderID
	default:
		return nil, fmt.Errorf("unsupported HTTP method: %q", httpMethod)
	}
	sealed, raw, err := c.sendEncryptedEnvelope(ctx, requestType, symbolID, plaintext, correlationID, route, clientOrderID, nil)
	if err != nil {
		return nil, err
	}
	return c.parseAck(raw, sealed)
}

func (c *GodarkRestClient) snapshotRPC(
	ctx context.Context,
	requestType string,
	build func([]byte, []byte) ([]byte, error),
	path string,
) (*NodeResponseVariant, error) {
	corrID := newCorrelationID()
	plaintext, err := build(c.userUUIDBytes(), corrID)
	if err != nil {
		return nil, err
	}
	headerSymbolID, ok := c.symbolMap["BTC-USDC-PERP"]
	if !ok {
		for _, id := range c.symbolMap {
			headerSymbolID = id
			break
		}
		if headerSymbolID == 0 {
			headerSymbolID = 1
		}
	}
	sealed, raw, err := c.sendEncryptedEnvelope(
		ctx, requestType, uint64(headerSymbolID), plaintext, corrID,
		restEncRoute{kind: "post_path", postPath: path}, "", nil,
	)
	if err != nil {
		return nil, err
	}
	if !hasEncrypted(raw) {
		return nil, newOrderError(fmt.Sprintf("expected encrypted snapshot reply for %s", requestType), "")
	}
	return c.decryptRestNodeResponse(raw, sealed)
}

func (c *GodarkRestClient) parseAck(raw map[string]any, sealed *hpke.SealedSession) (*OrderAck, error) {
	if hasEncrypted(raw) {
		if sealed == nil {
			return nil, newEncryptionError("encrypted REST ack requires one-shot HPKE session")
		}
		return c.decryptRestAck(raw, sealed)
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

func (c *GodarkRestClient) decryptRestPlaintext(msg map[string]any, sealed *hpke.SealedSession) ([]byte, error) {
	ctB64, _ := msg["encrypted_body"].(string)
	if ctB64 == "" {
		ctB64, _ = msg["ciphertext"].(string)
	}
	ct, err := base64.StdEncoding.DecodeString(ctB64)
	if err != nil {
		return nil, newEncryptionError(fmt.Sprintf("decode ack: %v", err))
	}
	nonce := coerceUint64(msg["nonce"])
	fencingEpoch := coerceUint64(msg["fencing_epoch"])
	messageType, _ := msg["message_type"].(string)
	if messageType == "" {
		messageType = "ack"
	}
	aad, err := BuildResponseHeaderAAD(
		c.userUUIDBytes(), messageType, uint32(len(ct)), nonce, fencingEpoch,
		correlationIDFromWire(msg["correlation_id"]), coerceUint64(msg["session_seq"]),
	)
	if err != nil {
		return nil, err
	}
	nonceBytes := hpke.NonceFromU64(nonce)
	pt, err := sealed.OpenS2C(nonceBytes[:], aad, ct)
	if err != nil {
		return nil, newEncryptionError(fmt.Sprintf("decrypt REST reply: %v", err))
	}
	return pt, nil
}

func (c *GodarkRestClient) decryptRestAck(msg map[string]any, sealed *hpke.SealedSession) (*OrderAck, error) {
	pt, err := c.decryptRestPlaintext(msg, sealed)
	if err != nil {
		return nil, err
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
			return nil, MakeOrderErrorFromCode(&code, ack.RejectText)
		}
		return nil, MakeOrderErrorFromJSON(ack.RejectText, "")
	}
	return &OrderAck{
		OrderID:  strconv.FormatUint(ack.OrderID, 10),
		Success:  true,
		Sequence: strconv.FormatUint(ack.Sequence, 10),
	}, nil
}

func (c *GodarkRestClient) decryptRestNodeResponse(msg map[string]any, sealed *hpke.SealedSession) (*NodeResponseVariant, error) {
	pt, err := c.decryptRestPlaintext(msg, sealed)
	if err != nil {
		return nil, err
	}
	messageType, _ := msg["message_type"].(string)
	return ParseNodeResponseVariant(pt, messageType)
}

func (c *GodarkRestClient) parseMassQuoteREST(raw map[string]any, sealed *hpke.SealedSession) (*MassQuoteAck, error) {
	pt, err := c.decryptRestPlaintext(raw, sealed)
	if err != nil {
		return nil, err
	}
	ack, ok, err := ParseMassQuoteAck(pt)
	if err != nil {
		return nil, err
	}
	if !ok {
		na, isAck, err := ParseNodeResponseAck(pt)
		if err != nil {
			return nil, err
		}
		if isAck && !na.Success {
			code := ""
			if na.ErrorCode != nil {
				code = strconv.FormatUint(uint64(*na.ErrorCode), 10)
			}
			return nil, MakeOrderErrorFromJSON(na.RejectText, code)
		}
		return nil, newOrderError("expected mass_quote_ack inside encrypted REST body", "")
	}
	return ack, nil
}

func (c *GodarkRestClient) parseBatchCancelREST(raw map[string]any, sealed *hpke.SealedSession) (*BatchCancelAck, error) {
	pt, err := c.decryptRestPlaintext(raw, sealed)
	if err != nil {
		return nil, err
	}
	ack, ok, err := ParseBatchCancelAck(pt)
	if err != nil {
		return nil, err
	}
	if !ok {
		na, isAck, err := ParseNodeResponseAck(pt)
		if err != nil {
			return nil, err
		}
		if isAck && !na.Success {
			code := ""
			if na.ErrorCode != nil {
				code = strconv.FormatUint(uint64(*na.ErrorCode), 10)
			}
			return nil, MakeOrderErrorFromJSON(na.RejectText, code)
		}
		return nil, newOrderError("expected batch_cancel_ack inside encrypted REST body", "")
	}
	return ack, nil
}

func (c *GodarkRestClient) parseBatchModifyREST(raw map[string]any, sealed *hpke.SealedSession) (*BatchModifyAck, error) {
	pt, err := c.decryptRestPlaintext(raw, sealed)
	if err != nil {
		return nil, err
	}
	ack, ok, err := ParseBatchModifyAck(pt)
	if err != nil {
		return nil, err
	}
	if !ok {
		na, isAck, err := ParseNodeResponseAck(pt)
		if err != nil {
			return nil, err
		}
		if isAck && !na.Success {
			code := ""
			if na.ErrorCode != nil {
				code = strconv.FormatUint(uint64(*na.ErrorCode), 10)
			}
			return nil, MakeOrderErrorFromJSON(na.RejectText, code)
		}
		return nil, newOrderError("expected batch_modify_ack inside encrypted REST body", "")
	}
	return ack, nil
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
	hpkePin := c.hpkePinHex
	c.mu.RUnlock()
	if bearer == "" {
		return newConnectionError("not connected: call Connect first")
	}
	if uid == "" {
		return newConnectionError("not authenticated")
	}
	if strings.TrimSpace(hpkePin) == "" {
		return newSessionError("HPKE static public key unset; set HpkeStaticPublicKeyHex or GDX_HPKE_STATIC_PUBLIC_KEY")
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

// inferEnvironmentFromRestURL maps a REST origin to an Environment for pin baking.
// Localhost / 127.0.0.1 → localnet (no baked pin); hosts containing "devnet" or
// the public devnet IP → devnet; godark-dex.com (and unknown hosts) → testnet.
func inferEnvironmentFromRestURL(base string) Environment {
	host := strings.TrimSpace(strings.ToLower(base))
	for _, prefix := range []string{"https://", "http://", "wss://", "ws://"} {
		if strings.HasPrefix(host, prefix) {
			host = host[len(prefix):]
			break
		}
	}
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	if host == "127.0.0.1" || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return EnvironmentLocalnet
	}
	if strings.Contains(host, "devnet") || host == "18.143.165.149" {
		return EnvironmentDevnet
	}
	if strings.Contains(host, "godark-dex.com") {
		return EnvironmentTestnet
	}
	return EnvironmentTestnet
}

// resolveRestHpkePin resolves the REST HPKE pin: explicit → env vars → REST-local
// baked testnet/devnet constants. Localnet returns "" (env/explicit required).
func resolveRestHpkePin(explicit string, env Environment) string {
	if v := strings.TrimSpace(explicit); v != "" {
		return v
	}
	for _, key := range []string{
		"GDX_HPKE_STATIC_PUBLIC_KEY",
		"GDX_HPKE_STATIC_PUBKEY",
		"GODARK_HPKE_STATIC_PUBLIC_KEY",
		"VITE_GDX_HPKE_STATIC_PUBKEY",
		"GODARK_NOISE_STATIC_PUBLIC_KEY",
		"GDX_NOISE_STATIC_PUBLIC_KEY",
		"GDX_NOISE_STATIC_PUBKEY",
	} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	switch env.normalize() {
	case EnvironmentTestnet:
		return restTestnetHpkeStaticPublicKeyHex
	case EnvironmentDevnet:
		return restDevnetHpkeStaticPublicKeyHex
	default:
		return ""
	}
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
