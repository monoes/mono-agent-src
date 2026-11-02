package autostart

import (
	"os"
	"path/filepath"
	"testing"
)

// The exe of a unit is read back exactly as renderUnit wrote it, whatever the path holds.
func TestRegisteredExeReadsBackWhatRenderUnitWrote(t *testing.T) {
	for _, exe := range []string{
		"/home/u/.local/bin/monoagentcli",
		`/home/u/My Apps/mono "agent"/100%/$x/monoagentcli`,
		`/opt/back\slash/monoagentcli`,
	} {
		unit, err := renderUnit(exe)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(t.TempDir(), "monoagent-daemon.service")
		if err := os.WriteFile(p, []byte(unit), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, ok := RegisteredExe(p); !ok || got != exe {
			t.Errorf("RegisteredExe = %q, %v, want %q", got, ok, exe)
		}
	}
}

func TestRegisteredExeReadsALaunchdPlist(t *testing.T) {
	p := filepath.Join(t.TempDir(), "me.monoes.monoagent.plist")
	plist := "<plist><dict><key>Label</key><string>x</string><key>ProgramArguments</key>\n<array>\n\t<string>/Users/u/My &amp; Apps/monoagentcli</string>\n\t<string>daemon</string>\n</array></dict></plist>"
	if err := os.WriteFile(p, []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := RegisteredExe(p); !ok || got != "/Users/u/My & Apps/monoagentcli" {
		t.Fatalf("RegisteredExe = %q, %v", got, ok)
	}
}

func TestRegisteredExeSaysFalseWhenItCannotTell(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for name, p := range map[string]string{
		"no such file":        filepath.Join(dir, "gone.service"),
		"another kind":        write("x.txt", "ExecStart=/bin/x"),
		"no ExecStart":        write("a.service", "[Service]\nType=simple\n"),
		"empty ExecStart":     write("b.service", "ExecStart=\n"),
		"unterminated quote":  write("c.service", `ExecStart="/bin/x daemon`+"\n"),
		"plist without array": write("d.plist", "<plist><dict></dict></plist>"),
	} {
		if got, ok := RegisteredExe(p); ok {
			t.Errorf("%s: RegisteredExe = %q, true", name, got)
		}
	}
}
