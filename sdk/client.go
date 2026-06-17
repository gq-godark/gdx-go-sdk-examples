package godark

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	gdxcrypto "github.com/gq-godark/gdx-go-sdk/internal/crypto"
	"github.com/gq-godark/gdx-go-sdk/internal/identity"
	"github.com/gq-godark/gdx-go-sdk/internal/session"
	"github.com/gq-godark/gdx-go-sdk/internal/transport"
)

// Default production WebSocket origin. The client appends `/ws/v1`.
const defaultEdgeBaseURL = "wss://api.godark-dex.com"

// TransportConfig is the public alias for the WebSocket transport
// configuration consumed by ClientConfig.Transport. It re-exports the
// internal/transport.Config type so external callers (examples, downstream
// consumers) can build it without crossing the internal/ boundary.
type TransportConfig = transport.Config

// ClientConfig holds the constructor arguments for NewClient. Either APIKey
// (legacy single opaque key) OR APIKeyID + APISecret (key-pair) must be set.
type ClientConfig struct {
	// APIKey is the legacy single-token auth value. Set this OR
	// (APIKeyID + APISecret), not both.
	APIKey string

	// APIKeyID / APISecret / Passphrase are the modern key-pair credentials.
	// All three must be set together (Passphrase may also come from
	// GODARK_PASSPHRASE / GDX_PASSPHRASE); the client joins them with `:`
	// to form the wire token `key_id:secret:passphrase`.
	APIKeyID   string
	APISecret  string
	Passphrase string

	// BaseURL is the edge WebSocket origin (host only, e.g.
	// `wss://api.godark-dex.com`). The client appends `/ws/v1` to produce
	// the final upgrade URL. Defaults to production; override with this
	// field or with the GODARK_EDGE_URL / GDX_EDGE_URL env vars.
	BaseURL string

	// UserUUID is an optional fallback when the edge auth response omits
	// `user_uuid` (e.g. local edge). Also read from GODARK_USER_UUID /
	// GDX_USER_UUID env vars.
	UserUUID string

	// SymbolMap overrides the embedded default symbol map. Useful when
	// running against a non-prod edge with a custom symbol set.
	SymbolMap map[string]int64

	// Transport tunes WebSocket transport behaviour (TLS, timeouts, etc.).
	// Zero-value defaults are reasonable.
	Transport transport.Config

	// HTTPClient is forwarded to the transport for the upgrade request.
	HTTPClient *http.Client

	// StreamBufferSize is the max in-memory buffer of push frames per stream
	// (order updates, position updates, etc.). When the buffer fills, the
	// oldest frame is dropped. Default 256.
	StreamBufferSize int
}

// GodarkClient is the encrypted trading client.
//
// Lifecycle: NewClient -> Connect -> (Subscribe ->) PlaceOrder / CancelOrder
// / ModifyOrder + (OrderUpdates / OnOrderUpdate) -> Disconnect. The
// concurrent-safety model is identical to python's: one trading command in
// flight at a time (gated by the transport mutex); push-stream consumers
// (channels and callbacks) run concurrently with command issuance.
type GodarkClient struct {
	authToken      string
	baseURL        string
	fallbackUserUU string
	symbolMap      map[string]int64
	bufSize        int

	transport *transport.Transport
	session   *session.CryptoSession

	mu             sync.RWMutex
	userUUID       string
	accountID      string
	loginSessionID string
	tokenExpiresAt string
	cancelOnDisc   bool
	connected      bool

	// per-stream queues + callbacks
	orderQueue        chan *OrderUpdate
	positionQueue     chan *PositionUpdate
	posSnapshotQueue  chan *PositionsSnapshot
	systemHealthQueue chan *SystemHealthUpdate
	balanceQueue      chan *BalanceUpdate
	marginAlertQueue  chan *MarginAlert
	fundingRateQueue  chan *FundingRateUpdate
	settlementQueue   chan *SettlementUpdate

	cbMu               sync.RWMutex
	orderCallbacks     []func(*OrderUpdate)
	positionCallbacks  []func(*PositionUpdate)
	snapshotCallbacks  []func(*PositionsSnapshot)
	healthCallbacks    []func(*SystemHealthUpdate)
	balanceCallbacks   []func(*BalanceUpdate)
	marginCallbacks    []func(*MarginAlert)
	fundingCallbacks   []func(*FundingRateUpdate)
	settlementCBs      []func(*SettlementUpdate)
	errorCallbacks     []func(error)
	disconnectCB       []func()

	// session-setup waiter
	sessionMu     sync.Mutex
	sessionReady  chan transport.Message
}

// NewClient validates config and returns an unconnected client. Call Connect
// to bring it up.
func NewClient(cfg ClientConfig) (*GodarkClient, error) {
	authToken, err := resolveAuthToken(cfg)
	if err != nil {
		return nil, err
	}

	baseURL := resolveEdgeBaseURL(cfg.BaseURL)
	fallbackUUID := resolveUserUUID(cfg.UserUUID)

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

	c := &GodarkClient{
		authToken:         authToken,
		baseURL:           baseURL,
		fallbackUserUU:    fallbackUUID,
		symbolMap:         symbolMap,
		bufSize:           bufSize,
		session:           &session.CryptoSession{},
		orderQueue:        make(chan *OrderUpdate, bufSize),
		positionQueue:     make(chan *PositionUpdate, bufSize),
		posSnapshotQueue:  make(chan *PositionsSnapshot, bufSize),
		systemHealthQueue: make(chan *SystemHealthUpdate, bufSize),
		balanceQueue:      make(chan *BalanceUpdate, bufSize),
		marginAlertQueue:  make(chan *MarginAlert, bufSize),
		fundingRateQueue:  make(chan *FundingRateUpdate, bufSize),
		settlementQueue:   make(chan *SettlementUpdate, bufSize),
	}

	c.transport = transport.New(transport.EdgeURL(baseURL), cfg.Transport, transport.Handlers{
		OnEncryptedPush:      c.handleEncryptedPush,
		OnSessionEstablished: c.handleSessionEstablished,
		OnRekeyRequired:      c.handleRekeyRequired,
		OnDisconnect:         c.handleDisconnect,
	})
	return c, nil
}

// UserUUID returns the authenticated user's canonical UUID. Empty until
// Connect has completed successfully.
func (c *GodarkClient) UserUUID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.userUUID
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
// completes the ECDH session handshake. After Connect returns nil, the client
// can issue trading commands.
func (c *GodarkClient) Connect(ctx context.Context) error {
	if err := c.transport.Connect(ctx); err != nil {
		return newConnectionError(err.Error())
	}

	auth, err := c.transport.Authenticate(ctx, c.authToken)
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

	uid := stringValue(auth["user_uuid"])
	if uid == "" {
		uid = stringValue(auth["user_id"])
	}
	if uid == "" {
		uid = c.fallbackUserUU
	}
	if uid == "" {
		_ = c.disconnectInternal()
		return newAuthenticationError(
			"authentication succeeded but user_uuid missing in auth_result " +
				"and no fallback provided via constructor or " +
				"GODARK_USER_UUID / GDX_USER_UUID env vars",
		)
	}

	c.mu.Lock()
	c.userUUID = uid
	c.accountID = stringValue(auth["account_id"])
	c.loginSessionID = stringValue(auth["session_id"])
	c.tokenExpiresAt = stringValue(auth["token_expires_at"])
	if v, ok := auth["cancel_on_disconnect"].(bool); ok {
		c.cancelOnDisc = v
	}
	c.mu.Unlock()

	if err := c.setupECDHSession(ctx); err != nil {
		_ = c.disconnectInternal()
		return err
	}

	c.mu.Lock()
	c.connected = true
	c.mu.Unlock()
	return nil
}

// Disconnect closes the WebSocket and clears the ECDH session. Safe to call
// from multiple goroutines.
func (c *GodarkClient) Disconnect() error {
	return c.disconnectInternal()
}

func (c *GodarkClient) disconnectInternal() error {
	c.mu.Lock()
	c.connected = false
	c.mu.Unlock()
	c.transport.Disconnect()
	c.session.Reset()
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
}

// PlaceOrder sends an encrypted place command and waits for its ack. The
// transport serializes commands so only one is in flight at a time.
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
		req.TimeInForce,
		req.AON,
		req.MinFillSize,
		req.ExpiryTime,
		corrID,
		uint64(time.Now().UnixNano()),
	)
	if err != nil {
		return nil, err
	}
	return c.sendEncryptedOrder(ctx, "place", uint64(symbolID), plaintext, corrID)
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
	plaintext, err := BuildCancelOrderRequest(oid, c.userUUIDBytes(), uint64(symbolID), corrID)
	if err != nil {
		return nil, err
	}
	return c.sendEncryptedOrder(ctx, "cancel", uint64(symbolID), plaintext, corrID)
}

// ModifyOrder sends an encrypted modify command and waits for its ack.
// Either newPrice or newQuantity (or both) must be non-nil.
func (c *GodarkClient) ModifyOrder(ctx context.Context, orderID, symbol string, newPrice, newQuantity *float64) (*OrderAck, error) {
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
	return c.sendEncryptedOrder(ctx, "modify", uint64(symbolID), plaintext, corrID)
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
	return c.transport.SendSubscribe(ctx, channels, "subscribe")
}

// Unsubscribe is the inverse of Subscribe.
func (c *GodarkClient) Unsubscribe(ctx context.Context, channels ...string) error {
	if !c.transport.IsConnected() {
		return nil
	}
	if len(channels) == 0 {
		channels = []string{"orders", "positions"}
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

// OnError registers a callback for non-fatal session / encryption / push-parse errors.
func (c *GodarkClient) OnError(cb func(error)) {
	c.cbMu.Lock()
	c.errorCallbacks = append(c.errorCallbacks, cb)
	c.cbMu.Unlock()
}

// OnDisconnect registers a callback fired when the WebSocket closes for any
// reason. The callback does not block reconnection logic (which is the
// caller's responsibility - the SDK does not auto-reconnect).
func (c *GodarkClient) OnDisconnect(cb func()) {
	c.cbMu.Lock()
	c.disconnectCB = append(c.disconnectCB, cb)
	c.cbMu.Unlock()
}

// -----------------------------------------------------------------------
// Internals
// -----------------------------------------------------------------------

func (c *GodarkClient) setupECDHSession(ctx context.Context) error {
	pubB64, err := c.session.GenerateKeypair()
	if err != nil {
		return newSessionError(fmt.Sprintf("generate keypair: %v", err))
	}

	c.sessionMu.Lock()
	c.sessionReady = make(chan transport.Message, 1)
	ready := c.sessionReady
	c.sessionMu.Unlock()

	defer func() {
		c.sessionMu.Lock()
		c.sessionReady = nil
		c.sessionMu.Unlock()
	}()

	payload := map[string]any{
		"id":   uuid.NewString(),
		"op":   "session.setup",
		"args": map[string]any{"client_ecdh_pubkey": pubB64},
	}
	if !c.transport.UseDocsWire() {
		payload = map[string]any{
			"type": "session_setup",
			"data": map[string]any{
				"user_uuid":          c.userUUID,
				"client_ecdh_pubkey": pubB64,
			},
		}
	}
	if err := c.transport.SendJSON(ctx, payload); err != nil {
		return newSessionError(err.Error())
	}

	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return newSessionError(ctx.Err().Error())
	case <-timer.C:
		return newSessionError("ECDH session setup timed out")
	case msg, ok := <-ready:
		if !ok {
			return newSessionError("session setup channel closed before reply")
		}
		if t, _ := msg["type"].(string); t == "error" {
			errText, _ := msg["message"].(string)
			if errText == "" {
				errText = "session setup failed"
			}
			return newSessionError(errText)
		}
		seqPub := stringValue(msg["sequencer_ecdh_pubkey"])
		sidRaw := msg["session_id"]
		var sid uint64
		switch v := sidRaw.(type) {
		case float64:
			sid = uint64(v)
		case int:
			sid = uint64(v)
		case int64:
			sid = uint64(v)
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
	// Rekey support: re-run ECDH on the fly. Failures are surfaced via OnError
	// rather than the connection being torn down (caller can choose to
	// reconnect manually).
	go func() {
		c.session.Reset()
		if err := c.setupECDHSession(context.Background()); err != nil {
			c.emitError(err)
		}
	}()
}

func (c *GodarkClient) handleDisconnect() {
	c.mu.Lock()
	c.connected = false
	c.mu.Unlock()
	c.cbMu.RLock()
	cbs := append([]func(){}, c.disconnectCB...)
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

	var payload any
	if c.transport.UseDocsWire() {
		opMap := map[string]string{
			"place":        "order.place",
			"cancel":       "order.cancel",
			"modify":       "order.modify",
			"mass_quote":   "order.mass_quote",
			"batch_cancel": "order.batch_cancel",
			"batch_modify": "order.batch_modify",
		}
		payload = map[string]any{
			"id":   uuid.NewString(),
			"op":   opMap[requestType],
			"args": map[string]any{"header": headerObj, "ciphertext": bodyB64},
		}
	} else {
		payload = map[string]any{
			"type": "encrypted_order",
			"data": map[string]any{"header": headerObj, "encrypted_body": bodyB64},
		}
	}

	resp, err := c.transport.SendCommand(ctx, payload)
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
			rawCode := msg["error_code"]
			if num := coerceNumericErrorCode(rawCode); num != nil {
				return nil, MakeOrderErrorFromCode(num)
			}
			ec := stringValue(rawCode)
			reason, _ := msg["error"].(string)
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
	ctB64, _ := msg["encrypted_body"].(string)
	ct, err := base64.StdEncoding.DecodeString(ctB64)
	if err != nil {
		return nil, newEncryptionError(fmt.Sprintf("decode ack body: %v", err))
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
		return nil, newOrderError("expected ack inside encrypted push", "")
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

	ctB64, _ := msg["encrypted_body"].(string)
	ct, err := base64.StdEncoding.DecodeString(ctB64)
	if err != nil {
		return nil, newEncryptionError(fmt.Sprintf("decode ack body: %v", err))
	}
	nonce := coerceUint32(msg["nonce"])
	fencingEpoch := coerceUint64(msg["fencing_epoch"])
	messageType, _ := msg["message_type"].(string)
	if messageType == "" {
		messageType = defaultMessageType
	}
	aad, err := BuildResponseHeaderAAD(c.userUUIDBytes(), messageType, uint32(len(ct)), uint64(nonce), fencingEpoch)
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
// postOnly is the batch-level post-only flag. When nil (the default) or true,
// a replacement leg that would cross is rejected as "failed". Pass a pointer to
// false to enable the relaxed path: a crossing leg takes liquidity up to its
// limit and rests the remainder; such a leg reports FillCount > 0 on its result.
func (c *GodarkClient) MassQuote(ctx context.Context, symbol string, legs []MassQuoteLegInput, leverage uint32, postOnly *bool) (*MassQuoteAck, error) {
	if err := c.ensureReady(); err != nil {
		return nil, err
	}
	symbolID, err := c.resolveSymbol(symbol)
	if err != nil {
		return nil, err
	}
	if leverage < 1 {
		leverage = 1
	}
	corrID := newCorrelationID()
	plaintext, err := BuildMassQuoteRequest(uint64(symbolID), c.userUUIDBytes(), legs, corrID, leverage, postOnly)
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
	plaintext, err := BuildBatchCancelRequest(uint64(symbolID), c.userUUIDBytes(), orderIDs, corrID)
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
	plaintext, err := BuildBatchModifyRequest(uint64(symbolID), c.userUUIDBytes(), legs, corrID)
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
		return nil, newOrderError("expected batch_modify_ack inside encrypted push", "")
	}
	return ack, nil
}

func (c *GodarkClient) handleEncryptedPush(msg transport.Message) {
	ctB64, _ := msg["encrypted_body"].(string)
	ct, err := base64.StdEncoding.DecodeString(ctB64)
	if err != nil {
		c.emitError(newEncryptionError(fmt.Sprintf("decode push body: %v", err)))
		return
	}
	nonce := coerceUint32(msg["nonce"])
	fencingEpoch := coerceUint64(msg["fencing_epoch"])
	messageType, _ := msg["message_type"].(string)

	switch messageType {
	case "ack", "mass_quote_ack", "batch_cancel_ack", "batch_modify_ack":
		c.transport.ResolveCommand(msg)
		return
	}

	if _, known := responseMessageTypeToProto[messageType]; !known {
		return
	}

	aad, err := BuildResponseHeaderAAD(c.userUUIDBytes(), messageType, uint32(len(ct)), uint64(nonce), fencingEpoch)
	if err != nil {
		c.emitError(err)
		return
	}

	pt, err := c.session.DecryptPush(nonce, aad, ct)
	if err != nil {
		c.emitError(newEncryptionError(fmt.Sprintf("decrypt push: %v", err)))
		return
	}

	parsed, err := ParseSequencerToEdgeMessage(pt)
	if err != nil {
		c.emitError(fmt.Errorf("parse encrypted push body: %w", err))
		return
	}
	c.dispatchSequencerPush(parsed)
}

func (c *GodarkClient) dispatchSequencerPush(parsed SequencerPush) {
	c.cbMu.RLock()
	defer c.cbMu.RUnlock()

	switch v := parsed.(type) {
	case *OrderUpdate:
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
		nonBlockingSend(c.fundingRateQueue, v)
		for _, cb := range c.fundingCallbacks {
			safeCallFunding(cb, v)
		}
	case *SettlementUpdate:
		nonBlockingSend(c.settlementQueue, v)
		for _, cb := range c.settlementCBs {
			safeCallSettlement(cb, v)
		}
	case *UnknownSequencerPush:
		// silently ignored - forward-compat contract
	}
}

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

func (c *GodarkClient) ensureReady() error {
	c.mu.RLock()
	connected := c.connected
	userUUID := c.userUUID
	c.mu.RUnlock()
	if !connected {
		return newConnectionError("not connected")
	}
	if userUUID == "" {
		return newConnectionError("not authenticated")
	}
	if !c.session.IsEstablished() {
		return newSessionError("ECDH session not established")
	}
	return nil
}

func (c *GodarkClient) userUUIDBytes() []byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.userUUID == "" {
		return make([]byte, identity.UserUUIDLen)
	}
	b, err := identity.ToBytes(c.userUUID)
	if err != nil {
		return make([]byte, identity.UserUUIDLen)
	}
	return b
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

func resolveAuthToken(cfg ClientConfig) (string, error) {
	if cfg.APIKeyID != "" || cfg.APISecret != "" {
		if cfg.APIKeyID == "" || cfg.APISecret == "" {
			return "", errors.New("APIKeyID and APISecret must be provided together")
		}
		if cfg.APIKey != "" {
			return "", errors.New("use either APIKey or (APIKeyID + APISecret), not both")
		}
		passphrase, err := resolvePassphrase(cfg.Passphrase)
		if err != nil {
			return "", err
		}
		return cfg.APIKeyID + ":" + cfg.APISecret + ":" + passphrase, nil
	}
	if cfg.APIKey != "" {
		if strings.TrimSpace(cfg.Passphrase) != "" {
			return "", errors.New("Passphrase must not be set when using legacy APIKey")
		}
		return cfg.APIKey, nil
	}
	return "", errors.New("provide APIKey or both APIKeyID + APISecret")
}

func resolveEdgeBaseURL(explicit string) string {
	if v := strings.TrimSpace(explicit); v != "" {
		return v
	}
	for _, key := range []string{"GODARK_EDGE_URL", "GDX_EDGE_URL"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return defaultEdgeBaseURL
}

func resolveUserUUID(explicit string) string {
	if v := strings.TrimSpace(explicit); v != "" {
		return v
	}
	for _, key := range []string{"GODARK_USER_UUID", "GDX_USER_UUID"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
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
func safeCallNoArg(cb func())                          { defer func() { _ = recover() }(); cb() }
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
