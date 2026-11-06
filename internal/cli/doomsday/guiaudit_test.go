package doomsday

import (
	"strings"
	"testing"
)

// The audit row of a start records how it was acknowledged, and `gui` only when the hidden --gui-phrase flag was used:
// a terminal's typed phrase or a script's full hash is never recorded as gui.
func TestAuditRowRecordsGUIOnlyForTheGUIFlag(t *testing.T) {
	for name, c := range map[string]struct {
		args  func(h string) []string
		stdin func(h string) string
		want  string
	}{
		"gui":      {func(h string) []string { return []string{"--gui-phrase", Phrase(0, h)} }, func(string) string { return "" }, "ack=gui"},
		"script":   {func(h string) []string { return []string{"--yes-i-lift", "--hash", h} }, func(string) string { return "" }, "ack=script"},
		"terminal": {func(string) []string { return nil }, func(h string) string { return Phrase(0, h) + "\n" }, "ack=terminal"},
	} {
		t.Run(name, func(t *testing.T) {
			w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
			w.charter = writeCharter(t, guiCharter)
			h := fullHash(t, w.rig, w.charter)
			w.rig.terminal = name == "terminal"
			out, err := run(t, w.deps, c.stdin(h), append([]string{"start", "--charter", w.charter}, c.args(h)...)...)
			if err != nil {
				t.Fatal(err, out)
			}
			var detail string
			rows, err := db.DB.Query(`SELECT payload FROM doomsday_audit WHERE kind = 'start'`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			for rows.Next() {
				var p []byte
				if err := rows.Scan(&p); err != nil {
					t.Fatal(err)
				}
				detail += string(p)
			}
			if !strings.Contains(detail, c.want) {
				t.Fatalf("the audit row does not say %s: %q", c.want, detail)
			}
			if name != "gui" && strings.Contains(detail, "ack=gui") {
				t.Fatalf("a %s start was recorded as gui: %q", name, detail)
			}
		})
	}
}
