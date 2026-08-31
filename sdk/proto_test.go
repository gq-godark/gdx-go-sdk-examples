package godark

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/proto"

	commonpb "github.com/gq-godark/gdx-go-sdk/proto/gdx/common/v1"
	edgepb "github.com/gq-godark/gdx-go-sdk/proto/gdx/edge/v1"
	healthpb "github.com/gq-godark/gdx-go-sdk/proto/gdx/health/v1"
	sequencerpb "github.com/gq-godark/gdx-go-sdk/proto/gdx/sequencer/v1"
)

func TestBuildOrderHeaderAAD_RoundTrips(t *testing.T) {
	uid := bytes.Repeat([]byte{0x42}, 16)
	out, err := BuildOrderHeaderAAD(uid, 1, "place", 99, 128, []byte{1, 2, 3, 4})
	if err != nil {
		t.Fatalf("BuildOrderHeaderAAD: %v", err)
	}
	var hdr edgepb.OrderHeader
	if err := proto.Unmarshal(out, &hdr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(hdr.UserUuid, uid) {
		t.Errorf("UserUuid = %x, want %x", hdr.UserUuid, uid)
	}
	if hdr.SymbolId != 1 {
		t.Errorf("SymbolId = %d, want 1", hdr.SymbolId)
	}
	if hdr.RequestType != commonpb.RequestType_REQUEST_TYPE_PLACE {
		t.Errorf("RequestType = %v, want REQUEST_TYPE_PLACE", hdr.RequestType)
	}
	if hdr.Nonce != 99 || hdr.BodyLength != 128 {
		t.Errorf("Nonce/BodyLength = %d/%d, want 99/128", hdr.Nonce, hdr.BodyLength)
	}
}

func TestBuildOrderHeaderAAD_UnknownRequestType(t *testing.T) {
	if _, err := BuildOrderHeaderAAD(nil, 0, "not-a-request-type", 0, 0, nil); err == nil {
		t.Fatal("expected error for unknown request type")
	}
}

func TestBuildOrderHeaderAAD_UpdateLeverageUsesRequestType8(t *testing.T) {
	uid := bytes.Repeat([]byte{0x42}, 16)
	out, err := BuildOrderHeaderAAD(uid, 1, "update_leverage", 99, 128, []byte{1, 2, 3, 4})
	if err != nil {
		t.Fatalf("BuildOrderHeaderAAD: %v", err)
	}
	var hdr edgepb.OrderHeader
	if err := proto.Unmarshal(out, &hdr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if hdr.RequestType != commonpb.RequestType_REQUEST_TYPE_UPDATE_LEVERAGE {
		t.Errorf("RequestType = %v, want REQUEST_TYPE_UPDATE_LEVERAGE (6)", hdr.RequestType)
	}
}

func TestBuildResponseHeaderAAD_RoundTrips(t *testing.T) {
	uid := bytes.Repeat([]byte{0x33}, 16)
	correlationID := bytes.Repeat([]byte{0x44}, 16)
	out, err := BuildResponseHeaderAAD(uid, "order_update", 64, 7, 1, correlationID, 42)
	if err != nil {
		t.Fatalf("BuildResponseHeaderAAD: %v", err)
	}
	var hdr edgepb.ResponseHeader
	if err := proto.Unmarshal(out, &hdr); err != nil {
		t.Fatal(err)
	}
	if hdr.MessageType != commonpb.ResponseMessageType_RESPONSE_MESSAGE_TYPE_ORDER_UPDATE {
		t.Errorf("MessageType = %v, want ORDER_UPDATE", hdr.MessageType)
	}
	if hdr.Nonce != 7 || hdr.BodyLength != 64 || hdr.FencingEpoch != 1 {
		t.Errorf("hdr nonce=%d body=%d epoch=%d", hdr.Nonce, hdr.BodyLength, hdr.FencingEpoch)
	}
	if !bytes.Equal(hdr.CorrelationId, correlationID) || hdr.SessionSeq != 42 {
		t.Errorf("response fields correlation=%x session_seq=%d", hdr.CorrelationId, hdr.SessionSeq)
	}
}

func TestBuildUpdateLeverageRequest_RoundTrip(t *testing.T) {
	uid := bytes.Repeat([]byte{0x99}, 16)
	out, err := BuildUpdateLeverageRequest(uid, 3, 5, []byte{0x01, 0x02})
	if err != nil {
		t.Fatalf("BuildUpdateLeverageRequest: %v", err)
	}
	var req sequencerpb.EdgeSequencerRequest
	if err := proto.Unmarshal(out, &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	inner, ok := req.Inner.(*sequencerpb.EdgeSequencerRequest_UpdateLeverage)
	if !ok || inner.UpdateLeverage == nil {
		t.Fatalf("inner = %T, want UpdateLeverage", req.Inner)
	}
	ul := inner.UpdateLeverage
	if ul.SymbolId != 3 || ul.Leverage != 5 {
		t.Errorf("symbol/leverage = %d/%d, want 3/5", ul.SymbolId, ul.Leverage)
	}
	if !bytes.Equal(ul.UserUuid, uid) {
		t.Errorf("UserUuid = %x, want %x", ul.UserUuid, uid)
	}
	if !bytes.Equal(ul.CorrelationId, []byte{0x02, 0x01}) {
		t.Errorf("CorrelationId = %x, want 0201", ul.CorrelationId)
	}
}

func TestBuildUpdateLeverageRequest_ClampLeverage(t *testing.T) {
	uid := bytes.Repeat([]byte{0x01}, 16)
	out, err := BuildUpdateLeverageRequest(uid, 1, 0, nil)
	if err != nil {
		t.Fatalf("BuildUpdateLeverageRequest: %v", err)
	}
	var req sequencerpb.EdgeSequencerRequest
	if err := proto.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	inner := req.GetUpdateLeverage()
	if inner == nil || inner.Leverage != 1 {
		t.Fatalf("leverage = %v, want clamp to 1", inner)
	}
}

func TestBuildPlaceOrderRequest_RoundTrip(t *testing.T) {
	uid := bytes.Repeat([]byte{0x11}, 16)
	price := 12345.67
	out, err := BuildPlaceOrderRequest(
		1, SideSell, OrderTypeLimit, 0.5, uid,
		&price, TimeInForceGTC, false, nil, nil, []byte{0xaa, 0xbb}, 999,
	)
	if err != nil {
		t.Fatalf("BuildPlaceOrderRequest: %v", err)
	}
	var req sequencerpb.EdgeSequencerRequest
	if err := proto.Unmarshal(out, &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	pl, ok := req.Inner.(*sequencerpb.EdgeSequencerRequest_Place)
	if !ok || pl.Place == nil {
		t.Fatalf("inner = %T, want *EdgeSequencerRequest_Place", req.Inner)
	}
	p := pl.Place
	if p.SymbolId != 1 {
		t.Errorf("SymbolId = %d, want 1", p.SymbolId)
	}
	if p.Side != commonpb.Side_SIDE_SELL {
		t.Errorf("Side = %v, want SELL", p.Side)
	}
	if p.OrderType != commonpb.OrderType_ORDER_TYPE_LIMIT {
		t.Errorf("OrderType = %v, want LIMIT", p.OrderType)
	}
	if p.Quantity != 0.5 {
		t.Errorf("Quantity = %f, want 0.5", p.Quantity)
	}
	if p.Price == nil || *p.Price != 12345.67 {
		t.Errorf("Price = %v, want 12345.67", p.Price)
	}
	if p.TimeInForce != commonpb.TimeInForce_TIME_IN_FORCE_GTC {
		t.Errorf("TimeInForce = %v, want GTC", p.TimeInForce)
	}
	if !bytes.Equal(p.UserUuid, uid) {
		t.Errorf("UserUuid = %x, want %x", p.UserUuid, uid)
	}
	if !bytes.Equal(p.CorrelationId, []byte{0xbb, 0xaa}) {
		t.Errorf("CorrelationId = %x, want bbaa", p.CorrelationId)
	}
}

func TestBuildCancelOrderRequest_RoundTrip(t *testing.T) {
	uid := bytes.Repeat([]byte{0x77}, 16)
	out, err := BuildCancelOrderRequest(42, uid, 5, []byte{0xde, 0xad})
	if err != nil {
		t.Fatal(err)
	}
	var req sequencerpb.EdgeSequencerRequest
	if err := proto.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	inner, ok := req.Inner.(*sequencerpb.EdgeSequencerRequest_Cancel)
	if !ok || inner.Cancel == nil {
		t.Fatalf("inner = %T, want Cancel", req.Inner)
	}
	c := inner.Cancel
	if c.OrderId != 42 || c.SymbolId != 5 {
		t.Errorf("cancel orderId=%d symbolId=%d", c.OrderId, c.SymbolId)
	}
	if !bytes.Equal(c.UserUuid, uid) {
		t.Errorf("UserUuid = %x, want %x", c.UserUuid, uid)
	}
}

func TestBuildPlaceOrderRequest_AonMapsToMinFillSize(t *testing.T) {
	uid := bytes.Repeat([]byte{0x11}, 16)
	out, err := BuildPlaceOrderRequest(
		1, SideBuy, OrderTypeLimit, 2.5, uid,
		nil, TimeInForceGTC, true, nil, nil, nil, 0,
	)
	if err != nil {
		t.Fatalf("BuildPlaceOrderRequest: %v", err)
	}
	var req sequencerpb.EdgeSequencerRequest
	if err := proto.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	place := req.GetPlace()
	if place == nil || place.MinFillSize == nil || *place.MinFillSize != 2.5 {
		t.Fatalf("aon min_fill_size = %v, want 2.5", place)
	}
}

func TestBuildModifyOrderRequest_RoundTrip(t *testing.T) {
	uid := bytes.Repeat([]byte{0x55}, 16)
	np := 100.0
	nq := 2.5
	out, err := BuildModifyOrderRequest(7, uid, 3, &np, &nq, []byte{0x01, 0x02, 0x03})
	if err != nil {
		t.Fatal(err)
	}
	var req sequencerpb.EdgeSequencerRequest
	if err := proto.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	inner, ok := req.Inner.(*sequencerpb.EdgeSequencerRequest_Modify)
	if !ok || inner.Modify == nil {
		t.Fatalf("inner = %T, want Modify", req.Inner)
	}
	m := inner.Modify
	if m.OrderId != 7 || m.SymbolId != 3 {
		t.Errorf("modify orderId=%d symbolId=%d", m.OrderId, m.SymbolId)
	}
	if m.NewPrice == nil || *m.NewPrice != 100.0 {
		t.Errorf("NewPrice = %v, want 100.0", m.NewPrice)
	}
	if m.NewQuantity == nil || *m.NewQuantity != 2.5 {
		t.Errorf("NewQuantity = %v, want 2.5", m.NewQuantity)
	}
}

func TestParseOrderUpdate(t *testing.T) {
	uid := bytes.Repeat([]byte{0xab}, 16)
	in := &sequencerpb.OrderUpdateMessage{
		MessageType:   commonpb.OrderUpdateType_ORDER_UPDATE_TYPE_FILLED,
		OrderId:       0xDEADBEEF,
		UserUuid:      uid,
		SymbolId:      2,
		OrderStatus:   commonpb.OrderStatus_ORDER_STATUS_FILLED,
		Price:         "100.5",
		Quantity:      "0.5",
		Side:          commonpb.Side_SIDE_BUY,
		FilledQty:     "0.5",
		RemainingQty:  "0",
		CumFill:       "0.5",
		CorrelationId: []byte{0, 0, 0, 0, 0, 0, 0, 1},
		Timestamp:     1234,
		Leverage:      5,
	}
	b, err := proto.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseOrderUpdate(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.OrderID != "3735928559" {
		t.Errorf("OrderID = %q, want \"3735928559\"", got.OrderID)
	}
	if got.Side != SideBuy {
		t.Errorf("Side = %v", got.Side)
	}
	if got.Status != OrderStatusFilled {
		t.Errorf("Status = %v", got.Status)
	}
	if got.UpdateType != OrderUpdateTypeFilled {
		t.Errorf("UpdateType = %v", got.UpdateType)
	}
	if got.Price != "100.5" || got.Quantity != "0.5" {
		t.Errorf("price/qty = %s/%s", got.Price, got.Quantity)
	}
	if got.SymbolID != 2 {
		t.Errorf("SymbolID = %d", got.SymbolID)
	}
	if got.CorrelationID != 1 {
		t.Errorf("CorrelationID = %d, want 1", got.CorrelationID)
	}
	if got.Leverage != 5 {
		t.Errorf("Leverage = %d", got.Leverage)
	}
}

func TestParseSequencerToEdgeMessage_OrderUpdate(t *testing.T) {
	inner := &sequencerpb.OrderUpdateMessage{
		MessageType: commonpb.OrderUpdateType_ORDER_UPDATE_TYPE_OPEN,
		OrderId:     1,
		Price:       "1",
		Quantity:    "1",
		Side:        commonpb.Side_SIDE_BUY,
		OrderStatus: commonpb.OrderStatus_ORDER_STATUS_NEW,
	}
	wrap := &sequencerpb.SequencerToEdgeMessage{
		Inner: &sequencerpb.SequencerToEdgeMessage_OrderUpdate{OrderUpdate: inner},
	}
	b, err := proto.Marshal(wrap)
	if err != nil {
		t.Fatal(err)
	}
	push, err := ParseSequencerToEdgeMessage(b)
	if err != nil {
		t.Fatal(err)
	}
	ou, ok := push.(*OrderUpdate)
	if !ok {
		t.Fatalf("push type = %T, want *OrderUpdate", push)
	}
	if ou.UpdateType != OrderUpdateTypeOpen {
		t.Errorf("UpdateType = %v", ou.UpdateType)
	}
}

func TestParseSequencerToEdgeMessage_SystemHealth(t *testing.T) {
	inner := &healthpb.HealthReport{
		ComponentId:    "sequencer-1",
		State:          healthpb.HealthState_HEALTH_STATE_READY,
		Serving:        true,
		UpdatedAtNanos: 1,
		Sequence:       2,
		SchemaVersion:  1,
	}
	wrap := &sequencerpb.SequencerToEdgeMessage{
		Inner: &sequencerpb.SequencerToEdgeMessage_HealthReport{HealthReport: inner},
	}
	b, _ := proto.Marshal(wrap)
	push, err := ParseSequencerToEdgeMessage(b)
	if err != nil {
		t.Fatal(err)
	}
	h, ok := push.(*SystemHealthUpdate)
	if !ok {
		t.Fatalf("got %T, want *SystemHealthUpdate", push)
	}
	if h.ComponentID != "sequencer-1" || !h.Serving || h.State != int32(healthpb.HealthState_HEALTH_STATE_READY) {
		t.Fatalf("SystemHealth component=%q serving=%v state=%d", h.ComponentID, h.Serving, h.State)
	}
}

func TestParseLeverageSettings(t *testing.T) {
	uid := bytes.Repeat([]byte{0xab}, 16)
	msg := &sequencerpb.LeverageSettings{
		UserUuid:        uid,
		ServerTimestamp: 123456789,
		Settings: []*sequencerpb.LeverageSettingRow{
			{SymbolId: 7, Leverage: 5},
			{SymbolId: 9, Leverage: 10},
		},
	}
	got := ParseLeverageSettings(msg)
	if got.UserUUID != uuidBytesToString(uid) {
		t.Errorf("UserUUID = %q", got.UserUUID)
	}
	if got.ServerTimestamp != 123456789 {
		t.Errorf("ServerTimestamp = %d", got.ServerTimestamp)
	}
	if len(got.Settings) != 2 {
		t.Fatalf("settings = %d, want 2", len(got.Settings))
	}
	if got.Settings[0].SymbolID != 7 || got.Settings[0].Leverage != 5 {
		t.Errorf("settings[0] = %+v", got.Settings[0])
	}
	if got.Settings[1].SymbolID != 9 || got.Settings[1].Leverage != 10 {
		t.Errorf("settings[1] = %+v", got.Settings[1])
	}
}

func TestParseSequencerToEdgeMessage_LeverageSettings(t *testing.T) {
	uid := bytes.Repeat([]byte{0xcd}, 16)
	inner := &sequencerpb.LeverageSettings{
		UserUuid:        uid,
		ServerTimestamp: 42,
		Settings:        []*sequencerpb.LeverageSettingRow{{SymbolId: 3, Leverage: 2}},
	}
	wrap := &sequencerpb.SequencerToEdgeMessage{
		Inner: &sequencerpb.SequencerToEdgeMessage_LeverageSettings{LeverageSettings: inner},
	}
	b, err := proto.Marshal(wrap)
	if err != nil {
		t.Fatal(err)
	}
	push, err := ParseSequencerToEdgeMessage(b)
	if err != nil {
		t.Fatal(err)
	}
	ls, ok := push.(*LeverageSettings)
	if !ok {
		t.Fatalf("push type = %T, want *LeverageSettings", push)
	}
	if ls.UserUUID != uuidBytesToString(uid) || ls.ServerTimestamp != 42 {
		t.Fatalf("snapshot = %+v", ls)
	}
	if len(ls.Settings) != 1 || ls.Settings[0].SymbolID != 3 || ls.Settings[0].Leverage != 2 {
		t.Fatalf("settings = %+v", ls.Settings)
	}
}

func TestParseNodeResponseAck_Success(t *testing.T) {
	ackMsg := &sequencerpb.AckMessage{
		Sequence: 42,
		OrderId:  42,
		AckOutcome: &sequencerpb.AckOutcomeWire{
			Kind: sequencerpb.AckOutcomeKind_ACK_OUTCOME_KIND_APPLIED,
		},
	}
	inner, _ := proto.Marshal(ackMsg)
	b := WrapLegacyNodeResponse("ack", inner)
	ack, isAck, err := ParseNodeResponseAck(b)
	if err != nil {
		t.Fatal(err)
	}
	if !isAck {
		t.Fatal("expected ack=true")
	}
	if ack.OrderID != 42 || !ack.Success {
		t.Fatalf("ack orderID=%d success=%v", ack.OrderID, ack.Success)
	}
}

func TestParseNodeResponseAck_RejectText(t *testing.T) {
	text := "price deviation too large"
	code := uint32(2015)
	ackMsg := &sequencerpb.AckMessage{
		RejectText: &text,
		AckOutcome: &sequencerpb.AckOutcomeWire{
			Kind:              sequencerpb.AckOutcomeKind_ACK_OUTCOME_KIND_SYSTEM_FAILED,
			BusinessErrorCode: &code,
		},
	}
	inner, _ := proto.Marshal(ackMsg)
	b := WrapLegacyNodeResponse("ack", inner)
	ack, isAck, err := ParseNodeResponseAck(b)
	if err != nil {
		t.Fatal(err)
	}
	if !isAck || ack == nil {
		t.Fatal("expected ack")
	}
	if ack.Success || ack.RejectText != text {
		t.Fatalf("success=%v reject_text=%q", ack.Success, ack.RejectText)
	}
	if ack.ErrorCode == nil || *ack.ErrorCode != code {
		t.Fatalf("error_code=%v", ack.ErrorCode)
	}
}

func TestParseNodeResponseAck_MissingOutcome(t *testing.T) {
	ackMsg := &sequencerpb.AckMessage{
		Sequence: 1,
		OrderId:  42,
	}
	inner, _ := proto.Marshal(ackMsg)
	b := WrapLegacyNodeResponse("ack", inner)
	ack, isAck, err := ParseNodeResponseAck(b)
	if err != nil {
		t.Fatal(err)
	}
	if !isAck || ack == nil {
		t.Fatal("expected ack")
	}
	if ack.Success {
		t.Fatal("missing ack_outcome must fail closed (success=false)")
	}
}

func TestParseNodeResponseAck_NonAck(t *testing.T) {
	// A NodeReady inner is parseable but is not an Ack - returns ok=false,
	// no error. Same for any non-Ack inner.
	inner, _ := proto.Marshal(&sequencerpb.TradeMessage{TradeId: 1})
	b := WrapLegacyNodeResponse("fill", inner)
	ack, isAck, err := ParseNodeResponseAck(b)
	if err != nil {
		t.Fatal(err)
	}
	if isAck || ack != nil {
		t.Fatalf("expected non-ack, got ack=%v isAck=%v", ack, isAck)
	}
}

func TestCorrelationIDToUint64(t *testing.T) {
	cases := []struct {
		in   []byte
		want uint64
	}{
		{nil, 0},
		{[]byte{0xff}, 0xff},
		{[]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, 1},
		{[]byte{0xde, 0xad, 0xbe, 0xef, 0xca, 0xfe, 0xba, 0xbe}, 0xDEADBEEFCAFEBABE},
	}
	for _, c := range cases {
		if got := correlationIDToUint64(c.in); got != c.want {
			t.Errorf("correlationIDToUint64(%x) = %x, want %x", c.in, got, c.want)
		}
	}
}

func TestUUIDBytesToString(t *testing.T) {
	if got := uuidBytesToString(nil); got != "00000000-0000-0000-0000-000000000000" {
		t.Errorf("nil uuid -> %q", got)
	}
	if got := uuidBytesToString(make([]byte, 16)); got != "00000000-0000-0000-0000-000000000000" {
		t.Errorf("zero uuid -> %q", got)
	}
}

func u64ptr(v uint64) *uint64   { return &v }
func f64ptr(v float64) *float64 { return &v }

func TestBuildMassQuoteRequest_RoundTrip(t *testing.T) {
	uid := bytes.Repeat([]byte{0x55}, 16)
	legs := []MassQuoteLegInput{
		{Side: SideBuy, Price: 100.5, Quantity: 1, CancelOrderID: u64ptr(42)},
		{Side: SideSell, Price: 200, Quantity: 2, TimeInForce: TimeInForceGTC},
	}
	relaxed := false
	out, err := BuildMassQuoteRequest(7, uid, legs, make([]byte, 16), &relaxed)
	if err != nil {
		t.Fatal(err)
	}
	var req sequencerpb.EdgeSequencerRequest
	if err := proto.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	inner, ok := req.Inner.(*sequencerpb.EdgeSequencerRequest_MassQuote)
	if !ok || inner.MassQuote == nil {
		t.Fatalf("inner = %T, want MassQuote", req.Inner)
	}
	mq := inner.MassQuote
	if mq.SymbolId != 7 {
		t.Errorf("symbolId=%d", mq.SymbolId)
	}
	if !bytes.Equal(mq.UserUuid, uid) {
		t.Errorf("userUuid mismatch")
	}
	if mq.PostOnly == nil || *mq.PostOnly != false {
		t.Errorf("postOnly = %v, want pointer to false", mq.PostOnly)
	}
	if len(mq.Legs) != 2 {
		t.Fatalf("legs = %d, want 2", len(mq.Legs))
	}
	if mq.Legs[0].CancelOrderId != 42 || mq.Legs[0].Price != 100.5 {
		t.Errorf("leg0 cancelId=%d price=%v", mq.Legs[0].CancelOrderId, mq.Legs[0].Price)
	}
	// Pure-place leg defaults the cancel target to 0.
	if mq.Legs[1].CancelOrderId != 0 {
		t.Errorf("leg1 cancelId = %d, want 0", mq.Legs[1].CancelOrderId)
	}
	// Each leg carries a unique 16-byte correlation id.
	if len(mq.Legs[0].CorrelationId) != 16 {
		t.Errorf("leg0 corr len = %d, want 16", len(mq.Legs[0].CorrelationId))
	}
	if bytes.Equal(mq.Legs[0].CorrelationId, mq.Legs[1].CorrelationId) {
		t.Errorf("leg correlation ids should be unique")
	}
}

func TestBuildMassQuoteRequest_DefaultPostOnly(t *testing.T) {
	uid := bytes.Repeat([]byte{0x77}, 16)
	out, err := BuildMassQuoteRequest(1, uid, []MassQuoteLegInput{
		{Side: SideBuy, Price: 100, Quantity: 1},
	}, make([]byte, 16), nil)
	if err != nil {
		t.Fatal(err)
	}
	var req sequencerpb.EdgeSequencerRequest
	if err := proto.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	inner, ok := req.Inner.(*sequencerpb.EdgeSequencerRequest_MassQuote)
	if !ok || inner.MassQuote == nil {
		t.Fatalf("inner = %T, want MassQuote", req.Inner)
	}
	if inner.MassQuote.PostOnly == nil || !*inner.MassQuote.PostOnly {
		t.Errorf("postOnly = %v, want pointer to true", inner.MassQuote.PostOnly)
	}
}

func TestBuildBatchCancelRequest_RoundTrip(t *testing.T) {
	uid := bytes.Repeat([]byte{0x66}, 16)
	out, err := BuildBatchCancelRequest(9, uid, []uint64{11, 22, 33}, []byte{0xbc})
	if err != nil {
		t.Fatal(err)
	}
	var req sequencerpb.EdgeSequencerRequest
	if err := proto.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	inner, ok := req.Inner.(*sequencerpb.EdgeSequencerRequest_BatchCancel)
	if !ok || inner.BatchCancel == nil {
		t.Fatalf("inner = %T, want BatchCancel", req.Inner)
	}
	bc := inner.BatchCancel
	if bc.SymbolId != 9 {
		t.Errorf("symbolId = %d", bc.SymbolId)
	}
	if len(bc.OrderIds) != 3 || bc.OrderIds[0] != 11 || bc.OrderIds[2] != 33 {
		t.Errorf("orderIds = %v", bc.OrderIds)
	}
}

func TestBuildBatchModifyRequest_RoundTrip(t *testing.T) {
	uid := bytes.Repeat([]byte{0x77}, 16)
	legs := []BatchModifyLegInput{
		{OrderID: 5, NewPrice: f64ptr(101)},
		{OrderID: 6, NewQuantity: f64ptr(4)},
	}
	out, err := BuildBatchModifyRequest(9, uid, legs, make([]byte, 16))
	if err != nil {
		t.Fatal(err)
	}
	var req sequencerpb.EdgeSequencerRequest
	if err := proto.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	inner, ok := req.Inner.(*sequencerpb.EdgeSequencerRequest_BatchModify)
	if !ok || inner.BatchModify == nil {
		t.Fatalf("inner = %T, want BatchModify", req.Inner)
	}
	bm := inner.BatchModify
	if len(bm.Legs) != 2 {
		t.Fatalf("legs = %d, want 2", len(bm.Legs))
	}
	if bm.Legs[0].OrderId != 5 || bm.Legs[0].NewPrice == nil || *bm.Legs[0].NewPrice != 101 {
		t.Errorf("leg0 = %+v", bm.Legs[0])
	}
	if bm.Legs[0].NewQuantity != nil {
		t.Errorf("leg0 NewQuantity should be nil")
	}
	if bm.Legs[1].NewQuantity == nil || *bm.Legs[1].NewQuantity != 4 {
		t.Errorf("leg1 NewQuantity = %v", bm.Legs[1].NewQuantity)
	}
	if len(bm.Legs[0].CorrelationId) != 16 {
		t.Errorf("leg0 corr len = %d, want 16", len(bm.Legs[0].CorrelationId))
	}
}

func TestParseMassQuoteAck_RoundTrip(t *testing.T) {
	ec := uint32(2018)
	mq := &sequencerpb.MassQuoteAck{
		Sequence: 99,
		Results: []*sequencerpb.MassQuoteLegResult{
			{LegIndex: 0, CancelledOrderId: 42, NewOrderId: 77, Status: sequencerpb.MassQuoteLegStatus_MASS_QUOTE_LEG_STATUS_OPEN},
			{LegIndex: 1, Status: sequencerpb.MassQuoteLegStatus_MASS_QUOTE_LEG_STATUS_FAILED, ErrorCode: &ec},
		},
	}
	inner, err := proto.Marshal(mq)
	if err != nil {
		t.Fatal(err)
	}
	data := WrapLegacyNodeResponse("mass_quote_ack", inner)
	ack, ok, err := ParseMassQuoteAck(data)
	if err != nil || !ok {
		t.Fatalf("parse ok=%v err=%v", ok, err)
	}
	if ack.Success {
		t.Error("expected Success=false (one leg failed)")
	}
	if ack.Sequence != "99" {
		t.Errorf("sequence = %q", ack.Sequence)
	}
	if len(ack.Results) != 2 {
		t.Fatalf("results = %d", len(ack.Results))
	}
	if ack.Results[0].Status != "open" || ack.Results[0].CancelledOrderID != "42" || ack.Results[0].NewOrderID != "77" {
		t.Errorf("result0 = %+v", ack.Results[0])
	}
	if ack.Results[1].Status != "failed" || ack.Results[1].CancelledOrderID != "" || ack.Results[1].ErrorCode == nil || *ack.Results[1].ErrorCode != 2018 {
		t.Errorf("result1 = %+v", ack.Results[1])
	}
}

func TestParseBatchCancelAck_RoundTrip(t *testing.T) {
	ec := uint32(2003)
	bc := &sequencerpb.BatchCancelAck{
		Sequence: 5,
		Results: []*sequencerpb.BatchCancelLegResult{
			{OrderId: 11, Cancelled: true},
			{OrderId: 22, Cancelled: false, ErrorCode: &ec},
		},
	}
	inner, _ := proto.Marshal(bc)
	data := WrapLegacyNodeResponse("batch_cancel_ack", inner)
	ack, ok, err := ParseBatchCancelAck(data)
	if err != nil || !ok {
		t.Fatalf("parse ok=%v err=%v", ok, err)
	}
	if ack.Success {
		t.Error("expected Success=false")
	}
	if ack.Results[0].OrderID != "11" || !ack.Results[0].Cancelled {
		t.Errorf("result0 = %+v", ack.Results[0])
	}
	if ack.Results[1].Cancelled || ack.Results[1].ErrorCode == nil || *ack.Results[1].ErrorCode != 2003 {
		t.Errorf("result1 = %+v", ack.Results[1])
	}
}

func TestParseOpenOrdersSnapshot(t *testing.T) {
	snapMsg := &sequencerpb.OpenOrdersSnapshot{
		ServerTimestamp: 999,
		CorrelationId:   []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 42},
		Rows: []*sequencerpb.OpenOrderRow{
			{
				OrderId:      12345,
				SymbolId:     7,
				Leverage:     3,
				Price:        "100.5",
				Quantity:     "2",
				RemainingQty: "1.5",
			},
		},
	}
	inner, err := proto.Marshal(snapMsg)
	if err != nil {
		t.Fatal(err)
	}
	data := WrapLegacyNodeResponse("open_orders_snapshot", inner)
	snap, err := ParseOpenOrdersSnapshot(data)
	if err != nil {
		t.Fatal(err)
	}
	if snap.ServerTimestamp != 999 {
		t.Errorf("ServerTimestamp = %d, want 999", snap.ServerTimestamp)
	}
	if snap.CorrelationID != 42 {
		t.Errorf("CorrelationID = %d, want 42", snap.CorrelationID)
	}
	if len(snap.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(snap.Rows))
	}
	row := snap.Rows[0]
	if row.OrderID != "12345" || row.SymbolID != 7 || row.Leverage != 3 {
		t.Errorf("row ids = %+v", row)
	}
	if row.Price != "100.5" || row.Quantity != "2" || row.RemainingQty != "1.5" {
		t.Errorf("row qty/price = %+v", row)
	}
}

func TestParseOpenOrdersSnapshot_WrongVariant(t *testing.T) {
	inner, _ := proto.Marshal(&sequencerpb.AckMessage{Sequence: 1})
	data := WrapLegacyNodeResponse("ack", inner)
	if _, err := ParseOpenOrdersSnapshot(data); err == nil {
		t.Fatal("expected error for non-open_orders_snapshot variant")
	}
}

func TestParseBatchModifyAck_RoundTrip(t *testing.T) {
	bm := &sequencerpb.BatchModifyAck{
		Sequence: 7,
		Results:  []*sequencerpb.BatchModifyLegResult{{OrderId: 5, Modified: true}},
	}
	inner, _ := proto.Marshal(bm)
	data := WrapLegacyNodeResponse("batch_modify_ack", inner)
	ack, ok, err := ParseBatchModifyAck(data)
	if err != nil || !ok {
		t.Fatalf("parse ok=%v err=%v", ok, err)
	}
	if !ack.Success || ack.Sequence != "7" {
		t.Errorf("ack = %+v", ack)
	}
	if ack.Results[0].OrderID != "5" || !ack.Results[0].Modified {
		t.Errorf("result0 = %+v", ack.Results[0])
	}
}
