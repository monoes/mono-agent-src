package main

import (
	"encoding/json"
	"testing"

	"github.com/monoes/mono-agent/internal/missionpursuit"
)

const (
	pHash   = "9f3a1c7e" + "0123456789abcdef0123456789abcdef0123456789abcdef01234567"
	pDigest = "ab12cd34" + "ef56789aab12cd34ef56789aab12cd34ef56789aab12cd34ef56789a"
	pChat   = "conv-77"
)

var pLimits = PursuitLimits{ParkedMax: 4, Pct: 25, WakesMax: 7}

// pursuitPreviewJSON is a preview as the CLI prints it (the shape of TestPursuitPreviewJSONKeySet in internal/cli/task).
func pursuitPreviewJSON(t *testing.T, edit func(m map[string]any)) string {
	t.Helper()
	m := map[string]any{
		"schema_version": 1, "profile": map[string]any{"id": "work", "name": "work"}, "session_id": 5, "chat_id": pChat,
		"goal": map[string]any{"state": "proposed", "hash": pHash, "criteria": []any{
			map[string]any{"id": "c1", "kind": "rubric", "spec": "the release notes are published", "expected": "yes"}}},
		"limits": map[string]any{"parked_max": pLimits.ParkedMax, "pct": pLimits.Pct, "wakes_max": pLimits.WakesMax}, "rubric_present": true,
		"cost": map[string]any{"rows": []any{
			map[string]any{"key": "mission_hour", "usd": 1.0, "known": true, "text": "One hour on this mission: row one."},
			map[string]any{"key": "host_hour", "usd": 2.0, "known": true, "text": "One hour on this whole computer: row two."},
			map[string]any{"key": "until_stop", "usd": 3.0, "known": true, "text": "Until your away limits stop it: row three."}}, "text": "table"},
		"checker":  map[string]any{"set_up": false},
		"host":     map[string]any{"loop_on": true, "judge_pin": "anthropic/sonnet", "wake_cap_usd": 1.0, "judge_call_cap_usd": 1.0},
		"blockers": []any{}, "phrase_template": missionpursuit.PhraseTemplate, "consent_digest": pDigest,
	}
	if edit != nil {
		edit(m)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func pursuitPhrase(session int64) string {
	return missionpursuit.Phrase(session, pHash, missionpursuit.AwayLimits{ParkedMax: pLimits.ParkedMax, Pct: pLimits.Pct, Wakes: pLimits.WakesMax})
}
