package monomind

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Texts captured from monomind v2.24.1 (see the PR).
const (
	roleSignRefusal = "Refusing: MONOMIND_ORG_ROLE is set — this is an org role or agent-exec process. Only the operator signs org definitions; run this yourself in a terminal."
	r6Refusal       = `Could not start org sec: org "sec" cannot start on this host: the authority mask (bubblewrap) is unavailable on this host (bwrap not found (ENOENT)), so mail digests, the envelope key and native runner copies are protected by file-tool rules alone; the SDK sandbox is unavailable on this host (bwrap and socat not found on PATH (install bubblewrap and socat)), so shell commands run without the read and write denials`
	r1Refusal       = `Could not start org sec: org root /mnt/nas/proj is on a network filesystem (nfs4); a sections org needs a local filesystem so one daemon can own it`
	// Recorded from monomind 2.25.0 `org run sec --resume` with bwrap and socat off PATH
	// (the line `org run --resume` / checkpoint resume prints; `org resume` itself only
	// clears a pause file and never runs the host preflight).
	r6ResumeRefusal = `Could not start org sec: org "sec" cannot resume on this host: the authority mask (bubblewrap) is unavailable on this host (bwrap not found (ENOENT)), so mail digests, the envelope key and native runner copies are protected by file-tool rules alone; the SDK sandbox is unavailable on this host (bwrap and socat not found on PATH (install bubblewrap and socat)), so shell commands run without the read and write denials`
	heldRefusal     = `Could not start org sec: org "sec" is already owned by another daemon on /home/u/proj`
)

// fakeOrgMonomind is a monomind whose `org` subcommands run body (sh),
// with the handshake answered. Returns the project root.
func fakeOrgMonomind(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	bin := filepath.Join(t.TempDir(), "monomind") // outside the project
	script := "#!/bin/sh\n" +
		`if [ "$1" = "--version" ]; then echo '{"v":1,"version":"2.24.1","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1"]}'; exit 0; fi` + "\n" + body + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvOverride, bin)
	ResetCapabilityCache()
	t.Cleanup(ResetCapabilityCache)
	return t.TempDir()
}

func sh(s string) string { return "cat <<'EOT'\n" + s + "\nEOT\n" }

// A sign attempt from an org role fails with monomind's refusal verbatim:
// the review never passes it off as a review, and the sign carries it.
func TestRoleSignAttemptCarriesMonomindsRefusal(t *testing.T) {
	root := fakeOrgMonomind(t, sh(roleSignRefusal)+"echo '[ERROR] refused: role context (MONOMIND_ORG_ROLE)'\nexit 1")
	if rv, err := OrgSignReview(context.Background(), root, "acme"); err == nil || !strings.Contains(err.Error(), roleSignRefusal) {
		t.Fatalf("review = %+v, %v: want monomind's refusal as the error", rv, err)
	}
	if _, err := OrgSign(context.Background(), root, "acme", ""); err == nil || !strings.Contains(err.Error(), roleSignRefusal) {
		t.Fatalf("sign: %v", err)
	}
}

func TestStartRefusalExtracted(t *testing.T) {
	for name, tc := range map[string]struct{ text, kind string }{
		"R6":                 {"noise\n  Total estimate: ~$1.80\n" + r6Refusal + "\n[ERROR] org start failed", RefusalPreflight},
		"R6 resume (2.25.0)": {"Total estimate: ~$1.80\n" + r6ResumeRefusal + "\n[ERROR] org start failed", RefusalPreflight},
		"R1":                 {r1Refusal, RefusalDaemonLock},
		"held":               {heldRefusal, RefusalDaemonLock},
	} {
		got := asStartRefusal("sec", tc.text)
		if got == nil || got.Kind != tc.kind {
			t.Fatalf("%s: %+v", name, got)
		}
		if !strings.Contains(tc.text, got.Text) || strings.Contains(got.Text, "\n") || !strings.Contains(got.Error(), got.Text) || got.Hint == "" {
			t.Errorf("%s: text %q hint %q", name, got.Text, got.Hint)
		}
	}
	if asStartRefusal("sec", "org run failed: boom") != nil {
		t.Error("an unrelated failure is not a start refusal")
	}
}

// org run prints the refusal on stdout and only "[ERROR] org start failed"
// on stderr: the message must reach the caller, not that bare line.
func TestOrgRunSurfacesStartRefusal(t *testing.T) {
	root := fakeOrgMonomind(t, sh(r6Refusal)+"echo '[ERROR] org start failed' >&2\nexit 1")
	_, err := OrgRun(context.Background(), root, "sec", "", false)
	ref, ok := AsStartRefusal(err)
	if !ok || ref.Kind != RefusalPreflight || !strings.Contains(err.Error(), r6Refusal) {
		t.Fatalf("err = %v", err)
	}
}

func TestOrgResumeSurfacesStartRefusal(t *testing.T) {
	text := `org "sec" cannot resume on this host: the SDK sandbox is unavailable on this host (bwrap and socat not found on PATH)`
	root := fakeOrgMonomind(t, sh(text)+"exit 1")
	_, err := OrgResume(context.Background(), root, "sec")
	if ref, ok := AsStartRefusal(err); !ok || ref.Kind != RefusalPreflight || !strings.Contains(err.Error(), text) {
		t.Fatalf("err = %v", err)
	}
}

// A detached start that dies at once (a refusal) is reported; one that
// stays up is not held up.
func TestOrgRunStartReportsEarlyRefusal(t *testing.T) {
	old := startWatch
	startWatch = 30 * time.Second
	t.Cleanup(func() { startWatch = old })

	root := fakeOrgMonomind(t, sh(r1Refusal)+"echo '[ERROR] org start failed' >&2\nexit 1")
	err := OrgRunStart(context.Background(), root, "sec", "")
	if ref, ok := AsStartRefusal(err); !ok || ref.Kind != RefusalDaemonLock || !strings.Contains(err.Error(), r1Refusal) {
		t.Fatalf("err = %v", err)
	}

	startWatch = 300 * time.Millisecond
	root = fakeOrgMonomind(t, "sleep 5")
	if err := OrgRunStart(context.Background(), root, "sec", ""); err != nil {
		t.Fatalf("a running org: %v", err)
	}
}

func setWatch(t *testing.T, d time.Duration) {
	t.Helper()
	old := startWatch
	startWatch = d
	t.Cleanup(func() { startWatch = old })
}

// A start that dies at once for any other reason is an error carrying the
// output, never nil; a signature refusal keeps its own type.
func TestOrgRunStartEarlyNonRefusalExitIsAnError(t *testing.T) {
	setWatch(t, 30*time.Second)
	root := fakeOrgMonomind(t, sh("boom: the config is broken")+"exit 3")
	err := OrgRunStart(context.Background(), root, "sec", "")
	if err == nil || !strings.Contains(err.Error(), "boom: the config is broken") {
		t.Fatalf("err = %v", err)
	}
	if _, ok := AsStartRefusal(err); ok {
		t.Error("not a start refusal")
	}

	root = fakeOrgMonomind(t, sh("org sec: the definition has no operator signature — run `monomind org sign sec`")+"exit 1")
	err = OrgRunStart(context.Background(), root, "sec", "")
	if se, ok := AsOrgSignatureError(err); !ok || se.Org != "sec" || !strings.Contains(err.Error(), "no operator signature") {
		t.Fatalf("err = %v", err)
	}
}

func TestOrgRunStartCancelStopsTheStart(t *testing.T) {
	setWatch(t, 10*time.Second)
	// Cancel only once the child is up, so the cancel cannot land in the
	// handshake or capture setup on a loaded machine.
	marker := filepath.Join(t.TempDir(), "up")
	root := fakeOrgMonomind(t, "touch '"+marker+"'\nsleep 30")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			if _, err := os.Stat(marker); err == nil {
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
	begin := time.Now()
	err := OrgRunStart(ctx, root, "sec", "")
	if err != context.Canceled || time.Since(begin) > 5*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(begin))
	}
}

// No capture file is left behind: not after a refusal, not while a
// started org keeps writing, and what it writes does not pile up.
func TestOrgRunStartLeavesNoTempFiles(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	left := func() []string {
		var l []string
		_ = filepath.WalkDir(tmp, func(p string, d os.DirEntry, err error) error {
			if err == nil && p != tmp {
				l = append(l, p)
			}
			return nil
		})
		return l
	}
	setWatch(t, 30*time.Second)
	root := fakeOrgMonomind(t, sh(r6Refusal)+"exit 1")
	if err := OrgRunStart(context.Background(), root, "sec", ""); err == nil {
		t.Fatal("want refusal")
	}
	if l := left(); len(l) != 0 {
		t.Fatalf("left after refusal: %v", l)
	}

	setWatch(t, 200*time.Millisecond)
	root = fakeOrgMonomind(t, "i=0; while [ $i -lt 20 ]; do echo chatter; i=$((i+1)); sleep 0.1; done")
	if err := OrgRunStart(context.Background(), root, "sec", ""); err != nil {
		t.Fatal(err)
	}
	if l := left(); len(l) != 0 {
		t.Fatalf("left while running: %v", l)
	}
}

// A start returns as soon as monomind records the run as running (its
// runtime.json: status running, this child's own pid), not after the whole
// watch window.
func TestOrgRunStartReturnsOnRunningEvidence(t *testing.T) {
	setWatch(t, 20*time.Second)
	root := t.TempDir()
	dir := filepath.Join(root, ".monomind", "orgs", "sec")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rt := filepath.Join(dir, "runtime.json")
	body := "sleep 0.2\n" +
		`printf '{"status":"running","run":"r1","pid":%s,"updated":"%s"}' $$ "$(date -u +%Y-%m-%dT%H:%M:%S.000Z)" > '` + rt + "'\nsleep 30"
	fakeOrgMonomind(t, body)
	begin := time.Now()
	if err := OrgRunStart(context.Background(), root, "sec", ""); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(begin); d > 10*time.Second {
		t.Fatalf("waited %v for a run that was recorded running within 0.2s", d)
	}
}

// A leftover runtime.json from an earlier run (running, a dead pid, an old
// timestamp) is not evidence that this start worked.
func TestOrgRunStartIgnoresStaleRunningRecord(t *testing.T) {
	setWatch(t, 800*time.Millisecond)
	root := t.TempDir()
	dir := filepath.Join(root, ".monomind", "orgs", "sec")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := `{"status":"running","run":"old","pid":` + strconv.Itoa(os.Getpid()) + `,"updated":"2020-01-01T00:00:00.000Z"}`
	if err := os.WriteFile(filepath.Join(dir, "runtime.json"), []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	fakeOrgMonomind(t, "sleep 30")
	begin := time.Now()
	if err := OrgRunStart(context.Background(), root, "sec", ""); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(begin); d < 700*time.Millisecond {
		t.Fatalf("a stale record counted as started after %v", d)
	}
}

// Captured from monomind v2.24.1 inside a real role sandbox (bwrap, fake
// codex runtime) after the role unset MONOMIND_ORG_ROLE and its siblings:
// no marker, yet monomind refuses, naming the protected directory.
const protectedDirRefusal = "org sign: the operator-credential directory (/home/monoes/scratch/ma25/home10/.monomind/orgrt-operator) is protected from org roles (an empty tmpfs is mounted over it in this sandbox): only the operator signs org definitions and holds the signing key. Nothing was written. Ask the operator to run `monomind org sign <org>` in their own terminal, or use a pre-signed org (`monomind org sign <org> --check` reports the state)."

func TestProtectedDirRefusalPassesThroughVerbatim(t *testing.T) {
	root := fakeOrgMonomind(t, sh(protectedDirRefusal)+"echo '[ERROR] "+protectedDirRefusal+"'\nexit 1")
	if rv, err := OrgSignReview(context.Background(), root, "acme"); err == nil || !strings.Contains(err.Error(), protectedDirRefusal) {
		t.Fatalf("review = %+v, %v: want the protected-directory refusal as the error", rv, err)
	}
	if _, err := OrgSign(context.Background(), root, "acme", ""); err == nil || !strings.Contains(err.Error(), protectedDirRefusal) {
		t.Fatalf("sign: %v", err)
	}
}
