package mcp

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/apikeys"
)

// A key is never printed, not even a throwaway one in a failing test: whatever a
// failure message shows of a tool's answer goes through scrubbed.
var keyRE = regexp.MustCompile(`(?i)sk-ma-[A-Za-z0-9_-]{43}`)

func scrubbed(s string) string { return keyRE.ReplaceAllString(s, "sk-ma-<redacted>") }

// createKey makes a key of the server's profile in the store, as the user's `api key
// create` does (api_key_create makes none), and returns its id and the key itself.
func createKey(t *testing.T, s *Server, name string, withContext bool) (id, secret string) {
	t.Helper()
	ctx, _, err := s.scopeCall(context.Background(), rawWithProfile(serverTestProfile(s), nil))
	if err != nil {
		t.Fatal(err)
	}
	store, profileID, err := s.apiKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	k, secret, err := store.Create(ctx, profileID, name, withContext)
	if err != nil {
		t.Fatal(err)
	}
	return k.ID, secret
}

func keyRows(t *testing.T, dbPath string) int {
	t.Helper()
	var n int
	if err := sideDB(t, dbPath).DB.QueryRow(`SELECT COUNT(*) FROM api_keys`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func sortedFields(m map[string]any) []string {
	fields := []string{}
	for f := range m {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	return fields
}

// The three tools that change something exist only with --allow-mutations, like the
// other mutating tools: omitted from tools/list, and refused by name without
// having done anything.
func TestAPIMutatingToolsAreGatedLikeTheOtherMutatingTools(t *testing.T) {
	mutating := []string{"api_key_create", "api_key_update", "api_key_revoke"}

	closed, _ := newAPIKeyServer(t, false)
	names := toolsListNames(t, closed)
	for _, n := range mutating {
		if names[n] {
			t.Errorf("%s changes keys and must not be listed without --allow-mutations", n)
		}
	}
	open, _ := newAPIKeyServer(t, true)
	names = toolsListNames(t, open)
	for _, n := range mutating {
		if !names[n] {
			t.Errorf("%s must be listed with --allow-mutations", n)
		}
	}
}

func TestAPIMutatingToolsAreRefusedByNameWithoutTheFlagAndDoNothing(t *testing.T) {
	s, dbPath := newAPIKeyServer(t, false)
	side := apikeys.NewStore(sideDB(t, dbPath).DB)
	existing, _, err := side.Create(context.Background(), "default", "existing", false)
	if err != nil {
		t.Fatal(err)
	}

	for name, args := range map[string]map[string]any{
		"api_key_create": {"name": "sneaky"},
		"api_key_update": {"id": existing.ID, "name": "renamed", "context": true},
		"api_key_revoke": {"id": existing.ID},
	} {
		text, err := callAPITool(t, s, name, args)
		if err == nil || !strings.Contains(err.Error(), "--allow-mutations") {
			t.Errorf("%s without the flag: %q, %v; want a refusal that names --allow-mutations", name, scrubbed(text), err)
		}
	}

	keys, err := side.List(context.Background(), "default", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].Name != "existing" || keys[0].Context || keys[0].RevokedAt != nil {
		t.Errorf("a refused call must change nothing, but the keys are now %+v", keys)
	}
}

func TestAPIMutatingToolAnnotations(t *testing.T) {
	want := map[string]map[string]bool{
		"api_key_create": {"readOnlyHint": false},
		"api_key_update": {"readOnlyHint": false},
		"api_key_revoke": {"readOnlyHint": false, "destructiveHint": true},
	}
	got := map[string]map[string]bool{}
	for _, def := range toolDefinitions(true) {
		if name := def["name"].(string); want[name] != nil {
			got[name], _ = def["annotations"].(map[string]bool)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("annotations\n got %v\nwant %v", got, want)
	}
}

// The description tells the model the rule of a name, in the store's own words, so
// that it cannot drift from what the store refuses.
func TestAPIKeyCreateDescriptionCarriesTheStoresNameRule(t *testing.T) {
	for _, tl := range apiTools() {
		if tl.name == "api_key_create" {
			if d := apiToolText(tl); !strings.Contains(d, apikeys.ErrInvalidName.Error()) {
				t.Errorf("the description of api_key_create lacks the store's name rule %q", apikeys.ErrInvalidName.Error())
			}
			return
		}
	}
	t.Fatal("api_key_create is not listed")
}

// The key tools change no key, for any caller: whoever calls an MCP tool is an
// AI, and a key signs in as the profile, so only the user makes one, changes
// what it reaches or revokes it. They stay listed, a call is told what the user
// runs instead, and nothing is stored, changed or comes back.
func TestTheKeyToolsChangeNoKey(t *testing.T) {
	s, dbPath := newAPIKeyServer(t, true)
	id, secret := createKey(t, s, "app", false)
	names := toolsListNames(t, s)
	for tool, c := range map[string]struct {
		args []map[string]any
		want string
	}{
		"api_key_create": {[]map[string]any{{"name": "made"}, {"name": "made", "context": true}, nil},
			"api_key_create makes no key: only the user creates an API key, which signs in as this profile: `monoagentcli --profile default api key create --name <name>` in their own terminal, or the app"},
		"api_key_update": {[]map[string]any{{"id": id, "name": "renamed"}, {"id": "app", "context": true}, nil},
			"api_key_update changes no key: only the user changes what an API key reaches: `monoagentcli --profile default api key update <id|name>` in their own terminal, or the app"},
		"api_key_revoke": {[]map[string]any{{"id": id}, {"id": "app"}, nil},
			"api_key_revoke revokes no key: only the user revokes an API key, which cuts off whatever signs in with it: `monoagentcli --profile default api key revoke <id|name>` in their own terminal, or the app"},
	} {
		if !names[tool] {
			t.Errorf("%s must stay listed, so that a call is told who changes keys", tool)
		}
		for _, args := range c.args {
			text, err := callAPITool(t, s, tool, args)
			if err == nil || err.Error() != c.want || keyRE.MatchString(text) {
				t.Errorf("%s %v: %q, %v; want %q", tool, args, scrubbed(text), err, c.want)
			}
		}
	}

	if n := keyRows(t, dbPath); n != 1 {
		t.Errorf("%d keys stored, want the one the user made", n)
	}
	keys := decodeKeys(t, mustCall(t, s, "api_key_list", map[string]any{"include_revoked": true}))
	if len(keys) != 1 || keys[0]["id"] != id || keys[0]["name"] != "app" || keys[0]["context"] != false || keys[0]["revoked_at"] != nil {
		t.Errorf("the user's key after the calls: %s", scrubbed(fmt.Sprint(keys)))
	}
	if _, err := apikeys.NewStore(sideDB(t, dbPath).DB).Authenticate(context.Background(), secret); err != nil {
		t.Errorf("the user's key stopped working: %v", err)
	}
}
