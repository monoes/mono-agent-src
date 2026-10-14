package nodes

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestActionLoggerUsesSharedLogger checks that MONOAGENT_DEBUG routes the
// action executor's events through the root slog logger (and at debug level,
// whatever the root level is), and that without it nothing is written.
func TestActionLoggerUsesSharedLogger(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	t.Setenv("MONOAGENT_DEBUG", "")
	l := actionLogger()
	l.Warn().Msg("quiet-event")
	if buf.Len() != 0 {
		t.Fatalf("without MONOAGENT_DEBUG the logger wrote %q", buf.String())
	}

	t.Setenv("MONOAGENT_DEBUG", "1")
	l = actionLogger()
	l.Debug().Str("step", "click").Msg("debug-event")
	out := buf.String()
	if !strings.Contains(out, "debug-event") || !strings.Contains(out, "step=click") {
		t.Fatalf("shared logger output = %q, want the debug event with its field", out)
	}
}
