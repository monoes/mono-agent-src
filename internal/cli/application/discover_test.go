package application

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/testdb"
)

func TestApplicationDiscoverRejectsUnknownSource(t *testing.T) {
	t.Parallel()
	dbPath := testdb.Path(t)
	_, err := run(t, dbPath, true, "discover", "--keywords", "engineer", "--source", "does-not-exist")
	if err == nil {
		t.Fatal("expected error for unknown source, got nil")
	}
}

func TestApplicationDiscoverRequiresKeywords(t *testing.T) {
	t.Parallel()
	dbPath := testdb.Path(t)
	_, err := run(t, dbPath, true, "discover")
	if err == nil {
		t.Fatal("expected error for missing --keywords, got nil")
	}
	if !strings.Contains(err.Error(), "keywords") {
		t.Fatalf("expected error to mention keywords, got: %v", err)
	}
}
