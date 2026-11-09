package main

// The saved settings of the API's server are held to the rule the flags and the
// environment are held to. One table of values is run against all three: the flag
// (parsed by cobra and handed to newAPIRuntime), the environment variable (read by
// newAPIRuntime) and the saved layer (apiconfig.Validate). A value that one of them takes
// and another refuses would let a server start from one source and not from another. The saved
// layer is stricter than the other two in two ways, both for text that outlives the process that
// wrote it (a control character in any value, a TLS file that is not an absolute path: see
// saved_text_test.go in internal/apiconfig), so what it takes the others take too.

import (
	"database/sql"
	"github.com/monoes/mono-agent/internal/testhome"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/apiconfig"
	"github.com/monoes/mono-agent/internal/testdb"
)

type parityRow struct {
	key, text string
	ok        bool
}

// parityRows: every setting that has a rule beyond "some text" (the TLS files have none),
// accepted and refused values, the odd ones on purpose: signs, zeros, padding, case.
var parityRows = []parityRow{
	// Nothing given is nothing set, in all three.
	{"v1_addr", "", true}, {"confinement", "", true}, {"context_confinement", "", true}, {"auto_confinement", "", true},
	{"max_concurrent", "", true}, {"turn_timeout", "", true}, {"image_runtimes", "", true}, {"tool_runtimes", "", true},

	{"v1_addr", "127.0.0.1:9443", true}, {"v1_addr", ":9443", true}, {"v1_addr", "0.0.0.0:0", true},
	{"v1_addr", "[::1]:9443", true}, {"v1_addr", "localhost:9443", true}, {"v1_addr", "example.com:65535", true},
	{"v1_addr", " 127.0.0.1:9443", true},  // the host is not checked, so padding is no reason to refuse
	{"v1_addr", "127.0.0.1:9443 ", false}, // ...but the port is, so trailing space is
	{"v1_addr", "9443", false}, {"v1_addr", "host", false}, {"v1_addr", "0.0.0.0:abc", false},
	{"v1_addr", "0.0.0.0:99999", false}, {"v1_addr", "0.0.0.0:-1", false}, {"v1_addr", "0.0.0.0:", false},
	{"v1_addr", "1:2:3", false},
}

func init() {
	for _, key := range []string{"confinement", "context_confinement", "auto_confinement"} {
		for _, v := range []string{"chat-only", "sandboxed", "any"} {
			parityRows = append(parityRows, parityRow{key, v, true})
		}
		for _, v := range []string{"none", "unconfined", "Any", "ANY", " any", "any ", "everything", "chat_only"} {
			parityRows = append(parityRows, parityRow{key, v, false})
		}
	}
	for _, v := range []string{"1", "4", "64", "+5", "007"} {
		parityRows = append(parityRows, parityRow{"max_concurrent", v, true})
	}
	for _, v := range []string{"0", "65", "-1", "abc", " 8", "8 ", "4.0", "1e1", "99999999999999999999"} {
		parityRows = append(parityRows, parityRow{"max_concurrent", v, false})
	}
	for _, v := range []string{"10s", "15m", "90s", "1h30m", "10m", "10.5s"} {
		parityRows = append(parityRows, parityRow{"turn_timeout", v, true})
	}
	for _, v := range []string{"9s", "9.9s", "0", "-5m", " 15m", "15m ", "15", "abc"} {
		parityRows = append(parityRows, parityRow{"turn_timeout", v, false})
	}
	for _, key := range []string{"image_runtimes", "tool_runtimes"} {
		for _, v := range []string{"none", "NONE", " none ", "codex", "agy", "codex,antigravity", "Codex, AGY", "codex,codex", " codex , claude ",
			" ", // blank is the default list in the environment; `api config set` refuses it on purpose (see the plan, P6)
		} {
			parityRows = append(parityRows, parityRow{key, v, true})
		}
		for _, v := range []string{"co dex", "codex,", ",codex", "codex,,claude", "none,codex", "codex,none", "-x", "a_b", "x" + strings.Repeat("y", 32), "none none"} {
			parityRows = append(parityRows, parityRow{key, v, false})
		}
	}
}

// parityAPIRuntime is newAPIRuntime over the test's database (nothing saved; newAPIRuntime only
// reads it, so one serves every row) with every variable of the API clear except the ones given.
func parityAPIRuntime(t *testing.T, db *sql.DB, f apiFlags, env map[string]string) error {
	t.Helper()
	for _, sp := range apiconfig.Specs() {
		t.Setenv(sp.Env, env[sp.Env])
	}
	_, err := newAPIRuntime(db, f, func(string, ...any) {})
	return err
}

func TestSavedSettingsAreHeldToTheRuleOfTheFlagsAndTheEnvironment(t *testing.T) {
	specs := map[string]apiconfig.Spec{}
	for _, sp := range apiconfig.Specs() {
		specs[sp.Key] = sp
	}
	testhome.SetHome(t, t.TempDir())
	db := testdb.Open(t).DB
	seen := map[string][2]bool{} // key -> an accepted and a refused row exist
	for _, r := range parityRows {
		r := r
		t.Run(r.key+"="+r.text, func(t *testing.T) {
			sp := specs[r.key]

			// The environment variable.
			if err := parityAPIRuntime(t, db, apiFlags{}, map[string]string{sp.Env: r.text}); (err == nil) != r.ok {
				t.Errorf("the environment variable %s=%q: error %v, accepted should be %v", sp.Env, r.text, err, r.ok)
			}

			// The saved layer.
			var s apiconfig.Settings
			if err := s.Set(r.key, r.text); err != nil {
				t.Fatal(err)
			}
			if problems := apiconfig.Validate(s); (len(problems) == 0) != r.ok {
				t.Errorf("the saved layer, %s=%q: problems %+v, accepted should be %v", r.key, r.text, problems, r.ok)
			}

			// The flag, for the settings that have one: through cobra, as a command line is.
			// A flag given empty is a flag not given, so the empty row has no flag to try.
			if sp.ServerFlag != "" && r.text != "" {
				var f apiFlags
				cmd := &cobra.Command{Use: "x"}
				f.Bind(cmd)
				accepted := cmd.ParseFlags([]string{sp.ServerFlag + "=" + r.text}) == nil
				if accepted {
					accepted = parityAPIRuntime(t, db, f, nil) == nil
				}
				if accepted != r.ok {
					t.Errorf("the flag %s=%q: accepted %v, should be %v", sp.ServerFlag, r.text, accepted, r.ok)
				}
			}
		})
		v := seen[r.key]
		if r.ok && r.text != "" {
			v[0] = true
		}
		if !r.ok {
			v[1] = true
		}
		seen[r.key] = v
	}
	for _, key := range []string{"v1_addr", "confinement", "context_confinement", "auto_confinement", "max_concurrent", "turn_timeout", "image_runtimes", "tool_runtimes"} {
		if v := seen[key]; !v[0] || !v[1] {
			t.Errorf("the table needs an accepted and a refused row for %s", key)
		}
	}
}

// The names the documents print are the ones the server has: its flags, and the
// variables of the TLS files, which tlsserve reads under the names this file declares.
func TestSpecsNameTheFlagsAndVariablesTheServerHas(t *testing.T) {
	var f apiFlags
	cmd := &cobra.Command{Use: "x"}
	f.Bind(cmd)
	for _, sp := range apiconfig.Specs() {
		dashed := strings.ReplaceAll(sp.Key, "_", "-")
		fl := cmd.Flags().Lookup(dashed)
		switch {
		case sp.ServerFlag != "" && (fl == nil || "--"+fl.Name != sp.ServerFlag):
			t.Errorf("%s: httpapi and daemon have no flag %s", sp.Key, sp.ServerFlag)
		case sp.ServerFlag == "" && fl != nil:
			t.Errorf("%s: the spec says there is no server flag, but --%s exists", sp.Key, dashed)
		}
	}
	for key, env := range map[string]string{"tls_cert_file": apiTLSCertEnv, "tls_key_file": apiTLSKeyEnv} {
		for _, sp := range apiconfig.Specs() {
			if sp.Key == key && sp.Env != env {
				t.Errorf("%s: the spec names %s, tlsserve is given %s", key, sp.Env, env)
			}
		}
	}
}
