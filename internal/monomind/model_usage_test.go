package monomind

import (
	"encoding/json"
	"testing"
)

// A turn's result carries the counters of its LAST result event, and a counter the runtime left out is marked: its 0
// must not be read as a measured 0 (#1744).
func TestTurnResultKeepsTheLastResultsModelUsage(t *testing.T) {
	var res TurnResult
	apply := func(line string) {
		t.Helper()
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		ApplyEventToResult(&res, ev)
	}
	apply(`{"v":1,"type":"usage","input_tokens":3,"output_tokens":4,"cost_usd":0.4}`)
	if res.ModelUsage != nil {
		t.Fatalf("counters before any result: %+v", res.ModelUsage)
	}
	apply(`{"v":1,"type":"result","cost_usd":0.4,"model_usage":{"opus":{"input":1,"output":2,"cache_read":0,"cache_creation":4},"haiku":{"input":1,"output":2,"cache_read":3}}}`)
	if got, want := res.ModelUsage["opus"], (ModelUsage{Input: 1, Output: 2, CacheCreation: 4}); got != want {
		t.Errorf("opus = %+v, want %+v (a reported 0 is a counter)", got, want)
	}
	if got, want := res.ModelUsage["haiku"], (ModelUsage{Input: 1, Output: 2, CacheRead: 3, Partial: true}); got != want {
		t.Errorf("haiku = %+v, want %+v", got, want)
	}
	apply(`{"v":1,"type":"result","cost_usd":0.5}`)
	if res.ModelUsage != nil {
		t.Errorf("a result with no counters kept an earlier one's beside its cost: %+v", res.ModelUsage)
	}
}
