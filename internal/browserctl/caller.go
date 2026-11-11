package browserctl

import (
	"os"
	"strings"
)

// Environment that tells kinds of agent apart.
const (
	EnvAgentKind = "MONOAGENT_AGENT_KIND" // "coder" (coder turns, dynorg workers), "apply" or "org"
	EnvAICaller  = "MONOAGENT_AI_CALLER"  // "chat": a run the in-app chat started
)

// Caller is who runs this process, for the browser permissions.
type Caller struct {
	// Kind is the kind of agent ("" for the user's own terminal): coder,
	// org, chat or other.
	Kind string `json:"kind"`
	// Declared is a kind (coder or org, or chat for a chat marker inside a turn) the shell declared that no token
	// vouches for (no token, a bad one, its process gone, the records
	// unreadable). Kind is then other, and the stricter of the two kinds'
	// levels applies (LevelOf): a claim never loosens, and neither does a
	// lost token.
	Declared string `json:"declared,omitempty"`
}

// AgentCaller is the caller this process shows: MarkerCaller's, and for a
// process with no marker that the turn check (SetTurnVerdict) places inside a
// coder session's turn, other: its markers were stripped, and it is not the
// user's own terminal. It never loosens MarkerCaller's answer, and a chat
// marker inside a turn (self-chosen, and looser than other by default) binds
// at the stricter of chat and other (Declared). Each call asks the turn
// check: a command calls it once and passes the Caller on.
func AgentCaller() Caller {
	c := MarkerCaller()
	if c.Kind != "" && c.Kind != KindChat {
		return c
	}
	if InTurn() {
		if c.Kind == KindChat {
			return Caller{Kind: KindOther, Declared: KindChat}
		}
		return Caller{Kind: KindOther}
	}
	return c
}

// MarkerCaller is the caller this process's environment shows. An agent
// shell (IsAgentContext) is the kind it declares — coder with
// MONOAGENT_AGENT_KIND=coder, org with MONOAGENT_AGENT_KIND=org or
// MONOMIND_ORG_ROLE — when a token its spawner issued vouches for it
// (IssueAgentKind), else other with that kind as Declared: a shell is
// never chat. chat is a process with MONOAGENT_AI_CALLER=chat and no shell
// markers. It reads the environment only: the operator gate's own marker
// check builds on it, so it must not ask the turn check (which asks the gate).
func MarkerCaller() Caller {
	if IsAgentContext() {
		kind := declaredKind()
		if kind != "" && kindVouched(kind) {
			return Caller{Kind: kind}
		}
		return Caller{Kind: KindOther, Declared: kind}
	}
	if strings.TrimSpace(os.Getenv(EnvAICaller)) == KindChat {
		return Caller{Kind: KindChat}
	}
	return Caller{}
}

// AgentKind is MarkerCaller's kind: "" when no marker names an agent. Marker
// only, for the operator gate; the browser permissions use AgentCaller.
func AgentKind() string { return MarkerCaller().Kind }

// strictness orders the levels, the strictest highest. dev is stricter than
// ask: dev refuses a new site, ask lets the agent raise an approval for it.
var strictness = map[string]int{LevelFullNoApprovals: 0, LevelFull: 1, LevelAsk: 2, LevelDev: 3, LevelOff: 4}

// LevelOf is the level that binds c: its kind's, and with a kind it
// declared but could not prove, the stricter of that kind's level and its
// own (off, dev, ask, full, full-no-approvals, strictest first).
func (p Permissions) LevelOf(c Caller) string {
	l := p.Level(c.Kind)
	if c.Declared != "" {
		if d := p.Level(c.Declared); strictness[d] > strictness[l] {
			l = d
		}
	}
	return l
}

// declaredKind is the kind an agent shell's environment claims.
func declaredKind() string {
	switch kind := strings.TrimSpace(os.Getenv(EnvAgentKind)); {
	case kind == KindCoder || kind == KindApply || kind == KindOrg:
		return kind
	case strings.TrimSpace(os.Getenv("MONOMIND_ORG_ROLE")) != "":
		return KindOrg
	}
	return ""
}
