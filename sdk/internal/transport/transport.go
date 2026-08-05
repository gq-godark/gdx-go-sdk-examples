// Package transport is the low-level WebSocket transport for gdx-edge.
//
// It handles:
//
//   - The docs-wire envelope (`{id, op, args}` out; `{id, op, code, data?,
//     message?}` in) and its translation to the legacy `{type, event, ...}`
//     shape the client uses internally.
//   - Authentication (login op -> auth_result frame).
//   - Noise XK handshake relay (noise.handshake -> noise_handshake_reply).
//   - Command serialization (one in flight at a time) with timeout.
//   - Subscribe / unsubscribe ack collation.
//   - Heartbeat + staleness detection.
//   - Push frame dispatch to handler callbacks set by client.go.
//
// It does NOT handle:
//
//   - Auto-reconnect. The client must close + reopen on `OnDisconnect`.
//     This matches the python SDK's contract.
//   - Crypto. Encrypted push frames are forwarded raw to the on-encrypted-push
//     handler; the client decrypts them via internal/session.
package transport

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

// Config holds optional WebSocket transport settings.
type Config struct {
	// HTTPClient is an optional override for the dial HTTP client (lets the
	// caller plug in a custom Transport with a proxy, custom TLS, etc.).
	HTTPClient *http.Client
	// Headers are extra HTTP headers sent on the upgrade request.
	Headers http.Header
	// MaxMessageSize is the per-frame size limit. Default 65536 bytes.
	MaxMessageSize int64
	// HeartbeatInterval is the ping period. Default 30s.
	HeartbeatInterval time.Duration
	// StaleTimeout: if no inbound traffic for this long, the connection is
	// considered dead. Default 60s.
	StaleTimeout time.Duration
	// CommandTimeout: max wait for a command ack. Default 30s.
	CommandTimeout time.Duration
	// LegacyWire selects the legacy `{type, data}` wire envelope. The default
	// (zero value, false) uses the docs envelope `{id, op, args}` which is
	// what every gdx-edge deployment after `v0.x` speaks.
	LegacyWire bool
}

func (c *Config) applyDefaults() {
	if c.MaxMessageSize == 0 {
		c.MaxMessageSize = 65536
	}
	if c.HeartbeatInterval == 0 {
		c.HeartbeatInterval = 30 * time.Second
	}
	if c.StaleTimeout == 0 {
		c.StaleTimeout = 60 * time.Second
	}
	if c.CommandTimeout == 0 {
		c.CommandTimeout = 30 * time.Second
	}
}

// useDocsWire is the boolean view the rest of the transport uses internally.
func (c *Config) useDocsWire() bool { return !c.LegacyWire }

// Message is the normalized inbound message envelope handled by the client.
type Message = map[string]any

// Handlers are the callbacks the client registers for unsolicited frames.
type Handlers struct {
	OnAuthResult         func(Message)
	OnSessionEstablished func(Message)
	OnRekeyRequired      func(Message)
	OnEncryptedPush      func(Message)
	OnDisconnect         func()
}

// Transport is one live WebSocket session with gdx-edge.
type Transport struct {
	url     string
	config  Config
	use     bool // docs-wire shorthand
	handler Handlers

	// connection state
	mu        sync.Mutex
	conn      *websocket.Conn
	connected bool
	closed    bool

	// Command waiters.
	//
	// cmdLock serializes only the *send* (nonce assignment, encryption, and
	// the WS write) so frames leave in nonce order; the round-trip wait
	// happens without the lock. Correlation-keyed commands (encrypted trading
	// ops) may therefore be in flight concurrently, each matched back by
	// correlation id (or wire id, which business rejects carry when the header
	// correlation id is dropped). Commands without a correlation id use the
	// single `pending` slot and stay serialized.
	cmdLock   sync.Mutex
	pending   chan Message
	pendingMu sync.Mutex
	byCorr    map[string]chan Message
	byWireID  map[string]chan Message

	// subscription ack waiter
	subWaiter chan error
	subExpect int
	subOp     string
	subMu     sync.Mutex

	// liveness tracking
	lastInbound time.Time
	livenessMu  sync.Mutex

	// goroutine bookkeeping
	loopCtx    context.Context
	loopCancel context.CancelFunc
	wg         sync.WaitGroup
}

// New constructs an unconnected Transport. Call Connect to dial.
func New(targetURL string, config Config, handlers Handlers) *Transport {
	config.applyDefaults()
	return &Transport{
		url:      targetURL,
		config:   config,
		use:      config.useDocsWire(),
		handler:  handlers,
		byCorr:   make(map[string]chan Message),
		byWireID: make(map[string]chan Message),
	}
}

// IsConnected reports whether the underlying WS connection is open.
func (t *Transport) IsConnected() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.connected && t.conn != nil
}

// UseDocsWire reports the current wire envelope mode.
func (t *Transport) UseDocsWire() bool {
	return t.use
}

func newWireID() string {
	return uuid.NewString()
}

// Connect opens the WebSocket and starts the recv + heartbeat goroutines.
// Connect is NOT safe to call concurrently with itself.
func (t *Transport) Connect(ctx context.Context) error {
	opts := &websocket.DialOptions{
		HTTPClient: t.config.HTTPClient,
		HTTPHeader: t.config.Headers,
	}
	conn, _, err := websocket.Dial(ctx, t.url, opts)
	if err != nil {
		return fmt.Errorf("websocket dial %s: %w", t.url, err)
	}
	if t.config.MaxMessageSize > 0 {
		conn.SetReadLimit(t.config.MaxMessageSize)
	}

	t.mu.Lock()
	t.conn = conn
	t.connected = true
	t.closed = false
	t.mu.Unlock()

	t.livenessMu.Lock()
	t.lastInbound = time.Now()
	t.livenessMu.Unlock()

	t.loopCtx, t.loopCancel = context.WithCancel(context.Background())
	t.wg.Add(2)
	go t.recvLoop()
	go t.heartbeatLoop()
	return nil
}

// Disconnect closes the WebSocket and stops all background goroutines. Safe
// to call from multiple goroutines; subsequent calls are no-ops.
func (t *Transport) Disconnect() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	t.connected = false
	conn := t.conn
	t.conn = nil
	t.mu.Unlock()

	if t.loopCancel != nil {
		t.loopCancel()
	}
	if conn != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "disconnect")
	}
	t.rejectPending(errors.New("disconnected"))
	t.wg.Wait()
}

// SendJSON serializes obj and writes it as a single WebSocket text frame.
func (t *Transport) SendJSON(ctx context.Context, obj any) error {
	t.mu.Lock()
	conn := t.conn
	connected := t.connected
	t.mu.Unlock()
	if conn == nil || !connected {
		return errors.New("not connected")
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return conn.Write(ctx, websocket.MessageText, b)
}

// commandKeys extracts the header correlation id and wire id from an outbound
// payload. Correlation id selects the concurrent routing path; wire id is a
// fallback for error frames that omit the correlation id.
func commandKeys(payload any) (corr, wireID string) {
	m, ok := payload.(map[string]any)
	if !ok {
		return "", ""
	}
	if id, ok := m["id"].(string); ok {
		wireID = id
	}
	var header map[string]any
	if args, ok := m["args"].(map[string]any); ok {
		header, _ = args["header"].(map[string]any)
	}
	if header == nil {
		if data, ok := m["data"].(map[string]any); ok {
			header, _ = data["header"].(map[string]any)
		}
	}
	if header != nil {
		if c, ok := header["correlation_id"].(string); ok && c != "" {
			corr = correlationKeyFromString(c)
		}
	}
	return corr, wireID
}

// SendCommand sends a JSON payload and waits for its matching ack or error.
func (t *Transport) SendCommand(ctx context.Context, payload any) (Message, error) {
	return t.SendCommandFunc(ctx, func() (any, error) { return payload, nil })
}

// SendCommandFunc builds the frame via prepare while holding the send lock,
// then sends it and awaits its ack/error. prepare lets the caller keep nonce
// assignment and encryption atomic with the send so concurrent commands still
// hit the wire in nonce order. Commands carrying a correlation id are matched
// back by that id (or wire id) and may be in flight concurrently; commands
// without one fall back to the serialized single slot.
func (t *Transport) SendCommandFunc(ctx context.Context, prepare func() (any, error)) (Message, error) {
	waiter := make(chan Message, 1)

	t.cmdLock.Lock()
	payload, err := prepare()
	if err != nil {
		t.cmdLock.Unlock()
		return nil, err
	}
	corr, wireID := commandKeys(payload)

	if corr == "" {
		// Serialized single-slot path: keep the lock across the round-trip so
		// the unkeyed response cannot be confused with another command.
		defer t.cmdLock.Unlock()
		t.pendingMu.Lock()
		t.pending = waiter
		t.pendingMu.Unlock()
		defer func() {
			t.pendingMu.Lock()
			t.pending = nil
			t.pendingMu.Unlock()
		}()
		if err := t.SendJSON(ctx, payload); err != nil {
			return nil, err
		}
		return t.awaitWaiter(ctx, waiter)
	}

	// Concurrent path: register keyed waiter, send, then release the lock and
	// await the response off-lock so other commands can pipeline.
	t.pendingMu.Lock()
	t.byCorr[corr] = waiter
	if wireID != "" {
		t.byWireID[wireID] = waiter
	}
	t.pendingMu.Unlock()

	cleanup := func() {
		t.pendingMu.Lock()
		delete(t.byCorr, corr)
		if wireID != "" {
			delete(t.byWireID, wireID)
		}
		t.pendingMu.Unlock()
	}

	if err := t.SendJSON(ctx, payload); err != nil {
		cleanup()
		t.cmdLock.Unlock()
		return nil, err
	}
	t.cmdLock.Unlock()

	defer cleanup()
	return t.awaitWaiter(ctx, waiter)
}

func (t *Transport) awaitWaiter(ctx context.Context, waiter chan Message) (Message, error) {
	timeout := t.config.CommandTimeout
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, fmt.Errorf("command timed out after %s", timeout)
	case msg, ok := <-waiter:
		if !ok {
			return nil, errors.New("transport closed while awaiting ack")
		}
		return msg, nil
	}
}

// SendSubscribe sends a subscribe/unsubscribe op and waits for all per-channel
// acks. op must be "subscribe" or "unsubscribe".
func (t *Transport) SendSubscribe(ctx context.Context, channels []string, op string) error {
	if len(channels) == 0 {
		return errors.New("subscribe: empty channel list")
	}

	t.cmdLock.Lock()
	defer t.cmdLock.Unlock()

	waiter := make(chan error, 1)
	t.subMu.Lock()
	t.subWaiter = waiter
	t.subExpect = len(channels)
	t.subOp = op
	t.subMu.Unlock()

	defer func() {
		t.subMu.Lock()
		t.subWaiter = nil
		t.subExpect = 0
		t.subOp = ""
		t.subMu.Unlock()
	}()

	args := make([]map[string]string, len(channels))
	for i, c := range channels {
		args[i] = map[string]string{"channel": c}
	}

	payload := map[string]any{
		"op":   op,
		"args": args,
	}
	if t.use {
		payload["id"] = newWireID()
	}
	if err := t.SendJSON(ctx, payload); err != nil {
		return err
	}

	timer := time.NewTimer(t.config.CommandTimeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("%s timed out", op)
	case err, ok := <-waiter:
		if !ok {
			return errors.New("transport closed while awaiting subscribe ack")
		}
		return err
	}
}

// Authenticate sends the login op and waits for the auth_result frame. Returns
// the parsed auth_result Message (with `user_uuid`, `session_id`, etc.).
func (t *Transport) Authenticate(ctx context.Context, apiKey string) (Message, error) {
	result := make(chan Message, 1)
	t.mu.Lock()
	prev := t.handler.OnAuthResult
	t.handler.OnAuthResult = func(m Message) {
		select {
		case result <- m:
		default:
		}
	}
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		t.handler.OnAuthResult = prev
		t.mu.Unlock()
	}()

	var payload any
	if t.use {
		payload = map[string]any{
			"id":   newWireID(),
			"op":   "login",
			"args": map[string]any{"token": apiKey},
		}
	} else {
		payload = map[string]any{"type": "auth", "data": map[string]any{"token": apiKey}}
	}
	if err := t.SendJSON(ctx, payload); err != nil {
		return nil, err
	}

	timer := time.NewTimer(t.config.CommandTimeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, errors.New("auth timed out")
	case m, ok := <-result:
		if !ok {
			return nil, errors.New("transport closed while awaiting auth")
		}
		return m, nil
	}
}

// ResolveCommand resolves any pending command future with the supplied
// message. Used by client.go to feed an encrypted_push -> decoded ack frame
// back into the awaiting SendCommand caller.
//
// Returns true if a pending command was resolved.
func (t *Transport) ResolveCommand(msg Message) bool {
	t.pendingMu.Lock()
	defer t.pendingMu.Unlock()
	return t.resolveLocked(msg)
}

// resolveLocked routes a response to its waiter by correlation id, then wire
// id, then the serialized single slot. Callers must hold pendingMu.
func (t *Transport) resolveLocked(msg Message) bool {
	if corr := correlationKey(msg["correlation_id"]); corr != "" {
		if ch, ok := t.byCorr[corr]; ok {
			t.forgetLocked(ch)
			select {
			case ch <- msg:
				return true
			default:
				return true
			}
		}
	}
	if wireID, _ := msg["wire_id"].(string); wireID != "" {
		if ch, ok := t.byWireID[wireID]; ok {
			t.forgetLocked(ch)
			select {
			case ch <- msg:
				return true
			default:
				return true
			}
		}
	}
	if t.pending != nil {
		select {
		case t.pending <- msg:
			return true
		default:
			return false
		}
	}
	return false
}

// forgetLocked removes ch from both keyed waiter maps. Callers hold pendingMu.
func (t *Transport) forgetLocked(ch chan Message) {
	for k, v := range t.byCorr {
		if v == ch {
			delete(t.byCorr, k)
		}
	}
	for k, v := range t.byWireID {
		if v == ch {
			delete(t.byWireID, k)
		}
	}
}

// correlationKey normalizes a wire correlation id to a canonical lowercase-hex
// key over the underlying 16 bytes. The request header stamps the id as hex
// while edge responses return it as a decimal integer string, so both encodings
// must map to the same key (matching the Java SDK's normalizeCorrelationKey).
func correlationKey(v any) string {
	var s string
	switch value := v.(type) {
	case string:
		s = value
	case float64:
		s = fmt.Sprintf("%.0f", value)
	case uint64:
		s = fmt.Sprintf("%d", value)
	default:
		return ""
	}
	return correlationKeyFromString(s)
}

func correlationKeyFromString(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var n *big.Int
	var ok bool
	switch {
	case isAllDigits(s):
		n, ok = new(big.Int).SetString(s, 10)
	case len(s) == 32 && isHex(s):
		n, ok = new(big.Int).SetString(s, 16)
	default:
		// Unknown format: fall back to a lowercased passthrough so at least
		// identical encodings still match.
		return strings.ToLower(s)
	}
	if !ok || n.Sign() <= 0 || n.BitLen() > 128 {
		return strings.ToLower(s)
	}
	raw := n.Bytes()
	out := make([]byte, 16)
	copy(out[16-len(raw):], raw)
	return hex.EncodeToString(out)
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isHex(s string) bool {
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

// rejectPending fails any in-flight command/subscribe waiter with err.
func (t *Transport) rejectPending(err error) {
	t.pendingMu.Lock()
	if t.pending != nil {
		close(t.pending)
	}
	// Close every concurrent waiter exactly once (the same channel may be
	// registered under both a correlation id and a wire id).
	closed := make(map[chan Message]struct{})
	for _, ch := range t.byCorr {
		if _, done := closed[ch]; !done {
			close(ch)
			closed[ch] = struct{}{}
		}
	}
	for _, ch := range t.byWireID {
		if _, done := closed[ch]; !done {
			close(ch)
			closed[ch] = struct{}{}
		}
	}
	t.byCorr = make(map[string]chan Message)
	t.byWireID = make(map[string]chan Message)
	t.pendingMu.Unlock()

	t.subMu.Lock()
	if t.subWaiter != nil {
		select {
		case t.subWaiter <- err:
		default:
		}
	}
	t.subMu.Unlock()
}

// recvLoop reads messages from the WS, normalizes them, and dispatches.
func (t *Transport) recvLoop() {
	defer t.wg.Done()
	defer t.afterDisconnect()

	for {
		select {
		case <-t.loopCtx.Done():
			return
		default:
		}

		t.mu.Lock()
		conn := t.conn
		t.mu.Unlock()
		if conn == nil {
			return
		}

		_, raw, err := conn.Read(t.loopCtx)
		if err != nil {
			return
		}

		t.livenessMu.Lock()
		t.lastInbound = time.Now()
		t.livenessMu.Unlock()

		var msg Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		t.dispatch(normalizeInboundMessage(msg))
	}
}

// dispatch routes a normalized inbound message to the appropriate handler.
func (t *Transport) dispatch(msg Message) {
	msgType, _ := msg["type"].(string)
	event, _ := msg["event"].(string)

	switch msgType {
	case "pong":
		return
	case "auth_result":
		if t.handler.OnAuthResult != nil {
			t.handler.OnAuthResult(msg)
		}
		return
	case "session_established":
		if t.handler.OnSessionEstablished != nil {
			t.handler.OnSessionEstablished(msg)
		}
		return
	case "noise_handshake_reply":
		if t.handler.OnSessionEstablished != nil {
			t.handler.OnSessionEstablished(msg)
		}
		return
	case "rekey_required":
		if t.handler.OnRekeyRequired != nil {
			t.handler.OnRekeyRequired(msg)
		}
		return
	case "encrypted_push":
		if t.handler.OnEncryptedPush != nil {
			t.handler.OnEncryptedPush(msg)
		}
		return
	case "ack", "error":
		t.pendingMu.Lock()
		t.resolveLocked(msg)
		t.pendingMu.Unlock()
		return
	}

	// Subscription acks come as `event=subscribe`/`unsubscribe`.
	if event == "subscribe" || event == "unsubscribe" {
		t.subMu.Lock()
		if t.subWaiter != nil && event == t.subOp {
			t.subExpect--
			if t.subExpect <= 0 {
				select {
				case t.subWaiter <- nil:
				default:
				}
				t.subWaiter = nil
			}
		}
		t.subMu.Unlock()
		return
	}

	if event == "error" {
		t.subMu.Lock()
		if t.subWaiter != nil {
			errMsg, _ := msg["message"].(string)
			if errMsg == "" {
				errMsg = "channel error"
			}
			select {
			case t.subWaiter <- errors.New(errMsg):
			default:
			}
			t.subWaiter = nil
		}
		t.subMu.Unlock()
	}
}

// heartbeatLoop sends periodic pings + closes the connection if no inbound
// traffic has been seen within stale_timeout.
func (t *Transport) heartbeatLoop() {
	defer t.wg.Done()

	ticker := time.NewTicker(t.config.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-t.loopCtx.Done():
			return
		case <-ticker.C:
			t.livenessMu.Lock()
			elapsed := time.Since(t.lastInbound)
			t.livenessMu.Unlock()

			if elapsed > t.config.StaleTimeout {
				t.mu.Lock()
				conn := t.conn
				t.mu.Unlock()
				if conn != nil {
					_ = conn.Close(websocket.StatusGoingAway, "heartbeat timeout")
				}
				return
			}

			var payload any
			if t.use {
				payload = map[string]any{
					"id":   newWireID(),
					"op":   "ping",
					"args": map[string]any{},
				}
			} else {
				payload = map[string]any{"type": "ping"}
			}

			pingCtx, cancel := context.WithTimeout(t.loopCtx, 5*time.Second)
			err := t.SendJSON(pingCtx, payload)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func (t *Transport) afterDisconnect() {
	t.mu.Lock()
	t.connected = false
	t.mu.Unlock()
	t.rejectPending(errors.New("connection lost"))
	if t.handler.OnDisconnect != nil {
		t.handler.OnDisconnect()
	}
}

// normalizeInboundMessage maps gdx-edge docs replies to the legacy
// `type` / `event` frames the dispatch logic expects.
//
// A docs reply has shape:
//
//	{ id, op, code, data?, message? }
//
// A legacy frame already carries `type` or `event` - those are passed through
// unchanged.
func normalizeInboundMessage(msg Message) Message {
	if _, hasType := msg["type"]; hasType {
		return msg
	}
	if _, hasOp := msg["op"]; !hasOp {
		return msg
	}
	codeFloat, ok := msg["code"].(float64)
	if !ok {
		return msg
	}
	code := int(codeFloat)

	op, _ := msg["op"].(string)
	data, _ := msg["data"].(map[string]any)
	message, _ := msg["message"].(string)
	wireID, _ := msg["id"].(string)

	switch {
	case op == "pong" && code == 0:
		return Message{"type": "pong"}

	case op == "login":
		if code != 0 {
			errText := message
			if errText == "" {
				errText = "authentication failed"
			}
			return Message{
				"type":    "auth_result",
				"success": false,
				"error":   errText,
			}
		}
		if data != nil {
			out := Message{
				"type":    "auth_result",
				"success": true,
			}
			for _, k := range []string{
				"user_uuid", "account_id", "session_id",
				"token_expires_at", "cancel_on_disconnect", "conn_id",
			} {
				if v, ok := data[k]; ok {
					out[k] = v
				}
			}
			return out
		}
		return Message{"type": "auth_result", "success": false, "error": "invalid auth response"}

	case op == "noise.handshake" || op == "noise_handshake":
		if code != 0 {
			errText := message
			if errText == "" {
				errText = "noise handshake failed"
			}
			return Message{"type": "error", "message": errText}
		}
		if data == nil {
			return Message{"type": "error", "message": "invalid noise handshake response"}
		}
		return Message{
			"type":        "noise_handshake_reply",
			"conn_id":     data["conn_id"],
			"message":     data["message"],
			"established": data["established"],
		}

	case op == "session.setup" || op == "session_setup":
		return Message{"type": "error", "message": "session.setup is not supported (Noise XK required)"}

	case op == "subscribe" || op == "unsubscribe":
		if code != 0 {
			ch := ""
			if data != nil {
				ch = stringFromAny(data["channel"])
			}
			errText := message
			if errText == "" {
				errText = "channel error"
			}
			return Message{"event": "error", "message": errText, "channel": ch}
		}
		if data != nil {
			if ch, hasCh := data["channel"]; hasCh {
				return Message{"event": op, "channel": ch}
			}
		}
		return Message{"event": op}

	case op == "logout":
		if code != 0 {
			errText := message
			if errText == "" {
				errText = "logout failed"
			}
			return Message{"type": "error", "message": errText}
		}
		return Message{"type": "ack", "success": true}

	case op == "order.place" || op == "order.cancel" || op == "order.modify" ||
		op == "order.mass_quote" || op == "order.batch_cancel" || op == "order.batch_modify":
		if code != 0 {
			errText := message
			if errText == "" {
				errText = "order error"
			}
			// wire_id lets a concurrent command's error route to its waiter
			// even when the edge omits the header correlation id on rejects.
			return Message{"type": "error", "message": errText, "wire_id": wireID}
		}
		if data == nil {
			return Message{"type": "error", "message": "invalid order response", "wire_id": wireID}
		}
		// If the server responded with an encrypted ack, surface it as an
		// encrypted_push frame the client will decrypt.
		if mt, _ := data["message_type"].(string); mt != "" {
			ciphertext := stringFromAny(data["ciphertext"])
			if ciphertext == "" {
				ciphertext = stringFromAny(data["encrypted_body"])
			}
			if ciphertext != "" {
				return Message{
					"type":           "encrypted_push",
					"message_type":   mt,
					"encrypted_body": ciphertext,
					"nonce":          data["nonce"],
					"fencing_epoch":  data["fencing_epoch"],
					"correlation_id": data["correlation_id"],
					"session_seq":    data["session_seq"],
					"wire_id":        wireID,
				}
			}
		}
		out := Message{"type": "ack", "wire_id": wireID}
		for _, k := range []string{"success", "order_id", "sequence", "error", "error_code", "correlation_id"} {
			if v, ok := data[k]; ok {
				out[k] = v
			}
		}
		if _, has := out["success"]; !has {
			out["success"] = true
		}
		return out
	}

	if data != nil {
		if ev, _ := data["event"].(string); ev == "rekey_required" {
			return Message{"type": "rekey_required", "session_id": data["session_id"]}
		}

		mt, _ := data["message_type"].(string)
		if mt != "" {
			ciphertext := stringFromAny(data["ciphertext"])
			if ciphertext == "" {
				ciphertext = stringFromAny(data["encrypted_body"])
			}
			if ciphertext != "" {
				return Message{
					"type":           "encrypted_push",
					"message_type":   mt,
					"encrypted_body": ciphertext,
					"nonce":          data["nonce"],
					"fencing_epoch":  data["fencing_epoch"],
					"correlation_id": data["correlation_id"],
					"session_seq":    data["session_seq"],
				}
			}
		}
	}

	return msg
}

func stringFromAny(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// EdgeURL canonicalises an SDK base URL by appending /ws/v1 when not already
// present. Mirrors python's _resolve_edge_url helper.
func EdgeURL(base string) string {
	base = strings.TrimRight(base, "/")
	parsed, err := url.Parse(base)
	if err != nil {
		return base + "/ws/v1"
	}
	if strings.HasSuffix(parsed.Path, "/ws/v1") {
		return base
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/ws/v1"
	return parsed.String()
}
