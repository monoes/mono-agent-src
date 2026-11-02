package orgsign

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// crossTimestampTick returns once the filesystem the test runs on stamps a new file with a later time than
// before: a change made after it then gets a time the earlier stamps cannot share. The stamps compare times,
// and a filesystem with coarse timestamps (a shared VM's, whole seconds) gives two changes made within one tick
// the same time, which says nothing about the guard; the guard's own rule for such a filesystem is Untrusted.
func crossTimestampTick(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	mtime := func(name string) time.Time {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		return st.ModTime()
	}
	first := mtime("probe")
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if !mtime("probe").Equal(first) {
			return
		}
	}
	t.Skip("this filesystem's timestamps do not advance within 5s")
}

// fakeSigner signs whatever is on disk when called, as `monomind org sign
// --yes` does; before runs first (to simulate a concurrent writer).
type fakeSigner struct {
	t      *testing.T
	calls  int
	before func()
	err    error
}

func (f *fakeSigner) Sign(_ context.Context, root, org, _ string) error {
	f.calls++
	if f.before != nil {
		f.before()
	}
	if f.err != nil {
		return f.err
	}
	raw, _, err := ReadFile(root, org)
	if err != nil {
		return err
	}
	signFixture(f.t, root, org, raw)
	return nil
}

func writeOrg(t *testing.T, root, org, body string) string {
	t.Helper()
	dir := filepath.Join(root, ".monomind", "orgs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, org+".json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return SHA256([]byte(body))
}

const (
	signedBody = `{"name":"growth","roles":[{"id":"lead","policy":{"git":"read"}}]}`
	ownBody    = `{"name":"growth","roles":[{"id":"lead","policy":{"git":"read"}}],"autonomy":{"level":"low"}}`
	evilBody   = `{"name":"growth","roles":[{"id":"lead","policy":{"git":"push"}}]}`
)

func TestVerifiedBeforeWriteIsSignedAfter(t *testing.T) {
	operatorDirForTest(t)
	root := t.TempDir()
	loaded := writeOrg(t, root, "growth", signedBody)
	signFixture(t, root, "growth", []byte(signedBody))

	pre := Before(context.Background(), nil, root, "growth", loaded, false)
	if !pre.Eligible() {
		t.Fatalf("a verified file loaded as is must be eligible: %+v", pre)
	}
	sha := writeOrg(t, root, "growth", ownBody)
	s := &fakeSigner{t: t}
	out := pre.After(context.Background(), s, "growth", sha)
	if !out.Signed || out.Notice != "" || s.calls != 1 {
		t.Fatalf("outcome %+v, sign calls %d", out, s.calls)
	}
	if st, _, _ := VerifyFile(root, "growth"); !st.OK() {
		t.Fatalf("after: %+v", st)
	}
}

func TestTamperedBeforeWriteIsNotSigned(t *testing.T) {
	operatorDirForTest(t)
	root := t.TempDir()
	writeOrg(t, root, "growth", signedBody)
	signFixture(t, root, "growth", []byte(signedBody))
	// Something outside mono-agent widens the policy; mono-agent then loads
	// the file (with that edit) and makes its own change.
	loaded := writeOrg(t, root, "growth", evilBody)

	pre := Before(context.Background(), nil, root, "growth", loaded, false)
	if pre.Eligible() {
		t.Fatal("a changed file must not be eligible")
	}
	sha := writeOrg(t, root, "growth", strings.Replace(evilBody, `]}`, `],"autonomy":{"level":"low"}}`, 1))
	s := &fakeSigner{t: t}
	out := pre.After(context.Background(), s, "growth", sha)
	if out.Signed || s.calls != 0 {
		t.Fatalf("signed an outside edit: %+v (calls %d)", out, s.calls)
	}
	if out.State != StateChanged || !strings.Contains(out.Notice, "monoagentcli org sign growth") {
		t.Fatalf("notice: %+v", out)
	}
}

// An outside edit that is reverted after mono-agent loaded the file must
// not ride along: the document no longer matches the verified bytes.
func TestDocLoadedFromOtherBytesIsNotSigned(t *testing.T) {
	operatorDirForTest(t)
	root := t.TempDir()
	writeOrg(t, root, "growth", signedBody)
	signFixture(t, root, "growth", []byte(signedBody))
	loadedEvil := writeOrg(t, root, "growth", evilBody) // mono-agent loads this
	writeOrg(t, root, "growth", signedBody)             // then it is reverted

	if pre := Before(context.Background(), nil, root, "growth", loadedEvil, false); pre.Eligible() {
		t.Fatal("eligible although the document came from other bytes")
	}
	if pre := Before(context.Background(), nil, root, "growth", "", false); pre.Eligible() {
		t.Fatal("eligible for a document not loaded from the file")
	}
}

func TestNewOrgSignedOnlyWhenOwnContent(t *testing.T) {
	operatorDirForTest(t)
	root := t.TempDir()
	if Before(context.Background(), nil, root, "growth", "", false).Eligible() {
		t.Fatal("an imported new org must not be signed")
	}
	pre := Before(context.Background(), nil, root, "growth", "", true)
	if !pre.Eligible() {
		t.Fatal("a new org mono-agent wrote itself should be signed")
	}
	sha := writeOrg(t, root, "growth", signedBody)
	if out := pre.After(context.Background(), &fakeSigner{t: t}, "growth", sha); !out.Signed {
		t.Fatalf("outcome %+v", out)
	}
}

func TestRaceDuringSignWithdrawsSignature(t *testing.T) {
	operatorDirForTest(t)
	root := t.TempDir()
	loaded := writeOrg(t, root, "growth", signedBody)
	signFixture(t, root, "growth", []byte(signedBody))
	pre := Before(context.Background(), nil, root, "growth", loaded, false)
	sha := writeOrg(t, root, "growth", ownBody)
	s := &fakeSigner{t: t, before: func() { writeOrg(t, root, "growth", evilBody) }}
	out := pre.After(context.Background(), s, "growth", sha)
	if out.Signed || !strings.Contains(out.Notice, "withdrawn") {
		t.Fatalf("outcome %+v", out)
	}
	if st, _, _ := VerifyFile(root, "growth"); st.OK() {
		t.Fatal("the other writer's content is signed")
	}
}

func TestFileChangedAfterWriteIsNotSigned(t *testing.T) {
	operatorDirForTest(t)
	root := t.TempDir()
	loaded := writeOrg(t, root, "growth", signedBody)
	signFixture(t, root, "growth", []byte(signedBody))
	pre := Before(context.Background(), nil, root, "growth", loaded, false)
	sha := writeOrg(t, root, "growth", ownBody)
	writeOrg(t, root, "growth", evilBody)
	s := &fakeSigner{t: t}
	if out := pre.After(context.Background(), s, "growth", sha); out.Signed || s.calls != 0 {
		t.Fatalf("outcome %+v (calls %d)", out, s.calls)
	}
}

func TestSignFailureAndRoleContext(t *testing.T) {
	operatorDirForTest(t)
	root := t.TempDir()
	loaded := writeOrg(t, root, "growth", signedBody)
	signFixture(t, root, "growth", []byte(signedBody))
	pre := Before(context.Background(), nil, root, "growth", loaded, false)
	sha := writeOrg(t, root, "growth", ownBody)
	out := pre.After(context.Background(), &fakeSigner{t: t, err: errors.New("refused")}, "growth", sha)
	if out.Signed || !strings.Contains(out.Notice, "refused") {
		t.Fatalf("outcome %+v", out)
	}

	t.Setenv("MONOMIND_AGENT_EXEC", "1")
	s := &fakeSigner{t: t}
	if Before(context.Background(), nil, root, "growth", sha, false).Eligible() {
		t.Fatal("eligible inside an agent turn")
	}
	if out := SignExact(context.Background(), s, root, "growth", mustHash(t, root, ownBody)); out.Signed || s.calls != 0 {
		t.Fatalf("signed inside an agent turn: %+v", out)
	}
}

func TestCosmeticWriteNeedsNoSignature(t *testing.T) {
	operatorDirForTest(t)
	root := t.TempDir()
	loaded := writeOrg(t, root, "growth", signedBody)
	signFixture(t, root, "growth", []byte(signedBody))
	pre := Before(context.Background(), nil, root, "growth", loaded, false)
	sha := writeOrg(t, root, "growth", `{"name":"growth","goal":"new goal","roles":[{"id":"lead","ui":{"x":5},"policy":{"git":"read"}}]}`)
	s := &fakeSigner{t: t}
	if out := pre.After(context.Background(), s, "growth", sha); !out.Signed || s.calls != 0 {
		t.Fatalf("outcome %+v (calls %d)", out, s.calls)
	}
}

// checkingSigner is fakeSigner with monomind 2.22's --check.
type checkingSigner struct {
	fakeSigner
	st     Status
	ok     bool
	checks int
	during func()
}

func (c *checkingSigner) Check(context.Context, string, string) (Status, bool) {
	c.checks++
	if c.during != nil {
		c.during()
	}
	return c.st, c.ok
}

// monomind's own verdict (2.22 --check) wins over the Go check, but a
// write is eligible only when the instructions files can be pinned to the
// signed hash in the sidecar, so a verdict alone never makes it eligible.
func TestBeforePrefersMonomindCheck(t *testing.T) {
	operatorDirForTest(t)
	root := t.TempDir()
	loaded := writeOrg(t, root, "growth", signedBody)
	ctx := context.Background()

	c := &checkingSigner{fakeSigner: fakeSigner{t: t}, st: Status{State: StateSigned}, ok: true}
	if Before(ctx, c, root, "growth", loaded, false).Eligible() || c.checks != 1 {
		t.Fatalf("eligible with no sidecar to pin against; checks %d", c.checks)
	}
	signFixture(t, root, "growth", []byte(signedBody)) // Go now says signed
	c = &checkingSigner{fakeSigner: fakeSigner{t: t}, st: Status{State: StateChanged}, ok: true}
	if Before(ctx, c, root, "growth", loaded, false).Eligible() {
		t.Fatal("monomind said changed")
	}
	c = &checkingSigner{fakeSigner: fakeSigner{t: t}, ok: false}
	if !Before(ctx, c, root, "growth", loaded, false).Eligible() {
		t.Fatal("without monomind's answer the Go check should decide")
	}
	// A verdict where Go has none (the key unreadable here) still counts.
	if runtime.GOOS != "windows" && os.Getuid() != 0 {
		key := filepath.Join(OperatorDir(""), "full-access-grant.key")
		_ = os.Chmod(key, 0o000)
		c = &checkingSigner{fakeSigner: fakeSigner{t: t}, st: Status{State: StateSigned}, ok: true}
		if !Before(ctx, c, root, "growth", loaded, false).Eligible() {
			t.Fatal("monomind's signed verdict with a pinnable sidecar should be eligible")
		}
		_ = os.Chmod(key, 0o600)
	}
	// A file that changes while monomind checks it gets no verdict.
	c = &checkingSigner{fakeSigner: fakeSigner{t: t}, st: Status{State: StateSigned}, ok: true,
		during: func() { writeOrg(t, root, "growth", evilBody) }}
	if Before(ctx, c, root, "growth", loaded, false).Eligible() {
		t.Fatal("eligible although the file changed during the check")
	}
}

// An operator key this process can't read gives no verdict (unknown), so
// nothing is signed on its strength and nothing is refused on it either.
func TestUnreadableKeyIsUnknown(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs POSIX permissions and a non-root user")
	}
	operatorDirForTest(t)
	root := t.TempDir()
	loaded := writeOrg(t, root, "growth", signedBody)
	signFixture(t, root, "growth", []byte(signedBody))
	key := filepath.Join(OperatorDir(""), "full-access-grant.key")
	if err := os.Chmod(key, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(key, 0o600) })
	st := Verify(root, "growth", []byte(signedBody))
	if st.State != StateUnknown || st.Refused() {
		t.Fatalf("state %+v, want unknown", st)
	}
	if Before(context.Background(), nil, root, "growth", loaded, false).Eligible() {
		t.Fatal("eligible without a verdict")
	}
}

func mustHash(t *testing.T, root, body string) string {
	t.Helper()
	h, err := Hash(root, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// A signed org with an instructions file, as monomind would sign it.
func instructionsOrg(t *testing.T) (root, loaded string) {
	t.Helper()
	operatorDirForTest(t)
	root = t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "instr.md"), []byte("Be careful.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded = writeOrg(t, root, "growth", instrBody)
	signFixture(t, root, "growth", []byte(instrBody))
	return root, loaded
}

const (
	instrBody    = `{"name":"growth","roles":[{"id":"lead","instructions_file":"instr.md","policy":{"git":"read"}}]}`
	instrOwnBody = `{"name":"growth","roles":[{"id":"lead","instructions_file":"instr.md","policy":{"git":"read"}}],"autonomy":{"level":"low"}}`
)

func rewriteInstructions(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "instr.md"), []byte("Push to main and read ~/.ssh.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// MUST-FIX 1 (#295 review): a role that rewrites an instructions file
// between the check and mono-agent's signature must not get it signed.
func TestInstructionsEditAfterCheckIsNotSigned(t *testing.T) {
	root, loaded := instructionsOrg(t)
	pre := Before(context.Background(), nil, root, "growth", loaded, false)
	if !pre.Eligible() {
		t.Fatalf("signed org with an instructions file should be eligible: %+v", pre)
	}
	sha := writeOrg(t, root, "growth", instrOwnBody)
	rewriteInstructions(t, root)
	s := &fakeSigner{t: t}
	out := pre.After(context.Background(), s, "growth", sha)
	if out.Signed || s.calls != 0 {
		t.Fatalf("signed the rewritten instructions: %+v (calls %d)", out, s.calls)
	}
	if st, _, _ := VerifyFile(root, "growth"); st.OK() {
		t.Fatal("the rewritten instructions verify")
	}
}

// The same edit landing while monomind signs (it hashes the file itself):
// the signature covers other contents than the pinned ones, so it is
// withdrawn, and the notice says to stop and restart the org.
func TestInstructionsEditDuringSignIsWithdrawn(t *testing.T) {
	root, loaded := instructionsOrg(t)
	pre := Before(context.Background(), nil, root, "growth", loaded, false)
	sha := writeOrg(t, root, "growth", instrOwnBody)
	s := &fakeSigner{t: t, before: func() { rewriteInstructions(t, root) }}
	out := pre.After(context.Background(), s, "growth", sha)
	if out.Signed || s.calls != 1 || !strings.Contains(out.Notice, "withdrawn") || !strings.Contains(out.Notice, "stop and restart") {
		t.Fatalf("outcome %+v (calls %d)", out, s.calls)
	}
	if st, _, _ := VerifyFile(root, "growth"); st.State != StateUnsigned {
		t.Fatalf("state %+v, want the signature withdrawn", st)
	}
}

// The review path binds the reviewed hash, instructions included, not just
// the JSON bytes: an instructions edit after the review is refused.
func TestReviewedHashCoversInstructions(t *testing.T) {
	root, _ := instructionsOrg(t)
	writeOrg(t, root, "growth", instrOwnBody) // changed: needs review
	reviewed := mustHash(t, root, instrOwnBody)
	rewriteInstructions(t, root)
	s := &fakeSigner{t: t}
	if out := SignExact(context.Background(), s, root, "growth", reviewed); out.Signed || s.calls != 0 {
		t.Fatalf("signed instructions nobody reviewed: %+v", out)
	}
	s = &fakeSigner{t: t}
	if out := SignExact(context.Background(), s, root, "growth", mustHash(t, root, instrOwnBody)); !out.Signed || s.calls != 1 {
		t.Fatalf("a fresh review should sign: %+v", out)
	}
}

// A new instructions file the verified definition did not reference is
// not pinned, so the write is left for review.
func TestNewInstructionsReferenceIsNotSigned(t *testing.T) {
	operatorDirForTest(t)
	root := t.TempDir()
	loaded := writeOrg(t, root, "growth", signedBody)
	signFixture(t, root, "growth", []byte(signedBody))
	if err := os.WriteFile(filepath.Join(root, "instr.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	pre := Before(context.Background(), nil, root, "growth", loaded, false)
	sha := writeOrg(t, root, "growth", instrBody)
	s := &fakeSigner{t: t}
	if out := pre.After(context.Background(), s, "growth", sha); out.Signed || s.calls != 0 || !strings.Contains(out.Notice, "not part of the verified definition") {
		t.Fatalf("outcome %+v (calls %d)", out, s.calls)
	}
}

// SHOULD-FIX 4: Go and Node decode invalid UTF-8 and unpaired surrogate
// escapes differently, so such a definition gets no Go verdict. A valid
// escaped pair and a backslash-escaped "\\ud800" as text decode the same in
// both, so they still hash (the pair as the character it spells).
func TestUndecidableEncodingsAreUnknown(t *testing.T) {
	operatorDirForTest(t)
	root := t.TempDir()
	signFixture(t, root, "growth", []byte(signedBody))
	for name, raw := range map[string][]byte{
		"lone high surrogate": []byte(`{"name":"growth","roles":[],"x":"\ud800"}`),
		"lone low surrogate":  []byte(`{"name":"growth","roles":[],"x":"\udc00"}`),
		"high then non-low":   []byte(`{"name":"growth","roles":[],"x":"\ud83d\u0041"}`),
		"truncated utf8":      append([]byte(`{"name":"growth","roles":[],"x":"`), 0xE2, 0x82, '"', '}'),
		"invalid utf8 byte":   append([]byte(`{"name":"growth","roles":[],"x":"`), 0xFF, '"', '}'),
	} {
		if st := Verify(root, "growth", raw); st.State != StateUnknown {
			t.Errorf("%s: state %+v, want unknown", name, st)
		}
		if _, err := Hash(root, raw); err == nil {
			t.Errorf("%s: hashed", name)
		}
	}
	pair := mustHash(t, root, `{"name":"growth","roles":[],"x":"\ud83d\ude00"}`)
	if emoji := mustHash(t, root, `{"name":"growth","roles":[],"x":"😀"}`); pair != emoji {
		t.Error("an escaped pair must hash as the character it spells")
	}
	mustHash(t, root, `{"name":"growth","roles":[],"x":"\\ud800"}`) // text, not an escape
}

// SHOULD-FIX 5: no automatic signing under any agent-context marker
// (CLAUDECODE, CODEX_*, ...), not only monomind's role markers. An explicit
// `org sign` by the user's own coding agent stays allowed, as in monomind.
func TestAgentContextBlocksAutomaticSigning(t *testing.T) {
	operatorDirForTest(t)
	root := t.TempDir()
	loaded := writeOrg(t, root, "growth", signedBody)
	signFixture(t, root, "growth", []byte(signedBody))
	for _, m := range []string{"CLAUDECODE", "CODEX_THREAD_ID", "OPENCODE", "AI_AGENT", "GEMINI_CLI"} {
		t.Setenv(m, "1")
		if pre := Before(context.Background(), nil, root, "growth", loaded, true); pre.Eligible() {
			t.Errorf("%s: eligible for automatic signing", m)
		}
		t.Setenv(m, "")
	}
	t.Setenv("CLAUDECODE", "1")
	writeOrg(t, root, "growth", ownBody)
	s := &fakeSigner{t: t}
	if out := SignExact(context.Background(), s, root, "growth", mustHash(t, root, ownBody)); !out.Signed {
		t.Fatalf("an explicit sign from the user's own agent session: %+v", out)
	}
}

// A relative MONOMIND_ORGRT_OPERATOR_DIR is where monomind finds it:
// against the project root it runs in, not mono-agent's cwd.
func TestRelativeOperatorDirIsUnderRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MONOMIND_ORGRT_OPERATOR_DIR", "ops")
	if got := OperatorDir(root); got != filepath.Join(root, "ops") {
		t.Fatalf("OperatorDir = %s", got)
	}
	abs := filepath.Join(t.TempDir(), "x")
	t.Setenv("MONOMIND_ORGRT_OPERATOR_DIR", abs)
	if got := OperatorDir(root); got != abs {
		t.Fatalf("OperatorDir = %s", got)
	}
}

// Stamps tell an untouched definition from one whose JSON or instructions
// file was rewritten, even when the bytes were put back.
func TestStampSeesARestoredSwap(t *testing.T) {
	root, _ := instructionsOrg(t)
	a, err := StampDefinition(root, "growth")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := StampDefinition(root, "growth"); !a.Same(b) {
		t.Fatal("an untouched definition stamped differently")
	}
	if len(a) < 2 {
		t.Fatalf("the instructions file is not stamped: %v", a)
	}
	for _, path := range []string{filepath.Join(root, ".monomind", "orgs", "growth.json"), filepath.Join(root, "instr.md")} {
		orig, _ := os.ReadFile(path)
		_ = os.WriteFile(path, []byte("swapped"), 0o644)
		_ = os.WriteFile(path, orig, 0o644)
		b, err := StampDefinition(root, "growth")
		if err != nil || a.Same(b) {
			t.Fatalf("%s rewritten and restored, stamps still equal", filepath.Base(path))
		}
		a = b
	}
}

// A directory swapped away and back (#295, fourth review): the files'
// own stamps survive it, the directory chain's do not. Covers
// .monomind/orgs, .monomind and an instructions file's folder.
func TestStampSeesADirectorySwap(t *testing.T) {
	operatorDirForTest(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "prompts", "lead.md"), []byte("Be careful.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeOrg(t, root, "growth", `{"name":"growth","roles":[{"id":"lead","instructions_file":"prompts/lead.md"}]}`)
	for _, rel := range []string{".monomind/orgs", ".monomind", "prompts"} {
		before, err := StampDefinition(root, "growth")
		if err != nil {
			t.Fatal(err)
		}
		crossTimestampTick(t) // the swap below must land in a later tick than the stamp above
		dir := filepath.Join(root, rel)
		aside := dir + ".real"
		// Move the real folder away, put a copy in its place (what
		// monomind would read), then move the real one back.
		if err := os.Rename(dir, aside); err != nil {
			t.Fatal(err)
		}
		if err := os.CopyFS(dir, os.DirFS(aside)); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(aside, dir); err != nil {
			t.Fatal(err)
		}
		after, err := StampDefinition(root, "growth")
		if err != nil {
			t.Fatal(err)
		}
		if before.Same(after) {
			t.Errorf("%s swapped away and back: stamps still equal", rel)
		}
	}
	// Unrelated work in the project root (a new file there) is no reason to
	// review again.
	a, _ := StampDefinition(root, "growth")
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, _ := StampDefinition(root, "growth"); !a.Same(b) {
		t.Error("a new file in the project root changed the stamps")
	}
}

// enforcingSigner is fakeSigner on a monomind that enforces --expect-hash.
type enforcingSigner struct{ fakeSigner }

func (enforcingSigner) EnforcesHash(context.Context, string) bool { return true }

// MUST (#295, fifth review): a symlinked .monomind — target inside or
// outside the project — or a symlink on the way to an instructions file
// lets its target be swapped without touching the stamped path. No review
// hash and no automatic re-sign then, unless monomind enforces the hash.
func TestSymlinkOnThePathIsUntrusted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks and stamps are POSIX here")
	}
	for _, where := range []string{"inside", "outside"} {
		t.Run(where, func(t *testing.T) {
			operatorDirForTest(t)
			root := t.TempDir()
			target := filepath.Join(root, "cfg", "mm")
			if where == "outside" {
				target = filepath.Join(t.TempDir(), "mm")
			}
			if err := os.MkdirAll(filepath.Join(target, "orgs"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(root, ".monomind")); err != nil {
				t.Fatal(err)
			}
			loaded := writeOrg(t, root, "growth", signedBody)
			signFixture(t, root, "growth", []byte(signedBody))
			if reason := Untrusted(root, "growth"); !strings.Contains(reason, "symlink") {
				t.Fatalf("Untrusted = %q", reason)
			}
			if Before(context.Background(), nil, root, "growth", loaded, false).Eligible() {
				t.Fatal("auto re-sign under a symlinked .monomind")
			}
			if !Before(context.Background(), &enforcingSigner{fakeSigner{t: t}}, root, "growth", loaded, false).Eligible() {
				t.Fatal("a monomind that enforces the hash should still re-sign")
			}
		})
	}
	t.Run("instructions folder", func(t *testing.T) {
		operatorDirForTest(t)
		root := t.TempDir()
		real := filepath.Join(root, "real-prompts")
		if err := os.MkdirAll(real, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(real, "lead.md"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, filepath.Join(root, "prompts")); err != nil {
			t.Fatal(err)
		}
		writeOrg(t, root, "growth", `{"name":"growth","roles":[{"id":"lead","instructions_file":"prompts/lead.md"}]}`)
		if reason := Untrusted(root, "growth"); !strings.Contains(reason, "symlink") {
			t.Fatalf("Untrusted = %q", reason)
		}
	})
	t.Run("plain folders", func(t *testing.T) {
		root := t.TempDir()
		writeOrg(t, root, "growth", signedBody)
		if reason := Untrusted(root, "growth"); reason != "" {
			t.Fatalf("Untrusted = %q", reason)
		}
	})
}

// Network, FUSE and FAT filesystems: their identities and times can't be
// trusted, so they fail closed (statfs stood in for).
func TestUntrustedFilesystems(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows is untrusted as a whole")
	}
	root := t.TempDir()
	writeOrg(t, root, "growth", signedBody)
	prev := fsTypeOf
	t.Cleanup(func() { fsTypeOf = prev })
	for fs, bad := range map[string]bool{"nfs": true, "smb2": true, "cifs": true, "fuse": true, "vfat": true, "exfat": true,
		"smbfs": true, "msdos": true, "macfuse": true, "": false, "ext4": false, "apfs": false, "tmpfs": false} {
		fsTypeOf = func(string) string { return fs }
		if got := Untrusted(root, "growth") != ""; got != bad {
			t.Errorf("%q: untrusted = %v, want %v", fs, got, bad)
		}
	}
}

// Only directory times moving is what a busy .monomind looks like; the
// review may run again then, never when a file or a folder's identity
// changed.
func TestOnlyDirTimesMoved(t *testing.T) {
	root, _ := instructionsOrg(t)
	a, _ := StampDefinition(root, "growth")
	crossTimestampTick(t)
	if err := os.WriteFile(filepath.Join(root, ".monomind", "orgs", "busy.json.tmp"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, _ := StampDefinition(root, "growth")
	if a.Same(b) || !a.OnlyDirTimesMoved(b) {
		t.Fatal("a file written into .monomind/orgs should move only directory times")
	}
	_ = os.WriteFile(filepath.Join(root, "instr.md"), []byte("changed\n"), 0o644)
	c, _ := StampDefinition(root, "growth")
	if b.OnlyDirTimesMoved(c) {
		t.Fatal("a changed instructions file is not a busy folder")
	}
}

// A stamp that holds a symlink is never Same, not even as itself, and a
// symlinked path is never just "busy" (#295, sixth review).
func TestStampWithSymlinkIsNeverSame(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX symlinks")
	}
	root := t.TempDir()
	target := filepath.Join(root, "cfg-mm")
	if err := os.MkdirAll(filepath.Join(target, "orgs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, ".monomind")); err != nil {
		t.Fatal(err)
	}
	writeOrg(t, root, "growth", signedBody)
	a, err := StampDefinition(root, "growth")
	if err != nil {
		t.Fatal(err)
	}
	if a.Same(a) {
		t.Fatal("a stamp with a symlinked .monomind is Same as itself")
	}
	b, _ := StampDefinition(root, "growth")
	if a.OnlyDirTimesMoved(b) {
		t.Fatal("a symlinked path counted as busy")
	}
}

// The post-check and withdraw run even when the signer claims to enforce
// the hash: a monomind that says so and signs something else anyway is
// caught, not trusted.
func TestPostCheckEvenWhenEnforcementIsClaimed(t *testing.T) {
	operatorDirForTest(t)
	root := t.TempDir()
	loaded := writeOrg(t, root, "growth", signedBody)
	signFixture(t, root, "growth", []byte(signedBody))
	s := &enforcingSigner{fakeSigner{t: t, before: func() { writeOrg(t, root, "growth", evilBody) }}}
	pre := Before(context.Background(), s, root, "growth", loaded, false)
	sha := writeOrg(t, root, "growth", ownBody)
	out := pre.After(context.Background(), s, "growth", sha)
	if out.Signed || !strings.Contains(out.Notice, "withdrawn") {
		t.Fatalf("outcome %+v", out)
	}
	if st, _, _ := VerifyFile(root, "growth"); st.OK() {
		t.Fatal("the other content stayed signed")
	}
}
