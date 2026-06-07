package godark

// Public types returned from the client. Fields that hold money / size /
// price values are stringly-typed decimals so callers don't lose precision
// to float64. Convert with math/big or shopspring/decimal as needed.

// LeverageSetting is one row in a leverage-settings snapshot.
type LeverageSetting struct {
	SymbolID int64
	Leverage int32
}

// LeverageSettings is the REST snapshot returned by
// GodarkRestClient.GetLeverage via `GET /api/v1/leverage`.
type LeverageSettings struct {
	Settings []LeverageSetting
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
//   WalletUSDTRaw      - SPL USDT sitting in the user's owner-controlled
//                        ATA (the on-chain wallet); funds that can be
//                        shielded.
//   PendingDepositsRaw - shield deposits the user has signed but the
//                        sequencer has not yet credited (e.g. inflight
//                        Solana txs).
//   ShieldedBalanceRaw - the user's shielded balance held inside the
//                        pool, as tracked by the sequencer. This is the
//                        same number streamed by the BalanceUpdate WS
//                        push, but as an on-demand snapshot.
//   WalletUSDTUI       - the same wallet amount expressed as the
//                        ui-decimal preview (USDT human units, e.g.
//                        12.345). Convenience only; do not use for
//                        arithmetic -- always reconcile against
//                        WalletUSDTRaw.
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
