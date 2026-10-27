package dynorg

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Repository-local configuration and object-store state can make git run a
// program when it has to read an object that is missing (a lazy fetch from a
// configured remote). These tests build a repository of that shape with a
// program that only creates a marker file, and assert state.

// lazyRepo is a repository whose HEAD commit references a blob that is not in
// the object store, with a promisor remote that names the program marker.
// It returns the repo, the missing blob and the marker.
func lazyRepo(t *testing.T) (repo, blob, marker string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the test program is a POSIX shell script")
	}
	isolateGit(t)
	repo = newRepo(t)
	marker = filepath.Join(t.TempDir(), "ran")
	script := filepath.Join(t.TempDir(), "prog.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(t.TempDir(), "in")
	blob = writeBlob(t, repo, "missing content\n")
	if err := os.WriteFile(in, []byte("100644 blob "+blob+"\tmissing.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(in)
	defer f.Close()
	cmd := exec.Command("git", "-C", repo, "mktree", "--missing")
	cmd.Stdin = f
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	tree := strings.TrimSpace(string(out))
	commit := gitT(t, repo, "commit-tree", tree, "-m", "lazy")
	gitT(t, repo, "update-ref", "refs/heads/lazy", commit)
	gitT(t, repo, "config", "core.repositoryformatversion", "1")
	gitT(t, repo, "config", "extensions.partialclone", "origin")
	gitT(t, repo, "config", "remote.origin.url", filepath.Join(t.TempDir(), "nowhere"))
	gitT(t, repo, "config", "remote.origin.promisor", "true")
	gitT(t, repo, "config", "remote.origin.uploadpack", script)
	return repo, blob, marker
}

func writeBlob(t *testing.T, repo, content string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "hash-object", "--stdin")
	cmd.Stdin = strings.NewReader(content)
	out, err := cmd.Output() // not written: the object stays missing
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// Without the pin, the same read does run the program (so the tests below
// prove something).
func TestLazyFetchRunsTheProgramWithoutThePin(t *testing.T) {
	repo, blob, marker := lazyRepo(t)
	cmd := exec.Command("git", "-C", repo, "cat-file", "-p", blob)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	_ = cmd.Run()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("git behaviour changed: it no longer lazy-fetches this shape, so the baseline proof that the pin matters no longer holds: %v", err)
	}
}

func TestGitRunDoesNotLazyFetchAMissingObject(t *testing.T) {
	repo, blob, marker := lazyRepo(t)
	if _, err := gitRun(context.Background(), repo, "cat-file", "-p", blob); err == nil {
		t.Fatal("reading a missing object succeeded")
	}
	noMarker(t, marker)
	found := false
	for _, kv := range gitEnv() {
		if kv == "GIT_NO_LAZY_FETCH=1" {
			found = true
		}
	}
	if !found {
		t.Fatal("gitEnv does not carry GIT_NO_LAZY_FETCH=1")
	}
}

func TestRefuseProgramConfigRefusesThePartialCloneShape(t *testing.T) {
	repo, _, marker := lazyRepo(t)
	branch := gitT(t, repo, "rev-parse", "--abbrev-ref", "HEAD")
	wantRefusal(t, commitAll(context.Background(), repo, branch, "x"), "")
	noMarker(t, marker)
}

func TestRefuseProgramConfigNetworkAndHelperKeys(t *testing.T) {
	isolateGit(t)
	cases := []struct{ key, val string }{
		{"extensions.partialclone", "origin"},
		{"remote.o.promisor", "true"},
		{"remote.o.partialclonefilter", "blob:none"},
		{"remote.o.uploadpack", "some-program"},
		{"remote.o.receivepack", "some-program"},
		{"remote.o.vcs", "some-program"},
		{"core.sshCommand", "some-program"},
		{"core.gitProxy", "some-program"},
		{"credential.helper", "some-program"},
		{"credential.https://example.com.helper", "some-program"},
		{"url.ext::sh -c x.insteadOf", "https://example.com/"},
		{"url.https://example.com/.pushInsteadOf", "ext::sh -c x"},
		{"protocol.ext.allow", "always"},
		{"protocol.allow", "always"},
		{"protocol.foo.allow", "user"},
		{"protocol.file.allow", "always"},
		{"protocol.allow", "user"},
		{"remote.o.url", "foo::bar"},
		{"remote.o.pushurl", "foo::bar"},
		{"core.askPass", "some-program"},
		{"core.alternateRefsCommand", "some-program"},
		{"trailer.t.command", "some-program"},
		{"submodule.s.update", "!some-program"},
		{"branch.main.mergeOptions", "--no-verify-signatures"},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			repo := newRepo(t)
			gitT(t, repo, "config", c.key, c.val)
			err := refuseProgramConfig(context.Background(), repo)
			wantRefusal(t, err, "")
			if strings.Contains(err.Error(), c.val) && c.val != "always" {
				t.Fatalf("the value is in the message: %v", err)
			}
		})
	}
	// Harmless settings pass.
	repo := newRepo(t)
	for _, kv := range [][2]string{
		{"remote.o.url", "https://example.com/r.git"}, {"remote.o.fetch", "+refs/heads/*:refs/remotes/o/*"},
		{"remote.o.promisor", "false"}, {"remote.o.uploadpack", ""}, {"core.sshCommand", ""},
		{"url.https://example.com/.insteadOf", "ex:"}, {"protocol.ext.allow", "never"}, {"protocol.file.allow", "never"}, {"protocol.foo.allow", ""},
		{"core.askPass", ""}, {"core.alternateRefsCommand", ""}, {"trailer.t.command", ""}, {"submodule.s.update", "checkout"},
		{"branch.main.remote", "o"}, {"branch.main.mergeOptions", ""},
	} {
		gitT(t, repo, "config", kv[0], kv[1])
	}
	if err := refuseProgramConfig(context.Background(), repo); err != nil {
		t.Fatalf("a harmless setting was refused: %v", err)
	}
}

// The refusal message is built from agent-controlled key text.
func TestGitEnvClosesTheUserProtocolPolicy(t *testing.T) {
	found := false
	for _, kv := range gitEnv() {
		if kv == "GIT_PROTOCOL_FROM_USER=0" {
			found = true
		}
	}
	if !found {
		t.Fatal("gitEnv does not carry GIT_PROTOCOL_FROM_USER=0")
	}
}

// Under git's "user" policy a plain fetch of a helper-scheme address starts
// the helper; the conductor's environment closes that policy.
func TestUserProtocolPolicyStartsAHelperOnlyWithoutTheEnvSetting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test program is a POSIX shell script")
	}
	isolateGit(t)
	repo := newRepo(t)
	marker := filepath.Join(t.TempDir(), "ran")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git-remote-foo"), []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	gitT(t, repo, "config", "protocol.foo.allow", "user")
	gitT(t, repo, "config", "remote.o.url", "foo::bar")
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_PROTOCOL_FROM_USER=") {
			env = append(env, kv)
		}
	}
	cmd := exec.Command("git", "-C", repo, "fetch", "o")
	cmd.Env = append(env, "GIT_TERMINAL_PROMPT=0")
	_ = cmd.Run()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("git behaviour changed: the user policy no longer starts the helper, so the baseline proof no longer holds: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	_, _ = gitRun(context.Background(), repo, "fetch", "o")
	noMarker(t, marker)
}

func TestRefusalMessageDropsControlCharactersAndQuotesTheKey(t *testing.T) {
	got := printableKey("remote.a\nb\x1b[31m.uploadpack\u0007‮")
	if strings.ContainsAny(got, "\n\x1b\a‮") {
		t.Fatalf("not printable: %q", got)
	}
	if !strings.HasPrefix(got, `"`) || !strings.HasSuffix(got, `"`) {
		t.Fatalf("not quoted: %s", got)
	}
	// A token in the subsection never reaches the message.
	got = printableKey("credential.https://user:s3cr3tt0ken@host.example/.helper")
	if strings.Contains(got, "s3cr3tt0ken") || strings.Contains(got, "host.example") || got != `"credential.<subsection>.helper"` {
		t.Fatalf("subsection echoed: %s", got)
	}
	isolateGit(t)
	tr := newRepo(t)
	gitT(t, tr, "config", "credential.https://user:s3cr3tt0ken@host.example/.helper", "some-program")
	if err := refuseProgramConfig(context.Background(), tr); err == nil || strings.Contains(err.Error(), "s3cr3tt0ken") {
		t.Fatalf("refusal = %v, want a refusal without the token", err)
	}
	// And through a real configuration file.
	isolateGit(t)
	repo := newRepo(t)
	cfg := filepath.Join(repo, ".git", "config")
	b, _ := os.ReadFile(cfg)
	b = append(b, []byte("[remote \"a\\\x1b[31mb\"]\n\tuploadpack = x\n")...)
	if err := os.WriteFile(cfg, b, 0o644); err != nil {
		t.Fatal(err)
	}
	err := refuseProgramConfig(context.Background(), repo)
	if err == nil {
		t.Fatal("not refused")
	}
	if strings.ContainsAny(err.Error(), "\n\x1b") && strings.Count(err.Error(), "\n") > 0 {
		t.Fatalf("control characters in %q", err.Error())
	}
}
