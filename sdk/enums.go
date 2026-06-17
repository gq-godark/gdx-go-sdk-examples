package godark

// String-typed enums match python / js / java; the underlying string is the
// stable wire value used in non-proto contexts (JSON acks, REST payloads).
// Each enum carries a *FromProto / *ToProto helper for the protobuf int paths.

type Side string

const (
	SideBuy  Side = "BUY"
	SideSell Side = "SELL"
)

type OrderType string

const (
	OrderTypeMarket   OrderType = "MARKET"
	OrderTypeLimit    OrderType = "LIMIT"
	OrderTypePegToMid OrderType = "PEG_TO_MID"
	OrderTypePegToBid OrderType = "PEG_TO_BID"
	OrderTypePegToAsk OrderType = "PEG_TO_ASK"
)

type TimeInForce string

const (
	TimeInForceGTC TimeInForce = "GTC"
	TimeInForceIOC TimeInForce = "IOC"
	TimeInForceFOK TimeInForce = "FOK"
	TimeInForceGTD TimeInForce = "GTD"
)

type OrderStatus string

const (
	OrderStatusNew             OrderStatus = "NEW"
	OrderStatusPartiallyFilled OrderStatus = "PARTIALLY_FILLED"
	OrderStatusFilled          OrderStatus = "FILLED"
	OrderStatusCancelled       OrderStatus = "CANCELLED"
	OrderStatusRejected        OrderStatus = "REJECTED"
)

type OrderUpdateType string

const (
	OrderUpdateTypeOpen            OrderUpdateType = "OPEN"
	OrderUpdateTypeFilled          OrderUpdateType = "FILLED"
	OrderUpdateTypePartiallyFilled OrderUpdateType = "PARTIALLY_FILLED"
	OrderUpdateTypeCancelled       OrderUpdateType = "CANCELLED"
	OrderUpdateTypeRejected        OrderUpdateType = "REJECTED"
	OrderUpdateTypeModified        OrderUpdateType = "MODIFIED"
	OrderUpdateTypeCancelRejected  OrderUpdateType = "CANCEL_REJECTED"
	OrderUpdateTypeModifyRejected  OrderUpdateType = "MODIFY_REJECTED"
)

type PositionUpdateType string

const (
	PositionUpdateTypeSnapshot       PositionUpdateType = "SNAPSHOT"
	PositionUpdateTypeOpen           PositionUpdateType = "OPEN"
	PositionUpdateTypeIncrease       PositionUpdateType = "INCREASE"
	PositionUpdateTypeDecrease       PositionUpdateType = "DECREASE"
	PositionUpdateTypeClose          PositionUpdateType = "CLOSE"
	PositionUpdateTypeFundingApplied PositionUpdateType = "FUNDING_APPLIED"
)

type CancelReason string

const (
	CancelReasonUserRequested CancelReason = "USER_REQUESTED"
	CancelReasonIOCRemainder  CancelReason = "IOC_REMAINDER"
	CancelReasonFOKNotFilled  CancelReason = "FOK_NOT_FILLED"
	CancelReasonExpired       CancelReason = "EXPIRED"
	CancelReasonSystem        CancelReason = "SYSTEM"
)

type PositionsSnapshotSource string

const (
	PositionsSnapshotSourceUnspecified PositionsSnapshotSource = "UNSPECIFIED"
	PositionsSnapshotSourceInitial     PositionsSnapshotSource = "INITIAL"
	PositionsSnapshotSourcePeriodic    PositionsSnapshotSource = "PERIODIC"
	PositionsSnapshotSourceEvent       PositionsSnapshotSource = "EVENT"
)

type SettlementBatchStatus string

const (
	SettlementBatchStatusUnspecified SettlementBatchStatus = "UNSPECIFIED"
	SettlementBatchStatusSubmitted   SettlementBatchStatus = "SUBMITTED"
	SettlementBatchStatusConfirmed   SettlementBatchStatus = "CONFIRMED"
	SettlementBatchStatusFailed      SettlementBatchStatus = "FAILED"
)

// Proto int <-> enum tables.
// The wire codes match python's mapping (which is the canonical reference).
//
// Helpers that return a zero value + ok flag follow the Go "comma ok" idiom;
// the proto bridge in proto.go is responsible for deciding how to react to
// an unknown wire value (typically: emit an UnknownSequencerPush instead of
// crashing).

var sideFromProto = map[int32]Side{
	1: SideBuy,
	2: SideSell,
}

var sideToProto = map[Side]int32{
	SideBuy:  1,
	SideSell: 2,
}

var orderTypeFromProto = map[int32]OrderType{
	1: OrderTypeMarket,
	2: OrderTypeLimit,
	3: OrderTypePegToMid,
	4: OrderTypePegToBid,
	5: OrderTypePegToAsk,
}

var orderTypeToProto = map[OrderType]int32{
	OrderTypeMarket:   1,
	OrderTypeLimit:    2,
	OrderTypePegToMid: 3,
	OrderTypePegToBid: 4,
	OrderTypePegToAsk: 5,
}

var timeInForceFromProto = map[int32]TimeInForce{
	1: TimeInForceGTC,
	2: TimeInForceIOC,
	3: TimeInForceFOK,
	4: TimeInForceGTD,
}

var timeInForceToProto = map[TimeInForce]int32{
	TimeInForceGTC: 1,
	TimeInForceIOC: 2,
	TimeInForceFOK: 3,
	TimeInForceGTD: 4,
}

var orderStatusFromProto = map[int32]OrderStatus{
	1: OrderStatusNew,
	2: OrderStatusPartiallyFilled,
	3: OrderStatusFilled,
	4: OrderStatusCancelled,
	5: OrderStatusRejected,
}

var orderUpdateTypeFromProto = map[int32]OrderUpdateType{
	1: OrderUpdateTypeOpen,
	2: OrderUpdateTypeFilled,
	3: OrderUpdateTypePartiallyFilled,
	4: OrderUpdateTypeCancelled,
	5: OrderUpdateTypeRejected,
	6: OrderUpdateTypeModified,
	7: OrderUpdateTypeCancelRejected,
	8: OrderUpdateTypeModifyRejected,
}

var positionUpdateTypeFromProto = map[int32]PositionUpdateType{
	1: PositionUpdateTypeSnapshot,
	2: PositionUpdateTypeOpen,
	3: PositionUpdateTypeIncrease,
	4: PositionUpdateTypeDecrease,
	5: PositionUpdateTypeClose,
	6: PositionUpdateTypeFundingApplied,
}

var cancelReasonFromProto = map[int32]CancelReason{
	1: CancelReasonUserRequested,
	2: CancelReasonIOCRemainder,
	3: CancelReasonFOKNotFilled,
	4: CancelReasonExpired,
	5: CancelReasonSystem,
}

// Request / response message-type ints used by the docs-wire envelope.
var requestTypeToProto = map[string]int32{
	"place":           1,
	"cancel":          2,
	"modify":          3,
	"subscribe":       4,
	"signing":         5,
	"update_leverage": 8,
	"mass_quote":      10,
	"batch_cancel":    11,
	"batch_modify":    12,
}

var responseMessageTypeToProto = map[string]int32{
	"order_update":           1,
	"system_health":          3,
	"ack":                    4,
	"open_orders_snapshot":   5,
	"order_history_snapshot": 6,
	"positions_snapshot":     7,
	"mass_quote_ack":         8,
	"batch_cancel_ack":       9,
	"batch_modify_ack":       10,
}

// SideFromProto / OrderTypeFromProto / ... are exported because the proto
// bridge in proto.go (a sibling package member) lives in the same package
// already - the public surface point is the enum constants and the wire-
// agnostic types in types.go. Helpers below are unexported but adjacent
// callers in this package can use them.

func sideEnumFromProto(v int32) (Side, bool)           { s, ok := sideFromProto[v]; return s, ok }
func orderTypeEnumFromProto(v int32) (OrderType, bool) { s, ok := orderTypeFromProto[v]; return s, ok }
func timeInForceEnumFromProto(v int32) (TimeInForce, bool) {
	s, ok := timeInForceFromProto[v]
	return s, ok
}
func orderStatusEnumFromProto(v int32) (OrderStatus, bool) {
	s, ok := orderStatusFromProto[v]
	return s, ok
}
func orderUpdateTypeEnumFromProto(v int32) (OrderUpdateType, bool) {
	s, ok := orderUpdateTypeFromProto[v]
	return s, ok
}
func positionUpdateTypeEnumFromProto(v int32) (PositionUpdateType, bool) {
	s, ok := positionUpdateTypeFromProto[v]
	return s, ok
}
func cancelReasonEnumFromProto(v int32) (CancelReason, bool) {
	s, ok := cancelReasonFromProto[v]
	return s, ok
}
