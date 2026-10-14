package application

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/apply"
	browserpkg "github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/documents"
	"github.com/monoes/mono-agent/internal/testdb"
)

func writeDataFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data.json")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestApplicationApplyAutoModeSkipsPrompt(t *testing.T) {
	origPDF := documents.RenderPDFFunc
	documents.RenderPDFFunc = func(ctx context.Context, html string) ([]byte, error) { return []byte("%PDF-fake"), nil }
	t.Cleanup(func() { documents.RenderPDFFunc = origPDF })

	// This test's assertion is about auto-mode prompt suppression, not
	// browser mechanics (TestOpenForApplicationUsesExtensionBridge in
	// internal/apply covers that separately) -- fake out both the extension
	// bridge lookup and the open call so the test doesn't depend on a real
	// MonoAgent extension/Chrome being connected in whatever environment
	// runs this suite.

	origOpen := apply.OpenForApplicationFunc
	apply.OpenForApplicationFunc = func(ctx context.Context, jobURL string, bridge browserpkg.ExtensionBridge) error { return nil }
	t.Cleanup(func() { apply.OpenForApplicationFunc = origOpen })

	dbPath := homeDB(t)
	d := testDeps(dbPath, true)
	d.Bridge = func(string) (browserpkg.ExtensionBridge, error) { return nil, nil }
	d.Confirm = func(string) bool {
		t.Fatal("auto mode must never call the confirmation prompt")
		return false
	}
	addOut, err := runWith(t, d, "add", "--kind", "job", "--title", "Backend Engineer", "--company", "Acme", "--url", "about:blank")
	if err != nil {
		t.Fatalf("application add: %v (%s)", err, addOut)
	}
	var id string
	for _, tok := range strings.Split(addOut, `"`) {
		if len(tok) == 36 && strings.Count(tok, "-") == 4 {
			id = tok
			break
		}
	}

	cvFile := writeDataFile(t, `{"name":"Jane Doe"}`)
	letterFile := writeDataFile(t, `{"senderName":"Jane Doe"}`)
	out, err := runWith(t, d, "apply", id, "--mode", "auto", "--cv-data-file", cvFile, "--cover-letter-data-file", letterFile)
	if err != nil {
		t.Fatalf("application apply: %v (%s)", err, out)
	}
}

func TestApplicationApplyConfirmModeDeclined(t *testing.T) {
	t.Parallel()
	dbPath := testdb.Path(t)
	d := testDeps(dbPath, true)
	d.Confirm = func(string) bool { return false }
	addOut, err := runWith(t, d, "add", "--kind", "job", "--title", "Backend Engineer", "--company", "Acme", "--url", "about:blank")
	if err != nil {
		t.Fatalf("application add: %v (%s)", err, addOut)
	}
	var id string
	for _, tok := range strings.Split(addOut, `"`) {
		if len(tok) == 36 && strings.Count(tok, "-") == 4 {
			id = tok
			break
		}
	}

	cvFile := writeDataFile(t, `{"name":"Jane Doe"}`)
	letterFile := writeDataFile(t, `{"senderName":"Jane Doe"}`)
	out, err := runWith(t, d, "apply", id, "--mode", "confirm", "--cv-data-file", cvFile, "--cover-letter-data-file", letterFile)
	if err != nil {
		t.Fatalf("application apply (declined): %v (%s)", err, out)
	}
	if !strings.Contains(out, "cancelled") && !strings.Contains(out, "Cancelled") {
		t.Fatalf("expected a cancellation message when the confirm prompt is declined, got: %s", out)
	}
}

func TestApplicationSendTransitionsToApplied(t *testing.T) {
	t.Parallel()
	dbPath := testdb.Path(t)
	addOut, err := run(t, dbPath, true, "add", "--kind", "job", "--title", "Backend Engineer", "--company", "Acme", "--url", "about:blank")
	if err != nil {
		t.Fatalf("application add: %v (%s)", err, addOut)
	}
	var id string
	for _, tok := range strings.Split(addOut, `"`) {
		if len(tok) == 36 && strings.Count(tok, "-") == 4 {
			id = tok
			break
		}
	}

	sendOut, err := run(t, dbPath, true, "send", id)
	if err != nil {
		t.Fatalf("application send: %v (%s)", err, sendOut)
	}

	getOut, err := run(t, dbPath, true, "get", id)
	if err != nil {
		t.Fatalf("application get: %v", err)
	}
	if !strings.Contains(getOut, "applied") {
		t.Fatalf("expected status applied after send, got: %s", getOut)
	}
}
