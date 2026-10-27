package gitevidence

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// signedBranch returns a repository whose mission branch holds a commit with a signature
// header followed by the evidence commit, and the evidence commit's id. The repository's own
// configuration names a program to verify signatures with and asks git to verify them while
// it reads history; the program leaves a marker file when it runs.
func signedBranch(t *testing.T) (dir, evidence, marker string) {
	t.Helper()
	dir, base, _, _ := repoWith(t)
	marker = filepath.Join(t.TempDir(), "ran")
	prog := filepath.Join(t.TempDir(), "verify.sh")
	if err := os.WriteFile(prog, []byte("#!/bin/sh\n: > '"+marker+"'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "checkout", "-q", "-b", "monoagent/t1/w1", base)
	write(t, dir, "feat/a.go", "a")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "work")
	raw := run(t, dir, "cat-file", "commit", "HEAD")
	head, rest, _ := strings.Cut(raw, "\n\n")
	lines := strings.Split(head, "\n")
	var withSig []string
	for _, l := range lines {
		withSig = append(withSig, l)
		if strings.HasPrefix(l, "committer ") {
			withSig = append(withSig, "gpgsig -----BEGIN PGP SIGNATURE-----", " ", " AAAA", " -----END PGP SIGNATURE-----")
		}
	}
	obj := strings.Join(withSig, "\n") + "\n\n" + rest + "\n"
	cmd := exec.Command("git", "hash-object", "-t", "commit", "-w", "--stdin")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(obj)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hash-object: %v\n%s", err, out)
	}
	signed := strings.TrimSpace(string(out))
	tree := run(t, dir, "rev-parse", signed+"^{tree}")
	evidence = run(t, dir, "commit-tree", tree, "-p", signed, "-m", "evidence")
	run(t, dir, "update-ref", "refs/heads/monoagent/t1/w1", evidence)
	run(t, dir, "checkout", "-q", "main")
	run(t, dir, "config", "log.showSignature", "true")
	run(t, dir, "config", "gpg.program", prog)
	run(t, dir, "config", "gpg.ssh.program", prog)
	run(t, dir, "config", "gpg.x509.program", prog)
	return dir, evidence, marker
}

func TestRepositoryConfigurationCannotRunAProgramInTheChecker(t *testing.T) {
	dir, evidence, marker := signedBranch(t)
	if err := Checker(dir)(bg, commitCheck(evidence, "feat/a.go")); err != nil {
		t.Fatalf("the evidence over a signed commit: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a program named by the repository's configuration ran inside the checker")
	}
}

func TestGlobIsTheFolderBeforeItsFirstWildcard(t *testing.T) {
	files, folders := cleanDeclared([]string{"src/**/*.go", "docs/*", "*.md", "**/x.go", "a/b[0-9]/c", "lib/util.go", "pkg/"})
	if strings.Join(files, ",") != "lib/util.go" {
		t.Fatalf("files %v", files)
	}
	if strings.Join(folders, ",") != "src/,docs/,a/,pkg/" {
		t.Fatalf("folders %v", folders)
	}
	dir, _, _, _ := repoWith(t)
	write(t, dir, "src/deep/er/x.go", "x")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "x")
	if err := Checker(dir)(bg, commitCheck(run(t, dir, "rev-parse", "HEAD"), "src/**/*.go")); err != nil {
		t.Fatalf("a commit under the glob's folder: %v", err)
	}
	if err := Checker(dir)(bg, commitCheck(run(t, dir, "rev-parse", "HEAD"), "other/**/*.go")); err == nil {
		t.Fatal("a commit outside the glob's folder passed")
	}
}
