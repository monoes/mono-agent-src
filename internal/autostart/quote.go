package autostart

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// Path quoting shared by the backends that put the executable path into a text format.
// They live here, not in the OS files, so that one test covers them on every platform.

// xmlText escapes s for the text of a plist <string>: text/template does not.
func xmlText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// checkTaskPath refuses a path that cannot be put inside the Scheduled Task's cmd wrapper:
// a quote ends the quoted path, and cmd expands %VAR% even inside quotes.
func checkTaskPath(p string) error {
	if strings.ContainsAny(p, "\"%\r\n") {
		return fmt.Errorf("the path %q holds a quote, a percent sign or a line break: it cannot be put in a scheduled task", p)
	}
	return nil
}
