package n8napps

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/apipkg"
	"github.com/monoes/mono-agent/internal/n8nimport"
	"github.com/monoes/mono-agent/internal/workflow"
)

func triggerNode(t *testing.T, wf *workflow.WorkflowFile) map[string]interface{} {
	t.Helper()
	for _, n := range wf.Nodes {
		if n.Type == "trigger.webhook" {
			return n.Config
		}
	}
	t.Fatalf("no trigger.webhook in %+v", wf.Nodes)
	return nil
}

// Every webhook trigger whose package has a webhook block registers
// itself on activation instead of asking for a manual registration.
func TestWebhookTriggersRegisterWithTheirApp(t *testing.T) {
	apipkg.Reload()
	n := 0
	for typ, m := range apipkg.N8nIndex() {
		if m.RegisterWith == "" || n8nimport.TriggerKind(typ) != "webhook" {
			continue
		}
		n++
		wf, rep := convertOne(t, typ, map[string]interface{}{})
		cfg := triggerNode(t, wf)
		if cfg["register_with"] != m.RegisterWith {
			t.Errorf("%s: register_with = %v, want %s", typ, cfg["register_with"], m.RegisterWith)
		}
		text := rep.Text()
		if !strings.Contains(text, "activation registers") || strings.Contains(text, "mono-agent does not") {
			t.Errorf("%s report:\n%s", typ, text)
		}
		for _, k := range m.RegisterRequired {
			if !strings.Contains(text, k+" is not set") {
				t.Errorf("%s: missing required %s is not reported:\n%s", typ, k, text)
			}
		}
	}
	if n < 62 {
		t.Errorf("only %d webhook triggers register themselves", n)
	}
}

func TestGithubTriggerRegisterConfig(t *testing.T) {
	apipkg.Reload()
	wf, rep, err := n8nimport.Convert([]byte(`{"name":"t","nodes":[{"name":"T","type":"n8n-nodes-base.githubTrigger",
	 "typeVersion":1,"position":[0,0],"webhookId":"gh-1","credentials":{"githubApi":{"id":"1","name":"GH"}},
	 "parameters":{"owner":{"__rl":true,"mode":"name","value":"octo"},"repository":"repo","events":["push"],"options":{"insecureSSL":true}}}],
	 "connections":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	cfg := triggerNode(t, wf)
	rc, _ := cfg["register_config"].(map[string]interface{})
	if cfg["register_with"] != "github" || cfg["credential_id"] != "github" || cfg["path"] != "gh-1" ||
		rc["repository"] != "repo" || rc["owner"] != "octo" ||
		rc["options"].(map[string]interface{})["insecureSSL"] != true {
		t.Errorf("config: %v", cfg)
	}
	if strings.Contains(rep.Text(), "is not set") {
		t.Errorf("report: %s", rep.Text())
	}
}

// A trigger whose app cannot register a webhook keeps the manual note.
func TestManualWebhookTriggerKeepsNote(t *testing.T) {
	apipkg.Reload()
	wf, rep := convertOne(t, "n8n-nodes-base.slackTrigger", map[string]interface{}{})
	if cfg := triggerNode(t, wf); cfg["register_with"] != nil {
		t.Errorf("slack: %v", cfg)
	}
	if !strings.Contains(rep.Text(), "mono-agent does not") {
		t.Errorf("report: %s", rep.Text())
	}
}

// App-level credentials of the Meta and HubSpot triggers map to their own
// connection platforms; the triggers register with their packages.
func TestAppCredentialTriggers(t *testing.T) {
	apipkg.Reload()
	for typ, want := range map[string][3]string{
		"hubspotTrigger":         {"hubspotDeveloperApi", "hubspot", "hubspot_developer"},
		"whatsAppTrigger":        {"whatsAppTriggerApi", "whats_app", "whatsapp_trigger"},
		"facebookTrigger":        {"facebookGraphAppApi", "facebook_graph_api", "facebook_graph_app"},
		"facebookLeadAdsTrigger": {"facebookLeadAdsOAuth2Api", "facebook_lead_ads", "facebook_lead_ads"},
		"formstackTrigger":       {"formstackApi", "formstack", "formstack"},
		"stravaTrigger":          {"stravaOAuth2Api", "strava", "strava"},
	} {
		wf, rep, err := n8nimport.Convert([]byte(`{"name":"t","nodes":[{"name":"T","type":"n8n-nodes-base.` + typ +
			`","typeVersion":1,"position":[0,0],"webhookId":"w","credentials":{"` + want[0] + `":{"id":"1","name":"C"}},"parameters":{}}],"connections":{}}`))
		if err != nil {
			t.Fatal(err)
		}
		cfg := triggerNode(t, wf)
		if cfg["register_with"] != want[1] || cfg["credential_id"] != want[2] {
			t.Errorf("%s: register_with=%v credential_id=%v, want %s / %s", typ, cfg["register_with"], cfg["credential_id"], want[1], want[2])
		}
		if strings.Contains(rep.Text(), "does not verify") {
			t.Errorf("%s report: %s", typ, rep.Text())
		}
	}
}
