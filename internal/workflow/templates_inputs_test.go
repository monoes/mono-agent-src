package workflow

import "testing"

func TestTemplateInputs_FromBundledTemplates(t *testing.T) {
	cases := map[string][]string{
		"gemimg":     {"prompt"},
		"gemimgmany": {"prompts"},
		"find_jobs":  {"keywords", "location"},
		// Downstream $json is the previous node's output, not trigger data.
		"hello":                {},
		"gmail_ai_triage":      {},
		"ai_social_post_draft": {"topic"},
		"http_to_slack":        {"event", "severity", "source"},
	}
	for id, want := range cases {
		wf, ok := GetTemplate(id)
		if !ok {
			t.Fatalf("template %q not found", id)
		}
		got := templateInputs(wf)
		if len(got) != len(want) {
			t.Errorf("%s: got inputs %v, want %v", id, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s: got inputs %v, want %v", id, got, want)
				break
			}
		}
	}
}

func TestTemplateInputs_NoTriggerData(t *testing.T) {
	wf, ok := GetTemplate("outlook_email_sync")
	if !ok {
		t.Skip("outlook template not bundled")
	}
	if got := templateInputs(wf); len(got) != 0 {
		t.Errorf("a scheduled template should read no trigger data, got %v", got)
	}
}
