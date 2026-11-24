package browserctl

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/testhome"
)

func TestReadUploadFilesRefusesCredentialLocations(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv("USERPROFILE", home)
	write := func(rel string) string {
		p := filepath.Join(home, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, rel := range []string{".ssh/id_ed25519", ".monoagent/extension.token", ".aws/credentials",
		".gnupg/private-keys-v1.d/k.key", ".config/google-chrome/Default/Cookies", ".netrc"} {
		p := write(rel)
		if _, err := ReadUploadFiles([]string{p}); err == nil || !strings.Contains(err.Error(), "never uploaded") {
			t.Errorf("%s: want a refusal, got %v", rel, err)
		}
	}
	if _, err := ReadUploadFiles([]string{"~/.ssh/id_ed25519"}); err == nil || !strings.Contains(err.Error(), "never uploaded") {
		t.Errorf("~ form: want a refusal, got %v", err)
	}
	if _, err := ReadUploadFiles([]string{"/etc/shadow"}); err == nil || !strings.Contains(err.Error(), "never uploaded") {
		t.Errorf("/etc/shadow: want a refusal, got %v", err)
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(t.TempDir(), "innocent.txt")
		if err := os.Symlink(write(".ssh/id_rsa"), link); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadUploadFiles([]string{link}); err == nil || !strings.Contains(err.Error(), "never uploaded") {
			t.Errorf("symlink into .ssh: want a refusal, got %v", err)
		}
	}
	ok := write("Documents/cv.pdf")
	if files, err := ReadUploadFiles([]string{ok}); err != nil || len(files) != 1 {
		t.Errorf("an ordinary file: %v %v", files, err)
	}
}
