package applyfill

import (
	"testing"

	"github.com/monoes/mono-agent/internal/applypark"
)

func TestFillTaskKindMatchesApplypark(t *testing.T) {
	if applypark.FillTaskKind != Kind {
		t.Fatalf("applypark.FillTaskKind = %q, want %q", applypark.FillTaskKind, Kind)
	}
}
