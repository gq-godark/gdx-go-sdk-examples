package godark

import "strconv"

// WsAdmitErrorEntry describes one Edge WS admit failure (`7xxx`).
type WsAdmitErrorEntry struct {
	Code     uint16
	Symbolic string
	Reason   string
}

// WsAdmitErrorCodes is built from proto identity + hand-authored reasons.
var WsAdmitErrorCodes = buildWsAdmitErrorCodes()

var wsAdmitByCode = func() map[uint16]WsAdmitErrorEntry {
	m := make(map[uint16]WsAdmitErrorEntry, len(WsAdmitErrorCodes))
	for _, e := range WsAdmitErrorCodes {
		m[e.Code] = e
	}
	return m
}()

// FindWsAdmitError looks up a WS admit entry by numeric code.
func FindWsAdmitError(code uint16) (WsAdmitErrorEntry, bool) {
	e, ok := wsAdmitByCode[code]
	return e, ok
}

// ResolveWsAdmitMessage returns catalog copy when code is known, else fallback.
func ResolveWsAdmitMessage(code any, fallback string) string {
	var parsed uint16
	switch v := code.(type) {
	case uint16:
		parsed = v
	case int:
		if v >= 0 && v <= 0xffff {
			parsed = uint16(v)
		}
	case float64:
		if v >= 0 && v <= 0xffff {
			parsed = uint16(v)
		}
	case string:
		if n, err := strconv.ParseUint(v, 10, 16); err == nil {
			parsed = uint16(n)
		}
	default:
		return fallback
	}
	if e, ok := FindWsAdmitError(parsed); ok {
		return e.Reason
	}
	return fallback
}
