package orggroup

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// A cost read that fails must not lift the ceiling: with a budget set, the
// start is refused as unreadable instead of counting the failed org as free.
func TestCheckStartRefusesWhenCostsAreUnreadable(t *testing.T) {
	root := t.TempDir()
	writeGroup(t, root)
	env := costsEnv(root, nil)
	env.Costs = func(context.Context, string, string) (json.RawMessage, error) {
		return nil, errors.New("monomind unavailable")
	}
	err := env.CheckStart(context.Background(), "hq", "growth")
	if err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("CheckStart with unreadable costs = %v, want a refusal naming unreadable costs", err)
	}
}
