package n8napps

import (
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/apipkg"
	"github.com/monoes/mono-agent/internal/n8nimport"
)

func install(t *testing.T, pkgs map[string]string) {
	t.Helper()
	home := t.TempDir()
	testhome.SetHome(t, home)
	for id, js := range pkgs {
		dir := filepath.Join(home, ".monoagent", "apipkgs", id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(js), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	apipkg.Reload()
	t.Cleanup(apipkg.Reload)
}

const fmPkg = `{"$schema":"apipkg.v1","id":"wiring_fm","title":"FM","base_url":"https://fm.test",
 "n8n_types":["n8n-nodes-base.wiringFm"],"n8n_trigger_types":["n8n-nodes-base.wiringFmTrigger"],"n8n_operation_param":"action",
 "credentials":[{"platform":"wiring_fm","methods":["api_key"],"fields":[{"key":"database","label":"DB","required":true}],
   "n8n_credential":"wiringFmApi","n8n_field_map":{"db":"database"},
   "inject":{"headers":{"X-DB":"{{ cred.database }}"}}}],
 "resources":[{"value":"record","label":"Record"},{"value":"layout","label":"Layout"}],
 "operations":[
  {"resource":"record","operation":"find","title":"Find","method":"GET","path":"/records"},
  {"resource":"record","operation":"create","title":"Create","method":"POST","path":"/records",
   "fields":[{"key":"layout_name","label":"Layout","type":"text"}],"n8n_param_map":{"layout":"layout_name"}},
  {"resource":"layout","operation":"list","title":"List","method":"GET","path":"/layouts"}]}`

const translatePkg = `{"$schema":"apipkg.v1","id":"wiring_translate","title":"Translate","base_url":"https://translation.googleapis.com",
 "n8n_types":["n8n-nodes-base.wiringTranslate"],
 "credentials":[{"platform":"wiring_translate","reuse_platform":"google","methods":["oauth"],"n8n_credential":"googleTranslateOAuth2Api",
   "inject":{"headers":{"Authorization":"Bearer {{ cred.access_token }}"}}}],
 "operations":[{"resource":"","operation":"translate","title":"Translate","method":"POST","path":"/language/translate/v2"}]}`

func TestMappersFromPackages(t *testing.T) {
	install(t, map[string]string{"wiring_fm": fmPkg, "wiring_translate": translatePkg})
	if _, err := apipkg.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	by := map[string]n8nimport.AppMapper{}
	for _, m := range Mappers() {
		by[m.N8nTypes[0]] = m
	}
	fm, ok := by["n8n-nodes-base.wiringFm"]
	if !ok {
		t.Fatalf("no mapper for the package's n8n type (%d mappers)", len(by))
	}
	if fm.NodeType != "app.wiring_fm" || fm.PackageID != "wiring_fm" || fm.Trigger || fm.OperationParam != "action" ||
		fm.DefaultResource != "record" || fm.DefaultOperations["record"] != "find" || fm.DefaultOperations["layout"] != "list" {
		t.Errorf("fm mapper: %+v", fm)
	}
	if fm.CredentialPlatform["wiringFmApi"] != "wiring_fm" || !strings.Contains(fm.CredentialHints["wiringFmApi"], "db → database") {
		t.Errorf("credential mapping: %v %v", fm.CredentialPlatform, fm.CredentialHints)
	}
	if fm.ParamMap["record.create"]["layout"] != "layout_name" {
		t.Errorf("param map: %v", fm.ParamMap)
	}
	if trig := by["n8n-nodes-base.wiringFmTrigger"]; !trig.Trigger {
		t.Errorf("trigger type: %+v", trig)
	}
	tr := by["n8n-nodes-base.wiringTranslate"]
	if tr.CredentialPlatform["googleTranslateOAuth2Api"] != "google" ||
		tr.CredentialHints["googleTranslateOAuth2Api"] != "monoagentcli connect google --scopes translate" {
		t.Errorf("google credential: %v %v", tr.CredentialPlatform, tr.CredentialHints)
	}
}

// The source registered at init is what Convert reads: a package installed
// after start-up is imported without any further registration.
func TestConvertSeesInstalledPackages(t *testing.T) {
	install(t, map[string]string{"wiring_fm": fmPkg})
	wf, rep, err := n8nimport.Convert([]byte(`{"name":"t","nodes":[
	 {"name":"M","type":"n8n-nodes-base.manualTrigger","typeVersion":1,"position":[0,0],"parameters":{}},
	 {"name":"C","type":"n8n-nodes-base.wiringFm","typeVersion":1,"position":[200,0],
	  "parameters":{"action":"create","layout":"Main"},"credentials":{"wiringFmApi":{"id":"1","name":"FM"}}}],
	 "connections":{"M":{"main":[[{"node":"C","type":"main","index":0}]]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]interface{}
	for _, n := range wf.Nodes {
		if n.Name == "C" {
			if n.Type != "app.wiring_fm" {
				t.Fatalf("type = %s", n.Type)
			}
			cfg = n.Config
		}
	}
	if cfg["resource"] != "record" || cfg["action"] != "create" || cfg["layout_name"] != "Main" || cfg["credential_id"] != "wiring_fm" {
		t.Errorf("config: %v", cfg)
	}
	if !rep.Complete {
		t.Errorf("report: %s", rep.Text())
	}
}
