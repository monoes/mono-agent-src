package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// These tests check the producer of manifest.json.sshsig with the real ssh-keygen, with temporary
// keys only. The principal and namespace are written out here, not taken from the producer: they
// are what install.sh and scripts/verify-sshsig.sh ask ssh-keygen for.
const (
	testPrincipal = "monoagent-release"
	testNamespace = "monoagent-manifest"
)

func sshKeygen(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("ssh-keygen is not installed")
	}
	return p
}

func sshLine(t *testing.T, pub ed25519.PublicKey) string {
	t.Helper()
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

// sshVerify runs `ssh-keygen -Y verify` over manifest with one allowed_signers line.
func sshVerify(t *testing.T, allowed, principal, namespace, sigPath string, manifest []byte) error {
	t.Helper()
	signers := filepath.Join(t.TempDir(), "allowed_signers")
	if err := os.WriteFile(signers, []byte(allowed+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sshKeygen(t), "-Y", "verify", "-f", signers, "-I", principal, "-n", namespace, "-s", sigPath)
	cmd.Stdin = bytes.NewReader(manifest)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("ssh-keygen: %s", out)
	}
	return err
}

// signedFixture signs a generated manifest with a temporary key through the command itself.
func signedFixture(t *testing.T) (dir, manifestPath string, manifest []byte, pub ed25519.PublicKey, priv ed25519.PrivateKey) {
	t.Helper()
	dir = fixture(t)
	if err := run([]string{"manifest", "-dir", dir, "-version", "v1.2.3"}); err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyFile, []byte(base64.StdEncoding.EncodeToString(priv)), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestPath = filepath.Join(dir, "manifest.json")
	if err := run([]string{"sign", "-manifest", manifestPath, "-key-file", keyFile}); err != nil {
		t.Fatal(err)
	}
	manifest, err = os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	return dir, manifestPath, manifest, pub, priv
}

func TestSignWritesSSHSignatureThatSSHKeygenVerifies(t *testing.T) {
	dir, path, manifest, pub, _ := signedFixture(t)
	sig := path + ".sshsig"
	restricted := testPrincipal + ` namespaces="` + testNamespace + `" ` + sshLine(t, pub)
	unrestricted := testPrincipal + " " + sshLine(t, pub)

	if err := sshVerify(t, restricted, testPrincipal, testNamespace, sig, manifest); err != nil {
		t.Fatalf("the signature does not verify: %v", err)
	}
	if err := sshVerify(t, unrestricted, testPrincipal, testNamespace, sig, manifest); err != nil {
		t.Fatalf("control without a namespace restriction does not verify: %v", err)
	}

	tampered := bytes.Replace(manifest, []byte("v1.2.3"), []byte("v1.2.4"), 1)
	if bytes.Equal(tampered, manifest) {
		t.Fatal("the tamper did not change the manifest")
	}
	if sshVerify(t, restricted, testPrincipal, testNamespace, sig, tampered) == nil {
		t.Error("a tampered manifest verified")
	}
	if sshVerify(t, restricted, testPrincipal, testNamespace, sig, append(append([]byte{}, manifest...), '\n')) == nil {
		t.Error("a manifest with one byte appended verified")
	}
	other, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if sshVerify(t, testPrincipal+" "+sshLine(t, other), testPrincipal, testNamespace, sig, manifest) == nil {
		t.Error("another key verified the signature")
	}
	// No namespace restriction here, so only the namespace bound into the signature refuses this.
	if sshVerify(t, unrestricted, testPrincipal, "git", sig, manifest) == nil {
		t.Error("the signature verified under another namespace")
	}
	if sshVerify(t, restricted, "someone-else", testNamespace, sig, manifest) == nil {
		t.Error("the signature verified for another principal")
	}
	// The publish job's strict check accepts the new file beside the manifest.
	if err := run([]string{"check", "-manifest", path, "-assets-dir", dir, "-expect-version", "v1.2.3",
		"-url-prefix", "https://github.com/monoes/mono-agent/releases/download/v1.2.3/", "-strict"}); err != nil {
		t.Errorf("check -strict with manifest.json.sshsig present: %v", err)
	}
}

// Ed25519 is deterministic, so for the same key and bytes the producer must write exactly what
// `ssh-keygen -Y sign` writes.
func TestSSHSignatureEqualsSSHKeygenSign(t *testing.T) {
	bin := sshKeygen(t)
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "k")
	if out, err := exec.Command(bin, "-q", "-t", "ed25519", "-N", "", "-C", "test", "-f", keyPath).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}
	pemBytes, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := ssh.ParseRawPrivateKey(pemBytes)
	if err != nil {
		t.Fatal(err)
	}
	priv, ok := raw.(*ed25519.PrivateKey)
	if !ok {
		t.Fatalf("key is %T", raw)
	}
	for _, msg := range [][]byte{[]byte("{\"schema\":1}\n"), {}, bytes.Repeat([]byte("x"), 100000)} {
		cmd := exec.Command(bin, "-Y", "sign", "-q", "-f", keyPath, "-n", testNamespace)
		cmd.Stdin = bytes.NewReader(msg)
		want, err := cmd.Output()
		if err != nil {
			t.Fatalf("ssh-keygen -Y sign: %v", err)
		}
		got, err := sshSignature(msg, *priv)
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Errorf("signature of %d bytes differs from ssh-keygen's:\n got:\n%s\nwant:\n%s", len(msg), got, want)
		}
	}
}

// The CI path: -key-env writes both signature files, and neither holds key material.
func TestSignKeyEnvWritesSSHSignatureWithoutKeyMaterial(t *testing.T) {
	dir := fixture(t)
	if err := run([]string{"manifest", "-dir", dir, "-version", "v1.2.3"}); err != nil {
		t.Fatal(err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_RELEASE_KEY", base64.StdEncoding.EncodeToString(priv.Seed()))
	path := filepath.Join(dir, "manifest.json")
	if err := run([]string{"sign", "-manifest", path, "-key-env", "TEST_RELEASE_KEY", "-assets-dir", dir,
		"-expect-version", "v1.2.3", "-url-prefix", "https://github.com/monoes/mono-agent/releases/download/v1.2.3/"}); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{".sig", ".sshsig"} {
		b, err := os.ReadFile(path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		body := strings.ReplaceAll(string(b), "\n", "")
		raw, _ := base64.StdEncoding.DecodeString(strings.TrimSuffix(strings.TrimPrefix(body, "-----BEGIN SSH SIGNATURE-----"), "-----END SSH SIGNATURE-----"))
		if bytes.Contains(raw, priv.Seed()) || strings.Contains(body, base64.StdEncoding.EncodeToString(priv.Seed())) {
			t.Errorf("%s holds key material", suffix)
		}
	}
}

// The signing job fails when the SSH signature cannot be produced: no success without both files.
func TestSignFailsWhenSSHSignatureCannotBeWritten(t *testing.T) {
	dir := fixture(t)
	if err := run([]string{"manifest", "-dir", dir, "-version", "v1.2.3"}); err != nil {
		t.Fatal(err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyFile, []byte(base64.StdEncoding.EncodeToString(priv)), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "manifest.json")
	if err := os.Mkdir(path+".sshsig", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"sign", "-manifest", path, "-key-file", keyFile}); err == nil {
		t.Fatal("sign succeeded without writing manifest.json.sshsig")
	}
}

// scripts/verify-sshsig.sh is what the release jobs run: against install.sh's embedded key (here a
// copy carrying a temporary key) and, in the bootstrap release only, against the derived key file.
func TestVerifySSHSigScript(t *testing.T) {
	sshKeygen(t)
	bash, err := exec.LookPath("bash")
	if err != nil || os.PathSeparator != '/' {
		t.Skip("needs bash on a Unix host")
	}
	_, path, manifest, pub, _ := signedFixture(t)
	work := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(work, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	other, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	installer := write("install.sh", "#!/bin/sh\nRELEASE_SSH_PUBKEY=\""+sshLine(t, pub)+"\"\n")
	wrongInstaller := write("wrong.sh", "RELEASE_SSH_PUBKEY=\""+sshLine(t, other)+"\"\n")
	noKey := write("nokey.sh", "#!/bin/sh\n")
	keyLine := write("pub.txt", keyID(pub)+" "+base64.StdEncoding.EncodeToString(pub)+"\n")
	wrongKeyLine := write("wrongpub.txt", keyID(other)+" "+base64.StdEncoding.EncodeToString(other)+"\n")
	tampered := write("tampered.json", string(manifest)+" ")
	empty := write("empty.sshsig", "")

	verify := func(args ...string) error {
		out, err := exec.Command(bash, append([]string{"../../scripts/verify-sshsig.sh"}, args...)...).CombinedOutput()
		t.Logf("verify-sshsig.sh %s: %s", args[0], out)
		return err
	}
	sig := path + ".sshsig"
	if err := verify("--installer", installer, path, sig); err != nil {
		t.Errorf("--installer with the signing key: %v", err)
	}
	if err := verify("--pubkey-file", keyLine, path, sig); err != nil {
		t.Errorf("--pubkey-file with the signing key: %v", err)
	}
	for name, args := range map[string][]string{
		"installer pins another key": {"--installer", wrongInstaller, path, sig},
		"installer has no key":       {"--installer", noKey, path, sig},
		"another pubkey file":        {"--pubkey-file", wrongKeyLine, path, sig},
		"tampered manifest":          {"--installer", installer, tampered, sig},
		"missing signature":          {"--installer", installer, path, filepath.Join(work, "absent")},
		"empty signature":            {"--installer", installer, path, empty},
		"unknown mode":               {"--any", installer, path, sig},
	} {
		if verify(args...) == nil {
			t.Errorf("%s: the script succeeded", name)
		}
	}
}
