package godark

import (
	commonv1 "github.com/gq-godark/gdx-go-sdk/proto/gdx/common/v1"
)

func buildOrderErrorCodes() []OrderErrorEntry {
	entries := make([]OrderErrorEntry, 0, len(orderErrorReasons))
	for symbolic, reason := range orderErrorReasons {
		protoName := "ORDER_ERROR_CODE_" + symbolic
		value, ok := commonv1.OrderErrorCode_value[protoName]
		if !ok || value == int32(commonv1.OrderErrorCode_ORDER_ERROR_CODE_UNSPECIFIED) {
			continue
		}
		entries = append(entries, OrderErrorEntry{
			Code:     uint16(value),
			Symbolic: symbolic,
			Reason:   reason,
		})
	}
	return entries
}
