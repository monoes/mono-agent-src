package shellpath

import (
	"context"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExtract_IgnoresRcNoise(t *testing.T) {
	out := "Welcome!\nnvm: using v22\n" + startMarker + "/a:/b" + endMarker + "\nbye"
	if got := extract(out); got != "/a:/b" {
		t.Fatalf("extract = %q, want /a:/b", got)
	}
	if got := extract("no markers here"); got != "" {
		t.Fatalf("extract without markers = %q, want empty", got)
	}
}

func TestMerge_FrontFirstDeduped(t *testing.T) {
	sep := string(os.PathListSeparator)
	got := Merge(strings.Join([]string{"/nvm/bin", "/usr/bin"}, sep), strings.Join([]string{"/usr/bin", "", "/bin"}, sep))
	want := strings.Join([]string{"/nvm/bin", "/usr/bin", "/bin"}, sep)
	if got != want {
		t.Fatalf("Merge = %q, want %q", got, want)
	}
}

func TestPrepend_Idempotent(t *testing.T) {
	sep := string(os.PathListSeparator)
	t.Setenv("PATH", "/usr/bin"+sep+"/bin")
	Prepend("/nvm/bin")
	Prepend("/nvm/bin")
	if got, want := os.Getenv("PATH"), "/nvm/bin"+sep+"/usr/bin"+sep+"/bin"; got != want {
		t.Fatalf("PATH = %q, want %q", got, want)
	}
}

// TestLoginPath_ReadsShellRc simulates the GUI case: a shell whose rc file
// adds a dir to PATH must have that dir reported back.
func TestLoginPath_ReadsShellRc(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("login-shell PATH import is unix-only")
	}
	sh := "/bin/sh"
	if _, err := os.Stat(sh); err != nil {
		t.Skip("no /bin/sh")
	}
	home := t.TempDir()
	extra := filepath.Join(home, "nvm-bin")
	// sh -il reads $ENV for interactive shells.
	rc := filepath.Join(home, ".shrc")
	if err := os.WriteFile(rc, []byte("echo noisy banner\nexport PATH=\""+extra+":$PATH\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", sh)
	testhome.SetHome(t, home)
	t.Setenv("ENV", rc)
	got, err := LoginPath(context.Background())
	if err != nil {
		t.Fatalf("LoginPath: %v", err)
	}
	if !strings.HasPrefix(got, extra+":") {
		t.Fatalf("LoginPath = %q, want it to start with %q", got, extra)
	}
}

func TestPrintPathCommand_FishJoinsWithColons(t *testing.T) {
	if got := printPathCommand("/opt/homebrew/bin/fish"); !strings.Contains(got, "string join : $PATH") {
		t.Fatalf("fish command = %q, want colon-joined PATH", got)
	}
	if got := printPathCommand("/bin/zsh"); !strings.Contains(got, `"$PATH"`) {
		t.Fatalf("zsh command = %q, want \"$PATH\"", got)
	}
}
