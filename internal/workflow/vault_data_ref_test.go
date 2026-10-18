package workflow

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/vault"
)

// A webhook's body can't choose what an approved workflow reads from the
// vault: "@img-1" in a field an expression fills is refused, naming the
// field, and so is a credential_id that came from the body, since data must
// not choose the saved connection a step uses. The workflow's own @img-1
// resolves to the image's file and its own credential_id stays (#845 review:
// data-chosen vault entries).
func TestAWebhookCantChooseAVaultImageOrAConnection(t *testing.T) {
	db, sqlStore := approvalsTestDB(t)
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db.DB), "default")
	if _, err := db.DB.Exec(`INSERT INTO vault_images (id, seq, path, filename, profile_id) VALUES ('img-1', 1, '/vault/passport.png', 'passport.png', 'default')`); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		config, body map[string]interface{}
		want         string
	}{
		"an image the body names": {map[string]interface{}{"file": "{{ $json.body.f }}"}, map[string]interface{}{"f": "@img-1"},
			`"file": ` + vault.ErrDataReference.Error()},
		"an image the expression builds": {map[string]interface{}{"file": "@img-{{ $json.body.n }}"}, map[string]interface{}{"n": "1"},
			`"file": ` + vault.ErrDataReference.Error()},
		"a connection the body names": {map[string]interface{}{"credential_id": "{{ $json.body.c }}"}, map[string]interface{}{"c": "conn-1"},
			`"credential_id": the saved connection must be written in the workflow, not taken from its data`},
	} {
		var got []map[string]interface{}
		err := runHookedWorkflow(t, ctx, sqlStore, &got, c.config, c.body)
		if err == nil || !strings.Contains(err.Error(), c.want) || len(got) != 0 {
			t.Errorf("%s: %v (the step ran with %v), want %q", name, err, got, c.want)
		}
		if strings.Contains(fmt.Sprint(err), "passport") {
			t.Errorf("%s: the refusal names the file: %v", name, err)
		}
	}

	var got []map[string]interface{}
	// Text that mentions "@img-" in the middle is no reference: it runs as text.
	err := runHookedWorkflow(t, ctx, sqlStore, &got, map[string]interface{}{"file": "@img-1", "credential_id": "conn-1", "msg": "{{ $json.body.m }}"},
		map[string]interface{}{"m": "see @img-1 and x@img-2"})
	if err != nil {
		t.Fatalf("the workflow's own image and connection: %v", err)
	}
	if len(got) != 1 || got[0]["file"] != "/vault/passport.png" || got[0]["credential_id"] != "conn-1" || got[0]["msg"] != "see @img-1 and x@img-2" {
		t.Errorf("the workflow's own image and connection: the step ran with %v", got)
	}
}
