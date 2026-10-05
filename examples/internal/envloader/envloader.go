// Package envloader provides a tiny standard-library-only `.env` loader
// shared by the example main packages, plus a pretty-printer for
// godark.OrderError so MMs can see the symbolic reject reason (e.g.
// PRICE_DEVIATION_TOO_LARGE) at a glance.
//
// LoadDotenv reads the bundle's `.env` file (next to the example binaries
// when run from the unzipped distribution; otherwise the repo root in
// dev). The OS environment always wins over the file -- this matches the
// behaviour of the standard `github.com/joho/godotenv` library and keeps
// CI overrides (env vars exported by the workflow) authoritative.
package envloader

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gq-godark/gdx-go-sdk"
)

// DemoQuantity is the largest size the trading examples place (4 dp).
const DemoQuantity = "0.001"

var (
	osPresent     map[string]struct{}
	fileVals      map[string]string
	osSnapshotted bool
)

// First returns OS values among keys (GODARK then GDX), then the same keys from .env.
func First(keys ...string) string {
	if osSnapshotted {
		for _, k := range keys {
			if _, ok := osPresent[k]; ok {
				if v := strings.TrimSpace(os.Getenv(k)); v != "" {
					return v
				}
			}
		}
		for _, k := range keys {
			if v := strings.TrimSpace(fileVals[k]); v != "" {
				return v
			}
		}
		return ""
	}
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// LoadDotenv reads a `.env` file from one of:
//  1. the directory of the currently running executable (so the unzipped
//     bundle's `.env` is picked up when MMs run ./quickstart from the
//     bundle root);
//  2. the current working directory (so `go run ./examples/quickstart`
//     from the repo root just works).
//
// Lines are `KEY=VALUE`. `#` comments and blank lines are skipped. Quoted
// values (single or double) are unquoted. Keys already present in os.Environ
// are NOT overwritten -- the OS environment always wins.
func LoadDotenv() {
	if osSnapshotted {
		return
	}
	osPresent = map[string]struct{}{}
	fileVals = map[string]string{}
	for _, e := range os.Environ() {
		k, v, ok := strings.Cut(e, "=")
		if ok && strings.TrimSpace(v) != "" {
			osPresent[k] = struct{}{}
		}
	}
	osSnapshotted = true
	candidates := []string{}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), ".env"))
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, ".env"))
	}
	for _, path := range candidates {
		if applyEnvFile(path) {
			return
		}
	}
}

func applyEnvFile(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if (strings.HasPrefix(val, "\"") && strings.HasSuffix(val, "\"")) ||
			(strings.HasPrefix(val, "'") && strings.HasSuffix(val, "'")) {
			val = val[1 : len(val)-1]
		}
		if key == "" {
			continue
		}
		fileVals[key] = val
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		_ = os.Setenv(key, val)
	}
	return true
}

// HTTPOrigin converts an edge or REST URL to an https/http origin.
// An empty input stays empty so the SDK can apply its own default.
func HTTPOrigin(raw string) string {
	u := trimWSPath(raw)
	if u == "" {
		return ""
	}
	switch {
	case strings.HasPrefix(u, "wss://"):
		return "https://" + strings.TrimPrefix(u, "wss://")
	case strings.HasPrefix(u, "ws://"):
		return "http://" + strings.TrimPrefix(u, "ws://")
	case strings.HasPrefix(u, "https://"), strings.HasPrefix(u, "http://"):
		return u
	default:
		return "https://" + u
	}
}

// WSOrigin converts an edge or REST URL to a ws/wss origin for GodarkClient.
func WSOrigin(raw string) string {
	u := trimWSPath(raw)
	if u == "" {
		return ""
	}
	switch {
	case strings.HasPrefix(u, "https://"):
		return "wss://" + strings.TrimPrefix(u, "https://")
	case strings.HasPrefix(u, "http://"):
		return "ws://" + strings.TrimPrefix(u, "http://")
	case strings.HasPrefix(u, "wss://"), strings.HasPrefix(u, "ws://"):
		return u
	default:
		return "wss://" + u
	}
}

func trimWSPath(raw string) string {
	u := strings.TrimRight(strings.TrimSpace(raw), "/")
	for _, suffix := range []string{"/ws/v1", "/ws"} {
		u = strings.TrimSuffix(u, suffix)
	}
	return u
}

// ResolveMark returns a positive mark. A price env var wins when it is set.
// Otherwise snapshotMark is used when it is positive. Otherwise the mark is
// open-interest notional/size (oi_ccy / open_interest) for BTC-USDC-PERP.
// There is no hardcoded price fallback.
func ResolveMark(snapshotMark float64, oi []map[string]any) (float64, error) {
	if raw := First("GODARK_E2E_PRICE", "GDX_E2E_PRICE", "GDX_LIVE_PRICE", "GODARK_LIVE_PRICE"); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, fmt.Errorf("price env %q is not a positive mark", raw)
		}
		return v, nil
	}
	if snapshotMark > 0 && !math.IsNaN(snapshotMark) && !math.IsInf(snapshotMark, 0) {
		return snapshotMark, nil
	}
	mark, ok := markFromOpenInterest(oi)
	if !ok {
		return 0, errors.New("no live mark: set no price default; open interest and position snapshot had no BTC mark")
	}
	return mark, nil
}

func markFromOpenInterest(rows []map[string]any) (float64, bool) {
	for _, row := range rows {
		if !btcOIRow(row) {
			continue
		}
		notional, nok := asFloat(row["oi_ccy"])
		if !nok {
			notional, nok = asFloat(row["notional"])
		}
		size, sok := asFloat(row["open_interest"])
		if !sok {
			size, sok = asFloat(row["size"])
		}
		if nok && sok && notional > 0 && size > 0 {
			mark := notional / size
			if mark > 0 && !math.IsNaN(mark) && !math.IsInf(mark, 0) {
				return mark, true
			}
		}
	}
	return 0, false
}

func btcOIRow(row map[string]any) bool {
	if sym, ok := row["symbol"].(string); ok && strings.EqualFold(strings.TrimSpace(sym), "BTC-USDC-PERP") {
		return true
	}
	id, ok := asFloat(row["symbol_id"])
	return ok && id == 1
}

func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	case jsonNumber:
		f, err := t.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// jsonNumber matches encoding/json.Number without importing encoding/json
// just for the type switch. The default decoder yields float64.
type jsonNumber interface{ Float64() (float64, error) }

// PostOnlySell is a 0.5-tick limit at least 500 above mark (1 decimal).
func PostOnlySell(mark float64) (string, error) {
	if mark <= 0 || math.IsNaN(mark) || math.IsInf(mark, 0) {
		return "", errors.New("mark must be positive")
	}
	px := alignUp(mark + 500)
	if px < mark+500 {
		px += 0.5
	}
	return strconv.FormatFloat(px, 'f', 1, 64), nil
}

// PostOnlyBuy is a 0.5-tick limit at least 500 below mark. extraBelow pushes
// further down so stacked quote legs do not share a price.
func PostOnlyBuy(mark, extraBelow float64) (string, error) {
	if mark <= 0 || math.IsNaN(mark) || math.IsInf(mark, 0) {
		return "", errors.New("mark must be positive")
	}
	if extraBelow < 0 {
		extraBelow = 0
	}
	px := alignDown(mark - 500 - extraBelow)
	if px > mark-500 {
		px -= 0.5
	}
	if px <= 0 {
		return "", errors.New("buy price is non-positive")
	}
	return strconv.FormatFloat(px, 'f', 1, 64), nil
}

func alignUp(px float64) float64 {
	const tick = 0.5
	return math.Ceil(px/tick-1e-9) * tick
}

func alignDown(px float64) float64 {
	const tick = 0.5
	return math.Floor(px/tick+1e-9) * tick
}

// PrintOrderError formats a godark.OrderError so the symbolic reject reason
// (when the SDK was able to canonicalise it) is the first thing the operator
// sees. For non-OrderError errors it falls back to the default formatter.
func PrintOrderError(operation string, err error) {
	var oe *godark.OrderError
	if errors.As(err, &oe) {
		code := oe.ErrorCode
		if code == "" {
			code = "<none>"
		}
		fmt.Fprintf(os.Stderr, "%s: OrderError code=%s reason=%s\n",
			operation, code, oe.Error())
		return
	}
	fmt.Fprintf(os.Stderr, "%s: %v\n", operation, err)
}
