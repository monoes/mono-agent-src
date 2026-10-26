package chat

import (
	"encoding/json"
	"testing"

	"github.com/monoes/mono-agent/internal/dynorg"
	"github.com/monoes/mono-agent/internal/tasks"
	"github.com/monoes/mono-agent/internal/testdb"
)

func TestMissionTurnRaisesOnlyAnUnsetWorkerCap(t *testing.T) {
	load := func(raw string) coderSettings {
		var s coderSettings
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			t.Fatal(err)
		}
		return s.withDefaults()
	}
	confirmed := &dynorg.MissionConfig{Store: nil, Access: "coding", Confirmed: true}
	confirmed.Store = missionStoreForTest(t)
	unconfirmed := &dynorg.MissionConfig{Store: confirmed.Store, Access: "coding"}
	for _, tc := range []struct {
		name string
		raw  string
		m    *dynorg.MissionConfig
		want int
	}{
		{"unset in a mission", `{}`, confirmed, dynorg.MissionMaxAgents},
		{"unset, no mission", `{}`, nil, 6},
		{"unset, unconfirmed", `{}`, unconfirmed, 6},
		{"the operator set the default value", `{"orgMaxAgents":6}`, confirmed, 6},
		{"the operator set a small cap", `{"orgMaxAgents":3}`, confirmed, 3},
		{"the operator set a big cap", `{"orgMaxAgents":20}`, confirmed, 20},
	} {
		if got := missionMaxAgents(load(tc.raw), tc.m); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
	}
}

func missionStoreForTest(t *testing.T) *tasks.Store {
	t.Helper()
	return tasks.NewStore(testdb.Open(t).DB)
}
