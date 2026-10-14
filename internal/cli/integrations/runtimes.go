package integrations

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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
