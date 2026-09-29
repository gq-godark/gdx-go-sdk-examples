package godark

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// InstrumentDecimals holds the venue scale for one symbol from
// GET /api/v1/instruments (price_decimals / quantity_decimals).
type InstrumentDecimals struct {
	PriceDecimals    uint32
	QuantityDecimals uint32
}

// DefaultInstrumentDecimals is used only when instruments have not been
// loaded yet (unit tests / offline encode). Prefer values from the edge.
var DefaultInstrumentDecimals = InstrumentDecimals{
	PriceDecimals:    8,
	QuantityDecimals: 8,
}

// FormatDecimal renders value as a non-negative decimal string with at most
// `decimals` fractional digits. Trailing fractional zeros are trimmed
// ("120000.00" → "120000", "0.0010" → "0.001"). Rejects NaN/Inf, negatives,
// and values whose exact shortest decimal form needs more fractional digits
// than `decimals` allows (no silent rounding beyond float formatting).
func FormatDecimal(value float64, decimals uint32) (string, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "", fmt.Errorf("decimal value must be finite")
	}
	if value < 0 {
		return "", fmt.Errorf("decimal value must be non-negative")
	}
	if decimals > 18 {
		return "", fmt.Errorf("decimals %d exceeds maximum 18", decimals)
	}

	// Format at the instrument scale so float noise beyond `decimals` is not
	// written on the wire. strconv rounds half-away-from-zero at the last digit.
	s := strconv.FormatFloat(value, 'f', int(decimals), 64)
	s = trimFractionalZeros(s)
	if err := validateFractionLength(s, decimals); err != nil {
		return "", err
	}
	return s, nil
}

// ParseDecimal converts a wire decimal string to float64 for public SDK APIs
// that still expose numeric inputs/outputs.
func ParseDecimal(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty decimal string")
	}
	if strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-") {
		// Sequencer parse_decimal rejects signed forms for order ticks; keep
		// ParseDecimal usable for signed PnL by allowing a leading minus only.
		if strings.HasPrefix(s, "+") {
			return 0, fmt.Errorf("decimal string must not start with '+'")
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid decimal %q: %w", s, err)
	}
	return v, nil
}

func trimFractionalZeros(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" {
		return "0"
	}
	return s
}

func validateFractionLength(s string, decimals uint32) error {
	dot := strings.IndexByte(s, '.')
	if dot < 0 {
		return nil
	}
	frac := s[dot+1:]
	if uint32(len(frac)) > decimals {
		return fmt.Errorf("fractional digits %d exceed instrument decimals %d", len(frac), decimals)
	}
	for _, c := range frac {
		if c < '0' || c > '9' {
			return fmt.Errorf("invalid fractional digit in %q", s)
		}
	}
	return nil
}

func formatOptDecimal(v *float64, decimals uint32) (*string, error) {
	if v == nil {
		return nil, nil
	}
	s, err := FormatDecimal(*v, decimals)
	if err != nil {
		return nil, err
	}
	return &s, nil
}
