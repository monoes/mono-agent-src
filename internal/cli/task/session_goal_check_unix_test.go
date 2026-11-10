//go:build !windows

package task

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/monoes/mono-agent/internal/missionpursuit/verifier"
)

// The lead controls the project's file names: a name made of terminal escapes must not reach the operator as bytes.
func TestGoalAnswerShowsAProjectFilenameCleaned(t *testing.T) {
	t.Parallel()
	m := newCheckedMission(t, verifier.ContainmentFileOnly)
	d := m.read(t)
	evil := "evil\x1b]0;PWNED\a\x1b[2J"
	if err := syscall.Mkfifo(filepath.Join(m.root, evil), 0o600); err != nil {
		t.Skip("no fifo")
	}
	out, _, _ := runTask(t, m.db, "default", true, "", m.answerArgs(d, "c3", "--yes")...)
	if strings.ContainsAny(out, "\x1b\a") {
		t.Errorf("terminal control bytes reached the operator: %q", out)
	}
	if !strings.Contains(out, "a new check will run") {
		t.Errorf("the refusal was not the project-read one: %q", out)
	}
	text, errText, err := runTask(t, m.db, "default", false, "", m.answerArgs(d, "c3", "--yes")...)
	if err == nil {
		t.Fatal("the answer for a project with a pipe in it was accepted")
	}
	if strings.ContainsAny(text+errText+err.Error(), "\x1b\a") {
		t.Errorf("terminal control bytes reached the operator in text mode: %q %q %q", text, errText, err.Error())
	}
	_ = os.Remove(filepath.Join(m.root, evil))
}
