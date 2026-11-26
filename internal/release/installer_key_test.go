package release

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"regexp"
	"testing"

	"golang.org/x/crypto/ssh"
)

var (
	installerRawKeyRe = regexp.MustCompile(`(?m)^RELEASE_PUBKEY="([A-Za-z0-9+/=]+)"$`)
	installerSSHKeyRe = regexp.MustCompile(`(?m)^RELEASE_SSH_PUBKEY="(ssh-ed25519 [A-Za-z0-9+/=]+)"$`)
)

// installerKeyViolations checks install.sh's two forms of the release key: the raw base64 openssl
// path and the SSH form ssh-keygen verifies with must be one key, pinned in keys.go and not revoked.
func installerKeyViolations(script string, pinned []Key) []string {
	raw := installerRawKeyRe.FindAllStringSubmatch(script, -1)
	sshForm := installerSSHKeyRe.FindAllStringSubmatch(script, -1)
	if len(raw) != 1 || len(sshForm) != 1 {
		return []string{fmt.Sprintf("want exactly one RELEASE_PUBKEY and one RELEASE_SSH_PUBKEY literal, got %d and %d", len(raw), len(sshForm))}
	}
	b, err := base64.StdEncoding.DecodeString(raw[0][1])
	if err != nil || len(b) != ed25519.PublicKeySize {
		return []string{"RELEASE_PUBKEY is not a base64 ed25519 public key"}
	}
	k, err := ssh.NewPublicKey(ed25519.PublicKey(b))
	if err != nil {
		return []string{err.Error()}
	}
	var out []string
	if want := "ssh-ed25519 " + base64.StdEncoding.EncodeToString(k.Marshal()); sshForm[0][1] != want {
		out = append(out, "RELEASE_SSH_PUBKEY is not the SSH form of RELEASE_PUBKEY, want "+want)
	}
	id := KeyID(ed25519.PublicKey(b))
	found := false
	for _, p := range pinned {
		found = found || p.ID == id
	}
	if !found || revokedReleaseKeyIDs[id] {
		out = append(out, "install.sh pins key "+id+", which keys.go does not pin (or has revoked)")
	}
	return out
}

func TestInstallerPinsTheReleaseKeyInBothForms(t *testing.T) {
	b, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	for _, v := range installerKeyViolations(script, PinnedKeys()) {
		t.Error(v)
	}
	if t.Failed() {
		return
	}
	other, err := ssh.NewPublicKey(ed25519.PublicKey(make([]byte, ed25519.PublicKeySize)))
	if err != nil {
		t.Fatal(err)
	}
	otherSSH := `RELEASE_SSH_PUBKEY="ssh-ed25519 ` + base64.StdEncoding.EncodeToString(other.Marshal()) + `"`
	otherRaw := `RELEASE_PUBKEY="` + base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize)) + `"`
	sshSwapped := installerSSHKeyRe.ReplaceAllLiteralString(script, otherSSH)
	for name, mutated := range map[string]string{
		"ssh form of another key": sshSwapped,
		"both forms unpinned":     installerRawKeyRe.ReplaceAllLiteralString(sshSwapped, otherRaw),
		"ssh form removed":        installerSSHKeyRe.ReplaceAllLiteralString(script, ""),
	} {
		if mutated == script || len(installerKeyViolations(mutated, PinnedKeys())) == 0 {
			t.Errorf("%s: not flagged", name)
		}
	}
}
