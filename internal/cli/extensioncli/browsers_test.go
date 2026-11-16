package extensioncli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/extension"
)

var testInfos = []extension.ConnInfo{
	{Instance: "3f2a91c0-0000-4000-8000-000000000001", Label: "Edge Work", Profile: "p-work", Version: "1.5.0", ConnectedAt: time.Date(2026, 9, 27, 14, 2, 0, 0, time.UTC)},
	{Instance: "3f2a0000-0000-4000-8000-000000000002", Label: "Chrome Home", Version: "1.5.0"},
	{Instance: "legacy", Legacy: true},
}

func TestMatchBrowser(t *testing.T) {
	cases := map[string]string{
		"3f2a91c0-0000-4000-8000-000000000001": "3f2a91c0-0000-4000-8000-000000000001",
		"3f2a91":                               "3f2a91c0-0000-4000-8000-000000000001",
		"chrome home":                          "3f2a0000-0000-4000-8000-000000000002",
	}
	for q, want := range cases {
		got, err := matchBrowser(testInfos, q)
		if err != nil || got.Instance != want {
			t.Errorf("matchBrowser(%q) = %q, %v; want %q", q, got.Instance, err, want)
		}
	}
	if _, err := matchBrowser(testInfos, "3f2a"); err == nil || !strings.Contains(err.Error(), "matches 2 browsers") {
		t.Errorf("ambiguous prefix: err = %v", err)
	}
	if _, err := matchBrowser(testInfos, "nope"); err == nil {
		t.Error("unknown browser must fail")
	}
	if _, err := matchBrowser(testInfos, "3f"); err == nil {
		t.Error("a prefix under 4 characters must not match")
	}
}

func TestBrowsersFlagAStaleExtension(t *testing.T) {
	infos := []extension.ConnInfo{
		{Instance: "aaaa0000-0000-4000-8000-000000000001", Label: "Old", Version: "1.5.0"},
		{Instance: "bbbb0000-0000-4000-8000-000000000002", Label: "New", Version: extension.MinExtension},
		{Instance: "cccc0000-0000-4000-8000-000000000003", Label: "Blank"},
	}
	rows := browserRows(infos, nil)
	if !rows[0].Outdated || rows[1].Outdated || !rows[2].Outdated || rows[0].Warning == "" || rows[1].Warning != "" {
		t.Fatalf("rows = %+v", rows)
	}
	var out bytes.Buffer
	printBrowserTable(&out, rows)
	if !strings.Contains(out.String(), "older than "+extension.MinExtension) {
		t.Errorf("table does not warn:\n%s", out.String())
	}
	w := outdatedWarnings(infos)
	if len(w) != 2 || !strings.Contains(w[0], "Old") || !strings.Contains(w[0], "update") {
		t.Errorf("warnings = %q", w)
	}
}

func TestBrowserRowsAndTable(t *testing.T) {
	rows := browserRows(testInfos, map[string]string{"p-work": "Work"})
	if rows[0].ProfileName != "Work" || rows[1].ProfileID != "" || !rows[2].Legacy {
		t.Fatalf("rows = %+v", rows)
	}
	var out bytes.Buffer
	printBrowserTable(&out, rows)
	s := out.String()
	for _, want := range []string{"Edge Work", "Work", "any profile", "3f2a91c0", "too old to bind"} {
		if !strings.Contains(s, want) {
			t.Errorf("table missing %q:\n%s", want, s)
		}
	}
}
