package node

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func runNodeCurl(t *testing.T, stdin string, args ...string) (map[string]interface{}, string, error) {
	t.Helper()
	var out, errOut strings.Builder
	cmd := NewCommand(testDeps("", "", false))
	cmd.SetArgs(append([]string{"curl"}, args...))
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(stdin))
	if err := cmd.Execute(); err != nil {
		return nil, errOut.String(), err
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(out.String()), &cfg); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	return cfg, errOut.String(), nil
}

func TestNodeCurlOneArgument(t *testing.T) {
	t.Parallel()
	cfg, stderr, err := runNodeCurl(t, "", `curl -k -X POST https://api.example.com/items -H "Content-Type: application/json" -d '{"name":"x"}'`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg["method"] != "POST" || cfg["url"] != "https://api.example.com/items" || cfg["body_type"] != "json" || cfg["body"] != `{"name":"x"}` {
		t.Fatalf("config = %v", cfg)
	}
	if !strings.Contains(stderr, "note: -k/--insecure ignored") {
		t.Fatalf("stderr = %q", stderr)
	}
}

// Words the shell already split (flags included) reach the parser intact.
func TestNodeCurlSplitArguments(t *testing.T) {
	t.Parallel()
	cfg, _, err := runNodeCurl(t, "", "curl", "-u", "ann:pw", "-H", "X-Note: it's", "https://api.example.com/x")
	if err != nil {
		t.Fatal(err)
	}
	if cfg["auth_type"] != "basic" || cfg["auth_username"] != "ann" || cfg["headers"].(map[string]interface{})["X-Note"] != "it's" {
		t.Fatalf("config = %v", cfg)
	}
}

func TestNodeCurlStdin(t *testing.T) {
	t.Parallel()
	cfg, _, err := runNodeCurl(t, "curl https://api.example.com/a \\\n  -H 'Accept: text/csv'\n", "-")
	if err != nil {
		t.Fatal(err)
	}
	if cfg["url"] != "https://api.example.com/a" || cfg["headers"].(map[string]interface{})["Accept"] != "text/csv" {
		t.Fatalf("config = %v", cfg)
	}
	if _, _, err := runNodeCurl(t, "  ", "-"); err == nil {
		t.Fatal("empty stdin was accepted")
	}
	if _, _, err := runNodeCurl(t, "", "curl -H 'X: 1'"); err == nil || !strings.Contains(err.Error(), "no URL") {
		t.Fatalf("err = %v", err)
	}
}

// Global flags before `node curl` are the CLI's, not curl's: they must not
// reach the curl parser (`--profile default` once became the URL).
func TestNodeCurlGlobalFlagsBeforeCommand(t *testing.T) {
	t.Parallel()
	var profile string
	var asJSON bool
	root := &cobra.Command{Use: "monoagentcli", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().StringVar(&profile, "profile", "", "")
	root.PersistentFlags().BoolVar(&asJSON, "json", false, "")
	root.AddCommand(NewCommand(testDeps("", "", false)))
	var out strings.Builder
	root.SetArgs([]string{"--profile", "default", "--json", "node", "curl", "-"})
	root.SetOut(&out)
	root.SetErr(&strings.Builder{})
	root.SetIn(strings.NewReader("curl https://api.example.com/a -H 'Accept: text/csv'"))
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(out.String()), &cfg); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	if cfg["url"] != "https://api.example.com/a" {
		t.Fatalf("url = %v", cfg["url"])
	}
	if profile != "default" || !asJSON {
		t.Fatalf("global flags not applied: profile=%q json=%v", profile, asJSON)
	}

	// After the command name too, and with curl's own words left intact.
	out.Reset()
	root.SetArgs([]string{"node", "curl", "--profile=p2", "curl", "-v", "https://api.example.com/b"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out.String()), &cfg); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	if cfg["url"] != "https://api.example.com/b" || profile != "p2" {
		t.Fatalf("url = %v profile = %q", cfg["url"], profile)
	}
}
