package chat

import "testing"

// There is no chat flag that confirms a mission: the host's turns carry a signed,
// single-use confirmation in their environment, and nothing else stands in for it
// (a bare flag was honoured from any caller that passed the operator check, and
// the coder host's children look like the operator to it).
func TestChatHasNoMissionFlag(t *testing.T) {
	cmd := newChatCmd(testDeps(&testConfig{DBPath: t.TempDir() + "/x.db", ProfileID: "default"}))
	for _, name := range []string{"mission", "mission-token"} {
		if f := cmd.Flags().Lookup(name); f != nil {
			t.Errorf("chat has a --%s flag", name)
		}
	}
	cmd.SetArgs([]string{"--mission", "--runtime", "fake", "hi"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); err == nil {
		t.Fatal("chat --mission was accepted")
	}
}
