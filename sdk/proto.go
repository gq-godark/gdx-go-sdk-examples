package godark

// Bridge between clean public Go types and serialized protobuf messages.
//
// The generated bindings under `proto/gdx/...` are kept untouched; everything
// the WS / REST clients actually call lives here so we have a single place
// to maintain wire compatibility with the python / rust / js SDKs.

import (
	"encoding/binary"
	"fmt"
	"math/big"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	commonpb "github.com/gq-godark/gdx-go-sdk/proto/gdx/common/v1"
	edgepb "github.com/gq-godark/gdx-go-sdk/proto/gdx/edge/v1"
	sequencerpb "github.com/gq-godark/gdx-go-sdk/proto/gdx/sequencer/v1"
)

// SequencerPush is the union type emitted by ParseSequencerToEdgeMessage.
// Callers should type-switch on it to handle individual variants. Any new
// inner variant the wire format gains will surface as *UnknownSequencerPush
// rather than panicking - this is the forward-compatibility contract
// mirrored from rust's `EdgeMessage::Unknown`.
type SequencerPush interface{ isSequencerPush() }

func (*OrderUpdate) isSequencerPush()          {}
func (*PositionUpdate) isSequencerPush()       {}
func (*PositionsSnapshot) isSequencerPush()    {}
func (*SystemHealthUpdate) isSequencerPush()   {}
func (*BalanceUpdate) isSequencerPush()        {}
func (*MarginAlert) isSequencerPush()          {}
func (*FundingRateUpdate) isSequencerPush()    {}
func (*SettlementUpdate) isSequencerPush()     {}
func (*UnknownSequencerPush) isSequencerPush() {}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// correlationIDToUint64 converts a 16-byte big-endian correlation id to
// uint64 (truncated to the low 8 bytes, matching python's int conversion
// truncation for the documented uint64 ceiling).
func correlationIDToUint64(raw []byte) uint64 {
	if len(raw) == 0 {
		return 0
	}
	if len(raw) >= 8 {
		return binary.BigEndian.Uint64(raw[len(raw)-8:])
	}
	var b [8]byte
	copy(b[8-len(raw):], raw)
	return binary.BigEndian.Uint64(b[:])
}

// correlationIDBigInt returns the full u128 correlation id as *big.Int so
// callers who care about the full width can read it. The on-wire format is
// big-endian.
func correlationIDBigInt(raw []byte) *big.Int {
	if len(raw) == 0 {
		return new(big.Int)
	}
	return new(big.Int).SetBytes(raw)
}

// uuidBytesToString converts 16 raw UUID bytes to canonical hex form, or
// returns the all-zero UUID on malformed input.
func uuidBytesToString(raw []byte) string {
	if len(raw) == 16 {
		var u uuid.UUID
		copy(u[:], raw)
		return u.String()
	}
	return "00000000-0000-0000-0000-000000000000"
}

func stringOr(p *string, fallback string) string {
	if p == nil {
		return fallback
	}
	return *p
}

// ---------------------------------------------------------------------------
// Builders -- public Go inputs => serialized protobuf bytes
// ---------------------------------------------------------------------------

// BuildPlaceOrderRequest assembles the encrypted-body payload for a Place
// command: a PlaceOrderInput wrapped in EdgeSequencerRequest, serialized.
func BuildPlaceOrderRequest(
	symbolID uint64,
	side Side,
	orderType OrderType,
	quantity float64,
	userUUID []byte,
	price *float64,
	timeInForce TimeInForce,
	aon bool,
	minFillSize *float64,
	expiryTime *uint64,
	correlationID []byte,
	timestamp uint64,
) ([]byte, error) {
	sideInt, ok := sideToProto[side]
	if !ok {
		return nil, fmt.Errorf("unknown side: %q", side)
	}
	otypeInt, ok := orderTypeToProto[orderType]
	if !ok {
		return nil, fmt.Errorf("unknown order type: %q", orderType)
	}
	tifInt, ok := timeInForceToProto[timeInForce]
	if !ok {
		return nil, fmt.Errorf("unknown time in force: %q", timeInForce)
	}

	place := &sequencerpb.PlaceOrderInput{
		SymbolId:       symbolID,
		Side:           commonpb.Side(sideInt),
		OrderType:      commonpb.OrderType(otypeInt),
		Quantity:       quantity,
		UserCommitment: nil,
		TimeInForce:    commonpb.TimeInForce(tifInt),
		Aon:            aon,
		Timestamp:      timestamp,
		UserUuid:       userUUID,
	}
	if price != nil {
		place.Price = price
	}
	if minFillSize != nil {
		place.MinFillSize = minFillSize
	}
	if expiryTime != nil {
		place.ExpiryTime = expiryTime
	}
	if correlationID != nil {
		place.CorrelationId = correlationID
	}

	req := &sequencerpb.EdgeSequencerRequest{
		Inner: &sequencerpb.EdgeSequencerRequest_Place{Place: place},
	}
	return proto.Marshal(req)
}

// BuildCancelOrderRequest serializes a CancelMessage wrapped in EdgeSequencerRequest.
func BuildCancelOrderRequest(orderID uint64, userUUID []byte, symbolID uint64, correlationID []byte) ([]byte, error) {
	cancel := &sequencerpb.CancelMessage{
		OrderId:        orderID,
		UserCommitment: make([]byte, 32),
		SymbolId:       symbolID,
		CorrelationId:  correlationID,
	}
	req := &sequencerpb.EdgeSequencerRequest{
		Inner: &sequencerpb.EdgeSequencerRequest_Cancel{Cancel: cancel},
	}
	return proto.Marshal(req)
}

// BuildModifyOrderRequest serializes a ModifyOrderInput wrapped in EdgeSequencerRequest.
func BuildModifyOrderRequest(
	orderID uint64,
	userUUID []byte,
	symbolID uint64,
	newPrice *float64,
	newQuantity *float64,
	correlationID []byte,
) ([]byte, error) {
	modify := &sequencerpb.ModifyOrderInput{
		OrderId:        orderID,
		UserCommitment: nil,
		SymbolId:       symbolID,
		CorrelationId:  correlationID,
		UserUuid:       userUUID,
	}
	if newPrice != nil {
		modify.NewPrice = newPrice
	}
	if newQuantity != nil {
		modify.NewQuantity = newQuantity
	}
	req := &sequencerpb.EdgeSequencerRequest{
		Inner: &sequencerpb.EdgeSequencerRequest_Modify{Modify: modify},
	}
	return proto.Marshal(req)
}

// BuildOrderHeaderAAD serializes an OrderHeader for use as AES-GCM AAD.
func BuildOrderHeaderAAD(userUUID []byte, symbolID uint64, requestType string, nonce uint64, bodyLength uint32, correlationID []byte) ([]byte, error) {
	reqInt, ok := requestTypeToProto[requestType]
	if !ok {
		return nil, fmt.Errorf("unknown request type: %q", requestType)
	}
	hdr := &edgepb.OrderHeader{
		UserUuid:      userUUID,
		SymbolId:      symbolID,
		RequestType:   commonpb.RequestType(reqInt),
		Nonce:         nonce,
		BodyLength:    bodyLength,
		CorrelationId: correlationID,
	}
	return proto.Marshal(hdr)
}

// BuildResponseHeaderAAD serializes a ResponseHeader for use as AES-GCM AAD.
func BuildResponseHeaderAAD(userUUID []byte, messageType string, bodyLength uint32, nonce uint64, fencingEpoch uint64) ([]byte, error) {
	msgInt, ok := responseMessageTypeToProto[messageType]
	if !ok {
		return nil, fmt.Errorf("unknown response message type: %q", messageType)
	}
	hdr := &edgepb.ResponseHeader{
		UserUuid:     userUUID,
		MessageType:  commonpb.ResponseMessageType(msgInt),
		BodyLength:   bodyLength,
		Nonce:        nonce,
		FencingEpoch: fencingEpoch,
	}
	return proto.Marshal(hdr)
}

// ---------------------------------------------------------------------------
// Parsers -- serialized protobuf bytes => public Go types
// ---------------------------------------------------------------------------

// NodeAck is the parsed form of a NodeResponse with an `ack` inner variant.
// Fill / Signing variants are intentionally not parsed in the trading path -
// the WS client only consumes acks here.
type NodeAck struct {
	NodeID        uint64
	Sequence      uint64
	OrderID       uint64
	Success       bool
	ErrorCode     *uint32
	CorrelationID []byte
	OrderStatus   *OrderStatus
	NodeHealth    *uint32
}

// ParseNodeResponseAck decodes a NodeResponse and returns its AckMessage
// projected into a NodeAck. Returns ok=false when the inner variant is not
// an ack.
func ParseNodeResponseAck(data []byte) (*NodeAck, bool, error) {
	var resp sequencerpb.NodeResponse
	if err := proto.Unmarshal(data, &resp); err != nil {
		return nil, false, err
	}
	inner, ok := resp.Inner.(*sequencerpb.NodeResponse_Ack)
	if !ok || inner.Ack == nil {
		return nil, false, nil
	}
	ack := inner.Ack
	out := &NodeAck{
		NodeID:        ack.NodeId,
		Sequence:      ack.Sequence,
		OrderID:       ack.OrderId,
		Success:       ack.Success,
		ErrorCode:     ack.ErrorCode,
		CorrelationID: ack.CorrelationId,
		NodeHealth:    ack.NodeHealth,
	}
	if ack.OrderStatus != nil {
		if s, ok := orderStatusEnumFromProto(int32(*ack.OrderStatus)); ok {
			sCopy := s
			out.OrderStatus = &sCopy
		}
	}
	return out, true, nil
}

// ParseOrderUpdate decodes an OrderUpdateMessage into the public OrderUpdate.
func ParseOrderUpdate(data []byte) (*OrderUpdate, error) {
	var msg sequencerpb.OrderUpdateMessage
	if err := proto.Unmarshal(data, &msg); err != nil {
		return nil, err
	}

	side, _ := sideEnumFromProto(int32(msg.Side))
	if side == "" {
		side = SideBuy
	}
	status, _ := orderStatusEnumFromProto(int32(msg.OrderStatus))
	if status == "" {
		status = OrderStatusNew
	}
	updateType, _ := orderUpdateTypeEnumFromProto(int32(msg.MessageType))
	if updateType == "" {
		updateType = OrderUpdateTypeOpen
	}

	var cancelReason CancelReason
	if msg.CancelReason != nil {
		if cr, ok := cancelReasonEnumFromProto(int32(*msg.CancelReason)); ok {
			cancelReason = cr
		}
	}

	var rejectReason string
	if msg.RejectReasonCode != nil {
		rejectReason = fmt.Sprintf("%d", *msg.RejectReasonCode)
	}

	out := &OrderUpdate{
		OrderID:       fmt.Sprintf("%d", msg.OrderId),
		UserUUID:      uuidBytesToString(msg.UserUuid),
		SymbolID:      int64(msg.SymbolId),
		Side:          side,
		Status:        status,
		UpdateType:    updateType,
		Price:         msg.Price,
		Quantity:      msg.Quantity,
		FilledQty:     msg.FilledQty,
		RemainingQty:  msg.RemainingQty,
		CumFill:       msg.CumFill,
		CancelReason:  cancelReason,
		RejectReason:  rejectReason,
		CorrelationID: correlationIDToUint64(msg.CorrelationId),
		Timestamp:     msg.Timestamp,
		Leverage:      int32(msg.Leverage),
	}
	if msg.RealizedPnl != nil {
		out.RealizedPnl = *msg.RealizedPnl
	}
	return out, nil
}

// ParsePositionUpdate decodes a PositionUpdateMessage into PositionUpdate.
func ParsePositionUpdate(data []byte) (*PositionUpdate, error) {
	var msg sequencerpb.PositionUpdateMessage
	if err := proto.Unmarshal(data, &msg); err != nil {
		return nil, err
	}
	side, _ := sideEnumFromProto(int32(msg.Side))
	if side == "" {
		side = SideBuy
	}
	ut, _ := positionUpdateTypeEnumFromProto(int32(msg.UpdateType))
	if ut == "" {
		ut = PositionUpdateTypeSnapshot
	}
	return &PositionUpdate{
		UserUUID:      uuidBytesToString(msg.UserUuid),
		SymbolID:      int64(msg.SymbolId),
		Side:          side,
		UpdateType:    ut,
		Size:          msg.Size,
		EntryPrice:    msg.EntryPrice,
		PreviousSize:  msg.PreviousSize,
		FillPrice:     stringOr(msg.FillPrice, ""),
		FillQty:       stringOr(msg.FillQty, ""),
		CorrelationID: correlationIDToUint64(msg.CorrelationId),
		Timestamp:     msg.Timestamp,
	}, nil
}

func parsePositionsSnapshotSource(v commonpb.PositionsSnapshotSource) PositionsSnapshotSource {
	switch v {
	case commonpb.PositionsSnapshotSource_POSITIONS_SNAPSHOT_SOURCE_INITIAL:
		return PositionsSnapshotSourceInitial
	case commonpb.PositionsSnapshotSource_POSITIONS_SNAPSHOT_SOURCE_PERIODIC:
		return PositionsSnapshotSourcePeriodic
	case commonpb.PositionsSnapshotSource_POSITIONS_SNAPSHOT_SOURCE_EVENT:
		return PositionsSnapshotSourceEvent
	}
	return PositionsSnapshotSourceUnspecified
}

func parseSettlementBatchStatus(v sequencerpb.SettlementBatchStatus) SettlementBatchStatus {
	switch v {
	case sequencerpb.SettlementBatchStatus_SETTLEMENT_BATCH_STATUS_SUBMITTED:
		return SettlementBatchStatusSubmitted
	case sequencerpb.SettlementBatchStatus_SETTLEMENT_BATCH_STATUS_CONFIRMED:
		return SettlementBatchStatusConfirmed
	case sequencerpb.SettlementBatchStatus_SETTLEMENT_BATCH_STATUS_FAILED:
		return SettlementBatchStatusFailed
	}
	return SettlementBatchStatusUnspecified
}

func parsePositionRow(row *sequencerpb.PositionRow) PositionRow {
	side, _ := sideEnumFromProto(int32(row.Side))
	if side == "" {
		side = SideBuy
	}
	out := PositionRow{
		SymbolID:   int64(row.SymbolId),
		Side:       side,
		Size:       row.Size,
		EntryPrice: row.EntryPrice,
		Leverage:   int32(row.Leverage),
	}
	if row.MarkPrice != nil {
		out.MarkPrice = *row.MarkPrice
	}
	if row.UnrealizedPnl != nil {
		out.UnrealizedPnl = *row.UnrealizedPnl
	}
	if row.Notional != nil {
		out.Notional = *row.Notional
	}
	if row.MarkPublishTimeSec != nil {
		out.MarkPublishTimeSec = *row.MarkPublishTimeSec
	}
	return out
}

// ParsePositionsSnapshot is exported because it consumes an already-decoded
// PositionsSnapshot proto (from a SequencerToEdgeMessage oneof). Callers that
// have raw bytes should use ParseSequencerToEdgeMessage and pattern-match the
// returned variant.
func ParsePositionsSnapshot(msg *sequencerpb.PositionsSnapshot) *PositionsSnapshot {
	rows := make([]PositionRow, len(msg.Rows))
	for i, r := range msg.Rows {
		rows[i] = parsePositionRow(r)
	}
	out := &PositionsSnapshot{
		UserUUID:        uuidBytesToString(msg.UserUuid),
		Rows:            rows,
		ServerTimestamp: msg.ServerTimestamp,
		Source:          parsePositionsSnapshotSource(msg.Source),
	}
	if len(msg.CorrelationId) > 0 {
		out.CorrelationID = correlationIDToUint64(msg.CorrelationId)
	}
	return out
}

// ParseSequencerToEdgeMessage is the umbrella push-frame parser. Decodes a
// SequencerToEdgeMessage and returns the appropriate public type based on the
// inner oneof variant. Unknown variants become *UnknownSequencerPush.
func ParseSequencerToEdgeMessage(data []byte) (SequencerPush, error) {
	var msg sequencerpb.SequencerToEdgeMessage
	if err := proto.Unmarshal(data, &msg); err != nil {
		return nil, err
	}

	switch inner := msg.Inner.(type) {
	case *sequencerpb.SequencerToEdgeMessage_OrderUpdate:
		// We re-serialize the inner and feed it back through the byte-level
		// parser to keep one canonical decode path. This is a micro-cost on
		// the order of a few hundred bytes per push.
		b, err := proto.Marshal(inner.OrderUpdate)
		if err != nil {
			return nil, err
		}
		return ParseOrderUpdate(b)
	case *sequencerpb.SequencerToEdgeMessage_PositionUpdate:
		b, err := proto.Marshal(inner.PositionUpdate)
		if err != nil {
			return nil, err
		}
		return ParsePositionUpdate(b)
	case *sequencerpb.SequencerToEdgeMessage_PositionsSnapshot:
		return ParsePositionsSnapshot(inner.PositionsSnapshot), nil
	case *sequencerpb.SequencerToEdgeMessage_SystemHealth:
		h := inner.SystemHealth
		return &SystemHealthUpdate{
			TotalNodes:      int32(h.TotalNodes),
			AcceptingOrders: h.AcceptingOrders,
			Ready:           int32(h.Ready),
			Degraded:        int32(h.Degraded),
			Exhausted:       int32(h.Exhausted),
			Warming:         int32(h.Warming),
			Draining:        int32(h.Draining),
			Waiting:         int32(h.Waiting),
		}, nil
	case *sequencerpb.SequencerToEdgeMessage_SettlementUpdate:
		s := inner.SettlementUpdate
		affected := make([]string, len(s.AffectedUserUuids))
		for i, raw := range s.AffectedUserUuids {
			affected[i] = uuidBytesToString(raw)
		}
		return &SettlementUpdate{
			BatchID:           s.BatchId,
			Status:            parseSettlementBatchStatus(s.Status),
			TxSignature:       s.TxSignature,
			Timestamp:         s.Timestamp,
			AffectedUserUUIDs: affected,
		}, nil
	case *sequencerpb.SequencerToEdgeMessage_FundingRateUpdate:
		f := inner.FundingRateUpdate
		return &FundingRateUpdate{
			SymbolID:        int64(f.SymbolId),
			CurrentRate:     f.CurrentRate,
			PredictedRate:   f.PredictedRate,
			NextFundingTime: f.NextFundingTime,
			Timestamp:       f.Timestamp,
		}, nil
	case *sequencerpb.SequencerToEdgeMessage_BalanceUpdate:
		b := inner.BalanceUpdate
		return &BalanceUpdate{
			UserUUID:           uuidBytesToString(b.UserUuid),
			ShieldedBalanceRaw: b.ShieldedBalanceRaw,
			Timestamp:          b.Timestamp,
		}, nil
	case *sequencerpb.SequencerToEdgeMessage_MarginAlert:
		m := inner.MarginAlert
		return &MarginAlert{
			Owner:               uuidBytesToString(m.Owner),
			SymbolID:            int64(m.SymbolId),
			Tier:                int32(m.Tier),
			MarginRatioBps:      int64(m.MarginRatioBps),
			MarkPriceBps:        int64(m.MarkPriceBps),
			LiquidationPriceBps: int64(m.LiquidationPriceBps),
			TS:                  uint64(m.Ts),
			StateVersion:        m.StateVersion,
			Recovered:           m.Recovered,
		}, nil
	default:
		// New variants (OrderHistoryInsert, OpenInterestUpdate, VolumeUpdate,
		// future additions) fall through here.
		return &UnknownSequencerPush{OneofField: fmt.Sprintf("%T", msg.Inner)}, nil
	}
}

// _ keeps math/big import used (correlation_id helpers are exported via
// correlationIDBigInt for callers that need the full u128 view).
var _ = big.NewInt
