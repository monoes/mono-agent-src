package nodes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// tokenSource serves acme/use_token: one bot call with the automation's
// {{secret:token}}.
type tokenSource struct{}

func (tokenSource) Load(a, t string) ([]byte, error) {
	if a != "acme" || t != "use_token" {
		return nil, fmt.Errorf("not found %s/%s", a, t)
	}
	return []byte(`{"actionType":"use_token","automation":"acme","sideEffects":"read",
	  "steps":[{"id":"use","type":"call_bot_method","methodName":"use","args":["{{secret:token}}"],
	    "onError":{"action":"abort"}}]}`), nil
}
func (tokenSource) List() ([]string, error)              { return []string{"acme/use_token"}, nil }
func (tokenSource) Package(string) action.PackageContext { return confirmedPkg{outPkg{native: "acme"}} }

// tokenBot records the token it was given; with needsToken it fails without one.
type tokenBot struct {
	needsToken bool
	got        []string
}

func (b *tokenBot) GetMethodByName(name string) (func(context.Context, ...interface{}) (interface{}, error), bool) {
	if name != "use" {
		return nil, false
	}
	return func(_ context.Context, args ...interface{}) (interface{}, error) {
		tok, _ := args[len(args)-1].(string)
		b.got = append(b.got, tok)
		if b.needsToken && tok == "" {
			return nil, errors.New("signed out")
		}
		return map[string]interface{}{"ok": true}, nil
	}, true
}

// A {{secret:}} lookup an unapproved run may not do reads nothing, and the
// user is told why: in the error of a step that failed without it, or on
// stderr when it went on. The secret is named, never its value (#845 code
// review, 4). The user's own run gets the value.
func TestARefusedSecretLookupSaysWhy(t *testing.T) {
	keyring.MockInit()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(workflow.WithNodeState(context.Background(), nil), db.DB), "default")
	if _, err := secrets.Add(ctx, db.DB, "default", "secret", "automation:acme/token", map[string]string{"secret": "vsecret-777"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	bot := &tokenBot{needsToken: true}
	setupSend(t, nil)
	action.SetDefSource(tokenSource{})
	SetGlobalBotRegistry(botRegistry{bot: bot})
	in := sendInput(workflow.NewItem(map[string]interface{}{}))
	const why = "{{secret:token}}: @secret:automation:acme/token is not read from the vault: the chat, an agent or another workflow wrote or changed this workflow"

	_, err = NewBrowserNode("acme", "use_token").Execute(workflow.WithAIAuthored(ctx), in, map[string]interface{}{})
	if err == nil || !strings.Contains(err.Error(), "signed out") || !strings.Contains(err.Error(), why) || strings.Contains(err.Error(), "vsecret-777") {
		t.Errorf("an unapproved run's failed step: %v, want it to say %q", err, why)
	}

	bot.needsToken = false
	stderr := captureStderr(t, func() {
		if _, err := NewBrowserNode("acme", "use_token").Execute(workflow.WithAIAuthored(ctx), in, map[string]interface{}{}); err != nil {
			t.Errorf("an unapproved run's step that needs no token: %v", err)
		}
	})
	if !strings.Contains(stderr, why) || strings.Contains(stderr, "vsecret-777") {
		t.Errorf("stderr of a step that went on without the secret: %q, want it to say %q", stderr, why)
	}

	if _, err := NewBrowserNode("acme", "use_token").Execute(ctx, in, map[string]interface{}{}); err != nil {
		t.Fatalf("the user's run: %v", err)
	}
	if bot.got[0] != "" || bot.got[1] != "" || bot.got[2] != "vsecret-777" {
		t.Errorf("tokens given = %q, want none in the unapproved runs and the value in the user's", bot.got)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	os.Stderr = prev
	w.Close()
	return <-done
}
