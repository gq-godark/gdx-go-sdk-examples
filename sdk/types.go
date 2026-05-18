package godark

// Public types returned from the client. Fields that hold money / size /
// price values are stringly-typed decimals so callers don't lose precision
// to float64. Convert with math/big or shopspring/decimal as needed.

// OrderAck is the response to a Place / Cancel / Modify command.
type OrderAck struct {
	OrderID  string
	Success  bool
	Sequence string
	// ErrorCode is set on failure (Success == false); the symbolic name of
	// the canonical reject reason (e.g. "PRICE_DEVIATION_TOO_LARGE").
	ErrorCode string
	// Error is the human-readable failure message; empty on success.
	Error string
}

// OrderUpdate is a push frame describing a single order lifecycle event.
type OrderUpdate struct {
	OrderID       string
	UserUUID      string
	SymbolID      int64
	Side          Side
	Status        OrderStatus
	UpdateType    OrderUpdateType
	Price         string
	Quantity      string
	FilledQty     string
	RemainingQty  string
	CumFill       string
	CancelReason  CancelReason
	RejectReason  string
	CorrelationID uint64
	Timestamp     uint64
	// Leverage is the client-selected leverage at order-placement time (1 = 1x).
	Leverage int32
	// RealizedPnl is set on closing / terminal fills; empty when absent.
	RealizedPnl string
}

// PositionUpdate is a push frame describing a single position delta (fill,
// open, close, funding settlement).
type PositionUpdate struct {
	UserUUID      string
	SymbolID      int64
	Side          Side
	UpdateType    PositionUpdateType
	Size          string
	EntryPrice    string
	PreviousSize  string
	FillPrice     string
	FillQty       string
	CorrelationID uint64
	Timestamp     uint64
}

// PositionRow is one row within a PositionsSnapshot.
type PositionRow struct {
	SymbolID           int64
	Side               Side
	Size               string
	EntryPrice         string
	Leverage           int32
	MarkPrice          string
	UnrealizedPnl      string
	Notional           string
	MarkPublishTimeSec uint64
}

// PositionsSnapshot is the periodic / event-triggered authoritative view of
// all open positions for the authenticated user.
type PositionsSnapshot struct {
	UserUUID        string
	Rows            []PositionRow
	ServerTimestamp uint64
	Source          PositionsSnapshotSource
	// CorrelationID echoes the SubscribePositions request id where present.
	CorrelationID uint64
}

// SystemHealthUpdate is a push frame describing the sequencer / MPC cluster.
type SystemHealthUpdate struct {
	TotalNodes      int32
	AcceptingOrders bool
	Ready           int32
	Degraded        int32
	Exhausted       int32
	Warming         int32
	Draining        int32
	Waiting         int32
}

// BalanceUpdate is a push frame describing the user's shielded balance.
type BalanceUpdate struct {
	UserUUID           string
	ShieldedBalanceRaw uint64
	Timestamp          uint64
}

// MarginAlert is a push frame describing a margin tier transition / recovery.
type MarginAlert struct {
	Owner               string
	SymbolID            int64
	Tier                int32
	MarginRatioBps      int64
	MarkPriceBps        int64
	LiquidationPriceBps int64
	TS                  uint64
	StateVersion        uint64
	Recovered           bool
}

// FundingRateUpdate is a push frame describing per-symbol funding ticks.
type FundingRateUpdate struct {
	SymbolID        int64
	CurrentRate     string
	PredictedRate   string
	NextFundingTime uint64
	Timestamp       uint64
}

// SettlementUpdate is a push frame describing a settlement batch lifecycle
// transition (Submitted / Confirmed / Failed).
type SettlementUpdate struct {
	BatchID           uint64
	Status            SettlementBatchStatus
	TxSignature       string
	Timestamp         uint64
	AffectedUserUUIDs []string
}

// UnknownSequencerPush is emitted by the proto bridge when the sequencer
// sends an inner message variant this SDK revision doesn't yet map. Callers
// can use this to detect new wire features without crashing.
type UnknownSequencerPush struct {
	// OneofField is the protobuf field name of the unknown variant, e.g.
	// "new_message_type_5", to help with debugging.
	OneofField string
}
