package apipkg

import (
	"net/http"
	"strings"
	"testing"
)

// An n8n v1 Twitter node sends a direct message to userId (a numeric id);
// the importer renames it to user. A plain numeric value is the recipient
// id, any other plain value a username to look up; a locator keeps its
// mode (a username may be all digits).
func TestTwitterDMUserIDOrUsername(t *testing.T) {
	p := catalogPkg(t, "twitter")
	if op, _ := p.Op("directMessage", "create"); op.N8nParamMap["userId"] != "user" {
		t.Fatal("directMessage.create lacks the v1 userId mapping")
	}
	cases := []struct {
		user  any
		paths []string
	}{
		{"783214", []string{"/2/dm_conversations/with/783214/messages"}},
		{"@ada", []string{"/2/users/by/username/ada", "/2/dm_conversations/with/99/messages"}},
		{map[string]any{"__rl": true, "mode": "username", "value": "2024"},
			[]string{"/2/users/by/username/2024", "/2/dm_conversations/with/99/messages"}},
		{map[string]any{"__rl": true, "mode": "id", "value": "12"}, []string{"/2/dm_conversations/with/12/messages"}},
	}
	for _, c := range cases {
		seen := pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
			if strings.Contains(r.URL.Path, "/by/username/") {
				w.Write([]byte(`{"data":{"id":"99"}}`))
				return
			}
			w.Write([]byte(`{"data":{"dm_event_id":"1"}}`))
		})
		params := map[string]any{"resource": "directMessage", "operation": "create", "user": c.user, "text": "hi"}
		if _, err := runPackageOp(p, params, map[string]any{"access_token": "t"}, nil); err != nil {
			t.Fatalf("%v: %v", c.user, err)
		}
		var got []string
		for _, r := range *seen {
			got = append(got, r.URL.Path)
		}
		if strings.Join(got, " ") != strings.Join(c.paths, " ") {
			t.Errorf("%v: requests %v, want %v", c.user, got, c.paths)
		}
	}
}
