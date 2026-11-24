package crashreport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/testhome"
)

func TestScrubRemovesHomeEmailsAndTokens(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	// Built at run time: these are synthetic, not credentials.
	skKey := "sk" + "-" + strings.Repeat("a", 24)
	ghKey := "gh" + "p_" + strings.Repeat("b", 36)
	bearer := strings.Repeat("c", 22)
	in := "panic: open " + home + "/.monoagent/x: denied for bob@example.com\n" +
		"Authorization: Bearer " + bearer + "\n" +
		"url https://h.example/p?access_token=zzTOPSECRET99&x=1 key " + skKey + " " + ghKey + "\n" +
		"\t" + home + "/go/src/x.go:12 +0x1"
	out := Scrub(in)
	for _, leak := range []string{home, "bob@example.com", bearer, "zzTOPSECRET99", skKey, ghKey} {
		if strings.Contains(out, leak) {
			t.Errorf("scrubbed text still has %q:\n%s", leak, out)
		}
	}
	if !strings.Contains(out, "~/.monoagent/x") || !strings.Contains(out, "x.go:12") {
		t.Errorf("scrub removed too much:\n%s", out)
	}
}

func TestReportWritesScrubbedLocalLog(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv("MONOAGENT_CRASH_REPORT", "")
	Report("failed for "+home+"/secret.txt carol@example.org", []byte("at "+home+"/a.go:1"), Build{Version: "v1"})
	entries, err := os.ReadDir(filepath.Join(home, ".monoagent", "crashes"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("crash log: %v %d", err, len(entries))
	}
	b, _ := os.ReadFile(filepath.Join(home, ".monoagent", "crashes", entries[0].Name()))
	if strings.Contains(string(b), home) || strings.Contains(string(b), "carol@example.org") {
		t.Errorf("local crash log leaks:\n%s", b)
	}
}
