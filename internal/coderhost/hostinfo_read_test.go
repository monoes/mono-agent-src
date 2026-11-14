package coderhost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func hostJSONAt(t *testing.T, write func(path string)) string {
	t.Helper()
	db := filepath.Join(t.TempDir(), "monoagent.db")
	if err := os.MkdirAll(HostDir(db), 0o700); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(HostDir(db), HostInfoFile))
	return db
}

// host.json is display-only and any process of the user can write it: the reader hands out nothing forged, and never reads a device.
func TestReadHostPursuitNeverHandsOutForgedOrUnreadableFacts(t *testing.T) {
	put := func(doc string) func(string) {
		return func(p string) {
			if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, c := range map[string]struct {
		write   func(string)
		invalid bool
	}{
		"a symlink to /dev/zero": {func(p string) {
			if err := os.Symlink("/dev/zero", p); err != nil {
				t.Skip("no symlinks here")
			}
		}, true},
		"a directory":             {func(p string) { _ = os.Mkdir(p, 0o700) }, true},
		"an oversize file":        {put(`{"pursuit":{"reason":"` + strings.Repeat("x", maxHostInfoBytes) + `"}}`), true},
		"a bidi reason":           {put(`{"pursuit":{"reason":"fine ‮ evil"}}`), true},
		"a bad wake cap":          {put(`{"pursuit":{"enabled":true,"pin":"a/b","wakeCapUsd":1e300}}`), true},
		"a forged claim of on":    {put(`{"pursuit":{"enabled":true,"pin":"","wakeCapUsd":2}}`), true},
		"an unknown missing code": {put(`{"pursuit":{"missing":"free-money"}}`), true},
		"a plain valid document":  {put(`{"pursuit":{"enabled":true,"pin":"a/b","wakeCapUsd":2,"pinSource":"env"}}`), false},
		"a valid idle document":   {put(`{"pursuit":{"missing":"judge-pin","reason":"judge-pin missing: run ` + "`x`" + `"}}`), false},
	} {
		t.Run(name, func(t *testing.T) {
			info, found := ReadHostPursuit(hostJSONAt(t, c.write))
			if !found {
				t.Fatal("not found")
			}
			if c.invalid {
				if !info.Invalid || info.Reason != "" || info.Pin != "" || info.WakeCapUSD != 0 || info.Missing != "" || ValidHostPursuitInfo(info) {
					t.Fatalf("an unusable file leaked: %+v", info)
				}
			} else if info.Invalid {
				t.Fatalf("a valid file was refused: %+v", info)
			}
		})
	}
}

func TestSanitizeKeepsOnlyTheClaimOfOn(t *testing.T) {
	got := SanitizeHostPursuit(PursuitHostInfo{Enabled: true, Pin: "no-slash", Reason: "x", WakeCapUSD: 5})
	if !got.Invalid || !got.Enabled || got.Pin != "" || got.Reason != "" || got.WakeCapUSD != 0 {
		t.Fatalf("%+v", got)
	}
}
