package godark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// MarketDataMessage is the parsed JSON envelope for one gomarket frame.
//
// Channels:
//   - `orderbook` -- L2 snapshot/diff for `Symbol`.
//   - `trades`    -- trade ticks for `Symbol` (server emits `type=trade`,
//     singular; the SDK normalises the channel string to plural `trades`).
//
// Raw is the underlying decoded JSON object, useful for fields the typed
// surface doesn't expose yet.
type MarketDataMessage struct {
	Type    string         // "orderbook" / "trade" / "status" / "pong" / "error" / ...
	Channel string         // "orderbook" / "trades" / "" for control frames
	Symbol  string
	Raw     map[string]any
}

// MarketDataConfig configures NewMarketDataClient.
type MarketDataConfig struct {
	// BaseURL is the edge host origin (e.g. `wss://api.godark-dex.com`). The
	// client appends `/ws/gomarket`. Defaults to the production edge.
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

// MarketDataClient is the public gomarket WebSocket client.
//
// It does NOT auto-reconnect; OnDisconnect is delivered when the WS closes
// and the caller decides whether to reopen. This matches the trading
// client's contract.
type MarketDataClient struct {
	url        string
	cfg        MarketDataConfig
	bufSize    int

	conn   *websocket.Conn
	connMu sync.Mutex

	// per-key callbacks, keyed by "<channel>:<symbol>" (channel = "orderbook"
	// or "trades").
	cbMu      sync.RWMutex
	callbacks map[string][]func(MarketDataMessage)

	// every decoded event is also pushed onto these buffered channels so a
	// channel-first style is supported alongside callbacks.
	orderbookCh chan MarketDataMessage
	tradesCh    chan MarketDataMessage
	rawCh       chan MarketDataMessage

	// desired subscriptions across reconnects (caller-controlled).
	desiredMu sync.Mutex
	desired   map[string]struct{} // key "<channel>:<symbol>"

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
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/ws/v1") {
		base = strings.TrimSuffix(base, "/ws/v1")
	} else if strings.HasSuffix(base, "/ws") {
		base = strings.TrimSuffix(base, "/ws")
	}

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
		url:         base + "/ws/gomarket",
		cfg:         cfg,
		bufSize:     bufSize,
		callbacks:   make(map[string][]func(MarketDataMessage)),
		orderbookCh: make(chan MarketDataMessage, bufSize),
		tradesCh:    make(chan MarketDataMessage, bufSize),
		rawCh:       make(chan MarketDataMessage, bufSize),
		desired:     make(map[string]struct{}),
	}
}

// URL returns the resolved gomarket WebSocket URL the client will dial.
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
		ch, sym := splitSubKey(key)
		if ch != "" && sym != "" {
			_ = m.sendSubscribe(ctx, ch, sym, "subscribe")
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
// optional callback.
func (m *MarketDataClient) SubscribeOrderbook(ctx context.Context, symbol string, cb func(MarketDataMessage)) error {
	return m.subscribe(ctx, "orderbook", symbol, cb)
}

// SubscribeTrades subscribes to trade events for symbol, with an optional
// callback.
func (m *MarketDataClient) SubscribeTrades(ctx context.Context, symbol string, cb func(MarketDataMessage)) error {
	return m.subscribe(ctx, "trades", symbol, cb)
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
	return m.sendSubscribe(ctx, channel, symbol, "unsubscribe")
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
	return m.sendSubscribe(ctx, channel, symbol, "subscribe")
}

func (m *MarketDataClient) sendSubscribe(ctx context.Context, channel, symbol, action string) error {
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
			b, _ := json.Marshal(map[string]any{"action": "ping"})
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

	if msg.Channel == "" || msg.Symbol == "" {
		return
	}
	key := subKey(msg.Channel, msg.Symbol)
	m.cbMu.RLock()
	cbs := append([]func(MarketDataMessage){}, m.callbacks[key]...)
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
