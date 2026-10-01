package godark

import commonv1 "github.com/gq-godark/gdx-go-sdk/proto/gdx/common/v1"

func buildWsAdmitErrorCodes() []WsAdmitErrorEntry {
	entries := make([]WsAdmitErrorEntry, 0, len(wsAdmitErrorReasons))
	for symbolic, reason := range wsAdmitErrorReasons {
		protoName := "WS_ADMIT_ERROR_CODE_" + symbolic
		value, ok := commonv1.WsAdmitErrorCode_value[protoName]
		if !ok || value == int32(commonv1.WsAdmitErrorCode_WS_ADMIT_ERROR_CODE_UNSPECIFIED) {
			continue
		}
		entries = append(entries, WsAdmitErrorEntry{
			Code:     uint16(value),
			Symbolic: symbolic,
			Reason:   reason,
		})
	}
	return entries
}
