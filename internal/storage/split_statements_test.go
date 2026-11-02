package storage

import (
	"strings"
	"testing"
)

// A trigger's body has semicolons of its own: the script is split after the END that closes it.
func TestSplitStatementsKeepsATriggerWhole(t *testing.T) {
	script := `-- header; with a semicolon
CREATE TABLE a (x INTEGER);
-- the trigger
CREATE TRIGGER IF NOT EXISTS trg AFTER UPDATE OF x ON a
WHEN NEW.x > 0
BEGIN
    UPDATE a SET x = 0 WHERE x = 'END;';
    SELECT 1;
END;
CREATE INDEX i ON a(x);`
	got := splitStatements(script)
	if len(got) != 3 {
		t.Fatalf("%d statements: %q", len(got), got)
	}
	if want := "UPDATE a SET x = 0 WHERE x = 'END;';"; !strings.Contains(got[1], want) || !strings.Contains(got[1], "SELECT 1;") || !strings.HasSuffix(got[1], "END") {
		t.Fatalf("trigger = %q", got[1])
	}
}
