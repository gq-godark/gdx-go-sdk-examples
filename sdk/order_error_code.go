package godark

import (
	"fmt"
	"strconv"
	"strings"
)

// OrderErrorEntry describes one canonical order-rejection reason emitted by
// the sequencer. The registry is shared across every GoDark SDK; keep this
// list in sync with `gdx-protocol`.
type OrderErrorEntry struct {
	// Code is the wire integer the sequencer emits.
	Code uint16
	// Symbolic is the SCREAMING_SNAKE_CASE name (matches the JSON wire form's
	// `OrderErrorCode::as_json_str`).
	Symbolic string
	// Reason is the human-readable explanation.
	Reason string
}

// OrderErrorCodes is the canonical registry. Keep ordered by numeric code for
// easy diff-against-python review.
var OrderErrorCodes = []OrderErrorEntry{
	// 1xxx -- Node / MPC
	{1001, "TRIPLE_EXHAUSTED", "Beaver triple store exhausted"},
	{1002, "RANDOM_BIT_EXHAUSTED", "random bit store exhausted"},
	{1003, "MPC_PROTOCOL_ERROR", "MPC protocol error"},
	{1004, "MPC_TIMEOUT", "MPC session timeout"},
	{1005, "MPC_CONFIG_ERROR", "MPC configuration error"},
	{1006, "MPC_OPS_LIMIT_EXCEEDED", "MPC ops limit exceeded"},
	// 2xxx -- Risk / validation
	{2001, "RISK_CHECK_FAILED", "pre-trade risk check failed"},
	{2002, "INSUFFICIENT_COLLATERAL", "insufficient collateral"},
	{2003, "ORDER_NOT_FOUND", "order not found in book"},
	{2004, "DUPLICATE_ORDER_ID", "duplicate order ID"},
	{2005, "INSUFFICIENT_LIQUIDITY", "insufficient liquidity"},
	{2006, "POSITION_UNDER_LIQUIDATION", "position is under active liquidation"},
	{2007, "PRICE_DEVIATION_TOO_LARGE", "order price too far from oracle price"},
	{2008, "LEVERAGE_EXCEEDS_MAX", "leverage exceeds instrument max"},
	{2009, "INSTRUMENT_HALTED", "instrument halted -- not currently accepting orders"},
	{2010, "LIQUIDITY_POOL_WITHDRAW_COOLDOWN", "withdrawal cooldown active"},
	{2011, "LIQUIDITY_POOL_PAUSED", "liquidity pool paused"},
	{2012, "LIQUIDITY_POOL_ILLIQUID", "insufficient pool liquidity for withdrawal"},
	{2013, "BELOW_MIN_NOTIONAL", "order notional below tier minimum"},
	{2014, "ORDER_EXCEEDS_COLLATERAL", "order size exceeds collateral value limits"},
	{2015, "MARGIN_INSUFFICIENT", "insufficient margin for this trade"},
	// 3xxx -- Sequencer
	{3001, "ACK_TIMEOUT", "ACK collection timed out"},
	{3002, "ACK_THRESHOLD_NOT_MET", "ACK threshold not met"},
	{3003, "SEQUENCER_NOT_PRIMARY", "sequencer is standby, not primary"},
	{3004, "INSUFFICIENT_MASKS", "insufficient input masks for authenticated split"},
	{3005, "FANOUT_FAILED", "fanout delivery failed"},
	{3006, "DESERIALIZATION_FAILED", "message deserialization failed"},
	{3007, "ALL_NODES_EXHAUSTED", "all MPC nodes have exhausted precompute pools"},
	{3008, "SESSION_EXPIRED", "E2E session expired or not established"},
	{3009, "E2E_DECRYPTION_FAILED", "E2E decryption failed (session key mismatch)"},
	{3010, "SHIELD_SUBMIT_RPC_FAILED", "shield transaction rejected by Solana RPC"},
	{3011, "SEQUENCER_BUSY", "sequencer busy -- try again"},
	// 4xxx -- Fencing / hot standby
	{4001, "EPOCH_STALE", "fencing epoch is stale"},
	// 9xxx -- catch-all
	{9999, "INTERNAL_ERROR", "internal processing error"},
}

// orderByCode is an internal lookup map built once at package init.
var orderByCode = func() map[uint16]OrderErrorEntry {
	m := make(map[uint16]OrderErrorEntry, len(OrderErrorCodes))
	for _, e := range OrderErrorCodes {
		m[e.Code] = e
	}
	return m
}()

// FindOrderErrorCode looks up an entry by its numeric wire code. Returns
// (entry, true) on hit and (zero, false) on miss.
func FindOrderErrorCode(code uint16) (OrderErrorEntry, bool) {
	e, ok := orderByCode[code]
	return e, ok
}

// FindOrderErrorSymbolic looks up an entry by its SCREAMING_SNAKE_CASE name.
// O(n) but the registry is tiny; not worth a second map.
func FindOrderErrorSymbolic(symbolic string) (OrderErrorEntry, bool) {
	for _, e := range OrderErrorCodes {
		if e.Symbolic == symbolic {
			return e, true
		}
	}
	return OrderErrorEntry{}, false
}

// MakeOrderErrorFromCode maps a protobuf `AckMessage.error_code` int to an
// OrderError carrying the symbolic reason. A negative or unknown code yields
// a generic "order rejected" error with the raw code string preserved when
// non-zero.
//
// Pass code = nil to signal "the server didn't supply a code at all".
// An optional detail (ack reject_text / update msg) is appended when non-empty.
func MakeOrderErrorFromCode(code *int32, detail ...string) *OrderError {
	detailSuffix := ""
	if len(detail) > 0 {
		if d := strings.TrimSpace(detail[0]); d != "" {
			detailSuffix = ": " + d
		}
	}
	if code == nil {
		return newOrderError("order rejected"+detailSuffix, "")
	}
	raw := *code
	if raw >= 0 && raw <= 0xFFFF {
		if e, ok := FindOrderErrorCode(uint16(raw)); ok {
			return newOrderError(
				fmt.Sprintf("%s (%s, code=%d)%s", e.Reason, e.Symbolic, e.Code, detailSuffix),
				e.Symbolic,
			)
		}
	}
	return newOrderError("order rejected"+detailSuffix, strconv.FormatInt(int64(raw), 10))
}

// MakeOrderErrorFromJSON is the JSON-ack equivalent of MakeOrderErrorFromCode.
// `reason` is the free-form text the server emitted; `code` may be empty, a
// numeric string, or already-symbolic. The returned OrderError surfaces a
// rich symbolic reason whenever possible.
func MakeOrderErrorFromJSON(reason, code string) *OrderError {
	finalReason := reason
	if finalReason == "" {
		finalReason = "order rejected"
	}
	finalCode := code

	if code != "" {
		stripped := strings.TrimSpace(code)
		if parsed, err := strconv.ParseInt(stripped, 10, 64); err == nil {
			if parsed >= 0 && parsed <= 0xFFFF {
				if e, ok := FindOrderErrorCode(uint16(parsed)); ok {
					finalCode = e.Symbolic
					if reason == "" || reason == "order rejected" {
						finalReason = fmt.Sprintf("%s (%s, code=%d)", e.Reason, e.Symbolic, e.Code)
					}
				}
			} else {
				finalCode = strconv.FormatInt(parsed, 10)
			}
		} else {
			if e, ok := FindOrderErrorSymbolic(stripped); ok {
				if reason == "" || reason == "order rejected" {
					finalReason = fmt.Sprintf("%s (%s, code=%d)", e.Reason, e.Symbolic, e.Code)
				}
			}
		}
	}

	return newOrderError(finalReason, finalCode)
}
