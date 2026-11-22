package util

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ConvertAbbreviatedNumber converts a human-readable abbreviated number string
// into an int64. Supported formats:
//
//   - Suffix notation: "12.5K" -> 12500, "1.2M" -> 1200000, "3B" -> 3000000000
//   - Comma-separated: "1,234" -> 1234, "1,234,567" -> 1234567
//   - Plain numbers:   "42" -> 42, "3.7" -> 3 (truncated)
//
// Suffixes are case-insensitive: K/k (thousand), M/m (million), B/b (billion).
func ConvertAbbreviatedNumber(value string) (int64, error) {
	if value == "" {
		return 0, fmt.Errorf("empty string")
	}

	// Trim whitespace.
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("empty string after trimming")
	}

	// Detect suffix multiplier.
	var multiplier float64 = 1
	last := value[len(value)-1]

	switch last {
	case 'K', 'k':
		multiplier = 1_000
		value = value[:len(value)-1]
	case 'M', 'm':
		multiplier = 1_000_000
		value = value[:len(value)-1]
	case 'B', 'b':
		multiplier = 1_000_000_000
		value = value[:len(value)-1]
	}

	// Remove commas (e.g. "1,234,567").
	value = strings.ReplaceAll(value, ",", "")

	if value == "" {
		return 0, fmt.Errorf("no numeric value found")
	}

	num, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("unable to parse %q as number: %w", value, err)
	}

	result := num * multiplier
	if math.IsNaN(result) || math.IsInf(result, 0) || math.Abs(result) >= math.MaxInt64 {
		return 0, fmt.Errorf("%q is not a number in range", value)
	}

	// Round to nearest integer to avoid floating-point drift
	// (e.g. 12.5 * 1000 should be exactly 12500).
	return int64(math.Round(result)), nil
}
