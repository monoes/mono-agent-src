package nodes

import (
	"context"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/workflow"
)

// sessionRecorder records the session name GetPage is asked for and stops
// the run there.
type sessionRecorder struct{ sessions []string }

var errStopAtPage = errors.New("stop at GetPage")

func (p *sessionRecorder) GetPage(_ context.Context, _ string, username string) (browser.PageInterface, error) {
	p.sessions = append(p.sessions, username)
	return nil, errStopAtPage
}

// instagram.list_user_posts takes its target profile as target_url; the
// "username" field is the browser session. With no target the node fails as
// missing target_url — the session placeholder never stands in for it.
func TestListUserPosts_TargetIsNotTheSession(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	action.SetDefSource(nil)
	rec := &sessionRecorder{}
	prev := globalSessionProvider
	SetGlobalSessionProvider(rec)
	t.Cleanup(func() { SetGlobalSessionProvider(prev) })

	run := func(cfg map[string]interface{}) error {
		_, err := NewBrowserNode("instagram", "list_user_posts").Execute(context.Background(), workflow.NodeInput{}, cfg)
		return err
	}

	if err := run(map[string]interface{}{}); err == nil || !strings.Contains(err.Error(), "target_url") {
		t.Fatalf("no target: want missing target_url, got %v", err)
	}
	if len(rec.sessions) != 0 {
		t.Fatalf("a tab was opened without a target: %v", rec.sessions)
	}

	// Target and session given separately: the session is the session.
	if err := run(map[string]interface{}{"target_url": "natgeo", "username": "me"}); !errors.Is(err, errStopAtPage) {
		t.Fatalf("with target: %v", err)
	}
	// A workflow saved before the split put the profile in "username"; it
	// still resolves (target_url aliases username).
	if err := run(map[string]interface{}{"username": "natgeo"}); !errors.Is(err, errStopAtPage) {
		t.Fatalf("legacy username-as-target: %v", err)
	}
	if want := []string{"me", "natgeo"}; strings.Join(rec.sessions, ",") != strings.Join(want, ",") {
		t.Errorf("sessions = %v, want %v", rec.sessions, want)
	}
}

// A workflow saved with the old generic form (message + targets) fills
// every required field of the new generated form and passes the action's
// input validation.
func TestOldGenericConfigFitsGeneratedForms(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	action.SetDefSource(nil)
	rec := &sessionRecorder{}
	prev := globalSessionProvider
	SetGlobalSessionProvider(rec)
	t.Cleanup(func() { SetGlobalSessionProvider(prev) })

	old := map[string]interface{}{
		"message": "Thanks!",
		"targets": []interface{}{"https://example.com/p/1"},
	}
	for _, nt := range []string{"instagram.reply_to_comments", "tiktok.comment_on_video", "linkedin.list_post_comments"} {
		s, err := workflow.LoadDefaultSchema(nt)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range s.Fields {
			if _, set := old[f.Key]; f.Required && !set && f.Default == nil {
				t.Errorf("%s: required field %q is not filled by an old message+targets config", nt, f.Key)
			}
		}
		dot := strings.Index(nt, ".")
		_, err = NewBrowserNode(nt[:dot], nt[dot+1:]).Execute(context.Background(), workflow.NodeInput{}, old)
		if !errors.Is(err, errStopAtPage) {
			t.Errorf("%s: old config failed before opening a tab: %v", nt, err)
		}
	}
}

// A caller that sends only the form's required fields (CLI, MCP, API — no
// editor pre-fill of limit) runs find_by_keyword: its result cap is
// optional with a default.
func TestFindByKeyword_RequiredFieldsOnly(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	action.SetDefSource(nil)
	prev := globalSessionProvider
	SetGlobalSessionProvider(&sessionRecorder{})
	t.Cleanup(func() { SetGlobalSessionProvider(prev) })

	for _, p := range []string{"instagram", "linkedin", "tiktok", "x"} {
		_, err := NewBrowserNode(p, "find_by_keyword").Execute(context.Background(), workflow.NodeInput{},
			map[string]interface{}{"keywords": "street photography"})
		if !errors.Is(err, errStopAtPage) {
			t.Errorf("%s.find_by_keyword with keywords only: %v", p, err)
		}
	}
}
