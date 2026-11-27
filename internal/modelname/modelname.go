// Package modelname reads model names: which model a newer version replaced, and
// whether two names are one model. It starts no process and opens no
// connection (it reads monomind's model list and Claude Code's settings files),
// so packages that may hold neither can use it.
package modelname

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
)

// No agent turn runs on a superseded model: an older version of a family is
// weaker and usually costs more than the newest one. The newest version of a
// family is the higher of latestFloor and what monomind last listed (its
// ~/.monomind/latest-models.json), so a release monomind has seen is used
// with no change here. The patterns mirror monomind's latest-models.ts.

// latestFloor holds the newest claude ids known at this release; raise them
// when a family ships. Claude needs it because an older Claude Code lists old
// models only. The other runtimes are judged against their own list alone:
// which models an account there has is not known here.
var latestFloor = []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5", "claude-fable-5-1"}

var (
	claudeModelID = regexp.MustCompile(`^claude-(opus|sonnet|haiku|fable)-(\d+)(?:-(\d{1,2}))?(?:-\d{8})?(?:\[1m\])?$`)
	// Claude 3 ids put the version first (claude-3-5-sonnet-20241022).
	claude3ModelID     = regexp.MustCompile(`^claude-(3)(?:-(\d))?-(opus|sonnet|haiku)(?:-\d{8}|-latest)?$`)
	codexModelID       = regexp.MustCompile(`^gpt-(\d+)(?:\.(\d+))?-(terra|sol|luna|astra)$`)
	antigravityModelID = regexp.MustCompile(`^gemini-(\d+)(?:\.(\d+))?-(flash|pro|argon)-(low|medium|high)$`)
)

type modelVersion struct {
	runtime, family string
	major, minor    int
}

func (v modelVersion) olderThan(o modelVersion) bool {
	return v.major < o.major || v.major == o.major && v.minor < o.minor
}

// parseModelID reads a model id; ok is false for a name this does not judge
// (an alias such as "opus", another vendor's or a gateway's id).
func parseModelID(id string) (modelVersion, bool) {
	num := func(s string) int { n, _ := strconv.Atoi(s); return n }
	if m := claudeModelID.FindStringSubmatch(id); m != nil {
		return modelVersion{"claude", m[1], num(m[2]), num(m[3])}, true
	}
	if m := claude3ModelID.FindStringSubmatch(id); m != nil {
		return modelVersion{"claude", m[3], 3, num(m[2])}, true
	}
	if m := codexModelID.FindStringSubmatch(id); m != nil {
		return modelVersion{"codex", m[3], num(m[1]), num(m[2])}, true
	}
	if m := antigravityModelID.FindStringSubmatch(id); m != nil {
		return modelVersion{"antigravity", m[3] + "-" + m[4], num(m[1]), num(m[2])}, true
	}
	return modelVersion{}, false
}

// CatalogPath is monomind's cache of the runtimes' model lists.
var CatalogPath = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".monomind", "latest-models.json")
}

func latestCatalog(runtime string) []string {
	raw, err := os.ReadFile(CatalogPath())
	if err != nil {
		return nil
	}
	var doc map[string]json.RawMessage
	var ids []string
	if json.Unmarshal(raw, &doc) != nil || json.Unmarshal(doc[runtime], &ids) != nil {
		return nil
	}
	return ids
}

// Newest is the newest id of a runtime's model family, "" when none is known.
func Newest(runtime, family string) string {
	id, _ := newestOf(modelVersion{runtime: runtime, family: family})
	return id
}

// newestOf is the newest id of v's family.
func newestOf(v modelVersion) (id string, newest modelVersion) {
	for _, c := range append(latestCatalog(v.runtime), latestFloor...) {
		p, ok := parseModelID(c)
		if !ok || p.runtime != v.runtime || p.family != v.family {
			continue
		}
		if id == "" || newest.olderThan(p) {
			id, newest = strings.TrimSuffix(c, "[1m]"), p
		}
	}
	return id, newest
}

// Superseded reports the model that replaces id on runtime, when id is an
// older version of its family there.
func Superseded(runtime, id string) (newest string, ok bool) {
	v, judged := parseModelID(id)
	if !judged || v.runtime != runtime {
		return "", false
	}
	if n, nv := newestOf(v); n != "" && v.olderThan(nv) {
		return n, true
	}
	return "", false
}

// CurrentModel is id, or the model that replaced it.
func CurrentModel(runtime, id string) string {
	if n, ok := Superseded(runtime, id); ok {
		return n
	}
	return id
}

// SameModel reports whether two names are one model on runtime: a superseded
// id is read as the model that runs in its place and a claude family alias as
// that family's newest. Every rule that asks for two different models, or for
// one of a chosen set, compares with it.
func SameModel(runtime, a, b string) bool {
	runtime = strings.ToLower(strings.TrimSpace(runtime))
	return runningModel(runtime, a) == runningModel(runtime, b)
}

// runningModel is the model a turn asked to run name gets, in lower case. On
// another provider, and for names this does not judge, that is name itself.
func runningModel(runtime, name string) string {
	id := strings.ToLower(strings.TrimSpace(name))
	if runtime == "claude" {
		if OtherClaudeProvider() {
			return id
		}
		family := strings.TrimSuffix(id, "[1m]")
		if _, alias := ClaudeAliasEnv[family]; alias {
			newest, _ := newestOf(modelVersion{runtime: "claude", family: family})
			return newest
		}
	}
	return CurrentModel(runtime, id)
}

// CurrentPin is a "runtime/model@version" pin without its version, in lower
// case, naming the model that runs (as SameModel reads it).
func CurrentPin(pin string) string {
	key, _, _ := strings.Cut(strings.TrimSpace(pin), "@")
	key = strings.ToLower(strings.TrimSpace(key))
	if runtime, model, ok := strings.Cut(key, "/"); ok {
		if running := runningModel(runtime, model); running != strings.TrimSpace(model) {
			return runtime + "/" + running
		}
	}
	return key
}

// ModelFamily is the family of a model: "opus" for claude's alias and for
// every opus id, else a judged id's family, else the id itself.
func ModelFamily(runtime, id string) string {
	l := strings.ToLower(strings.TrimSpace(id))
	if v, ok := parseModelID(l); ok && v.runtime == runtime {
		return v.family
	}
	if runtime == "claude" {
		for family := range ClaudeAliasEnv {
			if strings.TrimSuffix(l, "[1m]") == family {
				return family
			}
		}
	}
	return l
}

// LeadRank orders models for planning or leading, strongest first.
func LeadRank(id string) int {
	l := strings.ToLower(id)
	for i, name := range []string{"opus", "fable", "sonnet"} {
		if strings.Contains(l, name) {
			return i
		}
	}
	if IsSmallModel(l) {
		return 4
	}
	return 3
}

// IsSmallModel reports whether id is a runtime's small, fast tier: it does
// helper work well, but does not plan or lead.
func IsSmallModel(id string) bool {
	l := strings.ToLower(id)
	return strings.Contains(l, "haiku") || strings.HasSuffix(l, "-luna")
}

// ClaudeAliasEnv names, per family, the variable Claude Code resolves that
// family's alias (and, for haiku, its background work) through.
var ClaudeAliasEnv = map[string]string{
	"opus": "ANTHROPIC_DEFAULT_OPUS_MODEL", "sonnet": "ANTHROPIC_DEFAULT_SONNET_MODEL",
	"haiku": "ANTHROPIC_DEFAULT_HAIKU_MODEL", "fable": "ANTHROPIC_DEFAULT_FABLE_MODEL",
}

// OtherProviderEnv marks a Claude Code that talks to something other than
// Anthropic's API, whose models are not Anthropic's to name.
var OtherProviderEnv = []string{"ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"}

// OtherProvider reports whether a claude turn in cwd is pointed at
// another provider, by its environment or by a settings file's env block.
func OtherProvider(env map[string]string, ambient func(string) string, cwd string) bool {
	named := func(get func(string) string) bool {
		return slices.ContainsFunc(OtherProviderEnv, func(k string) bool { return get(k) != "" })
	}
	if named(func(k string) string { return env[k] }) || named(ambient) {
		return true
	}
	dir := ambient("CLAUDE_CONFIG_DIR")
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".claude")
		}
	}
	files := []string{filepath.Join(dir, "settings.json")}
	files = append(files, ManagedSettings()...)
	if cwd != "" {
		files = append(files, filepath.Join(cwd, ".claude", "settings.json"), filepath.Join(cwd, ".claude", "settings.local.json"))
	}
	for _, f := range files {
		var doc struct {
			Env map[string]any `json:"env"`
		}
		raw, err := os.ReadFile(f)
		if err != nil || json.Unmarshal(raw, &doc) != nil {
			continue
		}
		if named(func(k string) string { s, _ := doc.Env[k].(string); return s }) {
			return true
		}
	}
	return false
}

// OtherClaudeProvider reports whether this machine's claude is set to another
// provider.
func OtherClaudeProvider() bool { return OtherProvider(nil, os.Getenv, "") }

// ManagedSettings are where an administrator's Claude Code settings live.
var ManagedSettings = func() []string {
	switch goruntime.GOOS {
	case "darwin":
		return []string{"/Library/Application Support/ClaudeCode/managed-settings.json"}
	case "windows":
		return []string{`C:\Program Files\ClaudeCode\managed-settings.json`, `C:\ProgramData\ClaudeCode\managed-settings.json`}
	}
	return []string{"/etc/claude-code/managed-settings.json"}
}
