package godark

// Public types returned from the client. Fields that hold money / size /
// price values are stringly-typed decimals so callers don't lose precision
// to float64. Convert with math/big or shopspring/decimal as needed.

// LeverageSetting is one row in a leverage-settings snapshot.
type LeverageSetting struct {
	SymbolID int64
	Leverage int32
}

// LeverageSettings is the per-user leverage snapshot returned by
// GodarkRestClient.GetLeverage via `GET /api/v1/leverage` and pushed over
// the encrypted WS as `leverage_settings`. REST responses leave UserUUID
// empty and ServerTimestamp at zero.
type LeverageSettings struct {
	UserUUID        string
	Settings        []LeverageSetting
	ServerTimestamp uint64
}

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

// MassQuoteLegInput is one cancel-replace leg of a mass quote.
type MassQuoteLegInput struct {
	Side     Side
	Price    float64
	Quantity float64
	// CancelOrderID is the resting order to cancel-replace; nil/0 = pure place.
	CancelOrderID *uint64
	// TimeInForce defaults to GTC when empty.
	TimeInForce TimeInForce
	// ExpiryTime (ns) is required when TimeInForce == GTD.
	ExpiryTime *uint64
}

// BatchModifyLegInput is one amend leg of a batch modify. At least one of
// NewPrice / NewQuantity must be set.
type BatchModifyLegInput struct {
	OrderID     uint64
	NewPrice    *float64
	NewQuantity *float64
}

// MassQuoteLegResult is the outcome of one cancel-replace leg in a mass quote.
type MassQuoteLegResult struct {
	LegIndex uint32
	// Status is "open" | "filled" | "failed" | "unspecified" | "unknown".
	Status string
	// CancelledOrderID is empty when there was no cancel target / cancel failed.
	CancelledOrderID string
	// NewOrderID is empty when the replacement failed.
	NewOrderID string
	// ErrorCode is set (non-nil) when the leg failed.
	ErrorCode *uint32
	// FillCount is the number of taker fills this leg produced in relaxed
	// (post-only=false) mode. 0 for a pure rest or a post-only leg.
	FillCount uint32
}

// MassQuoteAck is the batch-level result of a mass quote: one entry per leg.
type MassQuoteAck struct {
	Success  bool
	Sequence string
	Results  []MassQuoteLegResult
}

// BatchCancelLegResult is the outcome of cancelling one order id in a batch.
type BatchCancelLegResult struct {
	OrderID   string
	Cancelled bool
	ErrorCode *uint32
}

// BatchCancelAck is the batch-level result of a batch cancel.
type BatchCancelAck struct {
	Success  bool
	Sequence string
	Results  []BatchCancelLegResult
}

// BatchModifyLegResult is the outcome of amending one resting order in a batch.
type BatchModifyLegResult struct {
	OrderID   string
	Modified  bool
	ErrorCode *uint32
}

// BatchModifyAck is the batch-level result of a batch modify.
type BatchModifyAck struct {
	Success  bool
	Sequence string
	Results  []BatchModifyLegResult
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
	Msg           string
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

// OpenOrderRow is one resting order within an OpenOrdersSnapshot.
type OpenOrderRow struct {
	OrderID      string
	SymbolID     int64
	Leverage     int32
	Price        string
	Quantity     string
	RemainingQty string
}

// OpenOrdersSnapshot is the authoritative view of all open orders for the
// authenticated user, returned in response to GetOpenOrders.
type OpenOrdersSnapshot struct {
	Rows            []OpenOrderRow
	ServerTimestamp uint64
	CorrelationID   uint64
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

// SystemHealthUpdate is a unified component health report.
type SystemHealthUpdate struct {
	ComponentID    string
	State          int32
	Serving        bool
	Cause          string
	UpdatedAtNanos uint64
	Sequence       uint64
	SchemaVersion  uint32
}

// BalanceUpdate is a push frame describing the user's shielded balance.
type BalanceUpdate struct {
	UserUUID           string
	ShieldedBalanceRaw uint64
	Timestamp          uint64
}

// Balance is the REST snapshot returned by
// GodarkRestClient.GetBalance(ctx, owner). It reports the on-chain
// USDT balance breakdown across the user's wallet, in-flight shield
// deposits, and the sequencer-tracked shielded trading balance.
//
// All `*Raw` fields are u64 amounts in the asset's smallest denomination
// (USDT decimals; today this is 1e6 = 1 USDT). Numeric ordering on the
// wire is decimal-encoded as strings (because u64 doesn't roundtrip
// JSON safely); the SDK parses them back to uint64 here.
//
//	WalletUSDTRaw      - SPL USDT sitting in the user's owner-controlled
//	                     ATA (the on-chain wallet); funds that can be
//	                     shielded.
//	PendingDepositsRaw - shield deposits the user has signed but the
//	                     sequencer has not yet credited (e.g. inflight
//	                     Solana txs).
//	ShieldedBalanceRaw - the user's shielded balance held inside the
//	                     pool, as tracked by the sequencer. This is the
//	                     same number streamed by the BalanceUpdate WS
//	                     push, but as an on-demand snapshot.
//	WalletUSDTUI       - the same wallet amount expressed as the
//	                     ui-decimal preview (USDT human units, e.g.
//	                     12.345). Convenience only; do not use for
//	                     arithmetic -- always reconcile against
//	                     WalletUSDTRaw.
type Balance struct {
	WalletUSDTRaw      uint64
	PendingDepositsRaw uint64
	ShieldedBalanceRaw uint64
	WalletUSDTUI       float64
}

// MeProfile is the REST profile snapshot returned by
// GodarkRestClient.GetMe, fetched from `GET /api/v1/auth/me`. Notably
// WalletAddress is the Solana base58 owner pubkey the SDK passes as the
// path parameter to GetBalance.
type MeProfile struct {
	ID            string
	DynamicUserID string
	Email         string
	WalletAddress string
	ReferralCode  string
	Tier          string
}

// MarginAlert is a push frame describing a margin tier transition / recovery.
type MarginAlert struct {
	Owner            string
	SymbolID         int64
	Tier             int32
	MarginRatioBps   int64
	MarkPrice        string
	LiquidationPrice string
	TS               uint64
	StateVersion     uint64
	Recovered        bool
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
