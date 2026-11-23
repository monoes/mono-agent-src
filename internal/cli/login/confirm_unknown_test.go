package login

import (
	"bytes"
	"strings"
	"testing"

	browserpkg "github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/testhome"
)

// When the tab does not name the account, the session is stored as "unknown". A second confirm would
// overwrite the first one's cookies unannounced, so it needs --replace.
func TestLoginConfirmWithAnUnknownAccountDoesNotOverwriteSilently(t *testing.T) {
	_, dbPath := newTestDB(t)
	d := testDeps(dbPath, "default", false)
	testhome.SetHome(t, t.TempDir())
	if err := saveLoginTabID("default", "PRODUCTHUNT", 7); err != nil {
		t.Fatal(err)
	}
	bridge := &tabBridge{url: "https://www.producthunt.com/"}
	d.ConnectBridge = func(string) (browserpkg.ExtensionBridge, error) { return bridge, nil }
	run := func(extra ...string) error {
		cmd := d.newLoginCmd()
		cmd.SetArgs(append([]string{"confirm", "producthunt"}, extra...))
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		return cmd.Execute()
	}
	if err := run(); err != nil {
		t.Fatalf("first confirm: %v", err)
	}
	err := run()
	if err == nil || !strings.Contains(err.Error(), "--replace") {
		t.Fatalf("second confirm of an unnamed account: %v; want a refusal naming --replace", err)
	}
	if err := run("--replace"); err != nil {
		t.Fatalf("confirm --replace: %v", err)
	}
}
