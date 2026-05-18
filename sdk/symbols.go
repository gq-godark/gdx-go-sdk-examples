package godark

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"
)

// Default symbol map is embedded at build time from `shared/symbols.json` -
// the same file every GoDark SDK reads. The embed directive guarantees
// recipients of the compiled module don't need to ship the JSON alongside.
//
//go:embed shared/symbols.json
var embeddedSymbolsJSON []byte

// hardcodedFallback is the last-resort symbol map, identical to python's
// `_symbols._FALLBACK`. Only used if the embedded JSON ever fails to parse,
// which would mean a build-time corruption rather than a runtime condition.
var hardcodedSymbolsFallback = map[string]int64{
	"BTC-USDC-PERP": 1,
	"ETH-USDC-PERP": 2,
	"SOL-USDC-PERP": 5,
}

var (
	defaultSymbolMapOnce sync.Once
	defaultSymbolMap     map[string]int64
)

// DefaultSymbolMap returns the canonical symbol -> symbol_id table embedded
// from `shared/symbols.json`. The returned map is a fresh copy; callers may
// mutate it freely.
func DefaultSymbolMap() map[string]int64 {
	defaultSymbolMapOnce.Do(func() {
		defaultSymbolMap = loadDefaultSymbolMap()
	})
	out := make(map[string]int64, len(defaultSymbolMap))
	for k, v := range defaultSymbolMap {
		out[k] = v
	}
	return out
}

// SymbolIDByName looks up a single canonical symbol id (perp ticker, e.g.
// "BTC-USDC-PERP"). Returns (id, true) on hit, (0, false) on miss.
func SymbolIDByName(name string) (int64, bool) {
	if defaultSymbolMap == nil {
		_ = DefaultSymbolMap()
	}
	id, ok := defaultSymbolMap[name]
	return id, ok
}

func loadDefaultSymbolMap() map[string]int64 {
	if len(embeddedSymbolsJSON) > 0 {
		var raw map[string]int64
		if err := json.Unmarshal(embeddedSymbolsJSON, &raw); err == nil {
			out := make(map[string]int64, len(raw))
			for k, v := range raw {
				out[k] = v
			}
			return out
		}
		// Embedded JSON corrupted at build time. Fall through.
		_ = fmt.Errorf("ignored")
	}

	out := make(map[string]int64, len(hardcodedSymbolsFallback))
	for k, v := range hardcodedSymbolsFallback {
		out[k] = v
	}
	return out
}
