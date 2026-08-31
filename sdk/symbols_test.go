package godark

import "testing"

func TestDefaultSymbolMapContainsCanonical(t *testing.T) {
	m := DefaultSymbolMap()
	cases := map[string]int64{
		"BTC-USDC-PERP": 1,
		"ETH-USDC-PERP": 2,
		"SOL-USDC-PERP": 5,
	}
	for k, want := range cases {
		got, ok := m[k]
		if !ok {
			t.Errorf("missing symbol %q in default map", k)
			continue
		}
		if got != want {
			t.Errorf("%s id = %d, want %d", k, got, want)
		}
	}
}

func TestDefaultSymbolMapIsACopy(t *testing.T) {
	m1 := DefaultSymbolMap()
	m1["BTC-USDC-PERP"] = 9999
	m2 := DefaultSymbolMap()
	if got := m2["BTC-USDC-PERP"]; got != 1 {
		t.Fatalf("mutating returned map mutated package state: BTC = %d, want 1", got)
	}
}

func TestSymbolIDByName(t *testing.T) {
	id, ok := SymbolIDByName("ETH-USDC-PERP")
	if !ok || id != 2 {
		t.Fatalf("ETH-USDC-PERP lookup = (%d, %v); want (2, true)", id, ok)
	}
	if _, ok := SymbolIDByName("NOT-A-SYMBOL"); ok {
		t.Fatal("unknown symbol should miss")
	}
}
