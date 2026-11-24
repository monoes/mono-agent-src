package release

import "testing"

// r05-D3: a shipped build never takes the unsigned GitHub-API update path.
func TestLegacyGitHubUpdatesAreOffByDefault(t *testing.T) {
	if AllowLegacyGitHubUpdates {
		t.Fatal("AllowLegacyGitHubUpdates must be false in shipped builds")
	}
}

func TestCheckLegacyURL(t *testing.T) {
	t.Parallel()
	ok := []string{
		"https://api.github.com/repos/monoes/mono-agent/releases/latest",
		"https://objects.githubusercontent.com/a/b",
		"http://127.0.0.1:8080/x",
		"http://[::1]:8080/x",
	}
	bad := []string{"https://evil.example/x", "http://github.com/x", "http://example.com/x", "http://localhost.evil.example/x", "file:///etc/passwd"}
	for _, u := range ok {
		if err := CheckLegacyURL(u); err != nil {
			t.Errorf("%s refused: %v", u, err)
		}
	}
	for _, u := range bad {
		if err := CheckLegacyURL(u); err == nil {
			t.Errorf("%s accepted", u)
		}
	}
}
