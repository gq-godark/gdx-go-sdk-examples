package godark

import (
	"fmt"
	"strconv"
	"strings"
)

// OrderErrorEntry describes one canonical order-rejection reason emitted by
// the sequencer. Identity comes from proto OrderErrorCode; reasons are hand-authored.
type OrderErrorEntry struct {
	Code     uint16
	Symbolic string
	Reason   string
}

// OrderErrorCodes is the retail trade table (proto identity + hand-authored reasons).
var OrderErrorCodes = buildOrderErrorCodes()

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
func FindOrderErrorSymbolic(symbolic string) (OrderErrorEntry, bool) {
	for _, e := range OrderErrorCodes {
		if e.Symbolic == symbolic {
			return e, true
		}
	}
	return OrderErrorEntry{}, false
}

// MakeOrderErrorFromCode maps a protobuf `AckMessage.error_code` int to an
// OrderError carrying the symbolic reason.
func MakeOrderErrorFromCode(code *int32, detail ...string) *OrderError {
	detailSuffix := ""
	if len(detail) > 0 {
		if d := strings.TrimSpace(detail[0]); d != "" {
			detailSuffix = ": " + d
		}
	}
	if code == nil {
		return newOrderError("order rejected"+detailSuffix, "", "")
	}
	raw := *code
	if raw >= 0 && raw <= 0xFFFF {
		if e, ok := FindOrderErrorCode(uint16(raw)); ok {
			return newOrderError(
				fmt.Sprintf("%s (%s, code=%d)%s", e.Reason, e.Symbolic, e.Code, detailSuffix),
				e.Symbolic,
				e.Reason,
			)
		}
	}
	return newOrderError("order rejected"+detailSuffix, strconv.FormatInt(int64(raw), 10), "")
}

// MakeOrderErrorFromJSON is the JSON-ack equivalent of MakeOrderErrorFromCode.
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
