package daemon

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/autostart"
	"github.com/monoes/mono-agent/internal/storage"
)

// `daemon install|uninstall --json` report their result as JSON, as `daemon restart` does.
func TestInstallAndUninstallEmitJSON(t *testing.T) {
	for sub, key := range map[string]string{"install": "installed", "uninstall": "uninstalled"} {
		rec := &recordingInstaller{}
		root := &cobra.Command{Use: "monoagentcli"}
		root.PersistentFlags().String("profile", "", "")
		root.AddCommand(NewCommand(Deps{
			Refusal:      func() string { return "" },
			JSONOutput:   func() bool { return true },
			OpenDB:       func() (*storage.Database, error) { return storage.NewDatabase(t.TempDir() + "/t.db") },
			NewInstaller: func() autostart.Installer { return rec },
		}))
		root.SetArgs([]string{"daemon", sub})
		root.SilenceErrors, root.SilenceUsage = true, true
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&bytes.Buffer{})
		if err := root.Execute(); err != nil {
			t.Fatalf("%s: %v", sub, err)
		}
		var got map[string]any
		if err := json.Unmarshal(out.Bytes(), &got); err != nil || got[key] != true {
			t.Fatalf("%s --json = %q (%v), want {%q: true}", sub, out.String(), err, key)
		}
	}
}
