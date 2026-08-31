package godark

// Bridge between clean public Go types and serialized protobuf messages.
//
// The generated bindings under `proto/gdx/...` are kept untouched; everything
// the WS / REST clients actually call lives here so we have a single place
// to maintain wire compatibility with the python / rust / js SDKs.

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

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
func (*LeverageSettings) isSequencerPush()     {}
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

// correlationIDBodyBytes converts canonical big-endian u128 bytes to the
// little-endian encoding used by correlation_id fields in sequencer messages.
func correlationIDBodyBytes(raw []byte) []byte {
	if len(raw) == 0 {
		return nil
	}
	out := make([]byte, len(raw))
	for i := range raw {
		out[len(raw)-1-i] = raw[i]
	}
	return out
}

// correlationIDFromWire parses the decimal u128 emitted by edge JSON responses
// and returns the canonical 16-byte big-endian representation used in AAD.
func correlationIDFromWire(v any) []byte {
	var text string
	switch value := v.(type) {
	case string:
		text = strings.TrimSpace(value)
	case float64:
		text = fmt.Sprintf("%.0f", value)
	case uint64:
		text = fmt.Sprintf("%d", value)
	case int64:
		if value < 0 {
			return nil
		}
		text = fmt.Sprintf("%d", value)
	default:
		return nil
	}
	if text == "" {
		return nil
	}
	if len(text) == 32 {
		if b, err := hex.DecodeString(text); err == nil && len(b) == 16 {
			return b
		}
	}
	n, ok := new(big.Int).SetString(text, 10)
	if !ok || n.Sign() <= 0 || n.BitLen() > 128 {
		return nil
	}
	raw := n.Bytes()
	out := make([]byte, 16)
	copy(out[16-len(raw):], raw)
	return out
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
	_ uint64,
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
	if aon && minFillSize == nil {
		q := quantity
		minFillSize = &q
	}

	place := &sequencerpb.PlaceOrderInput{
		SymbolId:    symbolID,
		Side:        commonpb.Side(sideInt),
		OrderType:   commonpb.OrderType(otypeInt),
		Quantity:    quantity,
		TimeInForce: commonpb.TimeInForce(tifInt),
		UserUuid:    userUUID,
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
		place.CorrelationId = correlationIDBodyBytes(correlationID)
	}

	req := &sequencerpb.EdgeSequencerRequest{
		Inner: &sequencerpb.EdgeSequencerRequest_Place{Place: place},
	}
	return proto.Marshal(req)
}

// BuildCancelOrderRequest serializes a CancelOrderInput wrapped in EdgeSequencerRequest.
func BuildCancelOrderRequest(orderID uint64, userUUID []byte, symbolID uint64, correlationID []byte) ([]byte, error) {
	cancel := &sequencerpb.CancelOrderInput{
		OrderId:       orderID,
		SymbolId:      symbolID,
		CorrelationId: correlationIDBodyBytes(correlationID),
		UserUuid:      userUUID,
	}
	req := &sequencerpb.EdgeSequencerRequest{
		Inner: &sequencerpb.EdgeSequencerRequest_Cancel{Cancel: cancel},
	}
	return proto.Marshal(req)
}

// BuildUpdateLeverageRequest serializes an UpdateLeverageRequest wrapped in
// EdgeSequencerRequest (field 19). Leverage is clamped to max(1, floor(value)).
func BuildUpdateLeverageRequest(userUUID []byte, symbolID uint64, leverage int, correlationID []byte) ([]byte, error) {
	lev := leverage
	if lev < 1 {
		lev = 1
	}
	ul := &sequencerpb.UpdateLeverageRequest{
		UserUuid:      userUUID,
		SymbolId:      symbolID,
		Leverage:      uint32(lev),
		CorrelationId: correlationIDBodyBytes(correlationID),
	}
	req := &sequencerpb.EdgeSequencerRequest{
		Inner: &sequencerpb.EdgeSequencerRequest_UpdateLeverage{UpdateLeverage: ul},
	}
	return proto.Marshal(req)
}

// BuildGetOpenOrdersRequest serializes a GetOpenOrdersRequest wrapped in
// EdgeSequencerRequest.
func BuildGetOpenOrdersRequest(userUUID, correlationID []byte) ([]byte, error) {
	inner := &sequencerpb.GetOpenOrdersRequest{
		UserUuid:      userUUID,
		CorrelationId: correlationIDBodyBytes(correlationID),
	}
	req := &sequencerpb.EdgeSequencerRequest{
		Inner: &sequencerpb.EdgeSequencerRequest_GetOpenOrders{GetOpenOrders: inner},
	}
	return proto.Marshal(req)
}

// BuildGetPositionsRequest serializes a GetPositionsRequest wrapped in
// EdgeSequencerRequest.
func BuildGetPositionsRequest(userUUID, correlationID []byte) ([]byte, error) {
	inner := &sequencerpb.GetPositionsRequest{
		UserUuid:      userUUID,
		CorrelationId: correlationIDBodyBytes(correlationID),
	}
	req := &sequencerpb.EdgeSequencerRequest{
		Inner: &sequencerpb.EdgeSequencerRequest_GetPositions{GetPositions: inner},
	}
	return proto.Marshal(req)
}

// BuildGetAccountRequest serializes a GetAccountRequest wrapped in
// EdgeSequencerRequest.
func BuildGetAccountRequest(userUUID, correlationID []byte) ([]byte, error) {
	inner := &sequencerpb.GetAccountRequest{
		UserUuid:      userUUID,
		CorrelationId: correlationIDBodyBytes(correlationID),
	}
	req := &sequencerpb.EdgeSequencerRequest{
		Inner: &sequencerpb.EdgeSequencerRequest_GetAccount{GetAccount: inner},
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
		OrderId:       orderID,
		SymbolId:      symbolID,
		CorrelationId: correlationIDBodyBytes(correlationID),
		UserUuid:      userUUID,
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

func newLegCorrelationID() []byte {
	u := uuid.New()
	return u[:]
}

// maxBatchLegs is the maximum number of legs / ids accepted in a single
// mass-quote, batch-cancel, or batch-modify request. The node fans batches out
// at ~constant MPC cost only up to this bound; larger batches are rejected
// client-side before reaching the wire.
const maxBatchLegs = 20

// BuildMassQuoteRequest serializes a MassQuoteInput (bulk cancel-replace)
// wrapped in EdgeSequencerRequest. Each leg becomes its own order and carries a
// unique 16-byte correlation id (the wire requires exactly 16 bytes per leg).
// postOnly is the batch-level post-only flag: nil defaults to true on the wire;
// false enables the relaxed path where a crossing leg takes liquidity up to its
// limit and rests the remainder instead of being rejected.
func BuildMassQuoteRequest(symbolID uint64, userUUID []byte, legs []MassQuoteLegInput, correlationID []byte, postOnly *bool) ([]byte, error) {
	if len(legs) == 0 {
		return nil, fmt.Errorf("mass quote requires at least one leg")
	}
	if len(legs) > maxBatchLegs {
		return nil, fmt.Errorf("mass quote accepts at most %d legs, got %d", maxBatchLegs, len(legs))
	}
	pbLegs := make([]*sequencerpb.MassQuoteLeg, 0, len(legs))
	for i, leg := range legs {
		sideInt, ok := sideToProto[leg.Side]
		if !ok {
			return nil, fmt.Errorf("mass quote leg %d: unknown side: %q", i, leg.Side)
		}
		tif := leg.TimeInForce
		if tif == "" {
			tif = TimeInForceGTC
		}
		tifInt, ok := timeInForceToProto[tif]
		if !ok {
			return nil, fmt.Errorf("mass quote leg %d: unknown time in force: %q", i, tif)
		}
		if tif == TimeInForceGTD && leg.ExpiryTime == nil {
			return nil, fmt.Errorf("mass quote leg %d: ExpiryTime is required when TimeInForce=GTD", i)
		}
		var cancelID uint64
		if leg.CancelOrderID != nil {
			cancelID = *leg.CancelOrderID
		}
		pbLeg := &sequencerpb.MassQuoteLeg{
			CancelOrderId: cancelID,
			Side:          commonpb.Side(sideInt),
			Price:         leg.Price,
			Quantity:      leg.Quantity,
			TimeInForce:   commonpb.TimeInForce(tifInt),
			CorrelationId: newLegCorrelationID(),
		}
		if leg.ExpiryTime != nil {
			pbLeg.ExpiryTime = leg.ExpiryTime
		}
		pbLegs = append(pbLegs, pbLeg)
	}
	postOnlyVal := true
	if postOnly != nil {
		postOnlyVal = *postOnly
	}
	mq := &sequencerpb.MassQuoteInput{
		SymbolId:      symbolID,
		Legs:          pbLegs,
		UserUuid:      userUUID,
		CorrelationId: correlationIDBodyBytes(correlationID),
		PostOnly:      &postOnlyVal,
	}
	req := &sequencerpb.EdgeSequencerRequest{
		Inner: &sequencerpb.EdgeSequencerRequest_MassQuote{MassQuote: mq},
	}
	return proto.Marshal(req)
}

// BuildBatchCancelRequest serializes a BatchCancelInput (cancel up to 20 resting
// orders on one symbol) wrapped in EdgeSequencerRequest.
func BuildBatchCancelRequest(symbolID uint64, userUUID []byte, orderIDs []uint64, correlationID []byte) ([]byte, error) {
	if len(orderIDs) == 0 {
		return nil, fmt.Errorf("batch cancel requires at least one order id")
	}
	if len(orderIDs) > maxBatchLegs {
		return nil, fmt.Errorf("batch cancel accepts at most %d ids, got %d", maxBatchLegs, len(orderIDs))
	}
	bc := &sequencerpb.BatchCancelInput{
		SymbolId:      symbolID,
		OrderIds:      orderIDs,
		UserUuid:      userUUID,
		CorrelationId: correlationIDBodyBytes(correlationID),
	}
	req := &sequencerpb.EdgeSequencerRequest{
		Inner: &sequencerpb.EdgeSequencerRequest_BatchCancel{BatchCancel: bc},
	}
	return proto.Marshal(req)
}

// BuildBatchModifyRequest serializes a BatchModifyInput (post-only amend up to
// 20 resting orders on one symbol) wrapped in EdgeSequencerRequest. Each leg
// carries a unique 16-byte correlation id.
func BuildBatchModifyRequest(symbolID uint64, userUUID []byte, legs []BatchModifyLegInput, correlationID []byte) ([]byte, error) {
	if len(legs) == 0 {
		return nil, fmt.Errorf("batch modify requires at least one leg")
	}
	if len(legs) > maxBatchLegs {
		return nil, fmt.Errorf("batch modify accepts at most %d legs, got %d", maxBatchLegs, len(legs))
	}
	for i, leg := range legs {
		if leg.NewPrice == nil && leg.NewQuantity == nil {
			return nil, fmt.Errorf("batch modify leg %d must set NewPrice and/or NewQuantity", i)
		}
	}
	pbLegs := make([]*sequencerpb.BatchModifyLeg, 0, len(legs))
	for _, leg := range legs {
		pbLeg := &sequencerpb.BatchModifyLeg{
			OrderId:       leg.OrderID,
			CorrelationId: newLegCorrelationID(),
		}
		if leg.NewPrice != nil {
			pbLeg.NewPrice = leg.NewPrice
		}
		if leg.NewQuantity != nil {
			pbLeg.NewQuantity = leg.NewQuantity
		}
		pbLegs = append(pbLegs, pbLeg)
	}
	bm := &sequencerpb.BatchModifyInput{
		SymbolId:      symbolID,
		Legs:          pbLegs,
		UserUuid:      userUUID,
		CorrelationId: correlationIDBodyBytes(correlationID),
	}
	req := &sequencerpb.EdgeSequencerRequest{
		Inner: &sequencerpb.EdgeSequencerRequest_BatchModify{BatchModify: bm},
	}
	return proto.Marshal(req)
}

// BuildOrderHeaderAAD serializes an OrderHeader for use as AES-GCM AAD.
func BuildOrderHeaderAAD(userUUID []byte, symbolID uint64, requestType string, nonce uint64, bodyLength uint32, correlationID []byte) ([]byte, error) {
	return BuildOrderHeaderAADWithConn(userUUID, symbolID, requestType, nonce, bodyLength, correlationID, 0)
}

// BuildOrderHeaderAADWithConn includes the Noise-bound WebSocket conn_id.
func BuildOrderHeaderAADWithConn(userUUID []byte, symbolID uint64, requestType string, nonce uint64, bodyLength uint32, correlationID []byte, connID uint64) ([]byte, error) {
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
		ConnId:        connID,
	}
	return proto.Marshal(hdr)
}

// BuildResponseHeaderAAD serializes a ResponseHeader for use as AES-GCM AAD.
func BuildResponseHeaderAAD(userUUID []byte, messageType string, bodyLength uint32, nonce uint64, fencingEpoch uint64, correlationID []byte, sessionSeq uint64) ([]byte, error) {
	return BuildResponseHeaderAADWithConn(userUUID, messageType, bodyLength, nonce, fencingEpoch, correlationID, sessionSeq, 0)
}

// BuildResponseHeaderAADWithConn includes the Noise-bound WebSocket conn_id.
func BuildResponseHeaderAADWithConn(userUUID []byte, messageType string, bodyLength uint32, nonce uint64, fencingEpoch uint64, correlationID []byte, sessionSeq uint64, connID uint64) ([]byte, error) {
	msgInt, ok := responseMessageTypeToProto[messageType]
	if !ok {
		return nil, fmt.Errorf("unknown response message type: %q", messageType)
	}
	hdr := &edgepb.ResponseHeader{
		UserUuid:      userUUID,
		MessageType:   commonpb.ResponseMessageType(msgInt),
		BodyLength:    bodyLength,
		Nonce:         nonce,
		FencingEpoch:  fencingEpoch,
		CorrelationId: correlationID,
		SessionSeq:    sessionSeq,
		ConnId:        connID,
	}
	return proto.Marshal(hdr)
}

// ---------------------------------------------------------------------------
// Legacy NodeResponse wire unwrap (pre hotpath-edge-frames REST replies)
// ---------------------------------------------------------------------------

const wireTypeLengthDelimited = 2

var legacyNodeResponseFieldNum = map[string]uint32{
	"ack":                   1,
	"fill":                  2,
	"open_orders_snapshot":  3,
	"node_ready":            4,
	"mass_quote_ack":        5,
	"batch_cancel_ack":      6,
	"batch_modify_ack":      7,
	"positions_snapshot":    8,
	"account_margin_update": 9,
	"cancel_all_ack":        10,
	"close_all_ack":         11,
	"reverse_ack":           12,
}

var legacyNodeResponseFieldName = map[uint32]string{
	1:  "ack",
	2:  "fill",
	3:  "open_orders_snapshot",
	4:  "node_ready",
	5:  "mass_quote_ack",
	6:  "batch_cancel_ack",
	7:  "batch_modify_ack",
	8:  "positions_snapshot",
	9:  "account_margin_update",
	10: "cancel_all_ack",
	11: "close_all_ack",
	12: "reverse_ack",
}

func readVarint(data []byte, i int) (uint64, int, error) {
	var shift uint
	var result uint64
	for i < len(data) {
		b := data[i]
		i++
		result |= uint64(b&0x7F) << shift
		if b&0x80 == 0 {
			return result, i, nil
		}
		shift += 7
		if shift >= 64 {
			return 0, i, fmt.Errorf("varint overflow")
		}
	}
	return 0, i, fmt.Errorf("truncated varint")
}

func writeVarint(value uint64) []byte {
	var out []byte
	for {
		b := byte(value & 0x7F)
		value >>= 7
		if value != 0 {
			b |= 0x80
		}
		out = append(out, b)
		if value == 0 {
			break
		}
	}
	return out
}

// WrapLegacyNodeResponse wraps inner bytes as a legacy NodeResponse oneof (tests/mocks).
func WrapLegacyNodeResponse(variant string, inner []byte) []byte {
	fieldNum, ok := legacyNodeResponseFieldNum[variant]
	if !ok {
		panic(fmt.Sprintf("unknown legacy NodeResponse variant: %s", variant))
	}
	out := []byte{byte((fieldNum << 3) | wireTypeLengthDelimited)}
	out = append(out, writeVarint(uint64(len(inner)))...)
	return append(out, inner...)
}

func unwrapLegacyNodeResponse(data []byte) (variant string, inner []byte, ok bool) {
	if len(data) == 0 {
		return "", nil, false
	}
	tag := data[0]
	wireType := tag & 0x07
	fieldNum := uint32(tag >> 3)
	variant, ok = legacyNodeResponseFieldName[fieldNum]
	if !ok || wireType != wireTypeLengthDelimited {
		return "", nil, false
	}
	length, i, err := readVarint(data, 1)
	if err != nil {
		return "", nil, false
	}
	end := i + int(length)
	if end != len(data) {
		return "", nil, false
	}
	return variant, data[i:end], true
}

func normalizeExpectedSnapshotVariant(messageType string) string {
	switch strings.ReplaceAll(messageType, "-", "_") {
	case "account_margin", "account_update":
		return "account_margin_update"
	default:
		return strings.ReplaceAll(messageType, "-", "_")
	}
}

func resolveRestPayload(data []byte, expected string) (variant string, payload []byte) {
	if v, inner, ok := unwrapLegacyNodeResponse(data); ok {
		return v, inner
	}
	if expected != "" {
		return expected, data
	}
	return "ack", data
}

func nodeAckFromProto(ack *sequencerpb.AckMessage) *NodeAck {
	out := &NodeAck{
		Sequence:      ack.Sequence,
		OrderID:       ack.OrderId,
		RejectText:    ack.GetRejectText(),
		CorrelationID: ack.CorrelationId,
	}
	if outcome := ack.GetAckOutcome(); outcome != nil {
		out.Success = outcome.GetKind() == sequencerpb.AckOutcomeKind_ACK_OUTCOME_KIND_APPLIED
		if code := outcome.GetBusinessErrorCode(); code != 0 {
			out.ErrorCode = &code
			out.Success = false
		} else if code := outcome.GetSystemErrorCode(); code != 0 {
			out.ErrorCode = &code
			out.Success = false
		}
		if outcome.OrderStatus != nil {
			if s, ok := orderStatusEnumFromProto(int32(*outcome.OrderStatus)); ok {
				sCopy := s
				out.OrderStatus = &sCopy
			}
		}
	} else {
		out.Success = false
	}
	return out
}

// ---------------------------------------------------------------------------
// Parsers -- serialized protobuf bytes => public Go types
// ---------------------------------------------------------------------------

// NodeAck is the parsed form of a NodeResponse with an `ack` inner variant.
// Fill / Signing variants are intentionally not parsed in the trading path -
// the WS client only consumes acks here.
type NodeAck struct {
	Sequence  uint64
	OrderID   uint64
	Success   bool
	ErrorCode *uint32
	// RejectText is AckMessage.reject_text when the sequencer supplied detail.
	RejectText    string
	CorrelationID []byte
	OrderStatus   *OrderStatus
}

// ParseNodeResponseAck decodes REST/WS ack plaintext (direct AckMessage or legacy wrapper).
func ParseNodeResponseAck(data []byte) (*NodeAck, bool, error) {
	variant, payload := resolveRestPayload(data, "ack")
	if variant != "ack" {
		return nil, false, nil
	}
	var ack sequencerpb.AckMessage
	if err := proto.Unmarshal(payload, &ack); err != nil {
		return nil, false, err
	}
	return nodeAckFromProto(&ack), true, nil
}

func massQuoteLegStatusString(s sequencerpb.MassQuoteLegStatus) string {
	switch s {
	case sequencerpb.MassQuoteLegStatus_MASS_QUOTE_LEG_STATUS_OPEN:
		return "open"
	case sequencerpb.MassQuoteLegStatus_MASS_QUOTE_LEG_STATUS_FILLED:
		return "filled"
	case sequencerpb.MassQuoteLegStatus_MASS_QUOTE_LEG_STATUS_FAILED:
		return "failed"
	case sequencerpb.MassQuoteLegStatus_MASS_QUOTE_LEG_STATUS_UNSPECIFIED:
		return "unspecified"
	default:
		return "unknown"
	}
}

func uint64OrEmpty(v uint64) string {
	if v == 0 {
		return ""
	}
	return fmt.Sprintf("%d", v)
}

// ParseMassQuoteAck decodes a MassQuoteAck (legacy NodeResponse wrapper or direct message).
func ParseMassQuoteAck(data []byte) (*MassQuoteAck, bool, error) {
	variant, payload := resolveRestPayload(data, "mass_quote_ack")
	if variant != "mass_quote_ack" {
		return nil, false, nil
	}
	var a sequencerpb.MassQuoteAck
	if err := proto.Unmarshal(payload, &a); err != nil {
		return nil, false, err
	}
	results := make([]MassQuoteLegResult, 0, len(a.Results))
	success := len(a.Results) > 0
	for _, r := range a.Results {
		status := massQuoteLegStatusString(r.Status)
		if status == "failed" {
			success = false
		}
		results = append(results, MassQuoteLegResult{
			LegIndex:         r.LegIndex,
			Status:           status,
			CancelledOrderID: uint64OrEmpty(r.CancelledOrderId),
			NewOrderID:       uint64OrEmpty(r.NewOrderId),
			ErrorCode:        r.ErrorCode,
			FillCount:        r.FillCount,
		})
	}
	return &MassQuoteAck{
		Success:  success,
		Sequence: fmt.Sprintf("%d", a.Sequence),
		Results:  results,
	}, true, nil
}

// ParseBatchCancelAck decodes a BatchCancelAck (legacy wrapper or direct message).
func ParseBatchCancelAck(data []byte) (*BatchCancelAck, bool, error) {
	variant, payload := resolveRestPayload(data, "batch_cancel_ack")
	if variant != "batch_cancel_ack" {
		return nil, false, nil
	}
	var a sequencerpb.BatchCancelAck
	if err := proto.Unmarshal(payload, &a); err != nil {
		return nil, false, err
	}
	results := make([]BatchCancelLegResult, 0, len(a.Results))
	success := len(a.Results) > 0
	for _, r := range a.Results {
		if !r.Cancelled {
			success = false
		}
		results = append(results, BatchCancelLegResult{
			OrderID:   fmt.Sprintf("%d", r.OrderId),
			Cancelled: r.Cancelled,
			ErrorCode: r.ErrorCode,
		})
	}
	return &BatchCancelAck{
		Success:  success,
		Sequence: fmt.Sprintf("%d", a.Sequence),
		Results:  results,
	}, true, nil
}

// ParseOpenOrdersSnapshot decodes an OpenOrdersSnapshot (legacy wrapper or direct).
func ParseOpenOrdersSnapshot(data []byte) (*OpenOrdersSnapshot, error) {
	variant, payload := resolveRestPayload(data, "open_orders_snapshot")
	if variant != "open_orders_snapshot" {
		return nil, fmt.Errorf("expected open_orders_snapshot, got %s", variant)
	}
	var s sequencerpb.OpenOrdersSnapshot
	if err := proto.Unmarshal(payload, &s); err != nil {
		return nil, err
	}
	rows := make([]OpenOrderRow, 0, len(s.Rows))
	for _, r := range s.Rows {
		if r == nil {
			continue
		}
		rows = append(rows, OpenOrderRow{
			OrderID:      fmt.Sprintf("%d", r.OrderId),
			SymbolID:     int64(r.SymbolId),
			Leverage:     int32(r.Leverage),
			Price:        r.Price,
			Quantity:     r.Quantity,
			RemainingQty: r.RemainingQty,
		})
	}
	return &OpenOrdersSnapshot{
		Rows:            rows,
		ServerTimestamp: s.ServerTimestamp,
		CorrelationID:   correlationIDToUint64(s.CorrelationId),
	}, nil
}

// ParseAccountMarginUpdate maps a sequencer AccountMarginUpdate proto to the
// public snapshot type.
func ParseAccountMarginUpdate(msg *sequencerpb.AccountMarginUpdate) *AccountMarginUpdate {
	if msg == nil {
		return &AccountMarginUpdate{}
	}
	out := &AccountMarginUpdate{
		UserUUID:        uuidBytesToString(msg.UserUuid),
		ServerTimestamp: msg.ServerTimestamp,
		CorrelationID:   correlationIDToUint64(msg.CorrelationId),
	}
	if a := msg.Account; a != nil {
		out.Account = &AccountMarginSummary{
			TotalCollateral:     a.TotalCollateral,
			PositionMargin:      a.PositionMargin,
			ReservedOrderMargin: a.ReservedOrderMargin,
			FreeCollateral:      a.FreeCollateral,
			AccountEquity:       a.AccountEquity,
			UnrealizedPnl:       a.UnrealizedPnl,
			CrossAvailable:      a.CrossAvailable,
			RealizedPnl:         a.RealizedPnl,
		}
	}
	return out
}

// ParsePositionsSnapshotFromNodeResponse decodes positions snapshot REST plaintext.
func ParsePositionsSnapshotFromNodeResponse(data []byte) (*PositionsSnapshot, error) {
	variant, payload := resolveRestPayload(data, "positions_snapshot")
	if variant != "positions_snapshot" {
		return nil, fmt.Errorf("expected positions_snapshot, got %s", variant)
	}
	var s sequencerpb.PositionsSnapshot
	if err := proto.Unmarshal(payload, &s); err != nil {
		return nil, err
	}
	return ParsePositionsSnapshot(&s), nil
}

// NodeResponseVariant is the parsed union of synchronous NodeResponse replies.
type NodeResponseVariant struct {
	Kind          string
	Ack           *NodeAck
	OpenOrders    *OpenOrdersSnapshot
	Positions     *PositionsSnapshot
	AccountMargin *AccountMarginUpdate
	MassQuote     *MassQuoteAck
	BatchCancel   *BatchCancelAck
	BatchModify   *BatchModifyAck
}

// ParseNodeResponseVariant decodes REST snapshot/ack plaintext into a public variant.
func ParseNodeResponseVariant(data []byte, messageType ...string) (*NodeResponseVariant, error) {
	expected := ""
	if len(messageType) > 0 {
		expected = normalizeExpectedSnapshotVariant(messageType[0])
	}
	variant, payload := resolveRestPayload(data, expected)
	switch variant {
	case "ack":
		var ack sequencerpb.AckMessage
		if err := proto.Unmarshal(payload, &ack); err != nil {
			return nil, err
		}
		return &NodeResponseVariant{Kind: "ack", Ack: nodeAckFromProto(&ack)}, nil
	case "open_orders_snapshot":
		snap, err := ParseOpenOrdersSnapshot(data)
		if err != nil {
			return nil, err
		}
		return &NodeResponseVariant{Kind: "open_orders_snapshot", OpenOrders: snap}, nil
	case "positions_snapshot":
		var s sequencerpb.PositionsSnapshot
		if err := proto.Unmarshal(payload, &s); err != nil {
			return nil, err
		}
		return &NodeResponseVariant{
			Kind:      "positions_snapshot",
			Positions: ParsePositionsSnapshot(&s),
		}, nil
	case "account_margin_update", "account_update":
		var s sequencerpb.AccountMarginUpdate
		if err := proto.Unmarshal(payload, &s); err != nil {
			return nil, err
		}
		return &NodeResponseVariant{
			Kind:          "account_margin_update",
			AccountMargin: ParseAccountMarginUpdate(&s),
		}, nil
	case "mass_quote_ack":
		ack, ok, err := ParseMassQuoteAck(data)
		if err != nil {
			return nil, err
		}
		if !ok {
			return &NodeResponseVariant{Kind: "mass_quote_ack"}, nil
		}
		return &NodeResponseVariant{Kind: "mass_quote_ack", MassQuote: ack}, nil
	case "batch_cancel_ack":
		ack, ok, err := ParseBatchCancelAck(data)
		if err != nil {
			return nil, err
		}
		if !ok {
			return &NodeResponseVariant{Kind: "batch_cancel_ack"}, nil
		}
		return &NodeResponseVariant{Kind: "batch_cancel_ack", BatchCancel: ack}, nil
	case "batch_modify_ack":
		ack, ok, err := ParseBatchModifyAck(data)
		if err != nil {
			return nil, err
		}
		if !ok {
			return &NodeResponseVariant{Kind: "batch_modify_ack"}, nil
		}
		return &NodeResponseVariant{Kind: "batch_modify_ack", BatchModify: ack}, nil
	default:
		return &NodeResponseVariant{Kind: variant}, nil
	}
}

// ParseBatchModifyAck decodes a BatchModifyAck (legacy wrapper or direct message).
func ParseBatchModifyAck(data []byte) (*BatchModifyAck, bool, error) {
	variant, payload := resolveRestPayload(data, "batch_modify_ack")
	if variant != "batch_modify_ack" {
		return nil, false, nil
	}
	var a sequencerpb.BatchModifyAck
	if err := proto.Unmarshal(payload, &a); err != nil {
		return nil, false, err
	}
	results := make([]BatchModifyLegResult, 0, len(a.Results))
	success := len(a.Results) > 0
	for _, r := range a.Results {
		if !r.Modified {
			success = false
		}
		results = append(results, BatchModifyLegResult{
			OrderID:   fmt.Sprintf("%d", r.OrderId),
			Modified:  r.Modified,
			ErrorCode: r.ErrorCode,
		})
	}
	return &BatchModifyAck{
		Success:  success,
		Sequence: fmt.Sprintf("%d", a.Sequence),
		Results:  results,
	}, true, nil
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
		Msg:           msg.GetMsg(),
		ReduceOnly:    msg.ReduceOnly,
		PostOnly:      msg.PostOnly,
		CorrelationID: correlationIDToUint64(msg.CorrelationId),
		Timestamp:     msg.Timestamp,
		Leverage:      int32(msg.Leverage),
	}
	if msg.RealizedPnl != nil {
		out.RealizedPnl = *msg.RealizedPnl
	}
	return out, nil
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

// ParseLeverageSettings converts a sequencer LeverageSettings proto into the
// public snapshot type.
func ParseLeverageSettings(msg *sequencerpb.LeverageSettings) *LeverageSettings {
	settings := make([]LeverageSetting, len(msg.Settings))
	for i, row := range msg.Settings {
		settings[i] = LeverageSetting{
			SymbolID: int64(row.SymbolId),
			Leverage: int32(row.Leverage),
		}
	}
	return &LeverageSettings{
		UserUUID:        uuidBytesToString(msg.UserUuid),
		Settings:        settings,
		ServerTimestamp: msg.ServerTimestamp,
	}
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
	case *sequencerpb.SequencerToEdgeMessage_PositionsSnapshot:
		return ParsePositionsSnapshot(inner.PositionsSnapshot), nil
	case *sequencerpb.SequencerToEdgeMessage_HealthReport:
		h := inner.HealthReport
		return &SystemHealthUpdate{
			ComponentID:    h.ComponentId,
			State:          int32(h.State),
			Serving:        h.Serving,
			Cause:          h.Cause,
			UpdatedAtNanos: h.UpdatedAtNanos,
			Sequence:       h.Sequence,
			SchemaVersion:  h.SchemaVersion,
		}, nil
	case *sequencerpb.SequencerToEdgeMessage_FundingRateUpdate:
		f := inner.FundingRateUpdate
		return &FundingRateUpdate{
			SymbolID:        int64(f.SymbolId),
			FundingRate:     f.FundingRate,
			Timestamp:       f.Timestamp,
			LastFundingRate: f.LastFundingRate,
		}, nil
	case *sequencerpb.SequencerToEdgeMessage_BalanceUpdate:
		b := inner.BalanceUpdate
		return &BalanceUpdate{
			UserUUID:          uuidBytesToString(b.UserUuid),
			BalanceRaw:        b.BalanceRaw,
			Timestamp:         b.Timestamp,
			Balance:           b.Balance,
			SignedBalance8dp:  b.SignedBalance_8Dp,
			FreeCollateral8dp: b.FreeCollateral_8Dp,
		}, nil
	case *sequencerpb.SequencerToEdgeMessage_LeverageSettings:
		return ParseLeverageSettings(inner.LeverageSettings), nil
	default:
		// New variants (OrderHistoryInsert, OpenInterestUpdate,
		// BalanceAndPosition, future additions) fall through here.
		return &UnknownSequencerPush{OneofField: fmt.Sprintf("%T", msg.Inner)}, nil
	}
}

// _ keeps math/big import used (correlation_id helpers are exported via
// correlationIDBigInt for callers that need the full u128 view).
var _ = big.NewInt

// ParseFundingRateSnapshotJSON decodes public WS funding_rate_snapshot rows.
func ParseFundingRateSnapshotJSON(obj map[string]any) []*FundingRateUpdate {
	typ, _ := obj["type"].(string)
	if typ != "funding_rate_snapshot" {
		return nil
	}
	rows, _ := obj["rows"].([]any)
	out := make([]*FundingRateUpdate, 0, len(rows))
	for _, row := range rows {
		m, ok := row.(map[string]any)
		if !ok {
			continue
		}
		rate, _ := m["funding_rate"].(string)
		if rate == "" {
			continue
		}
		last, _ := m["last_funding_rate"].(string)
		out = append(out, &FundingRateUpdate{
			SymbolID:        fundingSnapshotSymbolID(m["symbol_id"]),
			FundingRate:     rate,
			LastFundingRate: last,
			Timestamp:       uint64(coerceUint64(m["timestamp"])),
		})
	}
	return out
}

func fundingSnapshotSymbolID(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	default:
		return 0
	}
}
