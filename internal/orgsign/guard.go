package orgsign

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// Signer signs an org definition as the operator: `monomind org sign <org>
// --yes` run in the project root (internal/monomind.OrgSign). hash is the
// projection hash the signature must cover; a monomind that can refuse
// anything else (`--expect-hash`) is given it, others sign what is on disk
// and the caller checks afterwards.
type Signer interface {
	Sign(ctx context.Context, root, org, hash string) error
}

// HashEnforcer is a Signer whose monomind refuses to sign unless the hash
// of the content it reads equals the one it was given (2.22's
// --expect-hash, reading the files once). Only then may a hash this
// package can't compute itself — monomind's own reviewed hash — be signed.
// root matters: a version shim (mise, asdf) can pick another monomind per
// project, so the answer is the monomind that runs in root.
type HashEnforcer interface {
	EnforcesHash(ctx context.Context, root string) bool
}

// Checker is a Signer that can also ask monomind itself whether the
// definition on disk verifies (2.22+ `org sign <org> --check --format
// json`). ok is false when monomind can't answer; this package's own Go
// check is used then.
type Checker interface {
	Check(ctx context.Context, root, org string) (st Status, ok bool)
}

// verifyNow is org's state for raw, the bytes (sha256 sha) just read from
// its file: monomind's own check when s offers one and the file still held
// those bytes after it ran, else this package's Verify.
func verifyNow(ctx context.Context, s Signer, root, org string, raw []byte, sha string) Status {
	if c, ok := s.(Checker); ok {
		if st, ok := c.Check(ctx, root, org); ok {
			if _, again, err := ReadFile(root, org); err != nil || again != sha {
				return Status{State: StateUnknown, Detail: "the file changed while monomind checked it"}
			}
			return st
		}
	}
	return Verify(root, org, raw)
}

// roleContextMarkers are the env vars monomind sets on an org role's or an
// `agent exec` turn's process tree (org-signature.ts ROLE_CONTEXT_MARKERS).
// monomind refuses to sign under any of them; mono-agent does not even ask.
var roleContextMarkers = []string{
	"MONOMIND_ORG_ROLE", "MONOMIND_SDK_AGENT", "MONOMIND_AGENT_EXEC", "MONOMIND_CLINE_TURN", "MONOMIND_AIDER",
}

// agentContextMarkers are monomind's AGENT_CONTEXT_ENV_MARKERS
// (orgrt/agent-context.ts): any coding agent's or org role's process tree.
// mono-agent never signs automatically under one of them — a change an
// agent made through a mono-agent command is the operator's to review.
var agentContextMarkers = []string{
	"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "MONOMIND_ORG_ROLE", "MONOMIND_SDK_AGENT", "MONOMIND_AGENT_EXEC",
	"AI_AGENT", "AGENT", "CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED", "CODEX_THREAD_ID", "CODEX_CI",
	"OPENCODE", "OPENCODE_PID", "ANTIGRAVITY_AGENT", "GEMINI_CLI", "GROK_SESSION_ID", "GROK_MANAGED_BY_NPM",
	"COPILOT_CLI_BINARY_VERSION", "COPILOT_AGENT_SESSION_ID", "CRUSH", "PI_CODING_AGENT", "QWEN_CODE",
	"PI_SESSION_ID", "DSH_SHELL", "DSH_SESSION_ID", "MONOMIND_CLINE_TURN", "MONOMIND_AIDER",
}

func firstSet(keys []string) string {
	for _, k := range keys {
		if os.Getenv(k) != "" {
			return k
		}
	}
	return ""
}

// RoleContextMarker names the org-role/agent-exec marker set in this
// process's env, or "": monomind's own rule for any signature, so it also
// bounds an explicit `org sign`.
//
// This is a courtesy, not the barrier: a role can unset its env, and
// mono-agent has no second signal that is both reliable and free of false
// positives for the real operator. Observed in a role sandbox (monomind
// 2.24.1, bwrap): pid 1 is bwrap, NoNewPrivs is set, and the operator dir is
// an empty tmpfs; but a Flatpak or container operator has the same pid 1 and
// a fresh operator has an empty or missing dir. The barrier is the sandbox
// (it hides the operator dir, so a role has no key to read) and monomind's
// own refusal, which names that directory even with the markers gone.
func RoleContextMarker() string { return firstSet(roleContextMarkers) }

// AgentContextMarker names any agent-context marker set, or "": no
// automatic signing then.
func AgentContextMarker() string { return firstSet(agentContextMarkers) }

// AgentContextMarkers lists every marker AgentContextMarker looks at
// (tests clear them to act as the operator's own terminal).
func AgentContextMarkers() []string { return append([]string(nil), agentContextMarkers...) }

// turnVerdict says why this process is not the operator's own terminal ("" when it is), as the
// turn-aware gate (internal/cli/opgate) answers: its markers, and whether it runs inside a coder
// session's turn whose shell carries no marker. Only a process that registered the gate (the command
// line) has one; the app, the daemon and library use judge by AgentContextMarker alone.
var turnVerdict func() string

// SetTurnVerdict installs the verdict Before consults after the marker check; nil removes it. It is
// called from opgate only: orgsign cannot import it (operator imports orgsign).
func SetTurnVerdict(f func() string) { turnVerdict = f }

// Pre is an org file's state just before mono-agent writes it.
type Pre struct {
	root, org string
	eligible  bool
	why       string
	// pins are the instructions-file digests the file verified with: the
	// signature after the write covers those contents and nothing else.
	pins Pins
}

// Eligible reports whether the coming write may be re-signed.
func (p Pre) Eligible() bool { return p.eligible }

// PinnedHash is raw's projection hash with the instructions files p
// pinned: the hash a re-signature of mono-agent's write must cover.
func (p Pre) PinnedHash(raw []byte) (string, error) { return HashPinned(raw, p.pins) }

// Before records whether the write mono-agent is about to make to org
// (the file the document came from; a rename's old name) may be re-signed
// afterwards. It may when the file on disk verifies now AND the document
// was loaded from exactly those bytes (loadedSHA, orgdesign.Doc.LoadedSHA)
// — then the only change a signature approves is mono-agent's own. The
// instructions files it verified with are pinned (Pins). A missing file is
// eligible only when signNew says the new definition is the user's own
// content (the designer's New org), not an import or an agent's.
//
// s is the signer the write will use (its Checker, if any, decides); nil
// means this package's own check.
func Before(ctx context.Context, s Signer, root, org, loadedSHA string, signNew bool) Pre {
	p := Pre{root: root, org: org, pins: newPins()}
	if m := AgentContextMarker(); m != "" {
		p.why = fmt.Sprintf("%s is set: an agent or org role made this change, and only the operator signs", m)
		return p
	}
	// A turn whose shell lost its markers is an agent all the same: the edit lands, but its
	// approval is not the operator's to launder. Fail closed: a verdict that cannot place the
	// process counts as a turn.
	if turnVerdict != nil {
		if why := turnVerdict(); why != "" {
			p.why = "a coder session's turn or an agent made this change, and only the operator signs: " + why
			return p
		}
	}
	// Where the stamps can't vouch for the files (a symlink on the way, an
	// untrusted filesystem, Windows), only a monomind that enforces the
	// hash itself (--expect-hash) may re-sign.
	if reason := Untrusted(root, org); reason != "" && !enforcesHash(ctx, s, root) {
		p.why = reason
		return p
	}
	raw, sha, err := ReadFile(root, org)
	if errors.Is(err, os.ErrNotExist) {
		p.eligible = signNew
		if !signNew {
			p.why = "it did not come from you in mono-agent (an import, or a document supplied whole)"
		}
		return p
	}
	if err != nil {
		p.why = err.Error()
		return p
	}
	st := verifyNow(ctx, s, root, org, raw, sha)
	switch {
	case !st.OK():
		p.why = st.Describe()
		return p
	case loadedSHA == "" || loadedSHA != sha:
		p.why = "the change was not made to the signed file as it is on disk"
		return p
	}
	// Pin the instructions files, and only if what was pinned is what the
	// signature covers (a file edited since the check above is not).
	h, pins, err := HashPins(root, raw)
	if signed, ok := SignedHash(root, org); err != nil || !ok || signed != h {
		p.why = "its instructions files could not be pinned to the signed contents"
		return p
	}
	p.eligible, p.pins = true, pins
	return p
}

// Outcome is what happened to a write's signature.
type Outcome struct {
	Signed bool   `json:"signed"`
	State  string `json:"state"`
	// Notice is set when the org is left for the operator to review.
	Notice string `json:"notice,omitempty"`
}

// After re-signs org (the name just written; a rename's new name) when p
// allowed it and the file still holds exactly the bytes mono-agent wrote
// (sha, orgdesign.Save's return value), with the instructions files p
// pinned. Otherwise the org is left unsigned with a notice saying why and
// how to review and sign it.
func (p Pre) After(ctx context.Context, s Signer, org, sha string) Outcome {
	raw, got, err := ReadFile(p.root, org)
	if !p.eligible {
		st := Status{State: StateUnknown}
		if err == nil {
			st = verifyNow(ctx, s, p.root, org, raw, got)
		}
		out := Outcome{Signed: st.OK(), State: st.State}
		if !st.OK() {
			out.Notice = fmt.Sprintf("org %s is not signed for this change (%s) — review it, then sign it: monoagentcli org sign %s", org, p.why, org)
		}
		return out
	}
	if err != nil || got != sha {
		return notSigned(org, StateUnknown, "the file changed after mono-agent wrote it")
	}
	want, err := p.PinnedHash(raw)
	if err != nil {
		return notSigned(org, StateUnknown, err.Error())
	}
	return SignExact(ctx, s, p.root, org, want)
}

func enforcesHash(ctx context.Context, s Signer, root string) bool {
	e, ok := s.(HashEnforcer)
	return ok && e.EnforcesHash(ctx, root)
}

func notSigned(org, state, why string) Outcome {
	return Outcome{State: state, Notice: fmt.Sprintf("org %s is not signed: %s — review it, then sign it: monoagentcli org sign %s", org, why, org)}
}

// withdrawnNotice is the notice for a signature taken back: a reload in the
// moment it held may have loaded content nobody approved.
const withdrawnNotice = "the definition changed while it was being signed, so the signature was withdrawn. " +
	"If the org is running, stop and restart it: a reload in that moment may have loaded the unapproved change"

// SignExact signs org only if its definition, as this package hashes it
// now, has projection hash want (the reviewed or pinned one), and confirms
// afterwards that monomind signed exactly want: if the JSON or an
// instructions file changed in between, the signature is withdrawn. A
// definition that already verifies needs no new signature.
func SignExact(ctx context.Context, s Signer, root, org, want string) Outcome {
	if m := RoleContextMarker(); m != "" {
		return notSigned(org, StateUnknown, m+" is set, and only the operator signs")
	}
	raw, sha, err := ReadFile(root, org)
	if err != nil {
		return notSigned(org, StateUnknown, err.Error())
	}
	if want == "" {
		return notSigned(org, StateUnknown, "its hash can't be computed here — sign it with `monomind org sign "+org+"` in a terminal")
	}
	now, err := Hash(root, raw)
	switch {
	case err == nil && now != want:
		return notSigned(org, Verify(root, org, raw).State, "it changed after it was written or reviewed")
	case err != nil && !enforcesHash(ctx, s, root):
		// No Go hash to check want against, and a monomind that would sign
		// whatever it reads: never sign on its word (#295 review).
		return notSigned(org, StateUnknown, "its hash can't be computed here — sign it with `monomind org sign "+org+"` in a terminal")
	}
	if st := verifyNow(ctx, s, root, org, raw, sha); st.OK() {
		return Outcome{Signed: true, State: StateSigned}
	} else if st.State == StateForbiddenKey {
		return notSigned(org, st.State, Message(org, st))
	}
	if err := s.Sign(ctx, root, org, want); err != nil {
		return notSigned(org, verifyNow(ctx, s, root, org, raw, sha).State, "monomind did not sign it: "+err.Error())
	}
	if signed, ok := SignedHash(root, org); !ok || signed != want {
		_ = Withdraw(root, org)
		return notSigned(org, StateUnsigned, withdrawnNotice)
	}
	return Outcome{Signed: true, State: StateSigned}
}
