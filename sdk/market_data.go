package godark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

// MarketDataMessage is the parsed JSON envelope for one market-data frame.
//
// Channels:
//   - `orderbook` -- L2 snapshot/diff for `Symbol` (gomarket only).
//   - `trades`    -- trade ticks for `Symbol` (server emits `type=trade`,
//     singular; the SDK normalises the channel string to plural `trades`).
//   - `volume` / `open_interest` / `funding_rate` -- public `/ws/v1` feeds
//     (empty Symbol; callback key is `channel:`).
//
// Raw is the underlying decoded JSON object, useful for fields the typed
// surface doesn't expose yet.
type MarketDataMessage struct {
	Type    string // "orderbook" / "trade" / "status" / "pong" / "error" / ...
	Channel string // "orderbook" / "trades" / "volume" / ...
	Symbol  string
	Raw     map[string]any
}

// MarketDataConfig configures NewMarketDataClient.
type MarketDataConfig struct {
	// BaseURL is the edge host origin (e.g. `wss://api.godark-dex.com`).
	// Resolved via ResolveMarketDataWsUrl (default `/ws/v1`). Defaults to the
	// production edge when empty.
	BaseURL string

	// HTTPClient is forwarded to the underlying coder/websocket dialer for
	// the upgrade request.
	HTTPClient *http.Client

	// Headers are extra HTTP headers sent on the upgrade request.
	Headers http.Header

	// HeartbeatInterval is the JSON-ping period. Default 30s.
	HeartbeatInterval time.Duration

	// MaxMessageSize bounds the per-frame size. Default 1 MiB.
	MaxMessageSize int64

	// EventBufferSize is the max in-memory buffer of dispatched events on
	// the per-stream channels. When full, the oldest event is dropped.
	// Default 256.
	EventBufferSize int
}

const orderbookDocsWireMsg = "L2 orderbook is not available on /ws/v1; set GODARK_ORDERBOOK_WS_URL to a direct L2 " +
	"stream URL, use SubscribePublicChannel for public edge feeds, or " +
	"GODARK_MARKET_DATA_USE_GOMARKET=1 for local /ws/gomarket"

// GomarketWsURL strips edge `/ws` suffixes and appends `/ws/gomarket`
// (WebSocket scheme).
func GomarketWsURL(baseURL string) string {
	url := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(url, "/ws/v1") {
		url = strings.TrimSuffix(url, "/ws/v1")
	} else if strings.HasSuffix(url, "/ws") {
		url = strings.TrimSuffix(url, "/ws")
	}
	if strings.HasPrefix(url, "http://") {
		url = "ws://" + strings.TrimPrefix(url, "http://")
	} else if strings.HasPrefix(url, "https://") {
		url = "wss://" + strings.TrimPrefix(url, "https://")
	}
	return url + "/ws/gomarket"
}

func envFirst(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func envTruthy(keys ...string) bool {
	for _, k := range keys {
		raw := strings.ToLower(strings.TrimSpace(os.Getenv(k)))
		switch raw {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

// tradingWsURL mirrors Java GodarkClient.wsUrl / Python _ws_url.
func tradingWsURL(baseURL string) string {
	url := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(url, "/ws/v1") {
		return url
	}
	if strings.HasSuffix(url, "/ws") {
		return url + "/v1"
	}
	return url + "/ws/v1"
}

// ResolveMarketDataWsURL resolves the market-data WebSocket URL.
// Hosted edges default to `/ws/v1`. Override with GODARK_MARKET_DATA_WS_URL,
// or set GODARK_MARKET_DATA_USE_GOMARKET=1 for `/ws/gomarket`.
func ResolveMarketDataWsURL(baseURL string) string {
	if override := envFirst("GODARK_MARKET_DATA_WS_URL", "GDX_MARKET_DATA_WS_URL"); override != "" {
		return override
	}
	if envTruthy("GODARK_MARKET_DATA_USE_GOMARKET", "GDX_MARKET_DATA_USE_GOMARKET") {
		return GomarketWsURL(baseURL)
	}
	return tradingWsURL(baseURL)
}

// SubscriptionCallbackKey maps a market-data JSON message to callback key
// `channel:symbol` (mirrors Java/Python).
func SubscriptionCallbackKey(msg map[string]any) string {
	typ, _ := msg["type"].(string)
	switch typ {
	case "status", "subscribed", "unsubscribed", "pong", "error":
		return ""
	}
	symbol, _ := msg["symbol"].(string)
	switch typ {
	case "orderbook":
		return "orderbook:" + symbol
	case "trade":
		return "trades:" + symbol
	case "volume_snapshot":
		return "volume:"
	case "open_interest_snapshot":
		return "open_interest:"
	case "funding_rate_snapshot":
		return "funding_rate:"
	}
	if ch, ok := msg["channel"].(string); ok && ch != "" {
		return ch + ":" + symbol
	}
	return ""
}

// MarketDataClient is the public market-data WebSocket client.
//
// It does NOT auto-reconnect; OnDisconnect is delivered when the WS closes
// and the caller decides whether to reopen. This matches the trading
// client's contract.
type MarketDataClient struct {
	url      string
	docsWire bool
	cfg      MarketDataConfig
	bufSize  int

	conn   *websocket.Conn
	connMu sync.Mutex

	// per-key callbacks, keyed by "<channel>:<symbol>" (empty symbol for public).
	cbMu      sync.RWMutex
	callbacks map[string][]func(MarketDataMessage)

	// every decoded event is also pushed onto these buffered channels so a
	// channel-first style is supported alongside callbacks.
	orderbookCh chan MarketDataMessage
	tradesCh    chan MarketDataMessage
	rawCh       chan MarketDataMessage

	// desired subscriptions across reconnects (caller-controlled).
	// Keys are "channel:symbol" or "public\0channel" for public edge feeds.
	desiredMu sync.Mutex
	desired   map[string]struct{}

	disconnectMu sync.RWMutex
	disconnectCB []func()

	loopCtx    context.Context
	loopCancel context.CancelFunc
	wg         sync.WaitGroup

	closed bool
	mu     sync.Mutex
}

// NewMarketDataClient constructs an unconnected market-data client. Call
// Connect to dial.
func NewMarketDataClient(cfg MarketDataConfig) *MarketDataClient {
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		base = defaultEdgeBaseURL
	}
	resolved := ResolveMarketDataWsURL(base)

	bufSize := cfg.EventBufferSize
	if bufSize <= 0 {
		bufSize = 256
	}
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = 30 * time.Second
	}
	if cfg.MaxMessageSize == 0 {
		cfg.MaxMessageSize = 1 << 20
	}

	return &MarketDataClient{
		url:         resolved,
		docsWire:    strings.HasSuffix(resolved, "/ws/v1"),
		cfg:         cfg,
		bufSize:     bufSize,
		callbacks:   make(map[string][]func(MarketDataMessage)),
		orderbookCh: make(chan MarketDataMessage, bufSize),
		tradesCh:    make(chan MarketDataMessage, bufSize),
		rawCh:       make(chan MarketDataMessage, bufSize),
		desired:     make(map[string]struct{}),
	}
}

// URL returns the resolved market-data WebSocket URL the client will dial.
func (m *MarketDataClient) URL() string { return m.url }

// IsConnected reports whether the underlying WS is currently open.
func (m *MarketDataClient) IsConnected() bool {
	m.connMu.Lock()
	defer m.connMu.Unlock()
	return m.conn != nil && !m.closed
}

// Connect opens the WebSocket and starts the recv + heartbeat goroutines.
func (m *MarketDataClient) Connect(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("market data client is closed")
	}
	m.mu.Unlock()

	parsed, err := url.Parse(m.url)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	if parsed.Scheme != "ws" && parsed.Scheme != "wss" {
		return fmt.Errorf("market data url must be ws:// or wss://, got %q", parsed.Scheme)
	}

	opts := &websocket.DialOptions{
		HTTPClient: m.cfg.HTTPClient,
		HTTPHeader: m.cfg.Headers,
	}
	conn, _, err := websocket.Dial(ctx, m.url, opts)
	if err != nil {
		return fmt.Errorf("websocket dial %s: %w", m.url, err)
	}
	if m.cfg.MaxMessageSize > 0 {
		conn.SetReadLimit(m.cfg.MaxMessageSize)
	}

	m.connMu.Lock()
	m.conn = conn
	m.connMu.Unlock()

	m.loopCtx, m.loopCancel = context.WithCancel(context.Background())
	m.wg.Add(2)
	go m.recvLoop()
	go m.heartbeatLoop()

	// Replay every previously-requested subscription so callers can call
	// Subscribe / Disconnect / Connect again without re-registering.
	m.desiredMu.Lock()
	pending := make([]string, 0, len(m.desired))
	for k := range m.desired {
		pending = append(pending, k)
	}
	m.desiredMu.Unlock()
	for _, key := range pending {
		if strings.HasPrefix(key, "public\x00") {
			_ = m.sendPublicSubscribe(ctx, key[len("public\x00"):])
			continue
		}
		ch, sym := splitSubKey(key)
		if ch != "" {
			_ = m.sendSubscribeFrame(ctx, ch, sym)
		}
	}
	return nil
}

// Disconnect closes the WebSocket and stops background goroutines.
func (m *MarketDataClient) Disconnect() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.mu.Unlock()

	if m.loopCancel != nil {
		m.loopCancel()
	}
	m.connMu.Lock()
	conn := m.conn
	m.conn = nil
	m.connMu.Unlock()
	if conn != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "disconnect")
	}
	m.wg.Wait()
}

// SubscribeOrderbook subscribes to L2 orderbook updates for symbol, with an
// optional callback. Not available on `/ws/v1` (docs wire).
func (m *MarketDataClient) SubscribeOrderbook(ctx context.Context, symbol string, cb func(MarketDataMessage)) error {
	if m.docsWire {
		return errors.New(orderbookDocsWireMsg)
	}
	return m.subscribe(ctx, "orderbook", symbol, cb)
}

// SubscribeTrades subscribes to trade events for symbol, with an optional
// callback.
func (m *MarketDataClient) SubscribeTrades(ctx context.Context, symbol string, cb func(MarketDataMessage)) error {
	return m.subscribe(ctx, "trades", symbol, cb)
}

// SubscribePublicChannel subscribes to a public `/ws/v1` edge channel
// (volume, open_interest, funding_rate).
func (m *MarketDataClient) SubscribePublicChannel(ctx context.Context, channel string, cb func(MarketDataMessage)) error {
	if channel == "" {
		return errors.New("channel is required")
	}
	if !m.docsWire {
		return errors.New("SubscribePublicChannel requires /ws/v1 edge URL")
	}
	key := channel + ":"
	if cb != nil {
		m.cbMu.Lock()
		m.callbacks[key] = append(m.callbacks[key], cb)
		m.cbMu.Unlock()
	}
	m.desiredMu.Lock()
	m.desired["public\x00"+channel] = struct{}{}
	m.desiredMu.Unlock()
	return m.sendPublicSubscribe(ctx, channel)
}

// Unsubscribe removes the (channel, symbol) subscription, sending an
// unsubscribe op to the server and dropping any callbacks for the key.
func (m *MarketDataClient) Unsubscribe(ctx context.Context, channel, symbol string) error {
	key := subKey(channel, symbol)
	m.cbMu.Lock()
	delete(m.callbacks, key)
	m.cbMu.Unlock()
	m.desiredMu.Lock()
	delete(m.desired, key)
	m.desiredMu.Unlock()
	return m.sendAction(ctx, "unsubscribe", channel, symbol)
}

// OrderbookEvents returns a receive-only channel that emits every decoded
// orderbook frame across all subscribed symbols. Channel-first consumers
// can switch on `msg.Symbol`.
func (m *MarketDataClient) OrderbookEvents() <-chan MarketDataMessage { return m.orderbookCh }

// TradesEvents returns a receive-only channel of every decoded trades frame.
func (m *MarketDataClient) TradesEvents() <-chan MarketDataMessage { return m.tradesCh }

// RawEvents returns a receive-only channel of every decoded frame,
// including control frames (`status`, `pong`, `error`, ...). Useful for
// debugging or for routes the typed channels don't surface.
func (m *MarketDataClient) RawEvents() <-chan MarketDataMessage { return m.rawCh }

// OnDisconnect registers a callback fired when the WebSocket closes for any
// reason.
func (m *MarketDataClient) OnDisconnect(cb func()) {
	m.disconnectMu.Lock()
	m.disconnectCB = append(m.disconnectCB, cb)
	m.disconnectMu.Unlock()
}

// -----------------------------------------------------------------------
// Internals
// -----------------------------------------------------------------------

func (m *MarketDataClient) subscribe(ctx context.Context, channel, symbol string, cb func(MarketDataMessage)) error {
	if symbol == "" {
		return errors.New("symbol is required")
	}
	key := subKey(channel, symbol)
	if cb != nil {
		m.cbMu.Lock()
		m.callbacks[key] = append(m.callbacks[key], cb)
		m.cbMu.Unlock()
	}
	m.desiredMu.Lock()
	m.desired[key] = struct{}{}
	m.desiredMu.Unlock()
	return m.sendSubscribeFrame(ctx, channel, symbol)
}

func (m *MarketDataClient) sendPublicSubscribe(ctx context.Context, channel string) error {
	m.connMu.Lock()
	conn := m.conn
	m.connMu.Unlock()
	if conn == nil {
		return nil
	}
	payload := map[string]any{
		"id": uuid.NewString(),
		"op": "subscribe",
		"args": []map[string]any{
			{"channel": channel},
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, b)
}

func (m *MarketDataClient) sendSubscribeFrame(ctx context.Context, channel, symbol string) error {
	m.connMu.Lock()
	conn := m.conn
	m.connMu.Unlock()
	if conn == nil {
		return nil
	}
	var payload map[string]any
	if m.docsWire {
		payload = map[string]any{
			"id": uuid.NewString(),
			"op": "subscribe",
			"args": []map[string]any{
				{"channel": channel, "symbol": symbol},
			},
		}
	} else {
		payload = map[string]any{
			"action":  "subscribe",
			"channel": channel,
			"symbol":  symbol,
		}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, b)
}

func (m *MarketDataClient) sendAction(ctx context.Context, action, channel, symbol string) error {
	m.connMu.Lock()
	conn := m.conn
	m.connMu.Unlock()
	if conn == nil {
		return nil
	}
	payload := map[string]any{
		"action":  action,
		"channel": channel,
		"symbol":  symbol,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, b)
}

func (m *MarketDataClient) recvLoop() {
	defer m.wg.Done()
	defer m.afterDisconnect()

	for {
		select {
		case <-m.loopCtx.Done():
			return
		default:
		}

		m.connMu.Lock()
		conn := m.conn
		m.connMu.Unlock()
		if conn == nil {
			return
		}

		_, raw, err := conn.Read(m.loopCtx)
		if err != nil {
			return
		}
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err != nil {
			continue
		}
		m.dispatch(obj)
	}
}

func (m *MarketDataClient) heartbeatLoop() {
	defer m.wg.Done()
	ticker := time.NewTicker(m.cfg.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.loopCtx.Done():
			return
		case <-ticker.C:
			m.connMu.Lock()
			conn := m.conn
			m.connMu.Unlock()
			if conn == nil {
				return
			}
			var payload map[string]any
			if m.docsWire {
				payload = map[string]any{"id": uuid.NewString(), "op": "ping"}
			} else {
				payload = map[string]any{"action": "ping"}
			}
			b, _ := json.Marshal(payload)
			pingCtx, cancel := context.WithTimeout(m.loopCtx, 5*time.Second)
			err := conn.Write(pingCtx, websocket.MessageText, b)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func (m *MarketDataClient) dispatch(obj map[string]any) {
	typ, _ := obj["type"].(string)
	symbol, _ := obj["symbol"].(string)

	msg := MarketDataMessage{
		Type:   typ,
		Symbol: symbol,
		Raw:    obj,
	}
	switch typ {
	case "orderbook":
		msg.Channel = "orderbook"
	case "trade":
		msg.Channel = "trades"
	case "volume_snapshot":
		msg.Channel = "volume"
	case "open_interest_snapshot":
		msg.Channel = "open_interest"
	case "funding_rate_snapshot":
		msg.Channel = "funding_rate"
	default:
		if ch, ok := obj["channel"].(string); ok {
			msg.Channel = ch
		}
	}

	nonBlockingSend(m.rawCh, msg)

	switch msg.Channel {
	case "orderbook":
		nonBlockingSend(m.orderbookCh, msg)
	case "trades":
		nonBlockingSend(m.tradesCh, msg)
	}

	route := SubscriptionCallbackKey(obj)
	if route == "" {
		return
	}
	m.cbMu.RLock()
	cbs := append([]func(MarketDataMessage){}, m.callbacks[route]...)
	m.cbMu.RUnlock()
	for _, cb := range cbs {
		safeCallMarket(cb, msg)
	}
}

func (m *MarketDataClient) afterDisconnect() {
	m.disconnectMu.RLock()
	cbs := append([]func(){}, m.disconnectCB...)
	m.disconnectMu.RUnlock()
	for _, cb := range cbs {
		safeCallNoArg(cb)
	}
}

func safeCallMarket(cb func(MarketDataMessage), v MarketDataMessage) {
	defer func() { _ = recover() }()
	cb(v)
}

func subKey(channel, symbol string) string { return channel + ":" + symbol }

func splitSubKey(key string) (string, string) {
	i := strings.IndexByte(key, ':')
	if i < 0 {
		return "", ""
	}
	return key[:i], key[i+1:]
}
