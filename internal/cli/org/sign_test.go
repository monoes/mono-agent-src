package org

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/cli/orgenv"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orgsign"
	"github.com/monoes/mono-agent/internal/orgsign/orgsigntest"
)

// fakeSigningMonomind is a monomind of the given version that logs every
// call and answers `org sign` like 2.21 does: without --yes a review (plus
// the refusal footer) and exit 1; with --yes "signed". It signs nothing
// itself — recordingSigner writes the signature, as monomind would.
func fakeSigningMonomind(t *testing.T, version string) (logPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	bin := filepath.Join(dir, "monomind")
	// A 2.22 advertises its org-sign capabilities (#568), unless the test
	// sets FAKE_NO_SIGN_CAPS (an older 2.22 build without them).
	signCaps := ""
	if version == "2.22.0" {
		signCaps = `'"$SIGN_CAPS"'`
	}
	script := `#!/bin/sh
SIGN_CAPS=',"org-sign-expect-hash","org-sign-review-json","org-sign-check"'
if [ -n "$FAKE_NO_SIGN_CAPS" ]; then SIGN_CAPS=''; fi
echo "$(pwd) $*" >> '` + logPath + `'
if [ "$1" = "--version" ] && [ "$2" = "--json" ]; then
  echo '{"v":1,"version":"` + version + `","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1","org-tool-providers","org-endpoint-roles","org-federation","org-decision-attribution"` + signCaps + `]}'
  exit 0
fi
if [ "$1" = "org" ] && [ "$2" = "sign" ]; then
  if [ "$3" = "--help" ]; then echo "${FAKE_SIGN_HELP:-  --yes  Skip the confirmation}"; exit 0; fi
  if [ "$4" = "--format" ] && [ -n "$FAKE_REVIEW_JSON" ]; then printf '%s\n' "$FAKE_REVIEW_JSON"; exit 0; fi
  if [ "$4" = "--check" ]; then
    echo '{"orgs":[{"org":"'"$3"'","state":"'"${FAKE_CHECK_STATE:-changed}"'","message":"from monomind"}]}'
    exit 1
  fi
  if [ "$4" = "--yes" ]; then echo "org $3: signed"; exit 0; fi
  if [ -n "$FAKE_REVIEW_TOLINK" ] && [ ! -L .monomind ]; then
    # A role turns .monomind into a symlink to a copy during the read.
    mv .monomind .mm-real && ln -s .mm-real .monomind
  fi
  if [ -n "$FAKE_REVIEW_BUSY" ]; then
    # Something else writes into .monomind/orgs during the first N reviews.
    n=$(cat .monomind/busy-count 2>/dev/null || echo 0); n=$((n+1)); echo $n > .monomind/busy-count
    if [ "$n" -le "$FAKE_REVIEW_BUSY" ]; then : > ".monomind/orgs/busy-$$-$n.tmp"; fi
  fi
  if [ -n "$FAKE_REVIEW_DIRSWAP" ]; then
    # A role moves a whole folder away, puts a copy there for the read, and
    # moves the real one back (the copy is moved aside, not deleted).
    d="$FAKE_REVIEW_DIRSWAP"; mv "$d" "$d.real"; cp -R "$d.real" "$d"; mv "$d" "$d.copy"; mv "$d.real" "$d"
  fi
  if [ -n "$FAKE_REVIEW_SWAP" ]; then
    # A role swaps a file in for monomind's read and restores it after.
    f="$FAKE_REVIEW_SWAP"; cp "$f" "$f.bak"; echo '{"swapped":true}' > "$f"; cat "$f.bak" > "$f"; rm "$f.bak"
  fi
  printf '\033[1m\norg %s (changed):\033[0m\n  lead: runtime claude \302\267 git push \302\267 access scoped\n  Changed since the last signature:\n    roles.lead.policy.git: read -> push\n  1 protected path(s) would be quarantined as possible plants: .claude/settings.json \342\200\224 if they are yours, approve them with monomind org approve-paths <path>\nNot signed. Review the above, then sign it yourself in a terminal: monomind org sign <org> (or pass --yes).\n[ERROR] confirmation required (--yes)\n' "$3"
  exit 1
fi
if [ "$1" = "org" ] && [ "$2" = "run" ]; then
  echo "[ERROR] org $3: the definition has no operator signature — run monomind org sign $3 as the operator after reviewing the change"
  exit 1
fi
if [ "$1" = "org" ] && [ "$2" = "validate" ]; then echo "valid"; exit 0; fi
echo '{"v":1}'
exit 0
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(monomind.EnvOverride, bin)
	monomind.ResetCapabilityCache()
	t.Cleanup(monomind.ResetCapabilityCache)
	return logPath
}

// recordingSigner runs the real `monomind org sign --yes` invocation
// against the fake, then writes the signature the way monomind would.
type recordingSigner struct {
	t     *testing.T
	calls []string
}

func (s *recordingSigner) Sign(ctx context.Context, root, org, hash string) error {
	if err := (orgenv.MonomindSigner{}).Sign(ctx, root, org, hash); err != nil {
		return err
	}
	s.calls = append(s.calls, org)
	if _, err := orgsign.Hash(root, mustRead(s.t, root, org)); err != nil {
		// Content this package can't hash: monomind signs the hash it was
		// given (--expect-hash) after checking its own read.
		orgsigntest.SignHash(s.t, root, org, hash)
		return nil
	}
	orgsigntest.Sign(s.t, root, org)
	return nil
}

// EnforcesHash is the production signer's (monomind 2.22 --expect-hash).
func (s *recordingSigner) EnforcesHash(ctx context.Context, root string) bool {
	return orgenv.MonomindSigner{}.EnforcesHash(ctx, root)
}

func mustRead(t *testing.T, root, org string) []byte {
	t.Helper()
	raw, _, err := orgsign.ReadFile(root, org)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Check uses monomind's own --check, as the production signer does.
func (s *recordingSigner) Check(ctx context.Context, root, org string) (orgsign.Status, bool) {
	return orgenv.MonomindSigner{}.Check(ctx, root, org)
}

// newSigningFixture is newOrgCLIFixture on a fake monomind 2.21 with a
// scratch operator dir and a recording signer.
func newSigningFixture(t *testing.T, version string) (*orgCLIFixture, *recordingSigner, string) {
	t.Helper()
	f := newOrgCLIFixture(t)
	logPath := fakeSigningMonomind(t, version)
	t.Setenv("MONOMIND_ORGRT_OPERATOR_DIR", filepath.Join(t.TempDir(), "operator"))
	orgsigntest.AsOperator(t)
	s := &recordingSigner{t: t}
	prev := orgenv.Signer
	orgenv.Signer = s
	t.Cleanup(func() { orgenv.Signer = prev })
	return f, s, logPath
}

func (f *orgCLIFixture) signature(t *testing.T) orgsign.Status {
	t.Helper()
	st, _, err := orgsign.VerifyFile(f.root, "growth")
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// editOutside changes the org file the way a worker or a hand edit would.
func (f *orgCLIFixture) editOutside(t *testing.T, mutate func(m map[string]interface{})) {
	t.Helper()
	path := filepath.Join(orgdesign.OrgsDir(f.root), "growth.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	mutate(m)
	out, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

func widenLeadPolicy(m map[string]interface{}) {
	for _, r := range m["roles"].([]interface{}) {
		role := r.(map[string]interface{})
		if role["id"] == "lead" {
			role["policy"] = map[string]interface{}{"git": "push"}
		}
	}
}

func TestOrgWriteResignsAVerifiedOrg(t *testing.T) {
	f, s, logPath := newSigningFixture(t, "2.21.0")
	orgsigntest.Sign(t, f.root, "growth")

	f.mustRun(t, "automation", "add", "growth", "--workflow", f.outboundWF, "--alias", "publish_post")
	f.mustRun(t, "grant", "add", "growth", "--role", "writer", "--automation", "publish_post")
	if st := f.signature(t); !st.OK() {
		t.Fatalf("after mono-agent's own edits: %+v", st)
	}
	if len(s.calls) < 2 {
		t.Fatalf("sign calls %v, want one per changing write", s.calls)
	}
	calls, _ := os.ReadFile(logPath)
	root, _ := filepath.EvalSymlinks(f.root)
	if !strings.Contains(string(calls), root+" org sign growth --yes") {
		t.Fatalf("monomind org sign not run in the project root:\n%s", calls)
	}
}

func TestOrgWriteAfterOutsideEditIsNotSigned(t *testing.T) {
	f, s, _ := newSigningFixture(t, "2.21.0")
	orgsigntest.Sign(t, f.root, "growth")
	f.editOutside(t, widenLeadPolicy)

	stderr := captureStderr(t, func() {
		f.mustRun(t, "automation", "add", "growth", "--workflow", f.outboundWF, "--alias", "publish_post")
	})
	if len(s.calls) != 0 {
		t.Fatalf("signed an outside edit: %v", s.calls)
	}
	if st := f.signature(t); st.State != orgsign.StateChanged {
		t.Fatalf("state %+v, want changed", st)
	}
	if !strings.Contains(stderr, "not signed for this change") || !strings.Contains(stderr, "monoagentcli org sign growth") {
		t.Fatalf("no review notice on stderr: %q", stderr)
	}
}

// The daemon's reconcile (here `org reconcile`, the same pass) rewrites a
// file an outside edit left stale, and must not sign that edit with it.
func TestReconcileAfterOutsideEditNeverSigns(t *testing.T) {
	f, s, _ := newSigningFixture(t, "2.21.0")
	f.mustRun(t, "automation", "add", "growth", "--workflow", f.outboundWF, "--alias", "publish_post")
	f.mustRun(t, "grant", "add", "growth", "--role", "writer", "--automation", "publish_post")
	orgsigntest.Sign(t, f.root, "growth")
	s.calls = nil

	staleProvider(t, f.root) // what reconcile repairs
	f.editOutside(t, widenLeadPolicy)
	out := f.mustRun(t, "reconcile")
	orgs := out["orgs"].([]interface{})
	growth := orgs[0].(map[string]interface{})
	if growth["saved"] != true {
		t.Fatalf("reconcile did not rewrite the stale file: %v", growth)
	}
	if sig, _ := growth["signature"].(map[string]interface{}); sig["signed"] != false || !strings.Contains(sig["notice"].(string), "org sign growth") {
		t.Fatalf("reconcile outcome signature = %v", growth["signature"])
	}
	if len(s.calls) != 0 {
		t.Fatalf("reconcile signed an outside edit: %v", s.calls)
	}
	if providerCommand(t, f.root) != orgenv.SelfExecutable() {
		t.Fatal("provider not repaired")
	}
	if st := f.signature(t); st.OK() {
		t.Fatalf("outside edit ended up signed: %+v", st)
	}
}

func TestOrgSignCommand(t *testing.T) {
	f, s, logPath := newSigningFixture(t, "2.21.0")
	f.editOutside(t, widenLeadPolicy)

	st := f.mustRun(t, "sign", "growth", "--status")
	if st["supported"] != true || st["state"] != "unsigned" || st["signed"] != nil {
		t.Fatalf("status = %v", st)
	}

	// Without --yes (and not on a terminal): the review, nothing signed.
	rev := f.mustRun(t, "sign", "growth")
	review, _ := rev["review"].(string)
	// Everything monomind reports stays in the review, including paths
	// waiting for `org approve-paths` (signing approves none of them).
	if !strings.Contains(review, "git push") || !strings.Contains(review, "org approve-paths") ||
		strings.Contains(review, "confirmation required") || strings.Contains(review, "\x1b[") {
		t.Fatalf("review = %q", review)
	}
	if rev["signed"] != nil || len(s.calls) != 0 || rev["hash"] == nil || rev["hash"] == "" {
		t.Fatalf("review signed or lacks sha256: %v (calls %v)", rev, s.calls)
	}
	calls, _ := os.ReadFile(logPath)
	if !strings.Contains(string(calls), " org sign growth\n") {
		t.Fatalf("review did not run `monomind org sign growth` without --yes:\n%s", calls)
	}

	// A file that changed since the review is not signed.
	if _, err := f.run(t, "sign", "growth", "--yes", "--expect-hash", strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "changed after") {
		t.Fatalf("stale review signed: %v", err)
	}
	if len(s.calls) != 0 {
		t.Fatalf("signed a stale review: %v", s.calls)
	}

	signed := f.mustRun(t, "sign", "growth", "--yes", "--expect-hash", rev["hash"].(string))
	if signed["signed"] != true || len(s.calls) != 1 {
		t.Fatalf("sign = %v (calls %v)", signed, s.calls)
	}
	if st := f.signature(t); !st.OK() {
		t.Fatalf("after sign: %+v", st)
	}

	// Inside an org role or agent turn, never.
	f.editOutside(t, func(m map[string]interface{}) { m["runtime"] = "codex" })
	t.Setenv("MONOMIND_AGENT_EXEC", "1")
	if _, err := f.run(t, "sign", "growth", "--yes"); err == nil || !strings.Contains(err.Error(), "only the operator") {
		t.Fatalf("signed in an agent turn: %v", err)
	}
	if len(s.calls) != 1 {
		t.Fatalf("calls %v", s.calls)
	}
}

func TestConfirmOrgSign(t *testing.T) {
	var shown strings.Builder
	if !confirmOrgSign(&shown, strings.NewReader("y\n"), "growth", "org growth (unsigned):") {
		t.Fatal("yes declined")
	}
	if !strings.Contains(shown.String(), "org growth (unsigned):") || !strings.Contains(shown.String(), `Sign org "growth"`) {
		t.Fatalf("prompt = %q", shown.String())
	}
	if confirmOrgSign(&shown, strings.NewReader("\n"), "growth", "") {
		t.Fatal("empty answer signed")
	}
}

// Below 2.21 nothing about signing runs: writes don't sign, starts aren't
// checked, and `org sign` says it isn't needed.
func TestOrgSigningGatedOnMonomind221(t *testing.T) {
	f, s, logPath := newSigningFixture(t, "2.20.3")
	f.mustRun(t, "automation", "add", "growth", "--workflow", f.outboundWF, "--alias", "publish_post")
	if len(s.calls) != 0 {
		t.Fatalf("signed on 2.20: %v", s.calls)
	}
	st := f.mustRun(t, "sign", "growth", "--status")
	if st["supported"] != false {
		t.Fatalf("status on 2.20 = %v", st)
	}
	if _, err := f.run(t, "sign", "growth", "--yes"); err == nil || !strings.Contains(err.Error(), "2.21") {
		t.Fatalf("sign on 2.20: %v", err)
	}
	calls, _ := os.ReadFile(logPath)
	if strings.Contains(string(calls), "org sign") {
		t.Fatalf("ran org sign on 2.20:\n%s", calls)
	}
}

// monomind 2.21 refuses to start an unsigned org; the CLI says so up
// front, with the sign command and a machine-readable code.
func TestOrgRunRefusedForSignature(t *testing.T) {
	f, _, logPath := newSigningFixture(t, "2.21.0")
	_, err := f.run(t, "run", "growth")
	se, ok := monomind.AsOrgSignatureError(err)
	if !ok || se.Status.State != orgsign.StateUnsigned || !strings.Contains(err.Error(), "monoagentcli org sign growth") {
		t.Fatalf("run err = %v", err)
	}
	if se.JSONErrorFields()["code"] != "org_not_signed" {
		t.Fatalf("fields = %v", se.JSONErrorFields())
	}
	calls, _ := os.ReadFile(logPath)
	if strings.Contains(string(calls), "org run") {
		t.Fatalf("started monomind for an org it would refuse:\n%s", calls)
	}

	// A signed org starts (monomind's own refusal text is classified too).
	orgsigntest.Sign(t, f.root, "growth")
	_, err = f.run(t, "run", "growth")
	if _, ok := monomind.AsOrgSignatureError(err); !ok {
		t.Fatalf("monomind's refusal not classified: %v", err)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	// Drain the pipe while the command runs: output larger than the pipe buffer
	// would otherwise block the writer forever.
	drained := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r)
		drained <- b
	}()
	os.Stderr = w
	defer func() { os.Stderr = orig }()
	fn()
	w.Close()
	b := <-drained
	return string(b)
}

// On monomind 2.22 the state comes from monomind's own read-only check
// (`org sign --check --format json`), run in the project root; on 2.21
// that command does not exist and is never run.
func TestOrgSignUsesMonomindCheck(t *testing.T) {
	f, _, logPath := newSigningFixture(t, "2.22.0")
	t.Setenv("FAKE_CHECK_STATE", "signed") // no sidecar: the Go check alone says unsigned
	st := f.mustRun(t, "sign", "growth", "--status")
	if st["state"] != "signed" {
		t.Fatalf("status = %v, want monomind's verdict", st)
	}
	calls, _ := os.ReadFile(logPath)
	root, _ := filepath.EvalSymlinks(f.root)
	if !strings.Contains(string(calls), root+" org sign growth --check --format json") {
		t.Fatalf("--check not run in the project root:\n%s", calls)
	}

	f21, _, log21 := newSigningFixture(t, "2.21.0")
	if st := f21.mustRun(t, "sign", "growth", "--status"); st["state"] != "unsigned" {
		t.Fatalf("2.21 status = %v", st)
	}
	if calls, _ := os.ReadFile(log21); strings.Contains(string(calls), "--check") {
		t.Fatalf("ran --check on 2.21:\n%s", calls)
	}
}

// No mono-agent path signs from inside an org role's or agent turn's
// process tree (monomind's own markers): the write happens, unsigned,
// with a notice naming the marker, and monomind is never asked to sign.
func TestNoSigningFromRoleContext(t *testing.T) {
	for _, marker := range []string{"MONOMIND_ORG_ROLE", "MONOMIND_SDK_AGENT", "MONOMIND_AGENT_EXEC", "MONOMIND_CLINE_TURN", "MONOMIND_AIDER"} {
		t.Run(marker, func(t *testing.T) {
			f, s, logPath := newSigningFixture(t, "2.21.0")
			orgsigntest.Sign(t, f.root, "growth")
			t.Setenv(marker, "1")
			stderr := captureStderr(t, func() {
				f.mustRun(t, "automation", "add", "growth", "--workflow", f.outboundWF, "--alias", "publish_post")
			})
			if len(s.calls) != 0 || !strings.Contains(stderr, marker) {
				t.Fatalf("calls %v, stderr %q", s.calls, stderr)
			}
			if _, err := f.run(t, "sign", "growth", "--yes"); err == nil {
				t.Fatal("org sign --yes ran in a role context")
			}
			if calls, _ := os.ReadFile(logPath); strings.Contains(string(calls), "--yes") {
				t.Fatalf("monomind asked to sign:\n%s", calls)
			}
		})
	}
}

// A whole document from create-json is never signed automatically: the
// chat assistant is told to use it (SHOULD-FIX 5 of the #295 review).
func TestCreateJSONIsLeftForReview(t *testing.T) {
	f, s, _ := newSigningFixture(t, "2.21.0")
	doc := `{"name":"fresh","goal":"g","status":"stopped","schedule":null,"roles":[{"id":"lead","title":"Lead","type":"lead","reports_to":null,"responsibilities":["lead"],"policy":{"git":"push"}}]}`
	stderr := captureStderr(t, func() { f.mustRun(t, "create-json", "fresh", "--json", doc) })
	if len(s.calls) != 0 || !strings.Contains(stderr, "monoagentcli org sign fresh") {
		t.Fatalf("calls %v, stderr %q", s.calls, stderr)
	}
	if st, _, _ := orgsign.VerifyFile(f.root, "fresh"); st.State != orgsign.StateUnsigned {
		t.Fatalf("state %+v", st)
	}
}

// Nor is any write made under a coding agent's own markers (CLAUDECODE,
// CODEX_*, ...), and an explicit `org sign --yes` without a terminal is
// refused there too: that is how an agent would run it (#295 review).
func TestNoAutomaticSigningUnderAgentContext(t *testing.T) {
	f, s, _ := newSigningFixture(t, "2.21.0")
	orgsigntest.Sign(t, f.root, "growth")
	t.Setenv("CLAUDECODE", "1")
	stderr := captureStderr(t, func() {
		f.mustRun(t, "automation", "add", "growth", "--workflow", f.outboundWF, "--alias", "publish_post")
	})
	if len(s.calls) != 0 || !strings.Contains(stderr, "CLAUDECODE") {
		t.Fatalf("calls %v, stderr %q", s.calls, stderr)
	}
	for _, m := range []string{"CLAUDECODE", "CODEX_THREAD_ID", "OPENCODE"} {
		t.Setenv("CLAUDECODE", "")
		t.Setenv(m, "1")
		_, err := f.run(t, "sign", "growth", "--yes")
		blocked, ok := err.(*orgSignBlocked)
		if !ok || blocked.marker != m || blocked.JSONErrorFields()["code"] != "org_sign_agent_context" || !strings.Contains(err.Error(), "AI-agent shell") {
			t.Fatalf("%s: sign --yes without a terminal: %v", m, err)
		}
		// The app shows the reason before anyone clicks Sign.
		if st := f.mustRun(t, "sign", "growth", "--status"); st["blocked_by"] != m {
			t.Fatalf("%s: status = %v", m, st)
		}
		t.Setenv(m, "")
	}
	if len(s.calls) != 0 {
		t.Fatalf("calls %v", s.calls)
	}
	// The operator's own terminal (no markers) still signs.
	if signed := f.mustRun(t, "sign", "growth", "--yes"); signed["signed"] != true || len(s.calls) != 1 {
		t.Fatalf("explicit sign = %v (calls %v)", signed, s.calls)
	}
}

// org status asks monomind once for every org (2.22 --check --all).
func TestOrgStatusChecksAllOrgsInOneRun(t *testing.T) {
	f, _, logPath := newSigningFixture(t, "2.22.0")
	prev := orgenv.SignCheckAll
	orgenv.SignCheckAll = func(context.Context, string) (map[string]orgsign.Status, bool) {
		b, _ := os.ReadFile(logPath)
		_ = os.WriteFile(logPath, append(b, []byte("check-all\n")...), 0o644)
		return map[string]orgsign.Status{"growth": {State: orgsign.StateChanged}}, true
	}
	t.Cleanup(func() { orgenv.SignCheckAll = prev })
	out := orgenv.WithSignature(context.Background(), f.root, []byte(`{"v":1,"items":[{"name":"growth"},{"name":"other"}]}`))
	var got struct {
		Items []struct {
			Name      string          `json:"name"`
			Signature *orgsign.Status `json:"signature"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.Items[0].Signature == nil || got.Items[0].Signature.State != orgsign.StateChanged {
		t.Fatalf("growth = %+v", got.Items[0])
	}
	calls, _ := os.ReadFile(logPath)
	if strings.Count(string(calls), "check-all") != 1 || strings.Contains(string(calls), "--check") {
		t.Fatalf("calls:\n%s", calls)
	}
}

// MUST-FIX (third review of #295): a file swapped in only for monomind's
// review read, and restored after, would make the review show one
// definition and the hash sign another. The stamps (identity, size, mtime,
// ctime) of the org JSON and its instructions files catch it: no hash, so
// nothing can be signed from that review.
func TestReviewDetectsASwapDuringTheRead(t *testing.T) {
	f, s, _ := newSigningFixture(t, "2.21.0")
	instr := filepath.Join(f.root, "prompts.md")
	if err := os.WriteFile(instr, []byte("Be careful.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.editOutside(t, func(m map[string]interface{}) {
		m["roles"].([]interface{})[0].(map[string]interface{})["instructions_file"] = "prompts.md"
	})
	if rev := f.mustRun(t, "sign", "growth"); rev["hash"] == nil || rev["hash"] == "" {
		t.Fatalf("an undisturbed review has no hash: %v", rev)
	}
	for _, swapped := range []string{filepath.Join(orgdesign.OrgsDir(f.root), "growth.json"), instr} {
		t.Setenv("FAKE_REVIEW_SWAP", swapped)
		rev := f.mustRun(t, "sign", "growth")
		if h, _ := rev["hash"].(string); h != "" || !strings.Contains(rev["message"].(string), "changed during the review") {
			t.Fatalf("%s swapped during the review: %v", filepath.Base(swapped), rev)
		}
	}
	t.Setenv("FAKE_REVIEW_SWAP", "")
	for _, dir := range []string{orgdesign.OrgsDir(f.root), filepath.Join(f.root, ".monomind")} {
		t.Setenv("FAKE_REVIEW_DIRSWAP", dir)
		if h, _ := f.mustRun(t, "sign", "growth")["hash"].(string); h != "" {
			t.Fatalf("%s swapped during the review, hash %s", dir, h)
		}
	}
	t.Setenv("FAKE_REVIEW_DIRSWAP", "")
	// And an empty --expect-hash (what such a review hands over) never signs.
	if _, err := f.run(t, "sign", "growth", "--yes", "--expect-hash", ""); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("signed with an empty --expect-hash: %v", err)
	}
	if len(s.calls) != 0 {
		t.Fatalf("calls %v", s.calls)
	}
}

// monoagent's agent-context markers are monomind's AGENT_CONTEXT_ENV_MARKERS
// exactly (orgrt/agent-context.ts, 2.21.0 and #568), bare AGENT included.
func TestAgentContextMarkersMatchMonomind(t *testing.T) {
	t.Parallel()
	want := "CLAUDECODE CLAUDE_CODE_ENTRYPOINT MONOMIND_ORG_ROLE MONOMIND_SDK_AGENT MONOMIND_AGENT_EXEC AI_AGENT AGENT " +
		"CODEX_SANDBOX CODEX_SANDBOX_NETWORK_DISABLED CODEX_THREAD_ID CODEX_CI OPENCODE OPENCODE_PID ANTIGRAVITY_AGENT " +
		"GEMINI_CLI GROK_SESSION_ID GROK_MANAGED_BY_NPM COPILOT_CLI_BINARY_VERSION COPILOT_AGENT_SESSION_ID CRUSH " +
		"PI_CODING_AGENT QWEN_CODE PI_SESSION_ID DSH_SHELL DSH_SESSION_ID MONOMIND_CLINE_TURN MONOMIND_AIDER"
	if got := strings.Join(orgsign.AgentContextMarkers(), " "); got != want {
		t.Fatalf("markers\n got %s\nwant %s", got, want)
	}
}

// monomind 2.22's review JSON carries the hash of exactly what it
// reviewed, from one read: that is the signing target, with the stamps
// still guarding the read. Where Go can hash the definition too, the two
// must agree.
func TestReviewUsesMonomindsReviewedHash(t *testing.T) {
	f, s, _ := newSigningFixture(t, "2.22.0")
	t.Setenv("FAKE_SIGN_HELP", "  --expect-hash <hex>  Sign only if the signable hash is <hex>")
	// A blueprint role: Go has no hash for it (monomind#571), monomind does.
	f.editOutside(t, func(m map[string]interface{}) {
		m["roles"].([]interface{})[0].(map[string]interface{})["blueprint"] = "researcher"
	})
	const mmHash = "abababababababababababababababababababababababababababababababab"
	t.Setenv("FAKE_REVIEW_JSON", `{"org":"growth","state":"unsigned","hash":"`+strings.ToUpper(mmHash)+`","review":{"authority":["lead: runtime claude"],"diff":null},"reviewText":"org growth (unsigned):\n  lead: runtime claude · git read"}`)
	rev := f.mustRun(t, "sign", "growth")
	if rev["hash"] != mmHash || !strings.Contains(rev["review"].(string), "git read") {
		t.Fatalf("review = %v", rev)
	}
	signed := f.mustRun(t, "sign", "growth", "--yes", "--expect-hash", mmHash)
	if signed["signed"] != true || len(s.calls) != 1 {
		t.Fatalf("sign = %v (calls %v)", signed, s.calls)
	}

	// Without --expect-hash in this monomind, a hash Go can't check is
	// never signed on monomind's word.
	t.Setenv("FAKE_SIGN_HELP", "")
	monomind.ResetCapabilityCache()
	f.editOutside(t, func(m map[string]interface{}) { m["runtime"] = "codex" })
	if _, err := f.run(t, "sign", "growth", "--yes", "--expect-hash", mmHash); err == nil {
		t.Fatal("signed a hash nothing here could check")
	}
	if len(s.calls) != 1 {
		t.Fatalf("calls %v", s.calls)
	}
}

// Where Go can hash the definition, monomind's reviewed hash must equal
// it; any disagreement leaves the review without a hash to sign.
func TestReviewHashDisagreementSignsNothing(t *testing.T) {
	f, _, _ := newSigningFixture(t, "2.22.0")
	t.Setenv("FAKE_REVIEW_JSON", `{"org":"growth","state":"unsigned","hash":"`+strings.Repeat("cd", 32)+`","review":{},"reviewText":"org growth (unsigned):"}`)
	if rev := f.mustRun(t, "sign", "growth"); rev["hash"] != nil && rev["hash"] != "" {
		t.Fatalf("review = %v", rev)
	}
	raw := mustRead(t, f.root, "growth")
	goHash, _ := orgsign.Hash(f.root, raw)
	t.Setenv("FAKE_REVIEW_JSON", `{"org":"growth","state":"unsigned","hash":"`+goHash+`","review":{},"reviewText":"org growth (unsigned):"}`)
	if rev := f.mustRun(t, "sign", "growth"); rev["hash"] != goHash {
		t.Fatalf("agreeing review = %v", rev)
	}
}

// A review during which something else wrote into .monomind (only
// directory times moved) runs again, up to twice; one that stays busy asks
// for a new review. The count file sits in .monomind itself, which is
// stamped by identity and ctime too, so every attempt looks busy until N.
func TestBusyMonomindReviewIsRetried(t *testing.T) {
	f, _, _ := newSigningFixture(t, "2.21.0")
	prev := reviewRetryBackoff
	reviewRetryBackoff = time.Millisecond
	t.Cleanup(func() { reviewRetryBackoff = prev })
	count := filepath.Join(f.root, ".monomind", "busy-count")

	_ = os.WriteFile(count, []byte("0"), 0o644)
	t.Setenv("FAKE_REVIEW_BUSY", "1")
	if rev := f.mustRun(t, "sign", "growth"); rev["hash"] == nil || rev["hash"] == "" {
		t.Fatalf("one busy review then a clean one should give a hash: %v", rev)
	}
	_ = os.WriteFile(count, []byte("0"), 0o644)
	t.Setenv("FAKE_REVIEW_BUSY", "5")
	rev := f.mustRun(t, "sign", "growth")
	if h, _ := rev["hash"].(string); h != "" || !strings.Contains(rev["message"].(string), "review it again") {
		t.Fatalf("a review busy every time = %v", rev)
	}
	if b, _ := os.ReadFile(count); strings.TrimSpace(string(b)) != "3" {
		t.Fatalf("reviews run: %s, want 3 (one plus two retries)", b)
	}
}

// Under a symlinked .monomind the review hands over no hash on 2.21; on
// 2.22, monomind's own reviewed hash (enforced by --expect-hash) does.
func TestReviewUnderSymlinkedMonomind(t *testing.T) {
	f, _, _ := newSigningFixture(t, "2.21.0")
	mm := filepath.Join(f.root, ".monomind")
	target := filepath.Join(f.root, "cfg-mm")
	if err := os.Rename(mm, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, mm); err != nil {
		t.Fatal(err)
	}
	rev := f.mustRun(t, "sign", "growth")
	if h, _ := rev["hash"].(string); h != "" || !strings.Contains(rev["message"].(string), "symlink") {
		t.Fatalf("2.21 review under a symlink = %v", rev)
	}

	fakeSigningMonomind(t, "2.22.0")
	t.Setenv("FAKE_SIGN_HELP", "  --expect-hash <hex>")
	goHash, _ := orgsign.Hash(f.root, mustRead(t, f.root, "growth"))
	t.Setenv("FAKE_REVIEW_JSON", `{"org":"growth","state":"unsigned","hash":"`+goHash+`","review":{},"reviewText":"org growth (unsigned):"}`)
	if rev := f.mustRun(t, "sign", "growth"); rev["hash"] != goHash {
		t.Fatalf("2.22 review under a symlink = %v", rev)
	}
}

// MUST (#295, sixth review): the busy retry must not reuse a verdict from
// before the retry. .monomind turned into a symlink during the backoff, or
// during the first read, leaves the review without a hash.
func TestBusyRetryRechecksForSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX symlinks")
	}
	toLink := func(t *testing.T, root string) {
		t.Helper()
		mm := filepath.Join(root, ".monomind")
		if err := os.Rename(mm, mm+"-real"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(mm+"-real", mm); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("during the backoff", func(t *testing.T) {
		f, s, _ := newSigningFixture(t, "2.21.0")
		prev, prevSleep := reviewRetryBackoff, reviewSleep
		reviewRetryBackoff = time.Millisecond
		reviewSleep = func(time.Duration) { toLink(t, f.root) }
		t.Cleanup(func() { reviewRetryBackoff, reviewSleep = prev, prevSleep })
		_ = os.WriteFile(filepath.Join(f.root, ".monomind", "busy-count"), []byte("0"), 0o644)
		t.Setenv("FAKE_REVIEW_BUSY", "1") // attempt 1 busy, then a clean read
		rev := f.mustRun(t, "sign", "growth")
		if h, _ := rev["hash"].(string); h != "" || !strings.Contains(rev["message"].(string), "symlink") {
			t.Fatalf("review after .monomind became a symlink = %v", rev)
		}
		if len(s.calls) != 0 {
			t.Fatalf("calls %v", s.calls)
		}
	})
	t.Run("during the first read", func(t *testing.T) {
		f, _, _ := newSigningFixture(t, "2.21.0")
		t.Setenv("FAKE_REVIEW_TOLINK", "1")
		rev := f.mustRun(t, "sign", "growth")
		if h, _ := rev["hash"].(string); h != "" {
			t.Fatalf("review with .monomind linked mid-read = %v", rev)
		}
	})
}

// A 2.22 build without the org-sign capabilities is an older binary:
// its review JSON and --expect-hash are not used (fail closed), so a
// definition only it could hash is not signed.
func TestMissingSignCapabilitiesFailClosed(t *testing.T) {
	f, s, _ := newSigningFixture(t, "2.22.0")
	t.Setenv("FAKE_NO_SIGN_CAPS", "1")
	t.Setenv("FAKE_SIGN_HELP", "  --expect-hash <hex>")
	f.editOutside(t, func(m map[string]interface{}) {
		m["roles"].([]interface{})[0].(map[string]interface{})["blueprint"] = "researcher"
	})
	const mmHash = "abababababababababababababababababababababababababababababababab"
	t.Setenv("FAKE_REVIEW_JSON", `{"org":"growth","state":"unsigned","hash":"`+mmHash+`","review":{},"reviewText":"from JSON"}`)
	if rev := f.mustRun(t, "sign", "growth"); rev["hash"] != nil && rev["hash"] != "" {
		t.Fatalf("review without the capability = %v", rev)
	}
	if _, err := f.run(t, "sign", "growth", "--yes", "--expect-hash", mmHash); err == nil || len(s.calls) != 0 {
		t.Fatalf("signed without the capability: %v (calls %v)", err, s.calls)
	}
}
