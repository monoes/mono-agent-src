package httpapi

import (
	"net/http"
	"testing"
)

// `workflow apply-fix` and `workflow save-draft` are the operator's: no route of the API, agent
// token or the profile's own bearer, reaches them or the AI task rows they read.
func TestTheAPIHasNoRouteToApplyAnAITaskProposal(t *testing.T) {
	s := newTestServer(t, true)
	full := testToken(t, s, "default")
	for _, rc := range []struct{ method, path string }{
		{"POST", "/profiles/default/workflows/wf-1/apply-fix"}, {"POST", "/profiles/default/workflows/apply-fix"},
		{"POST", "/profiles/default/workflows/save-draft"}, {"POST", "/profiles/default/aitask/at-1/apply"},
		{"POST", "/profiles/default/ai-tasks/at-1/apply-fix"}, {"POST", "/profiles/default/ai-tasks/at-1/save-draft"},
		{"GET", "/profiles/default/aitask"}, {"GET", "/profiles/default/ai-tasks/at-1"},
		{"POST", "/profiles/default/workflows/apply-fix/at-1"}, {"POST", "/profiles/default/workflows/save-draft/at-1"},
	} {
		rw := doReq(t, s, rc.method, rc.path, full, []byte(`{"taskId":"at-1","allowActive":true}`))
		if rw.Code < 400 || rw.Code == http.StatusInternalServerError || rw.Code == http.StatusOK {
			t.Errorf("%s %s: %d %s", rc.method, rc.path, rw.Code, rw.Body)
		}
	}
}
