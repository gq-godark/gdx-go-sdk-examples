package godark

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/gq-godark/gdx-go-sdk/internal/hpke"
	"github.com/gq-godark/gdx-go-sdk/internal/identity"
	"github.com/gq-godark/gdx-go-sdk/internal/rest"
	"github.com/gq-godark/gdx-go-sdk/internal/session"
	"github.com/gq-godark/gdx-go-sdk/internal/transport"
	"github.com/gq-godark/gdx-go-sdk/internal/wire"
	commonpb "github.com/gq-godark/gdx-go-sdk/proto/gdx/common/v1"
	edgepb "github.com/gq-godark/gdx-go-sdk/proto/gdx/edge/v1"
)

// Default testnet WebSocket origin. The client appends `/ws/v1`.
//
// Public mainnet is not currently exposed; testnet is the live network for
// SDK users today and is the SDK default. For local development, override
// via EnvironmentLocalnet, BaseURL, GODARK_EDGE_URL, or GDX_EDGE_URL.
const defaultEdgeBaseURL = "wss://api.godark-dex.com"

// Default devnet WebSocket origin. The client appends `/ws/v1`.
const defaultDevnetEdgeBaseURL = "wss://api.devnet.godark-dex.com"

// Sequencer HPKE static public key for public testnet (64 hex).
const testnetHpkeStaticPublicKeyHex = "a9fdd7f26c0de36d82811e9fe1df2509960cd5b25eef037355e209b9222bea7d"

// Sequencer HPKE static public key for public devnet (64 hex).
const devnetHpkeStaticPublicKeyHex = "a6807e2f6cd04b54cc19be2fd4faea2a1239f1e2896912d91222678ab54cdd45"

// Environment names a deployment target. It selects the default edge URL and,
// when known, a baked-in sequencer HPKE public key pin.
//
// Explicit BaseURL / HpkeStaticPublicKeyHex and the corresponding
// environment variables still win over these presets.
type Environment string

const (
	// EnvironmentTestnet is the public testnet (default zero value). It uses
	// the public testnet edge URL and a baked-in sequencer HPKE pin.
	// Explicit HpkeStaticPublicKeyHex / GDX_HPKE_STATIC_PUBLIC_KEY still override.
	EnvironmentTestnet Environment = "testnet"
	// EnvironmentDevnet targets the public devnet edge
	// (wss://api.devnet.godark-dex.com) with a baked-in sequencer HPKE pin.
	// Explicit config / env still override.
	EnvironmentDevnet Environment = "devnet"
	// EnvironmentLocalnet targets ws://127.0.0.1:4000. No baked HPKE pin —
	// set HpkeStaticPublicKeyHex or GDX_HPKE_STATIC_PUBLIC_KEY.
	EnvironmentLocalnet Environment = "localnet"
)

// EdgeBaseURL returns the default edge host origin for this environment.
func (e Environment) EdgeBaseURL() string {
	switch e.normalize() {
	case EnvironmentDevnet:
		return defaultDevnetEdgeBaseURL
	case EnvironmentLocalnet:
		return "ws://127.0.0.1:4000"
	default:
		return defaultEdgeBaseURL
	}
}

// HpkeStaticPublicKeyHex returns the baked-in sequencer HPKE static
// public key (64 hex chars) when known for this environment; otherwise "".
func (e Environment) HpkeStaticPublicKeyHex() string {
	switch e.normalize() {
	case EnvironmentTestnet:
		return testnetHpkeStaticPublicKeyHex
	case EnvironmentDevnet:
		return devnetHpkeStaticPublicKeyHex
	default:
		return ""
	}
}

func (e Environment) normalize() Environment {
	if e == "" {
		return EnvironmentTestnet
	}
	return e
}

// TransportConfig is the public alias for the WebSocket transport
// configuration consumed by ClientConfig.Transport. It re-exports the
// internal/transport.Config type so external callers (examples, downstream
// consumers) can build it without crossing the internal/ boundary.
type TransportConfig = transport.Config

// ClientConfig holds the constructor arguments for NewClient. Either APIKey
// (legacy single opaque key) OR APIKeyID + APISecret (key-pair) must be set.
type ClientConfig struct {
	// APIKey is the legacy single-token auth value. Set this OR
	// (APIKeyID + APISecret), not both. Legacy keys (e.g. local
	// `test-key-*`) are sent as the WebSocket login token as-is.
	APIKey string

	// APIKeyID / APISecret / Passphrase are the modern key-pair credentials.
	// All three must be set together (Passphrase may also come from
	// GODARK_PASSPHRASE / GDX_PASSPHRASE). On Connect the client mints
	// POST /api/v1/auth/token (client_credentials) and logs in with the
	// returned access_token — the raw key triple is never sent on the socket.
	APIKeyID   string
	APISecret  string
	Passphrase string

	// Environment selects a named deployment. Defaults to EnvironmentTestnet
	// (zero value), which supplies the public testnet edge URL and HPKE
	// pin when those are not set explicitly or via environment variables.
	Environment Environment

	// BaseURL is the edge WebSocket origin (host only, e.g.
	// `wss://api.godark-dex.com`). The client appends `/ws/v1` to produce
	// the final upgrade URL. Preference: this field → GODARK_EDGE_URL /
	// GDX_EDGE_URL → Environment preset.
	BaseURL string

	// Account is an optional Solana base58 account fallback when an older local
	// edge omits `account`. Current edges always return it after login.
	// Also read from GODARK_ACCOUNT / GDX_ACCOUNT.
	Account string

	// UserUUID is retained for legacy edge metadata only. It is not used in
	// current encrypted protobuf or HPKE identity domains.
	UserUUID string

	// HpkeStaticPublicKeyHex pins the sequencer's 32-byte X25519 HPKE key.
	// Preference: this field → HPKE env vars → baked-in pin from Environment when set.
	HpkeStaticPublicKeyHex string

	// SymbolMap overrides the embedded default symbol map. Useful when
	// running against a non-prod edge with a custom symbol set.
	SymbolMap map[string]int64

	// Transport tunes WebSocket transport behaviour (TLS, timeouts, etc.).
	// Zero-value defaults are reasonable.
	Transport transport.Config

	// DisableAutoReconnect suppresses automatic reconnect after unexpected
	// WebSocket disconnects. Default false (auto-reconnect enabled).
	DisableAutoReconnect bool

	// HTTPClient is forwarded to the transport for the upgrade request.
	HTTPClient *http.Client

	// StreamBufferSize is the max in-memory buffer of push frames per stream
	// (order updates, position updates, etc.). When the buffer fills, the
	// oldest frame is dropped. Default 256.
	StreamBufferSize int

	// PlaceOrderTerminalTimeout is how long PlaceOrder waits for book
	// confirmation (OPEN/reject/fill/cancel) after the fast ack when
	// Confirmation is Book. Nil uses the command timeout (30s by default).
	// When set, the duration must be greater than zero.
	PlaceOrderTerminalTimeout *time.Duration
}

// PlaceOrderConfirmation selects the PlaceOrder completion boundary.
// Empty Confirmation on PlaceOrderRequest defaults to Book.
type PlaceOrderConfirmation string

const (
	// PlaceOrderConfirmationAck returns after the sequencer fast ack.
	// Callers must consume OrderUpdates / OnOrderUpdate for the later outcome.
	PlaceOrderConfirmationAck PlaceOrderConfirmation = "ack"
	// PlaceOrderConfirmationBook waits for OPEN, REJECTED, FILLED,
	// PARTIALLY_FILLED, or CANCELLED after the fast ack (default).
	PlaceOrderConfirmationBook PlaceOrderConfirmation = "book"
)

// inflightAckType maps encrypted command request_type values to the ack
// message_type each command must resolve on. Batched / multi-item commands
// get their own ack type so an async generic "ack" pushed mid-flight does
// not resolve them early.
var inflightAckType = map[string]string{
	"mass_quote":   "mass_quote_ack",
	"batch_cancel": "batch_cancel_ack",
	"batch_modify": "batch_modify_ack",
	"amend_tpsl":   "tpsl_ack",
	"cancel_tpsl":  "tpsl_ack",
	"cancel_all":   "cancel_all_ack",
	"close_all":    "close_all_ack",
	"reverse":      "reverse_ack",
}

type placeOutcomeResult struct {
	update *OrderUpdate
	err    error
}

type placeOutcomeWaiter struct {
	orderID string
	ch      chan placeOutcomeResult
}

// GodarkClient is the encrypted trading client.
//
// Lifecycle: NewClient -> Connect -> (Subscribe ->) PlaceOrder / CancelOrder
// / ModifyOrder + (OrderUpdates / OnOrderUpdate) -> Disconnect. The
// concurrent-safety model is identical to python's: one trading command in
// flight at a time (gated by the transport mutex); push-stream consumers
// (channels and callbacks) run concurrently with command issuance.
type GodarkClient struct {
	authToken       string
	apiKeyID        string
	apiSecret       string
	passphrase      string
	httpClient      *http.Client
	baseURL         string
	fallbackAccount string
	symbolMap       map[string]int64
	instrumentDecs  map[string]InstrumentDecimals
	bufSize         int

	transport *transport.Transport
	session   *session.CryptoSession

	mu             sync.RWMutex
	account        string
	userUUID       string
	connID         uint64
	hpkeStaticKey  string
	accountID      string
	loginSessionID string
	tokenExpiresAt string
	cancelOnDisc   bool
	connected      bool

	// per-stream queues + callbacks
	orderQueue              chan *OrderUpdate
	positionQueue           chan *PositionUpdate
	posSnapshotQueue        chan *PositionsSnapshot
	systemHealthQueue       chan *SystemHealthUpdate
	balanceQueue            chan *BalanceUpdate
	marginAlertQueue        chan *MarginAlert
	fundingRateQueue        chan *FundingRateUpdate
	settlementQueue         chan *SettlementUpdate
	leverageSettingsQueue   chan *LeverageSettings
	openOrdersSnapshotQueue chan *OpenOrdersSnapshot

	cbMu                        sync.RWMutex
	orderCallbacks              []func(*OrderUpdate)
	positionCallbacks           []func(*PositionUpdate)
	snapshotCallbacks           []func(*PositionsSnapshot)
	openOrdersSnapshotCallbacks []func(*OpenOrdersSnapshot)
	healthCallbacks             []func(*SystemHealthUpdate)
	balanceCallbacks            []func(*BalanceUpdate)
	marginCallbacks             []func(*MarginAlert)
	fundingCallbacks            []func(*FundingRateUpdate)
	settlementCBs               []func(*SettlementUpdate)
	leverageSettingsCallbacks   []func(*LeverageSettings)
	errorCallbacks              []func(error)
	disconnectCB                []func()
	reconnectCB                 []func()

	// subscription replay after reconnect
	subMu           sync.Mutex
	desiredChannels map[string]struct{}

	// reconnect lifecycle
	disableAutoReconnect bool
	intentionalClose     bool
	reconnectMu          sync.Mutex
	reconnectInProgress  bool

	// session-setup waiter
	sessionMu               sync.Mutex
	sessionReady            chan transport.Message
	pendingMu               sync.Mutex
	pendingEncryptedByNonce map[uint64]transport.Message

	placeMu              sync.Mutex
	placeOutcomeWaiters  []*placeOutcomeWaiter
	recentTerminalOrders []*OrderUpdate
	placeTerminalTimeout time.Duration
}

// NewClient validates config and returns an unconnected client. Call Connect
// to bring it up.
func NewClient(cfg ClientConfig) (*GodarkClient, error) {
	creds, err := resolveAuthCredentials(cfg)
	if err != nil {
		return nil, err
	}

	baseURL := resolveEdgeBaseURL(cfg.BaseURL, cfg.Environment)
	// When BaseURL is explicit (e.g. GODARK_EDGE_URL=devnet), bake the HPKE pin
	// from that host so EnvironmentTestnet + devnet URL does not pick the wrong key.
	pinEnv := cfg.Environment
	if strings.TrimSpace(cfg.BaseURL) != "" {
		pinEnv = inferEnvironmentFromRestURL(cfg.BaseURL)
	}
	fallbackAccount := resolveAccount(cfg.Account)

	symbolMap := cfg.SymbolMap
	if symbolMap == nil {
		symbolMap = DefaultSymbolMap()
	}

	bufSize := cfg.StreamBufferSize
	if bufSize <= 0 {
		bufSize = 256
	}

	if cfg.HTTPClient != nil {
		cfg.Transport.HTTPClient = cfg.HTTPClient
	}

	terminalTimeout := 30 * time.Second
	if cfg.Transport.CommandTimeout > 0 {
		terminalTimeout = cfg.Transport.CommandTimeout
	}
	if cfg.PlaceOrderTerminalTimeout != nil {
		if *cfg.PlaceOrderTerminalTimeout <= 0 {
			return nil, errors.New("PlaceOrderTerminalTimeout must be greater than 0")
		}
		terminalTimeout = *cfg.PlaceOrderTerminalTimeout
	}

	c := &GodarkClient{
		authToken:               creds.legacyToken,
		apiKeyID:                creds.apiKeyID,
		apiSecret:               creds.apiSecret,
		passphrase:              creds.passphrase,
		httpClient:              cfg.HTTPClient,
		baseURL:                 baseURL,
		fallbackAccount:         fallbackAccount,
		symbolMap:               symbolMap,
		instrumentDecs:          make(map[string]InstrumentDecimals),
		bufSize:                 bufSize,
		placeTerminalTimeout:    terminalTimeout,
		hpkeStaticKey:           resolveHpkeStaticPublicKey(cfg.HpkeStaticPublicKeyHex, pinEnv),
		session:                 &session.CryptoSession{},
		pendingEncryptedByNonce: make(map[uint64]transport.Message),
		desiredChannels:         make(map[string]struct{}),
		disableAutoReconnect:    cfg.DisableAutoReconnect,
		orderQueue:              make(chan *OrderUpdate, bufSize),
		positionQueue:           make(chan *PositionUpdate, bufSize),
		posSnapshotQueue:        make(chan *PositionsSnapshot, bufSize),
		systemHealthQueue:       make(chan *SystemHealthUpdate, bufSize),
		balanceQueue:            make(chan *BalanceUpdate, bufSize),
		marginAlertQueue:        make(chan *MarginAlert, bufSize),
		fundingRateQueue:        make(chan *FundingRateUpdate, bufSize),
		settlementQueue:         make(chan *SettlementUpdate, bufSize),
		leverageSettingsQueue:   make(chan *LeverageSettings, bufSize),
		openOrdersSnapshotQueue: make(chan *OpenOrdersSnapshot, bufSize),
	}

	c.transport = transport.New(transport.EdgeURL(baseURL), cfg.Transport, transport.Handlers{
		OnEncryptedPush:      c.handleEncryptedPush,
		OnPublicMessage:      c.handlePublicMessage,
		OnSessionEstablished: c.handleSessionEstablished,
		OnRekeyRequired:      c.handleRekeyRequired,
		OnStale:              c.handleStale,
		OnDisconnect:         c.handleDisconnect,
	})
	return c, nil
}

func defaultReconnectBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		return time.Second
	}
	d := time.Second << attempt
	if d > 30*time.Second {
		return 30 * time.Second
	}
	return d
}

var reconnectBackoff = defaultReconnectBackoff

// UserUUID returns the authenticated user's canonical UUID. Empty until
// Connect has completed successfully. Current account-based edges do not
// provide this legacy identifier, so it is normally empty.
func (c *GodarkClient) UserUUID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.userUUID
}

// Account returns the authenticated 32-byte Solana account as base58.
func (c *GodarkClient) Account() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.account
}

// AccountID, LoginSessionID, TokenExpiresAt, CancelOnDisconnect expose
// optional auth-response fields the edge may have populated.
func (c *GodarkClient) AccountID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.accountID
}
func (c *GodarkClient) LoginSessionID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.loginSessionID
}
func (c *GodarkClient) TokenExpiresAt() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tokenExpiresAt
}
func (c *GodarkClient) CancelOnDisconnect() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cancelOnDisc
}

// IsConnected reports whether the client completed Connect successfully.
func (c *GodarkClient) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected
}

// -----------------------------------------------------------------------
// Lifecycle
// -----------------------------------------------------------------------

// Connect opens the WebSocket, authenticates with the configured API key, and
// completes the HPKE binary session setup. After Connect returns nil, the client
// can issue trading commands.
func (c *GodarkClient) Connect(ctx context.Context) error {
	c.mu.Lock()
	c.intentionalClose = false
	c.mu.Unlock()
	return c.connectSession(ctx)
}

func (c *GodarkClient) connectSession(ctx context.Context) error {
	c.session.Reset()
	c.pendingMu.Lock()
	c.pendingEncryptedByNonce = make(map[uint64]transport.Message)
	c.pendingMu.Unlock()

	// Best-effort: local mocks / older edges may omit instruments. Trading
	// then falls back to the embedded symbol map and DefaultInstrumentDecimals.
	_ = c.refreshInstruments(ctx)

	loginToken, err := c.resolveLoginToken(ctx)
	if err != nil {
		return newAuthenticationError(err.Error())
	}

	if err := c.transport.Connect(ctx); err != nil {
		return newConnectionError(err.Error())
	}

	auth, err := c.transport.Authenticate(ctx, loginToken)
	if err != nil {
		_ = c.disconnectInternal()
		return newAuthenticationError(err.Error())
	}
	if success, _ := auth["success"].(bool); !success {
		_ = c.disconnectInternal()
		msg, _ := auth["error"].(string)
		if msg == "" {
			msg = "authentication failed"
		}
		return newAuthenticationError(msg)
	}

	account := stringValue(auth["account"])
	if account == "" {
		account = c.fallbackAccount
	}
	if _, err := identity.AccountToBytes(account); err != nil {
		_ = c.disconnectInternal()
		return newAuthenticationError(
			"authentication succeeded but account is missing or invalid; " +
				"set ClientConfig.Account or GODARK_ACCOUNT / GDX_ACCOUNT for a legacy local edge",
		)
	}

	c.mu.Lock()
	c.account = account
	c.userUUID = stringValue(auth["user_uuid"])
	c.accountID = stringValue(auth["account_id"])
	c.loginSessionID = stringValue(auth["session_id"])
	c.tokenExpiresAt = stringValue(auth["token_expires_at"])
	if v, ok := auth["cancel_on_disconnect"].(bool); ok {
		c.cancelOnDisc = v
	}
	c.mu.Unlock()

	connID := coerceUint64(auth["conn_id"])
	if connID == 0 {
		_ = c.disconnectInternal()
		return newAuthenticationError("auth response did not include a non-zero conn_id (required for HPKE)")
	}
	c.mu.Lock()
	c.connID = connID
	c.mu.Unlock()
	if err := c.setupHpkeSession(ctx); err != nil {
		_ = c.disconnectInternal()
		return err
	}

	c.mu.Lock()
	c.connected = true
	c.mu.Unlock()
	return nil
}

// Disconnect closes the WebSocket and clears the HPKE session. Safe to call
// from multiple goroutines.
func (c *GodarkClient) Disconnect() error {
	return c.disconnectInternal()
}

func (c *GodarkClient) disconnectInternal() error {
	c.mu.Lock()
	c.intentionalClose = true
	c.connected = false
	c.mu.Unlock()
	c.transport.Disconnect()
	c.session.Reset()
	c.pendingMu.Lock()
	c.pendingEncryptedByNonce = make(map[uint64]transport.Message)
	c.pendingMu.Unlock()
	return nil
}

// -----------------------------------------------------------------------
// Trading
// -----------------------------------------------------------------------

// PlaceOrderRequest is the input to PlaceOrder. Price is required for LIMIT
// orders, ignored for MARKET.
type PlaceOrderRequest struct {
	Symbol      string
	Side        Side
	OrderType   OrderType
	Quantity    float64
	Price       float64 // ignored when zero for non-LIMIT order types
	TimeInForce TimeInForce
	AON         bool
	MinFillSize *float64
	ExpiryTime  *uint64
	// Options carries optional reduce-only / post-only / STP flags.
	Options PlaceOrderOptions
	// Confirmation selects the completion boundary. Empty defaults to Book.
	// Ack returns on the sequencer fast ack; Book waits for a definitive
	// order update and returns OrderError on REJECTED.
	Confirmation PlaceOrderConfirmation
}

// PlaceOrder sends an encrypted place command and waits for its fast ack.
// By default (Confirmation Book) it then waits for OPEN / REJECTED / FILLED /
// PARTIALLY_FILLED / CANCELLED and surfaces REJECTED as OrderError with code
// and msg / reject text. Confirmation Ack returns after the fast ack; callers
// must consume OrderUpdates themselves. The transport serializes command sends.
func (c *GodarkClient) PlaceOrder(ctx context.Context, req PlaceOrderRequest) (*OrderAck, error) {
	if err := c.ensureReady(); err != nil {
		return nil, err
	}
	symbolID, err := c.resolveSymbol(req.Symbol)
	if err != nil {
		return nil, err
	}
	if req.TimeInForce == "" {
		req.TimeInForce = TimeInForceGTC
	}
	if req.Side == "" || req.OrderType == "" {
		return nil, errors.New("PlaceOrder: Side and OrderType are required")
	}
	confirmation := req.Confirmation
	if confirmation == "" {
		confirmation = PlaceOrderConfirmationBook
	}
	if confirmation != PlaceOrderConfirmationAck && confirmation != PlaceOrderConfirmationBook {
		return nil, fmt.Errorf("PlaceOrder: Confirmation must be %q or %q",
			PlaceOrderConfirmationAck, PlaceOrderConfirmationBook)
	}

	corrID := newCorrelationID()
	var pricePtr *float64
	if req.Price != 0 {
		p := req.Price
		pricePtr = &p
	}

	decimals, err := c.instrumentDecimals(req.Symbol)
	if err != nil {
		return nil, err
	}

	plaintext, err := BuildPlaceOrderRequest(
		uint64(symbolID),
		req.Side, req.OrderType,
		req.Quantity,
		c.accountBytes(),
		pricePtr,
		req.TimeInForce,
		req.AON,
		req.MinFillSize,
		req.ExpiryTime,
		corrID,
		req.Options,
		decimals,
	)
	if err != nil {
		return nil, err
	}
	// Register before send so a terminal push that races the ack is not lost.
	var waiter *placeOutcomeWaiter
	if confirmation == PlaceOrderConfirmationBook {
		waiter = c.registerPlaceOutcomeWaiter()
	}
	ack, err := c.sendEncryptedOrder(ctx, "place", uint64(symbolID), plaintext, corrID)
	if err != nil {
		c.cancelPlaceOutcomeWaiter(waiter)
		return nil, err
	}
	if waiter == nil {
		return ack, nil
	}
	update, err := c.awaitPlaceOutcome(ctx, ack.OrderID, waiter)
	if err != nil {
		return nil, err
	}
	if update.UpdateType == OrderUpdateTypeRejected || update.Status == OrderStatusRejected {
		if numeric, parseErr := strconv.ParseInt(update.RejectReason, 10, 32); parseErr == nil {
			code := int32(numeric)
			return nil, MakeOrderErrorFromCode(&code, update.Msg)
		}
		return nil, MakeOrderErrorFromJSON(update.Msg, update.RejectReason)
	}
	return ack, nil
}

// CancelOrder sends an encrypted cancel command and waits for its ack.
// orderID is the wire integer (decimal string) returned from PlaceOrder.
func (c *GodarkClient) CancelOrder(ctx context.Context, orderID string, symbol string) (*OrderAck, error) {
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
	plaintext, err := BuildCancelOrderRequest(oid, c.accountBytes(), uint64(symbolID), corrID)
	if err != nil {
		return nil, err
	}
	return c.sendEncryptedOrder(ctx, "cancel", uint64(symbolID), plaintext, corrID)
}

// ModifyOrder sends an encrypted modify command and waits for its ack.
// At least one of newPrice, newQuantity, or newTriggerPrice must be non-nil.
func (c *GodarkClient) ModifyOrder(ctx context.Context, orderID, symbol string, newPrice, newQuantity, newTriggerPrice *float64) (*OrderAck, error) {
	if err := c.ensureReady(); err != nil {
		return nil, err
	}
	if newPrice == nil && newQuantity == nil && newTriggerPrice == nil {
		return nil, errors.New("ModifyOrder: at least one of newPrice / newQuantity / newTriggerPrice required")
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
	decimals, err := c.instrumentDecimals(symbol)
	if err != nil {
		return nil, err
	}
	plaintext, err := BuildModifyOrderRequest(oid, c.accountBytes(), uint64(symbolID), newPrice, newQuantity, newTriggerPrice, corrID, decimals)
	if err != nil {
		return nil, err
	}
	return c.sendEncryptedOrder(ctx, "modify", uint64(symbolID), plaintext, corrID)
}

// CancelAllOrders cancels all open orders. When symbol is empty, cancels every market.
func (c *GodarkClient) CancelAllOrders(ctx context.Context, symbol string) (*CountAck, error) {
	if err := c.ensureReady(); err != nil {
		return nil, err
	}
	var headerSymbolID uint64
	var bodySymbolID *uint64
	if symbol != "" {
		sid, err := c.resolveSymbol(symbol)
		if err != nil {
			return nil, err
		}
		usid := uint64(sid)
		headerSymbolID = usid
		bodySymbolID = &usid
	}
	corrID := newCorrelationID()
	plaintext, err := BuildCancelAll(bodySymbolID, c.accountBytes(), corrID)
	if err != nil {
		return nil, err
	}
	resp, err := c.sendEncryptedCommand(ctx, "cancel_all", headerSymbolID, plaintext, corrID)
	if err != nil {
		return nil, err
	}
	return c.parseCountAckResponse(resp, "cancel_all_ack")
}

// CloseAll closes all positions at market (reduce-only IOC). Scoped to symbol when set.
func (c *GodarkClient) CloseAll(ctx context.Context, symbol string) (*CountAck, error) {
	if err := c.ensureReady(); err != nil {
		return nil, err
	}
	var headerSymbolID uint64
	var bodySymbolID *uint64
	if symbol != "" {
		sid, err := c.resolveSymbol(symbol)
		if err != nil {
			return nil, err
		}
		usid := uint64(sid)
		headerSymbolID = usid
		bodySymbolID = &usid
	}
	corrID := newCorrelationID()
	plaintext, err := BuildCloseAll(bodySymbolID, c.accountBytes(), corrID)
	if err != nil {
		return nil, err
	}
	resp, err := c.sendEncryptedCommand(ctx, "close_all", headerSymbolID, plaintext, corrID)
	if err != nil {
		return nil, err
	}
	return c.parseCountAckResponse(resp, "close_all_ack")
}

// ReversePosition reverses the open position on symbol (flatten + open opposite side).
func (c *GodarkClient) ReversePosition(ctx context.Context, symbol string) (*CountAck, error) {
	if err := c.ensureReady(); err != nil {
		return nil, err
	}
	if symbol == "" {
		return nil, errors.New("ReversePosition: symbol is required")
	}
	symbolID, err := c.resolveSymbol(symbol)
	if err != nil {
		return nil, err
	}
	corrID := newCorrelationID()
	plaintext, err := BuildReverse(uint64(symbolID), c.accountBytes(), corrID)
	if err != nil {
		return nil, err
	}
	resp, err := c.sendEncryptedCommand(ctx, "reverse", uint64(symbolID), plaintext, corrID)
	if err != nil {
		return nil, err
	}
	return c.parseCountAckResponse(resp, "reverse_ack")
}

// AmendTpsl amends or attaches TP/SL on a resting order or open position.
func (c *GodarkClient) AmendTpsl(
	ctx context.Context,
	symbol string,
	orderID uint64,
	takeProfitPrice *float64,
	stopLossPrice *float64,
	positionSide *Side,
) (*TpslAck, error) {
	if err := c.ensureReady(); err != nil {
		return nil, err
	}
	if orderID == 0 && positionSide == nil {
		return nil, errors.New("positionSide is required when orderID is 0")
	}
	symbolID, err := c.resolveSymbol(symbol)
	if err != nil {
		return nil, err
	}
	corrID := newCorrelationID()
	var bodySymbolID *uint64
	if orderID == 0 {
		sid := uint64(symbolID)
		bodySymbolID = &sid
	}
	decimals, err := c.instrumentDecimals(symbol)
	if err != nil {
		return nil, err
	}
	plaintext, err := BuildAmendTpsl(
		c.accountBytes(), orderID, corrID, takeProfitPrice, stopLossPrice, bodySymbolID, positionSide, decimals,
	)
	if err != nil {
		return nil, err
	}
	resp, err := c.sendEncryptedCommand(ctx, "amend_tpsl", uint64(symbolID), plaintext, corrID)
	if err != nil {
		return nil, err
	}
	return c.parseTpslAckResponse(resp)
}

// CancelTpsl cancels TP/SL without cancelling the parent entry or flattening the position.
func (c *GodarkClient) CancelTpsl(
	ctx context.Context,
	symbol string,
	orderID uint64,
	positionSide *Side,
) (*TpslAck, error) {
	if err := c.ensureReady(); err != nil {
		return nil, err
	}
	if orderID == 0 && positionSide == nil {
		return nil, errors.New("positionSide is required when orderID is 0")
	}
	symbolID, err := c.resolveSymbol(symbol)
	if err != nil {
		return nil, err
	}
	corrID := newCorrelationID()
	var bodySymbolID *uint64
	if orderID == 0 {
		sid := uint64(symbolID)
		bodySymbolID = &sid
	}
	plaintext, err := BuildCancelTpsl(c.accountBytes(), orderID, corrID, bodySymbolID, positionSide)
	if err != nil {
		return nil, err
	}
	resp, err := c.sendEncryptedCommand(ctx, "cancel_tpsl", uint64(symbolID), plaintext, corrID)
	if err != nil {
		return nil, err
	}
	return c.parseTpslAckResponse(resp)
}

// -----------------------------------------------------------------------
// Subscriptions
// -----------------------------------------------------------------------

// Subscribe subscribes to one or more push channels. Common channels are
// "orders", "positions".
func (c *GodarkClient) Subscribe(ctx context.Context, channels ...string) error {
	if err := c.ensureReady(); err != nil {
		return err
	}
	if len(channels) == 0 {
		channels = []string{"orders", "positions"}
	}
	c.subMu.Lock()
	for _, ch := range channels {
		c.desiredChannels[ch] = struct{}{}
	}
	c.subMu.Unlock()
	return c.transport.SendSubscribe(ctx, channels, "subscribe")
}

// Unsubscribe is the inverse of Subscribe.
func (c *GodarkClient) Unsubscribe(ctx context.Context, channels ...string) error {
	if len(channels) == 0 {
		channels = []string{"orders", "positions"}
	}
	c.subMu.Lock()
	for _, ch := range channels {
		delete(c.desiredChannels, ch)
	}
	c.subMu.Unlock()
	if !c.transport.IsConnected() {
		return nil
	}
	return c.transport.SendSubscribe(ctx, channels, "unsubscribe")
}

// -----------------------------------------------------------------------
// Push streams (channels + callbacks)
// -----------------------------------------------------------------------

// OrderUpdates returns a receive-only channel that emits decrypted order
// updates as they arrive from the sequencer. The channel has the configured
// StreamBufferSize; when it fills, the oldest update is dropped to make room.
//
// Channels remain open across the Client lifetime; closing them is the
// caller's responsibility (via context cancellation or by not selecting on
// them).
func (c *GodarkClient) OrderUpdates() <-chan *OrderUpdate { return c.orderQueue }

// PositionUpdates returns a receive-only channel of per-symbol position
// deltas (fills, opens, closes, funding).
func (c *GodarkClient) PositionUpdates() <-chan *PositionUpdate { return c.positionQueue }

// PositionsSnapshots returns a receive-only channel of authoritative full
// position-book batches (initial / periodic / event).
func (c *GodarkClient) PositionsSnapshots() <-chan *PositionsSnapshot { return c.posSnapshotQueue }

// SystemHealthUpdates emits cluster-health pulses from the sequencer.
func (c *GodarkClient) SystemHealthUpdates() <-chan *SystemHealthUpdate {
	return c.systemHealthQueue
}

// BalanceUpdates emits shielded-balance updates.
func (c *GodarkClient) BalanceUpdates() <-chan *BalanceUpdate { return c.balanceQueue }

// MarginAlerts emits margin-tier transitions.
func (c *GodarkClient) MarginAlerts() <-chan *MarginAlert { return c.marginAlertQueue }

// FundingRateUpdates emits per-symbol funding-rate ticks.
func (c *GodarkClient) FundingRateUpdates() <-chan *FundingRateUpdate {
	return c.fundingRateQueue
}

// SettlementUpdates emits settlement-batch lifecycle transitions.
func (c *GodarkClient) SettlementUpdates() <-chan *SettlementUpdate { return c.settlementQueue }

// LeverageSettingsUpdates emits authoritative per-user leverage snapshots.
func (c *GodarkClient) LeverageSettingsUpdates() <-chan *LeverageSettings {
	return c.leverageSettingsQueue
}

// OpenOrdersSnapshots returns a receive-only channel of authoritative open-order
// book batches returned by GetOpenOrders.
func (c *GodarkClient) OpenOrdersSnapshots() <-chan *OpenOrdersSnapshot {
	return c.openOrdersSnapshotQueue
}

// OnOrderUpdate registers a callback invoked on every decoded order update.
// Callbacks fire from the WS recv goroutine; keep them fast and non-blocking.
func (c *GodarkClient) OnOrderUpdate(cb func(*OrderUpdate)) {
	c.cbMu.Lock()
	c.orderCallbacks = append(c.orderCallbacks, cb)
	c.cbMu.Unlock()
}

// OnPositionUpdate registers a callback for incremental position deltas.
func (c *GodarkClient) OnPositionUpdate(cb func(*PositionUpdate)) {
	c.cbMu.Lock()
	c.positionCallbacks = append(c.positionCallbacks, cb)
	c.cbMu.Unlock()
}

// OnPositionsSnapshot registers a callback for full position-book batches.
func (c *GodarkClient) OnPositionsSnapshot(cb func(*PositionsSnapshot)) {
	c.cbMu.Lock()
	c.snapshotCallbacks = append(c.snapshotCallbacks, cb)
	c.cbMu.Unlock()
}

// OnSystemHealth registers a callback for sequencer / MPC cluster health pulses.
func (c *GodarkClient) OnSystemHealth(cb func(*SystemHealthUpdate)) {
	c.cbMu.Lock()
	c.healthCallbacks = append(c.healthCallbacks, cb)
	c.cbMu.Unlock()
}

// OnBalanceUpdate registers a callback for shielded-balance updates.
func (c *GodarkClient) OnBalanceUpdate(cb func(*BalanceUpdate)) {
	c.cbMu.Lock()
	c.balanceCallbacks = append(c.balanceCallbacks, cb)
	c.cbMu.Unlock()
}

// OnMarginAlert registers a callback for margin-tier transitions.
func (c *GodarkClient) OnMarginAlert(cb func(*MarginAlert)) {
	c.cbMu.Lock()
	c.marginCallbacks = append(c.marginCallbacks, cb)
	c.cbMu.Unlock()
}

// OnFundingRateUpdate registers a callback for per-symbol funding-rate ticks.
func (c *GodarkClient) OnFundingRateUpdate(cb func(*FundingRateUpdate)) {
	c.cbMu.Lock()
	c.fundingCallbacks = append(c.fundingCallbacks, cb)
	c.cbMu.Unlock()
}

// OnSettlementUpdate registers a callback for settlement-batch lifecycle.
func (c *GodarkClient) OnSettlementUpdate(cb func(*SettlementUpdate)) {
	c.cbMu.Lock()
	c.settlementCBs = append(c.settlementCBs, cb)
	c.cbMu.Unlock()
}

// OnLeverageSettings registers a callback for leverage-settings snapshots.
func (c *GodarkClient) OnLeverageSettings(cb func(*LeverageSettings)) {
	c.cbMu.Lock()
	c.leverageSettingsCallbacks = append(c.leverageSettingsCallbacks, cb)
	c.cbMu.Unlock()
}

// OnOpenOrdersSnapshot registers a callback for open-order book batches.
func (c *GodarkClient) OnOpenOrdersSnapshot(cb func(*OpenOrdersSnapshot)) {
	c.cbMu.Lock()
	c.openOrdersSnapshotCallbacks = append(c.openOrdersSnapshotCallbacks, cb)
	c.cbMu.Unlock()
}

// OnError registers a callback for non-fatal session / encryption / push-parse errors.
func (c *GodarkClient) OnError(cb func(error)) {
	c.cbMu.Lock()
	c.errorCallbacks = append(c.errorCallbacks, cb)
	c.cbMu.Unlock()
}

// OnDisconnect registers a callback fired when the WebSocket closes for any
// reason. Auto-reconnect runs unless DisableAutoReconnect is set or
// Disconnect() was called intentionally.
func (c *GodarkClient) OnDisconnect(cb func()) {
	c.cbMu.Lock()
	c.disconnectCB = append(c.disconnectCB, cb)
	c.cbMu.Unlock()
}

// OnReconnect registers a callback fired after a successful automatic reconnect
// (auth, HPKE setup, and subscription replay completed).
func (c *GodarkClient) OnReconnect(cb func()) {
	c.cbMu.Lock()
	c.reconnectCB = append(c.reconnectCB, cb)
	c.cbMu.Unlock()
}

// -----------------------------------------------------------------------
// Internals
// -----------------------------------------------------------------------

func (c *GodarkClient) setupHpkeSession(ctx context.Context) error {
	if c.hpkeStaticKey == "" {
		return newSessionError("HPKE static public key unset — set HpkeStaticPublicKeyHex, GDX_HPKE_STATIC_PUBLIC_KEY, or GODARK_HPKE_STATIC_PUBLIC_KEY")
	}
	remoteStatic, err := hpke.ParsePinnedStaticPublicKey(c.hpkeStaticKey)
	if err != nil {
		return newSessionError(err.Error())
	}

	encapped, err := c.session.Setup(remoteStatic, c.accountBytes(), c.connID)
	if err != nil {
		return newSessionError(err.Error())
	}
	frame, err := wire.EncodeHpkeSetup(c.accountBytes(), c.connID, encapped)
	if err != nil {
		return newSessionError(err.Error())
	}

	reply, err := c.transport.SendHpkeSetup(ctx, frame)
	if err != nil {
		c.session.AbortSetup()
		return newSessionError(err.Error())
	}
	replyConnID := coerceUint64(reply["conn_id"])
	if replyConnID != c.connID {
		c.session.AbortSetup()
		return newSessionError(fmt.Sprintf("HPKE setup conn_id mismatch: expected %d, got %d", c.connID, replyConnID))
	}
	if reply["established"] != true {
		c.session.AbortSetup()
		return newSessionError("HPKE setup not established")
	}
	if err := c.session.Establish(); err != nil {
		c.session.AbortSetup()
		return newSessionError(err.Error())
	}
	return nil
}

func (c *GodarkClient) handleSessionEstablished(msg transport.Message) {
	c.sessionMu.Lock()
	ch := c.sessionReady
	c.sessionMu.Unlock()
	if ch != nil {
		select {
		case ch <- msg:
		default:
		}
	}
}

func (c *GodarkClient) handleRekeyRequired(msg transport.Message) {
	// HPKE rekey re-runs the complete binary setup. Failures surface via OnError.
	go func() {
		c.session.Reset()
		if err := c.setupHpkeSession(context.Background()); err != nil {
			c.emitError(err)
		}
	}()
}

func (c *GodarkClient) handleStale(reason string) {
	c.emitError(newConnectionError(reason))
}

func (c *GodarkClient) handleDisconnect() {
	c.mu.Lock()
	c.connected = false
	c.mu.Unlock()
	c.rejectPlaceOutcomeWaiters(newConnectionError(
		"connection lost while waiting for order confirmation",
	))
	c.cbMu.RLock()
	cbs := append([]func(){}, c.disconnectCB...)
	c.cbMu.RUnlock()
	for _, cb := range cbs {
		safeCallNoArg(cb)
	}
	c.maybeStartReconnect()
}

func (c *GodarkClient) maybeStartReconnect() {
	c.mu.RLock()
	intentional := c.intentionalClose
	disable := c.disableAutoReconnect
	c.mu.RUnlock()
	if intentional || disable {
		return
	}
	c.reconnectMu.Lock()
	if c.reconnectInProgress {
		c.reconnectMu.Unlock()
		return
	}
	c.reconnectInProgress = true
	c.reconnectMu.Unlock()
	go c.reconnectLoop()
}

func (c *GodarkClient) reconnectLoop() {
	defer func() {
		c.reconnectMu.Lock()
		c.reconnectInProgress = false
		c.reconnectMu.Unlock()
	}()

	attempt := 0
	for {
		c.mu.RLock()
		intentional := c.intentionalClose
		disable := c.disableAutoReconnect
		c.mu.RUnlock()
		if intentional || disable {
			return
		}

		if delay := reconnectBackoff(attempt); delay > 0 {
			time.Sleep(delay)
		}
		attempt++

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		err := c.connectSession(ctx)
		cancel()
		if err != nil {
			continue
		}

		replayCtx, replayCancel := context.WithTimeout(context.Background(), 30*time.Second)
		_ = c.resubscribeChannels(replayCtx)
		replayCancel()

		c.fireReconnectCallbacks()
		return
	}
}

func (c *GodarkClient) resubscribeChannels(ctx context.Context) error {
	c.subMu.Lock()
	channels := make([]string, 0, len(c.desiredChannels))
	for ch := range c.desiredChannels {
		channels = append(channels, ch)
	}
	c.subMu.Unlock()
	if len(channels) == 0 {
		return nil
	}
	return c.transport.SendSubscribe(ctx, channels, "subscribe")
}

func (c *GodarkClient) fireReconnectCallbacks() {
	c.cbMu.RLock()
	cbs := append([]func(){}, c.reconnectCB...)
	c.cbMu.RUnlock()
	for _, cb := range cbs {
		safeCallNoArg(cb)
	}
}

func (c *GodarkClient) emitError(err error) {
	c.cbMu.RLock()
	cbs := append([]func(error){}, c.errorCallbacks...)
	c.cbMu.RUnlock()
	for _, cb := range cbs {
		func() {
			defer func() { _ = recover() }()
			cb(err)
		}()
	}
}

// sendEncryptedOrder is the shared encrypt-send-await path for place / cancel
// / modify.
func (c *GodarkClient) sendEncryptedOrder(ctx context.Context, requestType string, symbolID uint64, plaintext, correlationID []byte) (*OrderAck, error) {
	resp, err := c.sendEncryptedCommand(ctx, requestType, symbolID, plaintext, correlationID)
	if err != nil {
		return nil, err
	}
	return c.parseOrderResponse(resp)
}

// sendEncryptedCommand encrypts plaintext, sends it with the wire op matching
// requestType, and returns the raw response. Shared by the single-order path
// and the mass-quote / batch paths.
func (c *GodarkClient) sendEncryptedCommand(ctx context.Context, requestType string, symbolID uint64, plaintext, correlationID []byte) (transport.Message, error) {
	return c.sendEncryptedCommandEx(ctx, requestType, symbolID, plaintext, correlationID, false, nil)
}

func (c *GodarkClient) sendEncryptedCommandEx(ctx context.Context, requestType string, symbolID uint64, plaintext, correlationID []byte, _forceLegacy bool, _headerLeverage *int) (transport.Message, error) {
	bodyLength, err := session.BodyLengthForPlaintext(len(plaintext))
	if err != nil {
		return nil, newEncryptionError(err.Error())
	}
	corrKey := wire.CorrelationKeyFromBytes(correlationID)
	if corrKey == "" {
		return nil, newSessionError("encrypted command requires non-zero correlation_id")
	}

	resp, err := c.transport.SendBinaryCommand(ctx, corrKey, func() ([]byte, error) {
		nonceCounter := c.session.NextNonce()
		aad, err := BuildOrderHeaderAADWithConn(c.accountBytes(), symbolID, requestType, nonceCounter, bodyLength, correlationID, c.connectionID())
		if err != nil {
			return nil, err
		}
		actualNonce, ciphertext, err := c.session.EncryptOrder(aad, plaintext)
		if err != nil {
			return nil, newEncryptionError(fmt.Sprintf("encrypt order: %v", err))
		}
		rt, ok := requestTypeToProto[requestType]
		if !ok {
			return nil, fmt.Errorf("unknown request_type %q", requestType)
		}
		header := &edgepb.OrderHeader{
			Account:       c.accountBytes(),
			SymbolId:      symbolID,
			RequestType:   commonpb.RequestType(rt),
			Nonce:         actualNonce,
			BodyLength:    bodyLength,
			CorrelationId: correlationID,
			ConnId:        c.connectionID(),
		}
		req := wire.EncryptedOrderRequest(header, ciphertext)
		return wire.EncodeEncryptedOrder(req)
	})
	if err != nil {
		var to *TimeoutError
		if errors.As(err, &to) {
			return nil, err
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, newConnectionError(err.Error())
	}
	return resp, nil
}

func (c *GodarkClient) parseOrderResponse(msg transport.Message) (*OrderAck, error) {
	mt, _ := msg["type"].(string)
	switch mt {
	case "error":
		errMsg, _ := msg["message"].(string)
		ec, _ := msg["error_code"].(string)
		return nil, MakeOrderErrorFromJSON(errMsg, ec)
	case "ack":
		if v, _ := msg["success"].(bool); !v {
			rejectText := stringValue(msg["reject_text"])
			rawCode := msg["error_code"]
			if num := coerceNumericErrorCode(rawCode); num != nil {
				return nil, MakeOrderErrorFromCode(num, rejectText)
			}
			ec := stringValue(rawCode)
			reason, _ := msg["error"].(string)
			if reason == "" {
				reason = rejectText
			}
			if reason == "" {
				reason = "order rejected"
			}
			return nil, MakeOrderErrorFromJSON(reason, ec)
		}
		return &OrderAck{
			OrderID:  stringValue(msg["order_id"]),
			Success:  true,
			Sequence: stringValue(msg["sequence"]),
		}, nil
	case "encrypted_push":
		return c.decryptAckPush(msg)
	}
	return nil, newOrderError(fmt.Sprintf("unexpected response type: %v", mt), "")
}

func (c *GodarkClient) decryptAckPush(msg transport.Message) (*OrderAck, error) {
	if failed, ok := msg["_decrypt_error"].(string); ok {
		return nil, newEncryptionError("decrypt ack: " + failed)
	}
	if decrypted, ok := msg["_decrypted_plaintext"].([]byte); ok {
		ack, isAck, err := ParseNodeResponseAck(decrypted)
		if err != nil {
			return nil, err
		}
		if !isAck {
			return nil, newOrderError("expected ack inside encrypted push", "")
		}
		if !ack.Success {
			if ack.ErrorCode != nil {
				code := int32(*ack.ErrorCode)
				return nil, MakeOrderErrorFromCode(&code, ack.RejectText)
			}
			return nil, MakeOrderErrorFromJSON(ack.RejectText, "")
		}
		return &OrderAck{OrderID: strconv.FormatUint(ack.OrderID, 10), Success: true, Sequence: strconv.FormatUint(ack.Sequence, 10)}, nil
	}
	ctB64, _ := msg["encrypted_body"].(string)
	ct, err := base64.StdEncoding.DecodeString(ctB64)
	if err != nil {
		return nil, newEncryptionError(fmt.Sprintf("decode ack body: %v", err))
	}
	nonce := coerceUint64(msg["nonce"])
	fencingEpoch := coerceUint64(msg["fencing_epoch"])
	messageType, _ := msg["message_type"].(string)
	if messageType == "" {
		messageType = "ack"
	}

	aad, err := BuildResponseHeaderAADWithConn(
		c.accountBytes(), messageType, uint32(len(ct)), nonce, fencingEpoch,
		correlationIDFromWire(msg["correlation_id"]), coerceUint64(msg["session_seq"]), c.messageConnID(msg),
	)
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
		return nil, newOrderError("expected ack inside encrypted push", "")
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

// decryptCommandPlaintext decrypts an encrypted_push command response and
// returns the plaintext NodeResponse bytes. Used by the mass-quote / batch
// pipelines, which decode their own ack variants from the plaintext.
func (c *GodarkClient) decryptCommandPlaintext(msg transport.Message, defaultMessageType string) ([]byte, error) {
	mt, _ := msg["type"].(string)
	switch mt {
	case "error":
		errMsg, _ := msg["message"].(string)
		ec, _ := msg["error_code"].(string)
		return nil, MakeOrderErrorFromJSON(errMsg, ec)
	case "encrypted_push":
		// handled below
	default:
		return nil, newOrderError(fmt.Sprintf("unexpected response type: %v", mt), "")
	}
	if decrypted, ok := msg["_decrypted_plaintext"].([]byte); ok {
		return decrypted, nil
	}
	if failed, ok := msg["_decrypt_error"].(string); ok {
		return nil, newEncryptionError("decrypt ack: " + failed)
	}

	ctB64, _ := msg["encrypted_body"].(string)
	ct, err := base64.StdEncoding.DecodeString(ctB64)
	if err != nil {
		return nil, newEncryptionError(fmt.Sprintf("decode ack body: %v", err))
	}
	nonce := coerceUint64(msg["nonce"])
	fencingEpoch := coerceUint64(msg["fencing_epoch"])
	messageType, _ := msg["message_type"].(string)
	if messageType == "" {
		messageType = defaultMessageType
	}
	aad, err := BuildResponseHeaderAADWithConn(
		c.accountBytes(), messageType, uint32(len(ct)), nonce, fencingEpoch,
		correlationIDFromWire(msg["correlation_id"]), coerceUint64(msg["session_seq"]), c.messageConnID(msg),
	)
	if err != nil {
		return nil, err
	}
	pt, err := c.session.DecryptPush(nonce, aad, ct)
	if err != nil {
		return nil, newEncryptionError(fmt.Sprintf("decrypt ack: %v", err))
	}
	return pt, nil
}

// MassQuote performs a bulk cancel-replace (market-maker mass quote) on one
// symbol (up to 20 legs), which fuses into one MPC round. Returns one result
// per leg.
//
// Per-symbol leverage is account state; set it with UpdateLeverage before
// mass-quoting. postOnly is the batch-level post-only flag. When nil (the
// default) or true, a replacement leg that would cross is rejected as "failed".
// Pass a pointer to false to enable the relaxed path: a crossing leg takes
// liquidity up to its limit and rests the remainder; such a leg reports FillCount > 0.
func (c *GodarkClient) MassQuote(ctx context.Context, symbol string, legs []MassQuoteLegInput, postOnly *bool) (*MassQuoteAck, error) {
	if err := c.ensureReady(); err != nil {
		return nil, err
	}
	symbolID, err := c.resolveSymbol(symbol)
	if err != nil {
		return nil, err
	}
	corrID := newCorrelationID()
	decimals, err := c.instrumentDecimals(symbol)
	if err != nil {
		return nil, err
	}
	plaintext, err := BuildMassQuoteRequest(uint64(symbolID), c.accountBytes(), legs, corrID, postOnly, decimals)
	if err != nil {
		return nil, err
	}
	resp, err := c.sendEncryptedCommand(ctx, "mass_quote", uint64(symbolID), plaintext, corrID)
	if err != nil {
		return nil, err
	}
	pt, err := c.decryptCommandPlaintext(resp, "mass_quote_ack")
	if err != nil {
		return nil, err
	}
	ack, ok, err := ParseMassQuoteAck(pt)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, newOrderError("expected mass_quote_ack inside encrypted push", "")
	}
	return ack, nil
}

// UpdateLeverage sets per-symbol account leverage over the encrypted WebSocket session.
// Place/mass-quote inherit this setting server-side. Always uses the legacy
// encrypted_order frame (docs-wire has no update_leverage op).
func (c *GodarkClient) UpdateLeverage(ctx context.Context, symbol string, leverage int) (*OrderAck, error) {
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
	plaintext, err := BuildUpdateLeverageRequest(c.accountBytes(), uint64(symbolID), lev, corrID)
	if err != nil {
		return nil, err
	}
	resp, err := c.sendEncryptedCommandEx(ctx, "update_leverage", uint64(symbolID), plaintext, corrID, true, &lev)
	if err != nil {
		return nil, err
	}
	return c.parseOrderResponse(resp)
}

// BatchCancel cancels multiple resting orders on one symbol in a single
// fanned-out request (up to 20 ids). Cancels are pure index removals (zero
// online MPC rounds). An id that is not resting is reported Cancelled=false
// (error_code 2003) and never aborts the rest of the batch.
func (c *GodarkClient) BatchCancel(ctx context.Context, symbol string, orderIDs []uint64) (*BatchCancelAck, error) {
	if err := c.ensureReady(); err != nil {
		return nil, err
	}
	symbolID, err := c.resolveSymbol(symbol)
	if err != nil {
		return nil, err
	}
	corrID := newCorrelationID()
	plaintext, err := BuildBatchCancelRequest(uint64(symbolID), c.accountBytes(), orderIDs, corrID)
	if err != nil {
		return nil, err
	}
	resp, err := c.sendEncryptedCommand(ctx, "batch_cancel", uint64(symbolID), plaintext, corrID)
	if err != nil {
		return nil, err
	}
	pt, err := c.decryptCommandPlaintext(resp, "batch_cancel_ack")
	if err != nil {
		return nil, err
	}
	ack, ok, err := ParseBatchCancelAck(pt)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, newOrderError("expected batch_cancel_ack inside encrypted push", "")
	}
	return ack, nil
}

// BatchModify amends multiple resting orders on one symbol in a single
// fanned-out post-only request (up to 20 legs). Each leg sets NewPrice and/or
// NewQuantity (at least one). A leg whose amended order would cross is rejected
// (Modified=false, error_code 2018) rather than taking liquidity; a missing
// order id is reported Modified=false (error_code 2003). Neither aborts the
// rest of the batch.
func (c *GodarkClient) BatchModify(ctx context.Context, symbol string, legs []BatchModifyLegInput) (*BatchModifyAck, error) {
	if err := c.ensureReady(); err != nil {
		return nil, err
	}
	symbolID, err := c.resolveSymbol(symbol)
	if err != nil {
		return nil, err
	}
	corrID := newCorrelationID()
	decimals, err := c.instrumentDecimals(symbol)
	if err != nil {
		return nil, err
	}
	plaintext, err := BuildBatchModifyRequest(uint64(symbolID), c.accountBytes(), legs, corrID, decimals)
	if err != nil {
		return nil, err
	}
	resp, err := c.sendEncryptedCommand(ctx, "batch_modify", uint64(symbolID), plaintext, corrID)
	if err != nil {
		return nil, err
	}
	pt, err := c.decryptCommandPlaintext(resp, "batch_modify_ack")
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
			return nil, newOrderError(na.RejectText, code)
		}
		return nil, newOrderError("expected batch_modify_ack inside encrypted push", "")
	}
	return ack, nil
}

func (c *GodarkClient) parseCountAckResponse(msg transport.Message, messageType string) (*CountAck, error) {
	pt, err := c.decryptCommandPlaintext(msg, messageType)
	if err != nil {
		return nil, err
	}
	ack, ok, err := ParseCountAck(pt, messageType)
	if err != nil {
		return nil, err
	}
	if !ok {
		na, isAck, parseErr := ParseNodeResponseAck(pt)
		if parseErr != nil {
			return nil, parseErr
		}
		if isAck && !na.Success {
			code := ""
			if na.ErrorCode != nil {
				code = strconv.FormatUint(uint64(*na.ErrorCode), 10)
			}
			return nil, newOrderError(na.RejectText, code)
		}
		return nil, newOrderError(fmt.Sprintf("expected %s inside encrypted push", messageType), "")
	}
	if ack.ErrorCode != nil {
		code := int32(*ack.ErrorCode)
		return nil, MakeOrderErrorFromCode(&code, ack.RejectText)
	}
	return ack, nil
}

func (c *GodarkClient) parseTpslAckResponse(msg transport.Message) (*TpslAck, error) {
	pt, err := c.decryptCommandPlaintext(msg, "tpsl_ack")
	if err != nil {
		return nil, err
	}
	ack, ok, err := ParseTpslAck(pt)
	if err != nil {
		return nil, err
	}
	if !ok {
		na, isAck, parseErr := ParseNodeResponseAck(pt)
		if parseErr != nil {
			return nil, parseErr
		}
		if isAck && !na.Success {
			code := ""
			if na.ErrorCode != nil {
				code = strconv.FormatUint(uint64(*na.ErrorCode), 10)
			}
			return nil, newOrderError(na.RejectText, code)
		}
		return nil, newOrderError("expected tpsl_ack inside encrypted push", "")
	}
	if ack.ErrorCode != nil {
		code := int32(*ack.ErrorCode)
		return nil, MakeOrderErrorFromCode(&code, ack.RejectText)
	}
	return ack, nil
}

func (c *GodarkClient) handleEncryptedPush(msg transport.Message) {
	c.dispatchEncryptedPush(msg)
}

func (c *GodarkClient) handlePublicMessage(msg transport.Message) {
	for _, u := range ParseFundingRateSnapshotJSON(msg) {
		c.dispatchFundingRateUpdate(u)
	}
}

func (c *GodarkClient) dispatchFundingRateUpdate(u *FundingRateUpdate) {
	if u == nil || u.FundingRate == "" {
		return
	}
	nonBlockingSend(c.fundingRateQueue, u)
	c.cbMu.RLock()
	cbs := append([]func(*FundingRateUpdate){}, c.fundingCallbacks...)
	c.cbMu.RUnlock()
	for _, cb := range cbs {
		safeCallFunding(cb, u)
	}
}

func (c *GodarkClient) dispatchEncryptedPush(msg transport.Message) {
	ctB64, _ := msg["encrypted_body"].(string)
	ct, err := base64.StdEncoding.DecodeString(ctB64)
	if err != nil {
		c.emitError(newEncryptionError(fmt.Sprintf("decode push body: %v", err)))
		return
	}
	nonce := coerceUint64(msg["nonce"])
	fencingEpoch := coerceUint64(msg["fencing_epoch"])
	messageType, _ := msg["message_type"].(string)

	switch messageType {
	case "ack", "mass_quote_ack", "batch_cancel_ack", "batch_modify_ack", "cancel_all_ack", "close_all_ack", "reverse_ack", "tpsl_ack":
		aad, err := BuildResponseHeaderAADWithConn(
			c.accountBytes(), messageType, uint32(len(ct)), nonce, fencingEpoch,
			correlationIDFromWire(msg["correlation_id"]), coerceUint64(msg["session_seq"]), c.messageConnID(msg),
		)
		if err != nil {
			c.emitError(err)
			return
		}
		pt, err := c.session.DecryptPush(nonce, aad, ct)
		if err != nil {
			c.emitError(newEncryptionError(fmt.Sprintf("decrypt ack: %v", err)))
			msg["_decrypt_error"] = err.Error()
		} else {
			msg["_decrypted_plaintext"] = pt
		}
		c.transport.ResolveCommand(msg)
		return
	}

	if _, known := responseMessageTypeToProto[messageType]; !known {
		return
	}

	aad, err := BuildResponseHeaderAADWithConn(
		c.accountBytes(), messageType, uint32(len(ct)), nonce, fencingEpoch,
		correlationIDFromWire(msg["correlation_id"]), coerceUint64(msg["session_seq"]), c.messageConnID(msg),
	)
	if err != nil {
		c.emitError(err)
		return
	}

	pt, err := c.session.DecryptPush(nonce, aad, ct)
	if err != nil {
		c.emitError(newEncryptionError(fmt.Sprintf("decrypt push: %v", err)))
		return
	}

	if messageType == "open_orders_snapshot" {
		snap, err := ParseOpenOrdersSnapshot(pt)
		if err != nil {
			c.emitError(fmt.Errorf("parse open_orders_snapshot: %w", err))
			return
		}
		c.dispatchOpenOrdersSnapshot(snap)
		return
	}

	parsed, err := ParseSequencerToEdgeMessage(pt, messageType)
	if err != nil {
		c.emitError(fmt.Errorf("parse encrypted push body: %w", err))
		return
	}
	c.dispatchSequencerPush(parsed)
}

func isTerminalPlaceUpdate(update *OrderUpdate) bool {
	switch update.UpdateType {
	case OrderUpdateTypeOpen, OrderUpdateTypeRejected, OrderUpdateTypeFilled,
		OrderUpdateTypePartiallyFilled, OrderUpdateTypeCancelled:
		return true
	}
	return update.Status == OrderStatusRejected ||
		update.Status == OrderStatusFilled ||
		update.Status == OrderStatusCancelled
}

func (c *GodarkClient) registerPlaceOutcomeWaiter() *placeOutcomeWaiter {
	waiter := &placeOutcomeWaiter{
		ch: make(chan placeOutcomeResult, 1),
	}
	c.placeMu.Lock()
	c.placeOutcomeWaiters = append(c.placeOutcomeWaiters, waiter)
	c.placeMu.Unlock()
	return waiter
}

func (c *GodarkClient) removePlaceOutcomeWaiterLocked(target *placeOutcomeWaiter) {
	for i, waiter := range c.placeOutcomeWaiters {
		if waiter == target {
			c.placeOutcomeWaiters = append(c.placeOutcomeWaiters[:i], c.placeOutcomeWaiters[i+1:]...)
			return
		}
	}
}

func (c *GodarkClient) cancelPlaceOutcomeWaiter(waiter *placeOutcomeWaiter) {
	if waiter == nil {
		return
	}
	c.placeMu.Lock()
	c.removePlaceOutcomeWaiterLocked(waiter)
	c.placeMu.Unlock()
}

func (c *GodarkClient) rejectPlaceOutcomeWaiters(err error) {
	c.placeMu.Lock()
	waiters := c.placeOutcomeWaiters
	c.placeOutcomeWaiters = nil
	c.recentTerminalOrders = nil
	c.placeMu.Unlock()
	for _, waiter := range waiters {
		select {
		case waiter.ch <- placeOutcomeResult{err: err}:
		default:
		}
	}
}

func (c *GodarkClient) awaitPlaceOutcome(
	ctx context.Context, orderID string, waiter *placeOutcomeWaiter,
) (*OrderUpdate, error) {
	// Timeout starts after the fast ack (caller invokes this post-ack).
	c.placeMu.Lock()
	waiter.orderID = orderID
	for _, update := range c.recentTerminalOrders {
		if update.OrderID == orderID {
			c.removePlaceOutcomeWaiterLocked(waiter)
			c.placeMu.Unlock()
			return update, nil
		}
	}
	c.placeMu.Unlock()

	timer := time.NewTimer(c.placeTerminalTimeout)
	defer timer.Stop()
	select {
	case result := <-waiter.ch:
		if result.err != nil {
			return nil, result.err
		}
		return result.update, nil
	case <-ctx.Done():
		c.cancelPlaceOutcomeWaiter(waiter)
		return nil, ctx.Err()
	case <-timer.C:
		c.cancelPlaceOutcomeWaiter(waiter)
		return nil, newTimeoutError(fmt.Sprintf(
			"PlaceOrder timed out waiting for book confirmation after %s",
			c.placeTerminalTimeout,
		))
	}
}

func (c *GodarkClient) observeOrderUpdate(update *OrderUpdate) {
	if !isTerminalPlaceUpdate(update) {
		return
	}
	c.placeMu.Lock()
	defer c.placeMu.Unlock()
	c.recentTerminalOrders = append(c.recentTerminalOrders, update)
	if len(c.recentTerminalOrders) > 64 {
		c.recentTerminalOrders = c.recentTerminalOrders[1:]
	}
	for _, waiter := range c.placeOutcomeWaiters {
		if waiter.orderID != "" && waiter.orderID == update.OrderID {
			c.removePlaceOutcomeWaiterLocked(waiter)
			select {
			case waiter.ch <- placeOutcomeResult{update: update}:
			default:
			}
			return
		}
	}
}

func (c *GodarkClient) dispatchSequencerPush(parsed SequencerPush) {
	c.cbMu.RLock()
	defer c.cbMu.RUnlock()

	switch v := parsed.(type) {
	case *OrderUpdate:
		c.observeOrderUpdate(v)
		nonBlockingSend(c.orderQueue, v)
		for _, cb := range c.orderCallbacks {
			safeCallOrder(cb, v)
		}
	case *PositionUpdate:
		nonBlockingSend(c.positionQueue, v)
		for _, cb := range c.positionCallbacks {
			safeCallPosition(cb, v)
		}
	case *PositionsSnapshot:
		nonBlockingSend(c.posSnapshotQueue, v)
		for _, cb := range c.snapshotCallbacks {
			safeCallSnap(cb, v)
		}
	case *SystemHealthUpdate:
		nonBlockingSend(c.systemHealthQueue, v)
		for _, cb := range c.healthCallbacks {
			safeCallHealth(cb, v)
		}
	case *BalanceUpdate:
		nonBlockingSend(c.balanceQueue, v)
		for _, cb := range c.balanceCallbacks {
			safeCallBalance(cb, v)
		}
	case *MarginAlert:
		nonBlockingSend(c.marginAlertQueue, v)
		for _, cb := range c.marginCallbacks {
			safeCallMargin(cb, v)
		}
	case *FundingRateUpdate:
		c.dispatchFundingRateUpdate(v)
	case *SettlementUpdate:
		nonBlockingSend(c.settlementQueue, v)
		for _, cb := range c.settlementCBs {
			safeCallSettlement(cb, v)
		}
	case *LeverageSettings:
		nonBlockingSend(c.leverageSettingsQueue, v)
		for _, cb := range c.leverageSettingsCallbacks {
			safeCallLeverageSettings(cb, v)
		}
	case *UnknownSequencerPush:
		// silently ignored - forward-compat contract
	}
}

func (c *GodarkClient) dispatchOpenOrdersSnapshot(snap *OpenOrdersSnapshot) {
	c.cbMu.RLock()
	defer c.cbMu.RUnlock()
	nonBlockingSend(c.openOrdersSnapshotQueue, snap)
	for _, cb := range c.openOrdersSnapshotCallbacks {
		safeCallOpenOrdersSnap(cb, snap)
	}
}

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

func (c *GodarkClient) ensureReady() error {
	c.mu.RLock()
	connected := c.connected
	account := c.account
	c.mu.RUnlock()
	if !connected {
		return newConnectionError("not connected")
	}
	if account == "" {
		return newConnectionError("not authenticated")
	}
	if !c.session.IsEstablished() {
		return newSessionError("HPKE session not established")
	}
	return nil
}

func (c *GodarkClient) accountBytes() []byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.account == "" {
		return make([]byte, identity.AccountLen)
	}
	b, err := identity.AccountToBytes(c.account)
	if err != nil {
		return make([]byte, identity.AccountLen)
	}
	return b
}

func (c *GodarkClient) connectionID() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connID
}

func (c *GodarkClient) messageConnID(msg transport.Message) uint64 {
	if id := coerceUint64(msg["conn_id"]); id != 0 {
		return id
	}
	return c.connectionID()
}

func (c *GodarkClient) resolveSymbol(symbol string) (int64, error) {
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

func resolvePassphrase(explicit string) (string, error) {
	if v := strings.TrimSpace(explicit); v != "" {
		return v, nil
	}
	for _, key := range []string{"GODARK_PASSPHRASE", "GDX_PASSPHRASE"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v, nil
		}
	}
	return "", errors.New("passphrase is required when using APIKeyID and APISecret")
}

type authCredentials struct {
	legacyToken string
	apiKeyID    string
	apiSecret   string
	passphrase  string
}

func resolveAuthCredentials(cfg ClientConfig) (authCredentials, error) {
	if cfg.APIKeyID != "" || cfg.APISecret != "" {
		if cfg.APIKeyID == "" || cfg.APISecret == "" {
			return authCredentials{}, errors.New("APIKeyID and APISecret must be provided together")
		}
		if cfg.APIKey != "" {
			return authCredentials{}, errors.New("use either APIKey or (APIKeyID + APISecret), not both")
		}
		passphrase, err := resolvePassphrase(cfg.Passphrase)
		if err != nil {
			return authCredentials{}, err
		}
		return authCredentials{
			apiKeyID:   cfg.APIKeyID,
			apiSecret:  cfg.APISecret,
			passphrase: passphrase,
		}, nil
	}
	if cfg.APIKey != "" {
		if strings.TrimSpace(cfg.Passphrase) != "" {
			return authCredentials{}, errors.New("Passphrase must not be set when using legacy APIKey")
		}
		return authCredentials{legacyToken: cfg.APIKey}, nil
	}
	return authCredentials{}, errors.New("provide APIKey or both APIKeyID + APISecret")
}

// resolveAuthToken is retained for tests that assert legacy token joining is gone.
func resolveAuthToken(cfg ClientConfig) (string, error) {
	creds, err := resolveAuthCredentials(cfg)
	if err != nil {
		return "", err
	}
	if creds.legacyToken != "" {
		return creds.legacyToken, nil
	}
	// Key-triple clients mint a JWT at Connect; constructor must not embed the secret.
	return "", nil
}

func (c *GodarkClient) resolveLoginToken(ctx context.Context) (string, error) {
	if c.apiKeyID != "" {
		token, err := c.mintAccessToken(ctx)
		if err != nil {
			return "", err
		}
		c.mu.Lock()
		c.authToken = token
		c.mu.Unlock()
		return token, nil
	}
	if c.authToken == "" {
		return "", errors.New("missing login token")
	}
	return c.authToken, nil
}

func (c *GodarkClient) mintAccessToken(ctx context.Context) (string, error) {
	restURL := restOriginFromEdgeURL(c.baseURL)
	tr := rest.New(restURL, c.httpClient)
	authData, err := tr.AuthTokenClientCredentials(ctx, c.apiKeyID, c.apiSecret, c.passphrase)
	if err != nil {
		return "", err
	}
	bearer, _ := authData["access_token"].(string)
	if bearer == "" {
		bearer, _ = authData["token"].(string)
	}
	if bearer == "" {
		return "", errors.New("auth/token missing access_token/token")
	}
	return bearer, nil
}

func (c *GodarkClient) refreshInstruments(ctx context.Context) error {
	restURL := restOriginFromEdgeURL(c.baseURL)
	tr := rest.New(restURL, c.httpClient)
	data, err := tr.GetInstruments(ctx)
	if err != nil {
		return fmt.Errorf("load instruments: %w", err)
	}
	symMap, decs, err := parseInstrumentsPayload(data)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(symMap) > 0 {
		c.symbolMap = symMap
	}
	c.instrumentDecs = decs
	return nil
}

func (c *GodarkClient) instrumentDecimals(symbol string) (InstrumentDecimals, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if d, ok := c.instrumentDecs[symbol]; ok {
		return d, nil
	}
	if len(c.instrumentDecs) == 0 {
		return DefaultInstrumentDecimals, nil
	}
	return InstrumentDecimals{}, fmt.Errorf("missing instrument decimals for %q", symbol)
}

func parseInstrumentsPayload(data map[string]any) (map[string]int64, map[string]InstrumentDecimals, error) {
	raw, ok := data["instruments"]
	if !ok {
		return nil, nil, fmt.Errorf("instruments response missing instruments array")
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, nil, fmt.Errorf("instruments response has unexpected type")
	}
	symMap := make(map[string]int64, len(arr))
	decs := make(map[string]InstrumentDecimals, len(arr))
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("instruments[%d] is not an object", i)
		}
		symbol, _ := m["symbol"].(string)
		if symbol == "" {
			continue
		}
		symMap[symbol] = coerceInt64(m["symbol_id"])
		decs[symbol] = InstrumentDecimals{
			PriceDecimals:    uint32(coerceUint64(m["price_decimals"])),
			QuantityDecimals: uint32(coerceUint64(m["quantity_decimals"])),
		}
	}
	return symMap, decs, nil
}

func coerceInt64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case json.Number:
		i, _ := t.Int64()
		return i
	case string:
		i, _ := strconv.ParseInt(t, 10, 64)
		return i
	default:
		return 0
	}
}

func restOriginFromEdgeURL(edge string) string {
	u := strings.TrimSpace(edge)
	u = strings.TrimRight(u, "/")
	for _, suffix := range []string{"/ws/v1", "/ws"} {
		if strings.HasSuffix(u, suffix) {
			u = strings.TrimSuffix(u, suffix)
		}
	}
	switch {
	case strings.HasPrefix(u, "wss://"):
		return "https://" + strings.TrimPrefix(u, "wss://")
	case strings.HasPrefix(u, "ws://"):
		return "http://" + strings.TrimPrefix(u, "ws://")
	case strings.HasPrefix(u, "https://"), strings.HasPrefix(u, "http://"):
		return u
	default:
		return "https://" + u
	}
}

func resolveEdgeBaseURL(explicit string, env Environment) string {
	if v := strings.TrimSpace(explicit); v != "" {
		return v
	}
	for _, key := range []string{"GODARK_EDGE_URL", "GDX_EDGE_URL"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return env.EdgeBaseURL()
}

func resolveAccount(explicit string) string {
	if v := strings.TrimSpace(explicit); v != "" {
		return v
	}
	for _, key := range []string{"GODARK_ACCOUNT", "GDX_ACCOUNT"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

func resolveHpkeStaticPublicKey(explicit string, env Environment) string {
	if v := strings.TrimSpace(explicit); v != "" {
		return v
	}
	for _, key := range []string{
		"GDX_HPKE_STATIC_PUBLIC_KEY",
		"GDX_HPKE_STATIC_PUBKEY",
		"GODARK_HPKE_STATIC_PUBLIC_KEY",
		"VITE_GDX_HPKE_STATIC_PUBKEY",
	} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return env.HpkeStaticPublicKeyHex()
}

func newCorrelationID() []byte {
	id := uuid.New()
	b := make([]byte, 16)
	copy(b, id[:])
	return b
}

func stringValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	}
	return ""
}

func coerceNumericErrorCode(v any) *int32 {
	switch x := v.(type) {
	case nil:
		return nil
	case int:
		c := int32(x)
		return &c
	case int32:
		c := x
		return &c
	case int64:
		c := int32(x)
		return &c
	case float64:
		c := int32(x)
		return &c
	case string:
		if parsed, err := strconv.ParseInt(strings.TrimSpace(x), 10, 32); err == nil {
			c := int32(parsed)
			return &c
		}
	}
	return nil
}

func coerceUint32(v any) uint32 {
	switch x := v.(type) {
	case float64:
		return uint32(x)
	case int:
		return uint32(x)
	case int64:
		return uint32(x)
	case uint32:
		return x
	case uint64:
		return uint32(x)
	}
	return 0
}

func coerceUint64(v any) uint64 {
	switch x := v.(type) {
	case float64:
		return uint64(x)
	case int:
		return uint64(x)
	case int64:
		return uint64(x)
	case uint32:
		return uint64(x)
	case uint64:
		return x
	case string:
		// Decimal-encoded u64 (used by shielded-pool's BalancesResponse
		// because u64 doesn't roundtrip JSON safely).
		if x == "" {
			return 0
		}
		n, err := strconv.ParseUint(strings.TrimSpace(x), 10, 64)
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}

// coerceFloat64 best-effort-parses a JSON number / nullable number into
// a Go float64. nil and unparseable values become 0.
func coerceFloat64(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case string:
		if x == "" {
			return 0
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return 0
		}
		return f
	}
	return 0
}

func nonBlockingSend[T any](ch chan T, v T) {
	for {
		select {
		case ch <- v:
			return
		default:
			select {
			case <-ch:
			default:
				return
			}
		}
	}
}

// safeCall* wrappers swallow panics from user callbacks to protect the recv
// loop. Each variant is a thin wrapper because Go doesn't have a uniform
// "func with any arg" type without reflection.
func safeCallNoArg(cb func()) { defer func() { _ = recover() }(); cb() }
func safeCallOrder(cb func(*OrderUpdate), v *OrderUpdate) {
	defer func() { _ = recover() }()
	cb(v)
}
func safeCallPosition(cb func(*PositionUpdate), v *PositionUpdate) {
	defer func() { _ = recover() }()
	cb(v)
}
func safeCallSnap(cb func(*PositionsSnapshot), v *PositionsSnapshot) {
	defer func() { _ = recover() }()
	cb(v)
}
func safeCallHealth(cb func(*SystemHealthUpdate), v *SystemHealthUpdate) {
	defer func() { _ = recover() }()
	cb(v)
}
func safeCallBalance(cb func(*BalanceUpdate), v *BalanceUpdate) {
	defer func() { _ = recover() }()
	cb(v)
}
func safeCallMargin(cb func(*MarginAlert), v *MarginAlert) {
	defer func() { _ = recover() }()
	cb(v)
}
func safeCallFunding(cb func(*FundingRateUpdate), v *FundingRateUpdate) {
	defer func() { _ = recover() }()
	cb(v)
}
func safeCallSettlement(cb func(*SettlementUpdate), v *SettlementUpdate) {
	defer func() { _ = recover() }()
	cb(v)
}
func safeCallLeverageSettings(cb func(*LeverageSettings), v *LeverageSettings) {
	defer func() { _ = recover() }()
	cb(v)
}
func safeCallOpenOrdersSnap(cb func(*OpenOrdersSnapshot), v *OpenOrdersSnapshot) {
	defer func() { _ = recover() }()
	cb(v)
}
