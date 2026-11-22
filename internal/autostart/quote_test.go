package autostart

import "testing"

func TestXMLTextEscapesMarkup(t *testing.T) {
	if got, want := xmlText(`/Users/a&b/<x>"y"`), `/Users/a&amp;b/&lt;x&gt;&#34;y&#34;`; got != want {
		t.Errorf("xmlText = %q, want %q", got, want)
	}
	if got := xmlText("/usr/local/bin/monoagentcli"); got != "/usr/local/bin/monoagentcli" {
		t.Errorf("a plain path changed: %q", got)
	}
}

func TestCheckTaskPathRefusesWhatBreaksTheCommandLine(t *testing.T) {
	for _, bad := range []string{`C:\a"b\x.exe`, `C:\%USERNAME%\x.exe`, "C:\\a\nb\\x.exe"} {
		if checkTaskPath(bad) == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if err := checkTaskPath(`C:\Program Files\MonoAgent\monoagentcli.exe`); err != nil {
		t.Errorf("a plain path was refused: %v", err)
	}
}
