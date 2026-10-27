package health

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func publicationsEnv(info *WorkflowPublicationsInfo, err error) *Env {
	return &Env{WorkflowPublications: func(context.Context) (*WorkflowPublicationsInfo, error) { return info, err }}
}

func TestWorkflowPublicationsCheck(t *testing.T) {
	stale := OpenWorkflowPublication{WorkflowID: "wf-1", Kind: "save", Origin: "apply-fix", State: "pending", Age: time.Minute, OwnerGone: true}
	cases := []struct {
		name    string
		env     *Env
		status  Status
		summary string
		fix     string
	}{
		{"no hook", &Env{}, StatusSkip, "not available", ""},
		{"clean", publicationsEnv(&WorkflowPublicationsInfo{}, nil), StatusOK, "agree", ""},
		{"unreadable hook", publicationsEnv(nil, errors.New("boom")), StatusWarn, "cannot read", ""},
		{"abandoned change", publicationsEnv(&WorkflowPublicationsInfo{Open: []OpenWorkflowPublication{stale}}, nil), StatusWarn, "1 unfinished", FixWorkflowPublicationsRecover},
		{"change in progress", publicationsEnv(&WorkflowPublicationsInfo{Open: []OpenWorkflowPublication{{WorkflowID: "wf-2", Kind: "save", State: "pending"}}}, nil), StatusInfo, "in progress", ""},
		{"needs a choice", publicationsEnv(&WorkflowPublicationsInfo{Open: []OpenWorkflowPublication{{WorkflowID: "wf-3", Kind: "save", State: "unresolved", Note: "copy kept"}}}, nil), StatusWarn, "needing a choice", ""},
		{"unreadable file", publicationsEnv(&WorkflowPublicationsInfo{Unreadable: []string{"wf-4"}}, nil), StatusWarn, "unreadable file", ""},
		{"waiting import", publicationsEnv(&WorkflowPublicationsInfo{Pending: []string{"wf-5"}}, nil), StatusInfo, "waiting", ""},
	}
	for _, c := range cases {
		res := checkWorkflowPublications(context.Background(), c.env)
		if res.Status != c.status || !strings.Contains(res.Summary, c.summary) || res.FixID != c.fix {
			t.Errorf("%s: %s %q fix %q, want %s containing %q fix %q", c.name, res.Status, res.Summary, res.FixID, c.status, c.summary, c.fix)
		}
	}
}

func TestWorkflowPublicationsFixRunsTheRecovery(t *testing.T) {
	called := 0
	env := &Env{RecoverWorkflowPublications: func(context.Context) (int, error) { called++; return 2, nil }}
	if err := fixWorkflowPublications(context.Background(), env, func(string) {}); err != nil || called != 1 {
		t.Fatalf("called %d err %v", called, err)
	}
	if err := fixWorkflowPublications(context.Background(), &Env{}, func(string) {}); err == nil {
		t.Fatal("a fix with no recovery available must say so")
	}
}

func TestWorkflowPublicationsRowIsRegistered(t *testing.T) {
	found := false
	for _, c := range Default().Checks() {
		if c.ID == CheckWorkflowPublications {
			found = len(c.DependsOn) == 1 && c.DependsOn[0] == CheckDB
		}
	}
	if !found {
		t.Fatal("the row is missing or does not wait for the database")
	}
	if _, ok := Default().Fix(FixWorkflowPublicationsRecover); !ok {
		t.Fatal("the fix is not registered")
	}
}

func TestWorkflowPublicationsUnresolvedNamesBothChoicesAndPicksNone(t *testing.T) {
	info := &WorkflowPublicationsInfo{Open: []OpenWorkflowPublication{{WorkflowID: "wf-3", Kind: "save", State: "unresolved"}}}
	res := checkWorkflowPublications(context.Background(), publicationsEnv(info, nil))
	if len(res.Children) != 1 || res.Children[0].FixID != "" {
		t.Fatalf("an unresolved change must not default to a destructive fix: %+v", res.Children)
	}
	for _, want := range []string{"resolve:wf-3:keep-rows", "resolve:wf-3:keep-file"} {
		if !strings.Contains(res.Children[0].Detail, want) {
			t.Errorf("the detail does not offer %s: %q", want, res.Children[0].Detail)
		}
	}
	var got [2]string
	env := &Env{ResolveWorkflowPublication: func(_ context.Context, id, keep string) error { got = [2]string{id, keep}; return nil }}
	fix, ok := Default().Fix("workflows.publications.resolve:wf-3:keep-rows")
	if !ok {
		t.Fatal("the parameterized fix does not resolve")
	}
	if err := fix.Apply(context.Background(), env, func(string) {}); err != nil || got != [2]string{"wf-3", "keep-rows"} {
		t.Fatalf("%v %v", got, err)
	}
}

func TestWorkflowPublicationsOffersToAdoptANewFile(t *testing.T) {
	info := &WorkflowPublicationsInfo{Adoptable: []AdoptableWorkflowFile{
		{ID: "wf-new", ProfileID: "default", SHA: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", NodeTypes: []string{"core.set", "trigger.manual"}, Triggers: []string{"trigger.manual"}},
	}}
	res := checkWorkflowPublications(context.Background(), publicationsEnv(info, nil))
	if len(res.Children) != 1 || res.Children[0].FixID != "workflows.publications.adopt:wf-new:default:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
		t.Fatalf("%+v", res.Children)
	}
	for _, want := range []string{`"default"`, `"trigger.manual"`, `"core.set"`} {
		if !strings.Contains(res.Children[0].Summary, want) {
			t.Errorf("the row does not show %s: %q", want, res.Children[0].Summary)
		}
	}
	for _, want := range []string{"sha256sum", "--profile default doctor fix", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"} {
		if !strings.Contains(res.Children[0].Detail, want) {
			t.Errorf("the detail does not show %s: %q", want, res.Children[0].Detail)
		}
	}
	var got [3]string
	env := &Env{AdoptWorkflowFile: func(_ context.Context, id, profile, sha string) error { got = [3]string{id, profile, sha}; return nil }}
	fix, ok := Default().Fix(res.Children[0].FixID)
	if !ok || !fix.Optional {
		t.Fatalf("the fix is missing or not optional: %v %+v", ok, fix.FixInfo)
	}
	if err := fix.Apply(context.Background(), env, func(string) {}); err != nil || got != [3]string{"wf-new", "default", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"} {
		t.Fatalf("%v %v", got, err)
	}
	if bad, _ := Default().Fix("workflows.publications.adopt:wf-new"); bad.Apply != nil {
		if err := bad.Apply(context.Background(), env, func(string) {}); err == nil {
			t.Fatal("an adopt without a profile and sha was accepted")
		}
	}
}

// What a file calls itself cannot spoof the report: names are printed escaped, a long list is cut.
func TestWorkflowPublicationsEscapesAndCapsWhatFilesSay(t *testing.T) {
	evil := AdoptableWorkflowFile{ID: "wf-x", ProfileID: "default", SHA: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		NodeTypes: []string{"core.set\n\x1b[31mFAKE OK"}, Triggers: []string{"trigger.manual\nok: nothing to see"}}
	info := &WorkflowPublicationsInfo{Adoptable: []AdoptableWorkflowFile{evil}, MoreAdoptable: 7}
	res := checkWorkflowPublications(context.Background(), publicationsEnv(info, nil))
	for _, c := range res.Children {
		if strings.ContainsAny(c.Summary, "\n\x1b") {
			t.Fatalf("a control character reached the report: %q", c.Summary)
		}
	}
	if last := res.Children[len(res.Children)-1]; !strings.Contains(last.Summary, "7 more") {
		t.Fatalf("the cap is not reported: %q", last.Summary)
	}
}
