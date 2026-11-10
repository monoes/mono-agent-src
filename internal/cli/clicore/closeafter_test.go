package clicore

import (
	"errors"
	"testing"

	"github.com/spf13/cobra"
)

// A command that fails must still close what it opened: cobra skips PersistentPostRun
// when RunE returns an error.
func TestCloseAfterRunClosesWhenASubcommandFails(t *testing.T) {
	closed := 0
	root := &cobra.Command{Use: "root", PersistentPostRun: func(*cobra.Command, []string) { closed++ }}
	group := &cobra.Command{Use: "group"}
	boom := errors.New("boom")
	group.AddCommand(
		&cobra.Command{Use: "fail", RunE: func(*cobra.Command, []string) error { return boom }},
		&cobra.Command{Use: "ok", RunE: func(*cobra.Command, []string) error { return nil }},
	)
	root.AddCommand(group)
	CloseAfterRun(root, func() { closed++ })
	root.SilenceErrors, root.SilenceUsage = true, true

	root.SetArgs([]string{"group", "fail"})
	if err := root.Execute(); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if closed != 1 {
		t.Fatalf("closed %d times after a failing command, want 1", closed)
	}
	root.SetArgs([]string{"group", "ok"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if closed != 3 { // the wrapper and the (still registered) post-run, both safe to repeat
		t.Fatalf("closed %d times in all, want 3", closed)
	}
}
