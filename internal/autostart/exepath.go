package autostart

import (
	"os"
	"regexp"
	"strings"
)

// RegisteredExe is the binary a registered service definition starts: the first word of a systemd unit's ExecStart
// (a .service file) or the first ProgramArguments string of a launchd plist (a .plist file). where is the path
// Installer.Status reports for an installed service. It says false for any other file, or one it cannot read or parse:
// the caller then does not know which binary the service runs.
func RegisteredExe(where string) (string, bool) {
	raw, err := os.ReadFile(where)
	if err != nil {
		return "", false
	}
	switch {
	case strings.HasSuffix(where, ".service"):
		return unitExe(string(raw))
	case strings.HasSuffix(where, ".plist"):
		return plistExe(string(raw))
	}
	return "", false
}

var execStartLine = regexp.MustCompile(`(?m)^ExecStart=(.*)$`)

func unitExe(unit string) (string, bool) {
	m := execStartLine.FindStringSubmatch(unit)
	if m == nil {
		return "", false
	}
	return systemdUnquoteFirstWord(strings.TrimSpace(m[1]))
}

// systemdUnquoteFirstWord undoes systemdQuote on the first word of an ExecStart value: a double-quoted word has
// \\ and \" unescaped and %% and $$ folded; an unquoted word ends at the first space.
func systemdUnquoteFirstWord(s string) (string, bool) {
	if s == "" {
		return "", false
	}
	if s[0] != '"' {
		first, _, _ := strings.Cut(s, " ")
		return strings.NewReplacer("%%", "%", "$$", "$").Replace(first), first != ""
	}
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\' && i+1 < len(s):
			i++
			b.WriteByte(s[i])
		case c == '"':
			out := strings.NewReplacer("%%", "%", "$$", "$").Replace(b.String())
			return out, out != ""
		default:
			b.WriteByte(c)
		}
	}
	return "", false // unterminated quote
}

var plistFirstString = regexp.MustCompile(`(?s)<key>ProgramArguments</key>\s*<array>\s*<string>(.*?)</string>`)

func plistExe(plist string) (string, bool) {
	m := plistFirstString.FindStringSubmatch(plist)
	if m == nil {
		return "", false
	}
	out := strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&amp;", "&").Replace(strings.TrimSpace(m[1]))
	return out, out != ""
}
