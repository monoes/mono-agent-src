package control

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
)

// The "logic" of removeItemsRepeatedWithinPreviousExecutions, after n8n's
// Remove Duplicates v2. The two high-water-mark logics keep one stored
// maximum per scope instead of a history of values.
const (
	logicSeenValues  = "removeItemsWithAlreadySeenKeyValues"
	logicIncremental = "removeItemsUpToStoredIncrementalKey"
	logicDate        = "removeItemsUpToStoredDate"
)

// watermarkKeys are the static-data keys of the stored maximums.
var watermarkKeys = map[string]string{
	logicIncremental: "remove_duplicates:max_incremental",
	logicDate:        "remove_duplicates:max_date",
}

// removeUpToStored keeps the items whose dedupe value is above the maximum
// stored by earlier runs (a number for logicIncremental, a date for
// logicDate) and stores the new maximum. The first run keeps every item.
// Items are compared with the previous runs' maximum only, so two items of
// one run with the same new value both pass.
func removeUpToStored(ctx context.Context, input workflow.NodeInput, config map[string]interface{}, logic string) ([]workflow.NodeOutput, error) {
	scope, err := dedupeScope(config)
	if err != nil {
		return nil, err
	}
	sd, err := workflow.StaticDataFor(ctx, input, scope)
	if err != nil {
		return nil, fmt.Errorf("remove_duplicates: %w", err)
	}
	expr, _ := config["dedupeValue"].(string)
	if expr == "" {
		expr, _ = config["field"].(string)
	}
	if expr == "" {
		return nil, fmt.Errorf("%w: remove_duplicates: %s needs 'dedupeValue'", workflow.ErrInvalidConfig, logic)
	}
	parse := parseIncrementalKey
	if logic == logicDate {
		parse = parseDedupeDate
	}

	key := watermarkKeys[logic]
	var stored float64
	found, err := sd.Get(ctx, key, &stored)
	if err != nil {
		return nil, fmt.Errorf("remove_duplicates: %w", err)
	}
	engine := workflow.NewExpressionEngine()
	max, have := stored, found
	result := make([]workflow.Item, 0, len(input.Items))
	for i, item := range input.Items {
		raw, err := dedupeValueOf(engine, expr, item, input)
		if err != nil {
			return nil, fmt.Errorf("remove_duplicates: item %d: %w", i, err)
		}
		v, err := parse(raw)
		if err != nil {
			return nil, fmt.Errorf("remove_duplicates: item %d: %w", i, err)
		}
		if !found || v > stored {
			result = append(result, item)
		}
		if !have || v > max {
			max, have = v, true
		}
	}
	if have {
		if err := sd.Set(ctx, key, max); err != nil {
			return nil, fmt.Errorf("remove_duplicates: %w", err)
		}
	}
	return []workflow.NodeOutput{{Handle: "main", Items: result}}, nil
}

func parseIncrementalKey(s string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, fmt.Errorf("dedupe value %q is not a number", s)
	}
	return v, nil
}

// dedupeDateLayouts are the date forms removeItemsUpToStoredDate reads.
var dedupeDateLayouts = []string{
	time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05Z07:00", "2006-01-02 15:04:05", "2006-01-02",
	time.RFC1123Z, time.RFC1123, time.RFC850, time.ANSIC,
}

// parseDedupeDate reads a date as Unix microseconds (a float64, exact for
// any date, so the stored maximum has one type for both logics). A date
// without a zone is UTC.
func parseDedupeDate(s string) (float64, error) {
	s = strings.TrimSpace(s)
	for _, layout := range dedupeDateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return float64(t.UnixMicro()), nil
		}
	}
	return 0, fmt.Errorf("dedupe value %q is not a date (use ISO 8601, e.g. 2026-10-14T09:00:00Z)", s)
}

// clearWatermarks forgets the stored maximums of the scope.
func clearWatermarks(ctx context.Context, input workflow.NodeInput, scope workflow.StateScope) error {
	sd, err := workflow.StaticDataFor(ctx, input, scope)
	if err != nil {
		return err
	}
	for _, key := range watermarkKeys {
		if err := sd.Delete(ctx, key); err != nil {
			return err
		}
	}
	return nil
}
