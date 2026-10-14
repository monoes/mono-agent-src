package version

import (
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/spf13/cobra"
)

// run executes `<root> [--json] version` and returns what it printed on os.Stdout, where the
// command prints. The --json flag is the root's, as in the CLI.
func run(t *testing.T, args ...string) string {
	t.Helper()
	root := &cobra.Command{Use: "monoagentcli"}
	root.PersistentFlags().Bool("json", false, "")
	root.AddCommand(NewCommand(Deps{
		Version:   func() string { return "v1.2.3" },
		BuildDate: func() string { return "2026-10-13" },
	}))
	root.SetArgs(args)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	runErr := root.Execute()
	os.Stdout = orig
	w.Close()
	out, _ := io.ReadAll(r)
	if runErr != nil {
		t.Fatal(runErr)
	}
	return string(out)
}

func TestVersionTextAndJSON(t *testing.T) {
	if got := run(t, "version"); got != "monoagentcli v1.2.3 (built 2026-10-13)\n" {
		t.Fatalf("text = %q", got)
	}
	var v map[string]string
	if out := run(t, "--json", "version"); json.Unmarshal([]byte(out), &v) != nil || v["version"] != "v1.2.3" || v["build_date"] != "2026-10-13" || len(v) != 2 {
		t.Fatalf("--json = %q", out)
	}
}
