package account

import (
	"os"
	"path/filepath"
	"testing"
)

// #878: a host counts as the real monoes.me unless it is a loopback address. A spelling of
// monoes.me that differs from the constant still reaches it, so it is production too.
func TestEveryHostButLoopbackIsProduction(t *testing.T) {
	for host, want := range map[string]bool{
		"https://monoes.me":        true,
		"https://monoes.me:443":    true,
		"HTTPS://MONOES.ME":        true,
		"https://monoes.me/.":      true,
		"https://monoes.me.":       true,
		"monoes.me":                true,
		"":                         true,
		"http://0.0.0.0:8080":      true,
		"http://127.0.0.1.nip.io":  true,
		"http://example.test":      true,
		"http://127.0.0.1:9":       false,
		"http://127.1.2.3":         false,
		"http://[::1]:8080":        false,
		"http://localhost:3000":    false,
		"http://LOCALHOST":         false,
		"https://localhost/prefix": false,
	} {
		if got := isProductionHost(host); got != want {
			t.Errorf("isProductionHost(%q) = %v, want %v", host, got, want)
		}
	}
}

// Read without cgo, /etc/passwd has the last word: a uid it has no entry for (an LDAP account, a
// container's arbitrary uid), or only a commented-out one, has no home directory, where os/user
// would take $HOME.
func TestHomeFromPasswdHasNoFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	passwd := filepath.Join(t.TempDir(), "passwd")
	if err := os.WriteFile(passwd, []byte("root:x:0:0:root:/root:/bin/sh\n# 1000 is not here\n#old:x:1000:1000::/home/old:/bin/sh\n  # ada:x:1002:1002::/home/x:/bin/sh\nada:x:1001:1001::/home/ada:/bin/sh\nbroken:1000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if h, err := homeFromPasswd(passwd, "1001"); err != nil || h != "/home/ada" {
		t.Errorf("uid 1001: %q, %v", h, err)
	}
	for _, uid := range []string{"1000", "1002"} { // only commented out
		if h, err := homeFromPasswd(passwd, uid); err == nil || h != "" {
			t.Errorf("uid %s, not in the file: %q, %v; want no home", uid, h, err)
		}
	}
}

// The user's home comes from the system's user database, not from $HOME, which a test or an agent
// sets to a directory of its own. (Run without cgo too: CGO_ENABLED=0 reads /etc/passwd.)
func TestUserHomeIsNotHOME(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	got, err := userHome()
	if err != nil {
		t.Skip("the system has no entry for this user:", err)
	}
	if got == "" || sameFile(got, home) {
		t.Fatalf("the user's home is %q, the HOME of the test", got)
	}
}
