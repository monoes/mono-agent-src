package nodejs

import (
	"bytes"
	"strings"
	"testing"
)

// Answering "n" at the prompt removes nothing and says so.
func TestRemoveDeclinedSaysNotRemoved(t *testing.T) {
	cmd := NewCommand(Deps{JSONOutput: func() bool { return false }, StdinIsTerminal: func() bool { return true }})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetIn(strings.NewReader("n\n"))
	cmd.SetArgs([]string{"remove", "24.0.0"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Not removed.") {
		t.Fatalf("declined remove printed %q, want it to say Not removed.", out.String())
	}
}
