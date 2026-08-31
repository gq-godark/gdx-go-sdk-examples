package godark_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	godark "github.com/gq-godark/gdx-go-sdk"
)

// newMarketDataMock spins up a tiny in-process gomarket server that echoes
// subscribe acks and then pushes one orderbook + one trade frame so the test
// can verify the typed channel / callback paths.
type marketDataMock struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	conn   *websocket.Conn
	subbed atomic.Int32
}

func newMarketDataMock(t *testing.T) *marketDataMock {
	t.Helper()
	m := &marketDataMock{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/gomarket", m.handle)
	m.srv = httptest.NewServer(mux)
	return m
}

func (m *marketDataMock) close() { m.srv.Close() }

func (m *marketDataMock) wsBaseURL() string {
	return "ws://" + strings.TrimPrefix(m.srv.URL, "http://")
}

func (m *marketDataMock) handle(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "bye")
	m.mu.Lock()
	m.conn = conn
	m.mu.Unlock()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var msg map[string]any
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		action, _ := msg["action"].(string)
		channel, _ := msg["channel"].(string)
		symbol, _ := msg["symbol"].(string)

		if action == "subscribe" {
			m.subbed.Add(1)
			ack, _ := json.Marshal(map[string]any{
				"type":    "subscribed",
				"channel": channel,
				"symbol":  symbol,
			})
			_ = conn.Write(ctx, websocket.MessageText, ack)

			switch channel {
			case "orderbook":
				ob, _ := json.Marshal(map[string]any{
					"type":   "orderbook",
					"symbol": symbol,
					"bids":   []any{[]any{"30000.0", "0.5"}},
					"asks":   []any{[]any{"30100.0", "0.5"}},
				})
				_ = conn.Write(ctx, websocket.MessageText, ob)
			case "trades":
				tr, _ := json.Marshal(map[string]any{
					"type":   "trade",
					"symbol": symbol,
					"price":  "30050.0",
					"qty":    "0.01",
				})
				_ = conn.Write(ctx, websocket.MessageText, tr)
			}
		}
	}
}

func TestMarketDataClient_SubscribeAndReceive(t *testing.T) {
	t.Setenv("GODARK_MARKET_DATA_USE_GOMARKET", "1")
	t.Setenv("GODARK_MARKET_DATA_WS_URL", "")
	t.Setenv("GDX_MARKET_DATA_WS_URL", "")

	m := newMarketDataMock(t)
	defer m.close()

	c := godark.NewMarketDataClient(godark.MarketDataConfig{
		BaseURL:         m.wsBaseURL(),
		EventBufferSize: 16,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Disconnect()

	var (
		obSeen atomic.Int32
		trSeen atomic.Int32
	)

	if err := c.SubscribeOrderbook(ctx, "BTC-USDC-PERP", func(msg godark.MarketDataMessage) {
		if msg.Channel == "orderbook" && msg.Symbol == "BTC-USDC-PERP" {
			obSeen.Add(1)
		}
	}); err != nil {
		t.Fatalf("SubscribeOrderbook: %v", err)
	}
	if err := c.SubscribeTrades(ctx, "BTC-USDC-PERP", func(msg godark.MarketDataMessage) {
		if msg.Channel == "trades" && msg.Symbol == "BTC-USDC-PERP" {
			trSeen.Add(1)
		}
	}); err != nil {
		t.Fatalf("SubscribeTrades: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if obSeen.Load() >= 1 && trSeen.Load() >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if obSeen.Load() < 1 || trSeen.Load() < 1 {
		t.Fatalf("expected callbacks for both streams, got orderbook=%d trades=%d",
			obSeen.Load(), trSeen.Load())
	}

	// Channel API should also have received the events. The handlers above
	// already drained one each via callbacks; the channels stage every event
	// separately, so they should still have one in the buffer.
	select {
	case msg := <-c.OrderbookEvents():
		if msg.Channel != "orderbook" {
			t.Fatalf("OrderbookEvents: channel=%q", msg.Channel)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("OrderbookEvents: no message")
	}
	select {
	case msg := <-c.TradesEvents():
		if msg.Channel != "trades" {
			t.Fatalf("TradesEvents: channel=%q", msg.Channel)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("TradesEvents: no message")
	}

	if got := m.subbed.Load(); got != 2 {
		t.Fatalf("subscribe count: got %d want 2", got)
	}
}

func TestMarketDataClient_URLNormalisation(t *testing.T) {
	t.Setenv("GODARK_MARKET_DATA_WS_URL", "")
	t.Setenv("GDX_MARKET_DATA_WS_URL", "")
	t.Setenv("GODARK_MARKET_DATA_USE_GOMARKET", "")
	t.Setenv("GDX_MARKET_DATA_USE_GOMARKET", "")

	cases := []struct{ in, want string }{
		{"wss://api.godark-dex.com", "wss://api.godark-dex.com/ws/v1"},
		{"wss://api.godark-dex.com/", "wss://api.godark-dex.com/ws/v1"},
		{"wss://api.godark-dex.com/ws", "wss://api.godark-dex.com/ws/v1"},
		{"wss://api.godark-dex.com/ws/v1", "wss://api.godark-dex.com/ws/v1"},
		{"https://api.godark-dex.com", "wss://api.godark-dex.com/ws/v1"},
		{"http://localhost:4000", "ws://localhost:4000/ws/v1"},
	}
	for _, tc := range cases {
		c := godark.NewMarketDataClient(godark.MarketDataConfig{BaseURL: tc.in})
		if got := c.URL(); got != tc.want {
			t.Errorf("URL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMarketDataClient_URLGomarketFlag(t *testing.T) {
	t.Setenv("GODARK_MARKET_DATA_WS_URL", "")
	t.Setenv("GDX_MARKET_DATA_WS_URL", "")
	t.Setenv("GODARK_MARKET_DATA_USE_GOMARKET", "1")

	cases := []struct{ in, want string }{
		{"wss://api.godark-dex.com", "wss://api.godark-dex.com/ws/gomarket"},
		{"wss://api.godark-dex.com/ws", "wss://api.godark-dex.com/ws/gomarket"},
		{"wss://api.godark-dex.com/ws/v1", "wss://api.godark-dex.com/ws/gomarket"},
	}
	for _, tc := range cases {
		c := godark.NewMarketDataClient(godark.MarketDataConfig{BaseURL: tc.in})
		if got := c.URL(); got != tc.want {
			t.Errorf("URL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSubscriptionCallbackKey_Snapshots(t *testing.T) {
	if got := godark.SubscriptionCallbackKey(map[string]any{"type": "volume_snapshot"}); got != "volume:" {
		t.Fatalf("got %q", got)
	}
	if got := godark.SubscriptionCallbackKey(map[string]any{"type": "open_interest_snapshot"}); got != "open_interest:" {
		t.Fatalf("got %q", got)
	}
	if got := godark.SubscriptionCallbackKey(map[string]any{"type": "funding_rate_snapshot"}); got != "funding_rate:" {
		t.Fatalf("got %q", got)
	}
}

func TestSubscribeOrderbook_DocsWireRejected(t *testing.T) {
	t.Setenv("GODARK_MARKET_DATA_WS_URL", "")
	t.Setenv("GDX_MARKET_DATA_WS_URL", "")
	t.Setenv("GODARK_MARKET_DATA_USE_GOMARKET", "")
	t.Setenv("GDX_MARKET_DATA_USE_GOMARKET", "")

	c := godark.NewMarketDataClient(godark.MarketDataConfig{BaseURL: "wss://api.example"})
	err := c.SubscribeOrderbook(context.Background(), "BTC-USDC-PERP", nil)
	if err == nil || !strings.Contains(err.Error(), "L2 orderbook is not available") {
		t.Fatalf("expected docs-wire rejection, got %v", err)
	}
}

func TestUnsubscribe_DocsWirePublicChannel(t *testing.T) {
	t.Setenv("GODARK_MARKET_DATA_WS_URL", "")
	t.Setenv("GDX_MARKET_DATA_WS_URL", "")
	t.Setenv("GODARK_MARKET_DATA_USE_GOMARKET", "")
	t.Setenv("GDX_MARKET_DATA_USE_GOMARKET", "")

	var got atomic.Value
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/v1", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "bye")
		ctx := r.Context()
		for {
			_, raw, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var msg map[string]any
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			if op, _ := msg["op"].(string); op == "unsubscribe" {
				got.Store(msg)
			}
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	base := "ws://" + strings.TrimPrefix(srv.URL, "http://")

	c := godark.NewMarketDataClient(godark.MarketDataConfig{BaseURL: base})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.SubscribePublicChannel(ctx, "volume", nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Unsubscribe(ctx, "volume", ""); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if msg, ok := got.Load().(map[string]any); ok {
			if msg["op"] != "unsubscribe" {
				t.Fatalf("got %#v", msg)
			}
			args, _ := msg["args"].([]any)
			if len(args) == 0 {
				t.Fatalf("missing args: %#v", msg)
			}
			arg, _ := args[0].(map[string]any)
			if arg["channel"] != "volume" {
				t.Fatalf("channel: %#v", arg)
			}
			c.Disconnect()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for docs-wire unsubscribe")
}
