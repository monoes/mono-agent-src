package orgchat

import (
	"strings"
	"unicode/utf16"
)

// monomindInputCut is where monomind cuts each string of an approval's input
// before it records it (orgrt/policy-secrets.js, summarize): the first 200
// UTF-16 units, then "…".
const monomindInputCut = 200

// Command is the shell command an approval's input names ("" for a call that
// has none), and whether monomind cut it, so only its start is known.
func Command(input map[string]interface{}) (command string, truncated bool) {
	command = str(input, "command")
	truncated = strings.HasSuffix(command, "…") && len(utf16.Encode([]rune(command))) == monomindInputCut+1
	return command, truncated
}
