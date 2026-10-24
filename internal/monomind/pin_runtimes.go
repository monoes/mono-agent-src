package monomind

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// runtimeBinEnv is every agent CLI monomind starts by name, with the
// variable that overrides the path it runs (monomind's runner-specs.ts
// binEnv, 2.21). monomind starts them in the project root, so a shim on
// PATH would let the project pick them (#301): each is pinned the way
// monomind itself is and passed by absolute path. Claude Code is not here:
// monomind finds it itself and never runs one behind a shim.
var runtimeBinEnv = []struct{ name, env string }{
	{"codex", "CODEX_CLI_BIN"},
	{"kimi", "KIMI_CLI_BIN"},
	{"opencode", "OPENCODE_BIN"},
	{"agy", "ANTIGRAVITY_CLI_BIN"},
	{"grok", "GROK_CLI_BIN"},
	{"qwen", "QWEN_CLI_BIN"},
	{"crush", "CRUSH_CLI_BIN"},
	{"copilot", "COPILOT_CLI_BIN"},
	{"pi", "PI_CLI_BIN"},
	{"hermes", "HERMES_CLI_BIN"},
	{"cline", "CLINE_CLI_BIN"},
	{"aider", "AIDER_CLI_BIN"},
	{"dsh", "DSH_CLI_BIN"},
	{"kilo", "KILO_CLI_BIN"},
}

var runtimePins struct {
	sync.Mutex
	byPath map[string]pinnedRuntime // found path → its pin
}

type pinnedRuntime struct {
	path string // "" when it can't be pinned
	at   time.Time
}

// pinRuntimes sets each runtimeBinEnv variable in env to its pinned
// absolute path: the operator's own value when set, else the first match
// on searchPath, resolved through any shim. A runtime that can't be found
// or pinned has its variable removed; with the shims gone from PATH it
// then fails to start instead of running whatever a project picks. When
// shims are still on PATH (shimsOnPath: globalToolDirs' fallback), its
// variable names a file that doesn't exist (unpinnedPath) instead: monomind
// runs a set <X>_CLI_BIN as given, and reports it missing, with no PATH
// lookup that could reach a shim.
func pinRuntimes(env []string, searchPath string, shimsOnPath bool) []string {
	set := map[string]string{}
	out := make([]string, 0, len(env)+len(runtimeBinEnv))
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if isRuntimeBinEnv(k) {
			set[k] = v
			continue
		}
		out = append(out, kv)
	}
	for _, rt := range runtimeBinEnv {
		p := pinRuntime(rt.name, strings.TrimSpace(set[rt.env]), searchPath)
		if p == "" && shimsOnPath {
			p = unpinnedPath(rt.name)
		}
		if p != "" {
			out = append(out, rt.env+"="+p)
		}
	}
	return out
}

// unpinnedPath is where an agent CLI that must not be found by name is
// pointed: a path in mono-agent's own data dir that is never created, so
// monomind reports it missing. Never "": monomind treats an empty override
// as unset and looks the CLI up on PATH.
func unpinnedPath(name string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = string(filepath.Separator)
	}
	return filepath.Join(home, ".monoagent", "unpinned", name)
}

// runtimeName is the CLI whose override key is.
func runtimeName(key string) string {
	for _, rt := range runtimeBinEnv {
		if key == rt.env {
			return rt.name
		}
	}
	return ""
}

func isRuntimeBinEnv(key string) bool {
	for _, rt := range runtimeBinEnv {
		if key == rt.env {
			return true
		}
	}
	return false
}

func pinRuntime(name, override, searchPath string) string {
	found := override
	if !filepath.IsAbs(found) {
		look := name
		if found != "" {
			look = found
		}
		if found = lookPathIn(look, searchPath); found == "" {
			return ""
		}
	}
	runtimePins.Lock()
	defer runtimePins.Unlock()
	if p, ok := runtimePins.byPath[found]; ok && time.Since(p.at) < capabilityTTL {
		return p.path
	}
	path, err := pinPath(found, name)
	if err != nil || !isExecutableFile(path) {
		path = ""
	}
	if runtimePins.byPath == nil {
		runtimePins.byPath = map[string]pinnedRuntime{}
	}
	runtimePins.byPath[found] = pinnedRuntime{path: path, at: time.Now()}
	return path
}

func resetRuntimePins() {
	runtimePins.Lock()
	runtimePins.byPath = nil
	runtimePins.Unlock()
}

// lookPathIn is exec.LookPath over searchPath instead of this process's
// PATH (relative entries skipped): the absolute path, or "".
func lookPathIn(name, searchPath string) string {
	if strings.ContainsRune(name, filepath.Separator) {
		return ""
	}
	exts := []string{""}
	if runtime.GOOS == "windows" {
		exts = []string{".exe", ".cmd", ".bat", ""}
	}
	for _, dir := range filepath.SplitList(searchPath) {
		if !filepath.IsAbs(dir) {
			continue
		}
		for _, ext := range exts {
			if p := filepath.Join(dir, name+ext); isExecutableFile(p) {
				return p
			}
		}
	}
	return ""
}
