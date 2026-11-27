package monomind

import (
	"maps"

	"github.com/monoes/mono-agent/internal/modelname"
)

// No agent turn runs on a superseded model. The rules for what a model name
// means are internal/modelname's (a package that can start nothing, so the
// pursuit loop may use it); here is how a turn is moved onto the newest ones.

// Superseded reports the model that replaces id on runtime (modelname).
func Superseded(runtime, id string) (string, bool) { return modelname.Superseded(runtime, id) }

// CurrentModel is id, or the model that replaced it.
func CurrentModel(runtime, id string) string { return modelname.CurrentModel(runtime, id) }

// SameModel reports whether two names are one model on runtime.
func SameModel(runtime, a, b string) bool { return modelname.SameModel(runtime, a, b) }

// ModelFamily is the family of a model.
func ModelFamily(runtime, id string) string { return modelname.ModelFamily(runtime, id) }

// LeadRank orders models for planning or leading, strongest first.
func LeadRank(id string) int { return modelname.LeadRank(id) }

// IsSmallModel reports whether id is a runtime's small, fast tier.
func IsSmallModel(id string) bool { return modelname.IsSmallModel(id) }

// OtherClaudeProvider reports whether this machine's claude is set to another
// provider.
func OtherClaudeProvider() bool { return modelname.OtherClaudeProvider() }

// useLatestModels moves a turn off superseded models: the one asked for, and
// for claude the ones Claude Code would pick for an alias or its own
// background work. ambient reads the inherited environment.
func useLatestModels(opts *ExecOptions, ambient func(string) string) {
	if opts.Runtime == "claude" {
		if modelname.OtherProvider(opts.Env, ambient, opts.Cwd) {
			return
		}
		opts.Env = withLatestClaudeEnv(opts.Env, ambient)
	}
	opts.Model = modelname.CurrentModel(opts.Runtime, opts.Model)
}

// withLatestClaudeEnv is env plus the alias pins that make a claude turn's
// aliases and background calls use the newest models, whatever Claude Code
// version runs it (2.1.289 does its background work on Haiku 4.5). ambient
// reads the inherited environment; a current pin set there or in env stays.
func withLatestClaudeEnv(env map[string]string, ambient func(string) string) map[string]string {
	get := func(k string) string {
		if v, ok := env[k]; ok {
			return v
		}
		return ambient(k)
	}
	out := maps.Clone(env)
	if out == nil {
		out = map[string]string{}
	}
	for family, key := range modelname.ClaudeAliasEnv {
		cur := get(key)
		if _, old := modelname.Superseded("claude", cur); cur != "" && !old {
			continue
		}
		if id := modelname.Newest("claude", family); id != "" {
			out[key] = id
		}
	}
	return out
}
