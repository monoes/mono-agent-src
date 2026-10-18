package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
)

// openSideDB opens the database at a path, for a test that looks at what a command or a tool did.
func openSideDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db.DB
}

// setJevThreshold gives a profile a gate value for the api_auto surface.
func setJevThreshold(t *testing.T, path, profile string, v float64) {
	t.Helper()
	if err := jevconf.SetThreshold(openSideDB(t, path), profile, jevconf.APIAuto, v); err != nil {
		t.Fatal(err)
	}
}

// `jev enable|disable api_auto --json` and the tool api_auto_set are one document: the command's, and
// one more field, auto, which is what `api status --json` says of the auto model after the change
// (the command says it on stderr when there is no key). Each case runs the command on one copy of a
// database and the tool on another, and compares the document without auto byte for byte, auto with
// the one of api status, and what each switched.
func TestAPIAutoSetIsTheDocumentOfJevEnableAndDisable(t *testing.T) {
	cases := []struct {
		name      string
		on        bool    // switch on (else off)
		key       bool    // TYPESAFE_API_KEY is set
		threshold float64 // the profile's threshold, 0 for the default
		before    bool    // switched on before the call
		auto      string
	}{
		{name: "on, without a Jev key", on: true, auto: "no key"},
		{name: "on, with a Jev key in the environment", on: true, key: true, auto: "available"},
		{name: "on, with a threshold of the profile's", on: true, key: true, threshold: 0.6, auto: "available"},
		{name: "off, as it was", auto: "surface off"},
		{name: "off, after on", before: true, key: true, auto: "surface off"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := jevTestCfg(t, true) // the command's database; the variables of Jev are clear
			clearAPIEnv(t)
			t.Setenv("MONOAGENT_HTTPAPI_ADDR", apiServer(t, true))
			toolDB := testdb.Path(t)
			if c.key {
				t.Setenv("TYPESAFE_API_KEY", "jev-test-key-0123456789abcdef")
			}
			for _, path := range []string{cfg.DBPath, toolDB} {
				if c.threshold != 0 {
					setJevThreshold(t, path, "default", c.threshold)
				}
				if c.before {
					enableAPIAuto(t, path, "default")
				}
			}

			var cli string
			var err error
			if c.on {
				cli, _, err = runJev(t, cfg, "", "enable", "api_auto", "--yes")
			} else {
				cli, _, err = runJev(t, cfg, "", "disable", "api_auto")
			}
			if err != nil {
				t.Fatal(err)
			}
			tool, isErr := mcpOnce(t, mcpOptions(t, toolDB, "default", true), "api_auto_set", map[string]any{"enabled": c.on, "acknowledge_egress": c.on})
			if isErr {
				t.Fatalf("api_auto_set failed: %s", tool)
			}

			// The document of the command, byte for byte, without the one field the tool adds.
			var doc map[string]any
			if err := json.Unmarshal([]byte(tool), &doc); err != nil {
				t.Fatal(err)
			}
			autoField := doc["auto"]
			delete(doc, "auto")
			withoutAuto, _ := json.MarshalIndent(doc, "", "  ")
			if want := strings.TrimSuffix(cli, "\n"); string(withoutAuto) != want {
				t.Errorf("api_auto_set is not the document of the command: %s", firstDifference(want, string(withoutAuto)))
			}
			// The case exercises what it names.
			if doc["enabled"] != c.on {
				t.Errorf("the case does not exercise what it names: enabled %v", doc["enabled"])
			}
			if _, has := doc["egress"]; has != c.on {
				t.Errorf("egress is in the document: %v, want %v", has, c.on)
			}

			// What it adds is what `api status --json` says of the auto model, for the command's database.
			status, _, err := runAPI(t, cfg.DBPath, "default", true, "status")
			if err != nil {
				t.Fatal(err)
			}
			got, _ := json.Marshal(autoField)
			var gotAuto, wantAuto apiAutoJSON
			if err := json.Unmarshal(got, &gotAuto); err != nil {
				t.Fatal(err)
			}
			wantAuto = decodeStatus(t, status).Auto
			if !reflect.DeepEqual(gotAuto, wantAuto) || autoState(wantAuto) != c.auto {
				t.Errorf("auto %+v (%s), api status says %+v (want %s)", gotAuto, autoState(gotAuto), wantAuto, c.auto)
			}
			if jevconf.Enabled(openJevDB(t, cfg), "default", jevconf.APIAuto) != jevconf.Enabled(openSideDB(t, toolDB), "default", jevconf.APIAuto) {
				t.Error("the command and the tool left the surface in different states")
			}
		})
	}
}

// Both refuse to switch on without the acknowledgement, and show the same list of what is sent.
func TestAPIAutoSetRefusesWhatJevEnableRefusesWithoutYes(t *testing.T) {
	cfg := jevTestCfg(t, true)
	toolDB := testdb.Path(t)

	_, stderr, cliErr := runJev(t, cfg, "", "enable", "api_auto")
	if cliErr == nil || !strings.Contains(cliErr.Error(), "--yes") {
		t.Fatalf("the command without --yes: %v", cliErr)
	}
	text, isErr := mcpOnce(t, mcpOptions(t, toolDB, "default", true), "api_auto_set", map[string]any{"enabled": true})
	if !isErr || !strings.Contains(text, "acknowledge_egress") {
		t.Fatalf("the tool without acknowledge_egress: %q (error %v)", text, isErr)
	}
	for _, e := range jevconf.Egress[jevconf.APIAuto] {
		if !strings.Contains(stderr, "  - "+e) || !strings.Contains(text, e) {
			t.Errorf("%q is not in both: the command's note %q, the tool's refusal %q", e, stderr, text)
		}
	}
	// On a server whose operator did not allow it the tool shows the same list, and says whose decision it is.
	notAllowed, isErr := mcpOnce(t, mcpOptions(t, toolDB, "default", false), "api_auto_set", map[string]any{"enabled": true, "acknowledge_egress": true})
	if !isErr || !strings.Contains(notAllowed, "--allow-api-exposure") {
		t.Fatalf("the tool on a server whose operator did not allow it: %q (error %v)", notAllowed, isErr)
	}
	for _, e := range jevconf.Egress[jevconf.APIAuto] {
		if !strings.Contains(notAllowed, e) {
			t.Errorf("%q is not in the refusal of a server whose operator did not allow it: %q", e, notAllowed)
		}
	}
	if jevconf.Enabled(openJevDB(t, cfg), "default", jevconf.APIAuto) || jevconf.Enabled(openSideDB(t, toolDB), "default", jevconf.APIAuto) {
		t.Error("a refused call switched the surface on")
	}
}

// runJev runs `jev <args>` against cfg and returns stdout, stderr and the error.
func runJev(t *testing.T, cfg *globalConfig, stdin string, args ...string) (string, string, error) {
	t.Helper()
	cmd := newJevCmd(cfg)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func jevTestCfg(t *testing.T, jsonOut bool) *globalConfig {
	t.Helper()
	asOperatorProcess(t) // `jev enable` is the user's own
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("TYPESAFE_DEFAULT_MODEL", "")
	t.Setenv("TYPESAFE_BASE_URL", "")
	return &globalConfig{DBPath: newHILTestDB(t), ProfileID: "default", JSONOutput: jsonOut}
}

func openJevDB(t *testing.T, cfg *globalConfig) *sql.DB {
	t.Helper()
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db.DB
}
