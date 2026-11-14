package coderhost

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// ErrBaselineChanged is the refusal of a compare-and-set write whose expected settings are no longer the stored ones.
var ErrBaselineChanged = errors.New("the session's settings changed since they were read; read them again and redo the change")

// SettingsDigest is a stable hash of the settings as the host reads them (a map is written with its keys sorted): what a
// writer that judged a change against some settings passes back (`settings set --expect`) so that the write is refused if
// the stored settings are not those any more.
func SettingsDigest(s Settings) string {
	raw, _ := json.Marshal(s) // a Settings always marshals
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// AutonomyAfterPatch is the autonomy part of Settings.Apply for the keys of a patch, for a caller that must judge a patch by its
// result without writing it: a patched autonomy_preset replaces the stored one (null leaves it), a patched autonomy_classes
// REPLACES the stored map (null empties it). The property test in settings_cas_test.go holds it to Apply.
func AutonomyAfterPatch(cur Settings, keys map[string]json.RawMessage) (Settings, error) {
	next := cur
	if raw, ok := keys["autonomy_preset"]; ok {
		if err := json.Unmarshal(raw, &next.AutonomyPreset); err != nil {
			return cur, err
		}
	}
	if raw, ok := keys["autonomy_classes"]; ok {
		next.AutonomyClasses = map[string]AutonomyChoice{}
		if err := json.Unmarshal(raw, &next.AutonomyClasses); err != nil {
			return cur, err
		}
	}
	return next, nil
}

// AutonomyWidens says whether after makes any kind of question decided by someone less strict than before does
// (user < master < model, park < take_default), in any combination of reversibility and master.
func AutonomyWidens(before, after Settings) bool {
	rank := func(d Decision) int {
		return map[string]int{WhoUser: 0, WhoMaster: 1, WhoModel: 2}[d.Answerer]*2 + map[string]int{FallbackPark: 0, FallbackTakeDefault: 1}[d.Fallback]
	}
	for _, class := range GapClasses {
		for _, reversible := range []bool{false, true} {
			for _, hasMaster := range []bool{false, true} {
				if rank(Decide(after, class, reversible, hasMaster)) > rank(Decide(before, class, reversible, hasMaster)) {
					return true
				}
			}
		}
	}
	return false
}
