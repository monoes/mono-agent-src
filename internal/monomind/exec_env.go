package monomind

import (
	"os"
	"slices"
	"strings"
)

// sessionMarkerEnvVars are Claude Code's own session-identity env vars —
// see the comment at Exec's cmd.Env construction for why these must never
// reach a spawned `claude` CLI invocation.
var sessionMarkerEnvVars = []string{
	"CLAUDECODE",
	"CLAUDE_PID",
	"CLAUDE_EFFORT",
}

// sessionMarkerEnvPrefixes catches every CLAUDE_CODE_* variant without
// needing to enumerate them (new ones can be added by Claude Code itself
// without this list going stale), and every MONOMIND_* scoping override
// (notably MONOMIND_CWD): those are per-call configuration, set
// explicitly via opts.Env / individual command builders — an ambient value
// inherited from whatever launched this process must not reach the child,
// where a duplicate entry could shadow the intended per-profile scoping.
var sessionMarkerEnvPrefixes = []string{
	"CLAUDE_CODE_",
	"MONOMIND_",
	// A mission token and its labels belong to the one process the conductor minted them for,
	// which gets them through opts.Env; a process that merely inherits them (a conductor
	// started inside a worker's shell) must not hand them on to workers of its own.
	"MONOAGENT_TASK_",
}

// operatorEnvVars are MONOMIND_* settings the operator sets on purpose
// (monomind 2.24): they are not per-call scoping, so they pass through.
var operatorEnvVars = []string{
	"MONOMIND_CLAUDE_PATH",
	"MONOMIND_CODER_MAX_AGENTS",
	"MONOMIND_CODER_MAX_REVIEW_AGENTS",
}

// FilteredEnviron returns the current process environment with Claude
// Code's session-marker variables and ambient MONOMIND_* overrides
// removed. Exported so command builders outside this package (e.g. the
// chat KG tools) build children with the identical stripped base before
// appending their own explicit MONOMIND_* values.
func FilteredEnviron() []string {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if slices.Contains(sessionMarkerEnvVars, key) {
			continue
		}
		if slices.Contains(operatorEnvVars, key) {
			out = append(out, kv)
			continue
		}
		skip := false
		for _, prefix := range sessionMarkerEnvPrefixes {
			if strings.HasPrefix(key, prefix) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// envSlice flattens a map into KEY=VALUE strings.
func envSlice(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}
