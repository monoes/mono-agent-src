package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/nodemgr"
)

// mcpServerName is the name doctor/setup register monoagent's MCP server
// under in every coding-agent runtime.
const mcpServerName = "monoagent"

// mcpServeArgs are the arguments the registered server runs with. Doctor and
// setup register the full tool set (the user's decision): a registration
// without --allow-mutations is reported as outdated.
var mcpServeArgs = []string{"mcp", "--allow-mutations"}

// Seams for tests: which runtimes are on PATH, how their CLIs run, and the
// binary that gets registered.
var (
	agentLookPath = exec.LookPath
	runRuntimeCLI = func(ctx context.Context, progress func(string), name string, args ...string) error {
		return nodemgr.StreamCmd(exec.CommandContext(ctx, name, args...), progress)
	}
	mcpExecutable = os.Executable
)

// stdioServer is a {command, args} server entry (Claude Code, Gemini CLI).
type stdioServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

func (s stdioServer) cmd() (string, []string) { return s.Command, s.Args }

// mcpEntry is one MCP server found in a runtime's config.
type mcpEntry struct {
	name    string
	command string
	args    []string
}

// isMonoagent is true for a server that runs `monoagentcli mcp ...` for the
// whole tool set; --tasks-only and --api-only servers are special-purpose
// registrations of the user's and don't count.
func (e mcpEntry) isMonoagent() bool {
	base := strings.TrimSuffix(filepath.Base(e.command), ".exe")
	if base != "monoagentcli" || len(e.args) == 0 || e.args[0] != "mcp" {
		return false
	}
	for _, a := range e.args[1:] {
		if a == "--tasks-only" || a == "--api-only" {
			return false
		}
	}
	return true
}

func (e mcpEntry) current() bool {
	for _, a := range e.args[1:] {
		if a == "--allow-mutations" {
			return true
		}
	}
	return false
}

// mcpRuntimeState is what doctor knows about one runtime's registration.
type mcpRuntimeState struct {
	ID, Label  string
	Installed  bool
	Registered bool   // a monoagentcli mcp server is configured
	Current    bool   // ... and it runs with --allow-mutations
	Entry      string // its name in the config
	Where      string // the config file
	Err        error  // the config could not be read
}

// summary is the one-line state doctor shows for the runtime.
func (s mcpRuntimeState) summary() string {
	switch {
	case s.Err != nil:
		return s.Label + ": " + s.Err.Error()
	case s.Current:
		return fmt.Sprintf("%s: %q in %s", s.Label, s.Entry, s.Where)
	case s.Registered:
		return fmt.Sprintf("%s: %q in %s is outdated (no --allow-mutations)", s.Label, s.Entry, s.Where)
	}
	return s.Label + ": not registered"
}

// pickEntry fills st from the servers found in a config.
func (st *mcpRuntimeState) pickEntry(entries []mcpEntry) {
	for _, e := range entries {
		if !e.isMonoagent() {
			continue
		}
		if !st.Registered || (e.current() && !st.Current) {
			st.Registered, st.Current, st.Entry = true, e.current(), e.name
		}
	}
}

// mcpRuntime is one coding-agent runtime monoagent registers its MCP server
// with.
type mcpRuntime struct {
	id, label string
	state     func(home string) mcpRuntimeState
	register  func(ctx context.Context, home, exe string, st mcpRuntimeState, progress func(string)) error
}

func mcpRuntimes() []mcpRuntime {
	return []mcpRuntime{
		{id: "claude", label: "Claude Code", state: claudeMCPState, register: registerClaudeMCPServer},
		{id: "codex", label: "Codex", state: codexMCPState, register: registerCodexMCP},
		{id: "gemini", label: "Gemini CLI", state: geminiMCPState, register: registerGeminiMCP},
		{id: "opencode", label: "opencode", state: opencodeMCPState, register: registerOpencodeMCP},
	}
}

// mcpRuntimeStates reports every runtime, installed or not.
func mcpRuntimeStates() []mcpRuntimeState {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var out []mcpRuntimeState
	for _, rt := range mcpRuntimes() {
		st := rt.state(home)
		st.ID, st.Label = rt.id, rt.label
		out = append(out, st)
	}
	return out
}

// agentsMCPRegistration is the doctor view over every installed runtime:
// registered only when each of them has a current registration.
func agentsMCPRegistration() (found, registered bool, where string) {
	var parts []string
	registered = true
	for _, st := range mcpRuntimeStates() {
		if !st.Installed {
			continue
		}
		found = true
		if !st.Current {
			registered = false
		}
		parts = append(parts, st.summary())
	}
	return found, found && registered, strings.Join(parts, "; ")
}

// registerAgentsMCP registers (or re-registers) the MCP server with every
// installed runtime whose registration is missing or outdated. A runtime
// that is not installed is never touched.
func registerAgentsMCP(ctx context.Context, progress func(string)) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	exe, err := mcpExecutable()
	if err != nil {
		return err
	}
	var errs []error
	did := false
	for _, rt := range mcpRuntimes() {
		st := rt.state(home)
		st.ID, st.Label = rt.id, rt.label
		if !st.Installed || st.Current {
			continue
		}
		did = true
		progress("registering monoagent's MCP server with " + rt.label)
		if err := rt.register(ctx, home, exe, st, progress); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", rt.label, err))
		}
	}
	if !did {
		progress("already registered with every installed runtime")
	}
	return errors.Join(errs...)
}

// --- Claude Code ------------------------------------------------------

func claudeMCPState(home string) mcpRuntimeState {
	path := filepath.Join(home, ".claude.json")
	st := mcpRuntimeState{Where: path}
	b, err := os.ReadFile(path)
	if err != nil {
		_, derr := os.Stat(filepath.Join(home, ".claude"))
		st.Installed = derr == nil
		return st
	}
	st.Installed = true
	var cfg struct {
		MCPServers map[string]stdioServer `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return st
	}
	st.pickEntry(sortedEntries(cfg.MCPServers, stdioServer.cmd))
	return st
}

// registerClaudeMCPServer runs `claude mcp add --scope user monoagent --
// <exe> mcp --allow-mutations`. `claude mcp add` refuses a name that
// exists, so an outdated registration is removed first.
func registerClaudeMCPServer(ctx context.Context, _, exe string, st mcpRuntimeState, progress func(string)) error {
	claude, err := agentLookPath("claude")
	if err != nil {
		return fmt.Errorf("the claude CLI is not on PATH — install Claude Code first (monoagentcli agent install claude)")
	}
	if st.Registered {
		if err := runRuntimeCLI(ctx, progress, claude, "mcp", "remove", "--scope", "user", st.Entry); err != nil {
			return err
		}
	}
	args := append([]string{"mcp", "add", "--scope", "user", mcpServerName, "--", exe}, mcpServeArgs...)
	return runRuntimeCLI(ctx, progress, claude, args...)
}

// --- Codex ------------------------------------------------------------

func codexHome(home string) string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return h
	}
	return filepath.Join(home, ".codex")
}

func codexMCPState(home string) mcpRuntimeState {
	path := filepath.Join(codexHome(home), "config.toml")
	st := mcpRuntimeState{Where: path}
	if _, err := agentLookPath("codex"); err != nil {
		return st
	}
	st.Installed = true
	b, err := os.ReadFile(path)
	if err != nil {
		return st
	}
	st.pickEntry(codexMCPEntries(b))
	return st
}

// codexMCPEntries reads the [mcp_servers.<name>] tables of a Codex
// config.toml: just their command and args, which is all doctor needs.
func codexMCPEntries(b []byte) []mcpEntry {
	var out []mcpEntry
	var cur *mcpEntry
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	var pending strings.Builder // a multi-line args array
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if pending.Len() > 0 {
			pending.WriteString(" " + line)
			if strings.Contains(line, "]") {
				cur.args = tomlStrings(pending.String())
				pending.Reset()
			}
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if cur != nil {
				out = append(out, *cur)
				cur = nil
			}
			hdr := strings.TrimSpace(strings.Trim(line, "[]"))
			if name, ok := strings.CutPrefix(hdr, "mcp_servers."); ok {
				name = strings.Trim(name, `"'`)
				if !strings.Contains(name, ".") {
					cur = &mcpEntry{name: name}
				}
			}
			continue
		}
		if cur == nil {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		switch key {
		case "command":
			cur.command = tomlString(val)
		case "args":
			if strings.HasPrefix(val, "[") && !strings.Contains(val, "]") {
				pending.WriteString(val)
				continue
			}
			cur.args = tomlStrings(val)
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

// tomlString decodes a TOML basic ("...") or literal ('...') string,
// ignoring anything after it.
func tomlString(v string) string {
	if strings.HasPrefix(v, "'") {
		if end := strings.Index(v[1:], "'"); end >= 0 {
			return v[1 : end+1]
		}
		return ""
	}
	var s string
	_ = json.NewDecoder(strings.NewReader(v)).Decode(&s)
	return s
}

// tomlStrings decodes a one-line TOML array of strings.
func tomlStrings(v string) []string {
	if i := strings.LastIndex(v, "]"); i >= 0 {
		v = v[:i+1]
	}
	var out []string
	if json.Unmarshal([]byte(v), &out) == nil {
		return out
	}
	// Literal strings or a trailing comma: split by hand.
	inner := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(v), "["), "]")
	for _, part := range strings.Split(inner, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, tomlString(part))
		}
	}
	return out
}

// registerCodexMCP runs `codex mcp add monoagent -- <exe> mcp
// --allow-mutations`, which replaces a server of the same name. An outdated
// registration under another name is removed first. config.toml is copied
// to config.toml.bak before Codex rewrites it.
func registerCodexMCP(ctx context.Context, home, exe string, st mcpRuntimeState, progress func(string)) error {
	codex, err := agentLookPath("codex")
	if err != nil {
		return fmt.Errorf("the codex CLI is not on PATH")
	}
	path := filepath.Join(codexHome(home), "config.toml")
	if b, err := os.ReadFile(path); err == nil {
		if err := os.WriteFile(path+".bak", b, 0o600); err != nil {
			return fmt.Errorf("back up %s: %w", path, err)
		}
	}
	if st.Registered && st.Entry != mcpServerName {
		if err := runRuntimeCLI(ctx, progress, codex, "mcp", "remove", st.Entry); err != nil {
			return err
		}
	}
	args := append([]string{"mcp", "add", mcpServerName, "--", exe}, mcpServeArgs...)
	return runRuntimeCLI(ctx, progress, codex, args...)
}

// --- Gemini CLI -------------------------------------------------------

func geminiSettingsPath(home string) string {
	return filepath.Join(home, ".gemini", "settings.json")
}

func geminiMCPState(home string) mcpRuntimeState {
	path := geminiSettingsPath(home)
	st := mcpRuntimeState{Where: path}
	if _, err := agentLookPath("gemini"); err != nil {
		return st
	}
	st.Installed = true
	root, err := readJSONObject(path)
	if err != nil {
		st.Err = err
		return st
	}
	var servers map[string]stdioServer
	if raw, ok := root["mcpServers"]; ok {
		_ = json.Unmarshal(raw, &servers)
	}
	st.pickEntry(sortedEntries(servers, stdioServer.cmd))
	return st
}

// registerGeminiMCP sets mcpServers.monoagent in ~/.gemini/settings.json
// (the user scope `gemini mcp add -s user` writes), keeping every other
// setting and any other field of an existing entry.
func registerGeminiMCP(_ context.Context, home, exe string, st mcpRuntimeState, progress func(string)) error {
	path := geminiSettingsPath(home)
	progress("editing " + path)
	return editJSONObject(path, func(root map[string]json.RawMessage) error {
		return setServer(root, "mcpServers", st, func(e map[string]any) {
			e["command"] = exe
			e["args"] = mcpServeArgs
		})
	})
}

// --- opencode ---------------------------------------------------------

func opencodeConfigPath(home string) string {
	dir := filepath.Join(home, ".config", "opencode")
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		dir = filepath.Join(x, "opencode")
	}
	for _, name := range []string{"opencode.json", "opencode.jsonc"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return filepath.Join(dir, name)
		}
	}
	return filepath.Join(dir, "opencode.json")
}

func opencodeMCPState(home string) mcpRuntimeState {
	path := opencodeConfigPath(home)
	st := mcpRuntimeState{Where: path}
	if _, err := agentLookPath("opencode"); err != nil {
		return st
	}
	st.Installed = true
	root, err := readJSONObject(path)
	if err != nil {
		st.Err = err
		return st
	}
	var servers map[string]struct {
		Type    string   `json:"type"`
		Command []string `json:"command"`
	}
	if raw, ok := root["mcp"]; ok {
		_ = json.Unmarshal(raw, &servers)
	}
	st.pickEntry(sortedEntries(servers, func(s struct {
		Type    string   `json:"type"`
		Command []string `json:"command"`
	}) (string, []string) {
		if s.Type != "local" || len(s.Command) == 0 {
			return "", nil
		}
		return s.Command[0], s.Command[1:]
	}))
	return st
}

// registerOpencodeMCP sets mcp.monoagent (a local server) in opencode's
// global config, keeping everything else.
func registerOpencodeMCP(_ context.Context, home, exe string, st mcpRuntimeState, progress func(string)) error {
	path := opencodeConfigPath(home)
	progress("editing " + path)
	return editJSONObject(path, func(root map[string]json.RawMessage) error {
		if _, ok := root["$schema"]; !ok {
			root["$schema"] = json.RawMessage(`"https://opencode.ai/config.json"`)
		}
		return setServer(root, "mcp", st, func(e map[string]any) {
			e["type"] = "local"
			e["command"] = append([]string{exe}, mcpServeArgs...)
			e["enabled"] = true
		})
	})
}

// --- shared helpers ---------------------------------------------------

// sortedEntries turns a decoded server map into entries in name order, so
// which of several registrations is reported does not depend on map order.
func sortedEntries[T any](m map[string]T, get func(T) (string, []string)) []mcpEntry {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]mcpEntry, 0, len(names))
	for _, n := range names {
		cmd, args := get(m[n])
		out = append(out, mcpEntry{name: n, command: cmd, args: args})
	}
	return out
}

// setServer writes the monoagent entry under root[key], merged into the
// existing monoagent entry (or the outdated one, which it replaces).
func setServer(root map[string]json.RawMessage, key string, st mcpRuntimeState, fill func(map[string]any)) error {
	servers := map[string]json.RawMessage{}
	if raw, ok := root[key]; ok && len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return fmt.Errorf("%q is not an object", key)
		}
	}
	entry := map[string]any{}
	src := mcpServerName
	if st.Registered && st.Entry != "" {
		src = st.Entry
	}
	if raw, ok := servers[src]; ok {
		_ = json.Unmarshal(raw, &entry)
	} else if raw, ok := servers[mcpServerName]; ok {
		_ = json.Unmarshal(raw, &entry)
	}
	if src != mcpServerName {
		delete(servers, src)
	}
	fill(entry)
	b, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	servers[mcpServerName] = b
	if root[key], err = json.Marshal(servers); err != nil {
		return err
	}
	return nil
}

// readJSONObject reads a JSON object config; a missing file is empty.
func readJSONObject(path string) (map[string]json.RawMessage, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	root := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(b)) == 0 {
		return root, nil
	}
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, fmt.Errorf("cannot parse %s as JSON (comments?) — not editing it: %v", path, err)
	}
	return root, nil
}

// editJSONObject applies edit to a JSON object config: the original is
// kept as <path>.bak, and the new content is written to a temp file and
// renamed over it, with the original's permissions.
func editJSONObject(path string, edit func(map[string]json.RawMessage) error) error {
	root, err := readJSONObject(path)
	if err != nil {
		return err
	}
	if err := edit(root); err != nil {
		return err
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
		orig, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path+".bak", orig, mode); err != nil {
			return fmt.Errorf("back up %s: %w", path, err)
		}
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(out)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp.Name(), mode)
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
	}
	return werr
}
