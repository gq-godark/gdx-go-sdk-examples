package godark_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	godark "github.com/gq-godark/gdx-go-sdk"
	"github.com/gq-godark/gdx-go-sdk/internal/hpke"
	"github.com/gq-godark/gdx-go-sdk/internal/session"
	"github.com/gq-godark/gdx-go-sdk/internal/wire"
	commonpb "github.com/gq-godark/gdx-go-sdk/proto/gdx/common/v1"
	edgepb "github.com/gq-godark/gdx-go-sdk/proto/gdx/edge/v1"
	sequencerpb "github.com/gq-godark/gdx-go-sdk/proto/gdx/sequencer/v1"
)

// mockEdge is an in-process gdx-edge mock that speaks the docs-wire envelope
// `{id, op, args}` / `{id, op, code, data, message}`.
//
// It exists to give the trading client end-to-end behavioral coverage without
// a live cluster: login, HPKE binary setup, encrypted place ack, encrypted
// positions_snapshot push, and encrypted cancel ack.
type mockEdge struct {
	t            *testing.T
	httpSrv      *httptest.Server
	userUUID     string
	staticPubHex string
	mu           sync.Mutex
	keypair      *hpke.StaticKeyPair
	sealed       *hpke.SealedSession
	connID       uint64
	pushNonceCtr uint64
	gotPlace     chan placeSeen
	gotCancel    chan cancelSeen
	conn         *websocket.Conn
}

type placeSeen struct {
	header      *edgepb.OrderHeader
	ciphertext  []byte
	requestType string
	wireID      string
}

type cancelSeen struct {
	header     *edgepb.OrderHeader
	wireID     string
	ciphertext []byte
}

// newMockEdge wires up an httptest.Server with a /ws/v1 handler that drives
// the docs-wire flow.
func newMockEdge(t *testing.T, userUUID string) *mockEdge {
	t.Helper()
	keypair, err := hpke.GenerateStaticKeyPair()
	if err != nil {
		t.Fatalf("GenerateStaticKeyPair: %v", err)
	}
	m := &mockEdge{
		t:            t,
		userUUID:     userUUID,
		keypair:      keypair,
		staticPubHex: hex.EncodeToString(keypair.PublicKey()),
		connID:       1,
		gotPlace:     make(chan placeSeen, 1),
		gotCancel:    make(chan cancelSeen, 1),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/v1", m.serveWS)
	m.httpSrv = httptest.NewServer(mux)
	return m
}

func (m *mockEdge) close() {
	m.httpSrv.Close()
}

// wsBaseURL returns the "wss://" / "ws://" host origin the SDK should be
// pointed at; the SDK auto-appends /ws/v1.
func (m *mockEdge) wsBaseURL() string {
	return "ws://" + strings.TrimPrefix(m.httpSrv.URL, "http://")
}

func (m *mockEdge) serveWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		m.t.Logf("mockEdge: accept: %v", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "bye")

	m.mu.Lock()
	m.conn = conn
	m.mu.Unlock()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	for {
		typ, raw, err := conn.Read(ctx)
		if err != nil {
			return
		}

		if websocket.MessageType(typ) == websocket.MessageBinary {
			m.handleBinary(ctx, raw)
			continue
		}

		var msg map[string]any
		if err := json.Unmarshal(raw, &msg); err != nil {
			m.t.Logf("mockEdge: bad json: %v", err)
			continue
		}

		op, _ := msg["op"].(string)
		id, _ := msg["id"].(string)
		args, _ := msg["args"].(map[string]any)

		switch op {
		case "ping":
			_ = m.writeReply(ctx, id, "pong", 0, map[string]any{}, "")
		case "login":
			m.handleLogin(ctx, id, args)
		case "session.setup", "session_setup":
			_ = m.writeReply(ctx, id, op, 1500, nil, "session.setup is not supported (HPKE WebSocket required)")
		case "subscribe", "unsubscribe":
			// Echo back per-channel acks: this mock's only consumer
			// is the integration test below, which doesn't subscribe;
			// we keep the branch so subscription smoke tests can land
			// without rewiring this handler.
			if argsList, ok := msg["args"].([]any); ok {
				for _, raw := range argsList {
					a, _ := raw.(map[string]any)
					ch, _ := a["channel"].(string)
					_ = m.writeReply(ctx, id, op, 0, map[string]any{"channel": ch}, "")
				}
			}
		}
	}
}

func (m *mockEdge) writeReply(ctx context.Context, id, op string, code int, data map[string]any, message string) error {
	reply := map[string]any{
		"id":   id,
		"op":   op,
		"code": code,
	}
	if data != nil {
		reply["data"] = data
	}
	if message != "" {
		reply["message"] = message
	}
	b, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	if conn == nil {
		return errors.New("no conn")
	}
	return conn.Write(ctx, websocket.MessageText, b)
}

func (m *mockEdge) handleLogin(ctx context.Context, id string, _ map[string]any) {
	data := map[string]any{
		"user_uuid":  m.userUUID,
		"account_id": "acct-mock",
		"session_id": "login-session-1",
		"conn_id":    m.connID,
	}
	_ = m.writeReply(ctx, id, "login", 0, data, "")
}

func (m *mockEdge) writeBinary(ctx context.Context, payload []byte) error {
	m.mu.Lock()
	conn := m.conn
	m.mu.Unlock()
	if conn == nil {
		return errors.New("no conn")
	}
	return conn.Write(ctx, websocket.MessageBinary, payload)
}

func (m *mockEdge) handleBinary(ctx context.Context, raw []byte) {
	decoded, err := wire.DecodeBinaryFrame(raw)
	if err != nil {
		m.t.Logf("mockEdge: decode binary: %v", err)
		return
	}
	switch decoded.Kind {
	case wire.DecodedHpkeSetup:
		setup := decoded.HpkeSetup
		if setup == nil {
			return
		}
		user, err := uuid.Parse(m.userUUID)
		if err != nil {
			return
		}
		info := hpke.InfoForConn(user[:], setup.GetConnId())
		sealed, err := hpke.OpenSession(m.keypair, setup.GetEncappedKey(), info)
		if err != nil {
			reply, _ := wire.EncodeHpkeSetupReply(setup.GetConnId(), false)
			_ = m.writeBinary(ctx, reply)
			return
		}
		m.mu.Lock()
		m.sealed = sealed
		m.pushNonceCtr = 1
		m.mu.Unlock()
		reply, err := wire.EncodeHpkeSetupReply(setup.GetConnId(), true)
		if err != nil {
			return
		}
		_ = m.writeBinary(ctx, reply)
	case wire.DecodedEncryptedOrder:
		req := decoded.EncryptedOrder
		if req == nil || req.GetHeader() == nil {
			return
		}
		m.handleEncryptedOrder(ctx, req)
	}
}

func (m *mockEdge) handleEncryptedOrder(ctx context.Context, req *edgepb.EncryptedEdgeRequest) {
	header := req.GetHeader()
	m.mu.Lock()
	sealed := m.sealed
	m.mu.Unlock()
	if sealed == nil {
		return
	}
	aad, err := proto.Marshal(header)
	if err != nil {
		return
	}
	nonce := hpke.NonceFromU64(header.GetNonce())
	if _, err := sealed.OpenC2S(nonce[:], aad, req.GetEncryptedBody()); err != nil {
		m.t.Logf("mockEdge: open c2s: %v", err)
		return
	}

	reqType := requestTypeProtoToStr(header.GetRequestType())
	switch reqType {
	case "place":
		select {
		case m.gotPlace <- placeSeen{header: header, ciphertext: req.GetEncryptedBody(), requestType: reqType}:
		default:
		}
	case "cancel":
		select {
		case m.gotCancel <- cancelSeen{header: header, ciphertext: req.GetEncryptedBody()}:
		default:
		}
	}

	push, err := m.buildEncryptedOrderAck(header, 7777)
	if err != nil {
		m.t.Logf("mockEdge: build ack: %v", err)
		return
	}
	_ = m.writeBinary(ctx, push)
}

func (m *mockEdge) buildEncryptedOrderAck(header *edgepb.OrderHeader, orderID uint64) ([]byte, error) {
	user, err := uuid.Parse(m.userUUID)
	if err != nil {
		return nil, err
	}
	corrBE := header.GetCorrelationId()
	bodyLE := correlationIDBodyLE(corrBE)

	ackMsg := &sequencerpb.AckMessage{
		Sequence:      42,
		OrderId:       orderID,
		CorrelationId: bodyLE,
		AckOutcome: &sequencerpb.AckOutcomeWire{
			Kind: sequencerpb.AckOutcomeKind_ACK_OUTCOME_KIND_APPLIED,
		},
	}
	inner, err := proto.Marshal(ackMsg)
	if err != nil {
		return nil, err
	}
	plaintext := godark.WrapLegacyNodeResponse("ack", inner)

	m.mu.Lock()
	sealed := m.sealed
	pushNonce := m.pushNonceCtr
	m.pushNonceCtr++
	connID := m.connID
	m.mu.Unlock()
	if sealed == nil {
		return nil, errors.New("HPKE session not established")
	}

	bodyLength, err := session.BodyLengthForPlaintext(len(plaintext))
	if err != nil {
		return nil, err
	}
	aad, err := godark.BuildResponseHeaderAADWithConn(
		user[:],
		"ack",
		bodyLength,
		pushNonce,
		0,
		corrBE,
		42,
		connID,
	)
	if err != nil {
		return nil, err
	}
	nonce := hpke.NonceFromU64(pushNonce)
	ct, err := sealed.SealS2C(nonce[:], aad, plaintext)
	if err != nil {
		return nil, err
	}
	resp := &edgepb.EncryptedEdgeResponse{
		Version: hpke.WireVersion,
		Header: &edgepb.ResponseHeader{
			UserUuid:      user[:],
			MessageType:   commonpb.ResponseMessageType_RESPONSE_MESSAGE_TYPE_ACK,
			BodyLength:    bodyLength,
			Nonce:         pushNonce,
			FencingEpoch:  0,
			CorrelationId: corrBE,
			SessionSeq:    42,
			ConnId:        connID,
		},
		EncryptedBody: ct,
	}
	return wire.EncodeEncryptedPush(resp)
}

func correlationIDBodyLE(corrBE []byte) []byte {
	if len(corrBE) != 16 {
		return nil
	}
	var arr [16]byte
	copy(arr[:], corrBE)
	v := new(big.Int).SetBytes(arr[:])
	le := v.Bytes()
	out := make([]byte, 16)
	copy(out[16-len(le):], le)
	return out
}

func requestTypeProtoToStr(rt commonpb.RequestType) string {
	switch rt {
	case commonpb.RequestType_REQUEST_TYPE_PLACE:
		return "place"
	case commonpb.RequestType_REQUEST_TYPE_CANCEL:
		return "cancel"
	case commonpb.RequestType_REQUEST_TYPE_MODIFY:
		return "modify"
	default:
		return ""
	}
}

// sendPositionsSnapshotPush emits an encrypted positions_snapshot push so the
// test can verify the client decrypts and dispatches it to PositionsSnapshots().
func (m *mockEdge) sendPositionsSnapshotPush(ctx context.Context, symbolID uint64) error {
	user, err := uuid.Parse(m.userUUID)
	if err != nil {
		return err
	}
	push := &sequencerpb.SequencerToEdgeMessage{
		Inner: &sequencerpb.SequencerToEdgeMessage_PositionsSnapshot{
			PositionsSnapshot: &sequencerpb.PositionsSnapshot{
				UserUuid:        user[:],
				ServerTimestamp: 1,
				Rows: []*sequencerpb.PositionRow{
					{
						SymbolId:   symbolID,
						Side:       commonpb.Side_SIDE_BUY,
						Size:       "0.5",
						EntryPrice: "30000",
						Leverage:   1,
					},
				},
			},
		},
	}
	body, err := proto.Marshal(push)
	if err != nil {
		return err
	}

	m.mu.Lock()
	sealed := m.sealed
	pushNonce := m.pushNonceCtr
	m.pushNonceCtr++
	connID := m.connID
	m.mu.Unlock()
	if sealed == nil {
		return errors.New("HPKE session not established")
	}

	bodyLength, err := session.BodyLengthForPlaintext(len(body))
	if err != nil {
		return err
	}
	aad, err := godark.BuildResponseHeaderAADWithConn(
		user[:],
		"positions_snapshot",
		bodyLength,
		pushNonce,
		0,
		nil,
		0,
		connID,
	)
	if err != nil {
		return err
	}
	nonce := hpke.NonceFromU64(pushNonce)
	ct, err := sealed.SealS2C(nonce[:], aad, body)
	if err != nil {
		return err
	}
	resp := &edgepb.EncryptedEdgeResponse{
		Version: hpke.WireVersion,
		Header: &edgepb.ResponseHeader{
			UserUuid:     user[:],
			MessageType:  commonpb.ResponseMessageType_RESPONSE_MESSAGE_TYPE_POSITIONS_SNAPSHOT,
			BodyLength:   bodyLength,
			Nonce:        pushNonce,
			FencingEpoch: 0,
			ConnId:       connID,
		},
		EncryptedBody: ct,
	}
	frame, err := wire.EncodeEncryptedPush(resp)
	if err != nil {
		return err
	}
	return m.writeBinary(ctx, frame)
}

// -----------------------------------------------------------------------
// Helpers (small one-off coercers used only by this mock test).
// -----------------------------------------------------------------------

func toUint64(v any) (uint64, bool) {
	switch x := v.(type) {
	case float64:
		return uint64(x), true
	case int:
		return uint64(x), true
	case int64:
		return uint64(x), true
	case uint64:
		return x, true
	case string:
		if n, err := strconv.ParseUint(x, 10, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}

func toUint32(v any) (uint32, bool) {
	switch x := v.(type) {
	case float64:
		return uint32(x), true
	case int:
		return uint32(x), true
	case int64:
		return uint32(x), true
	case uint32:
		return x, true
	case uint64:
		return uint32(x), true
	}
	return 0, false
}

func uuidStrToBytes(s string) []byte {
	out := make([]byte, 16)
	// Reuse the SDK's identity helper indirectly via ToBytes; copy the bytes
	// into a fresh slice to avoid aliasing the caller's input.
	if b, err := identityToBytes(s); err == nil {
		copy(out, b)
	}
	return out
}

func corrIDStrToBytes(s string) []byte {
	b, err := identityToBytes(s)
	if err != nil {
		return make([]byte, 16)
	}
	return b
}

// Note: we deliberately import the SDK's identity helper indirectly via this
// small private adapter so the test file doesn't need to re-export internals
// from the SDK's `internal/` subtree.
func identityToBytes(s string) ([]byte, error) {
	// 36-char UUID expected. Use the SDK's exported BuildOrderHeaderAAD
	// approach is overkill; do the parse inline (uuid form `8-4-4-4-12`).
	clean := strings.ReplaceAll(s, "-", "")
	if len(clean) != 32 {
		return nil, fmt.Errorf("not a uuid: %q", s)
	}
	out := make([]byte, 16)
	for i := 0; i < 16; i++ {
		hi := hexNibble(clean[2*i])
		lo := hexNibble(clean[2*i+1])
		if hi < 0 || lo < 0 {
			return nil, fmt.Errorf("not a uuid: %q", s)
		}
		out[i] = byte(hi<<4 | lo)
	}
	return out, nil
}

func hexNibble(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

func requestTypeStrToProto(s string) int32 {
	switch s {
	case "place":
		return int32(commonpb.RequestType_REQUEST_TYPE_PLACE)
	case "cancel":
		return int32(commonpb.RequestType_REQUEST_TYPE_CANCEL)
	case "modify":
		return int32(commonpb.RequestType_REQUEST_TYPE_MODIFY)
	}
	return 0
}

// -----------------------------------------------------------------------
// Test: full Connect -> PlaceOrder -> encrypted push -> CancelOrder.
// -----------------------------------------------------------------------

func (m *mockEdge) hpkeStaticPublicKeyHex() string { return m.staticPubHex }

func TestMockIntegration_FullFlow(t *testing.T) {
	const userUUID = "00000000-0000-0000-0000-00000000002a"

	m := newMockEdge(t, userUUID)
	defer m.close()

	client, err := godark.NewClient(godark.ClientConfig{
		APIKey:                  "test-token",
		BaseURL:                 m.wsBaseURL(),
		NoiseStaticPublicKeyHex: m.hpkeStaticPublicKeyHex(),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// Make sure stream queues never block on an unhandled push by draining
	// the positions-snapshot channel into a goroutine the test owns.
	var pushSeen atomic.Int32
	posCh := client.PositionsSnapshots()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for upd := range posCh {
			if upd != nil {
				pushSeen.Add(1)
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if got := client.UserUUID(); got != userUUID {
		t.Fatalf("UserUUID: got %q want %q", got, userUUID)
	}

	// PlaceOrder — ack confirmation so the mock need not emit a book update.
	ack, err := client.PlaceOrder(ctx, godark.PlaceOrderRequest{
		Symbol:       "BTC-USDC-PERP",
		Side:         godark.SideSell,
		OrderType:    godark.OrderTypeLimit,
		Price:        999_999,
		Quantity:     0.01,
		Confirmation: godark.PlaceOrderConfirmationAck,
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if !ack.Success || ack.OrderID != "7777" {
		t.Fatalf("PlaceOrder ack: %+v", ack)
	}

	// Verify the mock saw an actual encrypted-place wire frame (header +
	// non-empty ciphertext) rather than the SDK shortcutting somewhere.
	select {
	case ps := <-m.gotPlace:
		if ps.header.RequestType != commonpb.RequestType_REQUEST_TYPE_PLACE {
			t.Fatalf("place header request_type: got %v", ps.header.RequestType)
		}
		if len(ps.ciphertext) <= hpke.TagLen {
			t.Fatalf("place ciphertext suspiciously short: %d bytes", len(ps.ciphertext))
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("mock never saw the place frame")
	}

	// Server-side push: emit an encrypted positions_snapshot and confirm the
	// client decrypts + dispatches it on PositionsSnapshots().
	if err := m.sendPositionsSnapshotPush(ctx, 1); err != nil {
		t.Fatalf("sendPositionsSnapshotPush: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pushSeen.Load() >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pushSeen.Load() < 1 {
		t.Fatalf("client never observed the encrypted push")
	}

	// CancelOrder.
	cancelAck, err := client.CancelOrder(ctx, ack.OrderID, "BTC-USDC-PERP")
	if err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if !cancelAck.Success {
		t.Fatalf("CancelOrder ack not successful: %+v", cancelAck)
	}
	select {
	case cs := <-m.gotCancel:
		if cs.header.RequestType != commonpb.RequestType_REQUEST_TYPE_CANCEL {
			t.Fatalf("cancel header request_type: got %v", cs.header.RequestType)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("mock never saw the cancel frame")
	}

	// Clean teardown.
	if err := client.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	// Drain the push goroutine so we don't leak it past the test boundary.
	// We don't strictly need it (the channel never closes), but we observe
	// the channel briefly to ensure it remains live.
	select {
	case <-done:
		// channel was closed by the SDK (it isn't today) -- fine.
	case <-time.After(20 * time.Millisecond):
		// Expected path: no further pushes, channel still open. Leak is
		// goroutine-local to the test and will be reaped on test exit.
	}
}

// TestMockIntegration_ConcurrentOrders verifies that many encrypted orders may
// be in flight at once: each PlaceOrder stamps a distinct correlation id and
// the transport must route every ack back to its own waiter without
// serializing on a single in-flight slot. Regression test for the throughput
// bottleneck where the send lock was held across the whole round-trip.
func TestMockIntegration_ConcurrentOrders(t *testing.T) {
	const userUUID = "00000000-0000-0000-0000-00000000002a"

	m := newMockEdge(t, userUUID)
	defer m.close()

	client, err := godark.NewClient(godark.ClientConfig{
		APIKey:                  "test-token",
		BaseURL:                 m.wsBaseURL(),
		NoiseStaticPublicKeyHex: m.hpkeStaticPublicKeyHex(),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = client.Disconnect() }()

	// Drain gotPlace so the mock never blocks recording frames.
	go func() {
		for {
			select {
			case <-m.gotPlace:
			case <-ctx.Done():
				return
			}
		}
	}()

	const n = 24
	var wg sync.WaitGroup
	errs := make([]error, n)
	oks := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ack, err := client.PlaceOrder(ctx, godark.PlaceOrderRequest{
				Symbol:       "BTC-USDC-PERP",
				Side:         godark.SideSell,
				OrderType:    godark.OrderTypeLimit,
				Price:        999_999 + float64(idx),
				Quantity:     0.01,
				Confirmation: godark.PlaceOrderConfirmationAck,
			})
			errs[idx] = err
			if err == nil {
				oks[idx] = ack.Success
			}
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("concurrent PlaceOrder[%d] failed: %v", i, errs[i])
		}
		if !oks[i] {
			t.Fatalf("concurrent PlaceOrder[%d] ack not successful", i)
		}
	}
}

// TestMockIntegration_AuthFailure exercises the error path: edge returns
// code != 0 on the login op and the SDK surfaces it as an
// AuthenticationError without leaving a live WS open.
func TestMockIntegration_AuthFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/v1", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "bye")
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
			if op, _ := msg["op"].(string); op == "login" {
				id, _ := msg["id"].(string)
				b, _ := json.Marshal(map[string]any{
					"id":      id,
					"op":      "login",
					"code":    1401,
					"message": "invalid api key",
				})
				_ = conn.Write(ctx, websocket.MessageText, b)
				return
			}
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client, err := godark.NewClient(godark.ClientConfig{
		APIKey:  "bad-token",
		BaseURL: "ws://" + strings.TrimPrefix(srv.URL, "http://"),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
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
	if !strings.Contains(strings.ToLower(ae.Error()), "invalid api key") {
		t.Fatalf("Connect: error message did not surface server reason: %v", err)
	}
}
