package godark

import "testing"

func TestOrderErrorCodesCount(t *testing.T) {
	// Lock in parity with the python / rust / js registries.
	if got := len(OrderErrorCodes); got != 34 {
		t.Fatalf("OrderErrorCodes length = %d, want 34", got)
	}
}

func TestRegistryHasNoDuplicateCodes(t *testing.T) {
	seen := make(map[uint16]string)
	for _, e := range OrderErrorCodes {
		if prev, dup := seen[e.Code]; dup {
			t.Fatalf("duplicate code %d: %q and %q", e.Code, prev, e.Symbolic)
		}
		seen[e.Code] = e.Symbolic
	}
}

func TestFindOrderErrorCode_Hit(t *testing.T) {
	e, ok := FindOrderErrorCode(2007)
	if !ok {
		t.Fatal("2007 should be a known code")
	}
	if e.Symbolic != "PRICE_DEVIATION_TOO_LARGE" {
		t.Fatalf("symbolic = %q, want PRICE_DEVIATION_TOO_LARGE", e.Symbolic)
	}
}

func TestFindOrderErrorCode_Miss(t *testing.T) {
	if _, ok := FindOrderErrorCode(31337); ok {
		t.Fatal("31337 should not be a known code")
	}
}

func TestFindOrderErrorSymbolic(t *testing.T) {
	e, ok := FindOrderErrorSymbolic("MARGIN_INSUFFICIENT")
	if !ok || e.Code != 2015 {
		t.Fatalf("MARGIN_INSUFFICIENT lookup got code=%d ok=%v; want 2015 / true", e.Code, ok)
	}
	if _, ok := FindOrderErrorSymbolic("NOT_A_REAL_CODE"); ok {
		t.Fatal("expected miss for unknown symbolic")
	}
}

func TestMakeOrderErrorFromCode_Known(t *testing.T) {
	code := int32(2007)
	got := MakeOrderErrorFromCode(&code)
	if got.ErrorCode != "PRICE_DEVIATION_TOO_LARGE" {
		t.Fatalf("ErrorCode = %q, want PRICE_DEVIATION_TOO_LARGE", got.ErrorCode)
	}
	if got.Message == "" || got.Message == "order rejected" {
		t.Fatalf("Message = %q, want rich reason", got.Message)
	}
}

func TestMakeOrderErrorFromCode_Nil(t *testing.T) {
	got := MakeOrderErrorFromCode(nil)
	if got.ErrorCode != "" || got.Message != "order rejected" {
		t.Fatalf("nil code: got code=%q msg=%q; want \"\" / \"order rejected\"", got.ErrorCode, got.Message)
	}
}

func TestMakeOrderErrorFromCode_UnknownInRange(t *testing.T) {
	code := int32(31337)
	got := MakeOrderErrorFromCode(&code)
	if got.ErrorCode != "31337" {
		t.Fatalf("ErrorCode = %q, want \"31337\"", got.ErrorCode)
	}
	if got.Message != "order rejected" {
		t.Fatalf("Message = %q, want \"order rejected\"", got.Message)
	}
}

func TestMakeOrderErrorFromCode_OutOfRange(t *testing.T) {
	code := int32(-1)
	got := MakeOrderErrorFromCode(&code)
	if got.ErrorCode != "-1" {
		t.Fatalf("ErrorCode = %q, want \"-1\"", got.ErrorCode)
	}
}

func TestMakeOrderErrorFromJSON_NumericKnown(t *testing.T) {
	got := MakeOrderErrorFromJSON("", "2015")
	if got.ErrorCode != "MARGIN_INSUFFICIENT" {
		t.Fatalf("ErrorCode = %q, want MARGIN_INSUFFICIENT", got.ErrorCode)
	}
	if got.Message == "order rejected" {
		t.Fatalf("Message should have been hydrated, got %q", got.Message)
	}
}

func TestMakeOrderErrorFromJSON_NumericKeepsServerReason(t *testing.T) {
	got := MakeOrderErrorFromJSON("server-supplied detail", "2015")
	if got.ErrorCode != "MARGIN_INSUFFICIENT" {
		t.Fatalf("ErrorCode = %q, want MARGIN_INSUFFICIENT", got.ErrorCode)
	}
	if got.Message != "server-supplied detail" {
		t.Fatalf("Message = %q, want to keep server-supplied detail", got.Message)
	}
}

func TestMakeOrderErrorFromJSON_SymbolicHydratesReason(t *testing.T) {
	got := MakeOrderErrorFromJSON("", "MARGIN_INSUFFICIENT")
	if got.ErrorCode != "MARGIN_INSUFFICIENT" {
		t.Fatalf("ErrorCode = %q, want MARGIN_INSUFFICIENT", got.ErrorCode)
	}
	if got.Message == "order rejected" {
		t.Fatalf("Message should have been hydrated, got %q", got.Message)
	}
}

func TestMakeOrderErrorFromJSON_EmptyEverything(t *testing.T) {
	got := MakeOrderErrorFromJSON("", "")
	if got.ErrorCode != "" || got.Message != "order rejected" {
		t.Fatalf("empty/empty: code=%q msg=%q", got.ErrorCode, got.Message)
	}
}

func TestMakeOrderErrorFromJSON_OutOfRangeNumeric(t *testing.T) {
	got := MakeOrderErrorFromJSON("", "70000")
	if got.ErrorCode != "70000" {
		t.Fatalf("ErrorCode = %q, want \"70000\"", got.ErrorCode)
	}
	if got.Message != "order rejected" {
		t.Fatalf("Message = %q, want unchanged", got.Message)
	}
}

func TestMakeOrderErrorFromJSON_UnknownSymbolic(t *testing.T) {
	got := MakeOrderErrorFromJSON("free-text reason", "NO_SUCH_CODE")
	if got.ErrorCode != "NO_SUCH_CODE" {
		t.Fatalf("ErrorCode = %q, want preserved \"NO_SUCH_CODE\"", got.ErrorCode)
	}
	if got.Message != "free-text reason" {
		t.Fatalf("Message = %q, want preserved server text", got.Message)
	}
}

// Spot-check a few canonical (code, symbolic) pairs against the python
// reference so any future re-numbering is caught at SDK build time.
func TestKnownCanonicalPairs(t *testing.T) {
	cases := map[uint16]string{
		1001: "TRIPLE_EXHAUSTED",
		2001: "RISK_CHECK_FAILED",
		2007: "PRICE_DEVIATION_TOO_LARGE",
		2015: "MARGIN_INSUFFICIENT",
		3001: "ACK_TIMEOUT",
		3008: "SESSION_EXPIRED",
		4001: "EPOCH_STALE",
		9999: "INTERNAL_ERROR",
	}
	for code, want := range cases {
		e, ok := FindOrderErrorCode(code)
		if !ok {
			t.Errorf("code %d missing", code)
			continue
		}
		if e.Symbolic != want {
			t.Errorf("code %d symbolic = %q, want %q", code, e.Symbolic, want)
		}
	}
}
