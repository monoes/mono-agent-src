package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// TestErrorHintsNameRealCommands: every `monoagentcli …` command an error
// hint tells the user to run exists.
func TestErrorHintsNameRealCommands(t *testing.T) {
	root := newRootCmd()
	cmdRe := regexp.MustCompile("`monoagentcli ([^`]+)`")
	for _, c := range workflow.AllErrorClasses {
		for _, m := range cmdRe.FindAllStringSubmatch(workflow.ErrorHint(c), -1) {
			var words []string
			for _, w := range strings.Fields(m[1]) {
				if strings.HasPrefix(w, "<") || strings.HasPrefix(w, "-") {
					break
				}
				words = append(words, w)
			}
			found, rest, err := root.Find(words)
			if err != nil || len(rest) > 0 || found == root {
				t.Errorf("%s hint names `monoagentcli %s`, which is not a command", c, m[1])
			}
		}
	}
}
