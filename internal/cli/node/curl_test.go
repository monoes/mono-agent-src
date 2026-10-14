package node

import (
	"encoding/json"
	"strings"
	"testing"
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
