// Package meshclient speaks to monomind's Mesh hub: one JSON line per request
// and per reply over a Unix socket in a private directory (monomind
// packages/@monomind/cli/src/mesh). It is the Go side of the same protocol the
// monomind CLI and MCP tools use, written so the coder host can keep a coder
// session visible on the mesh and read what other agents sent it. The hub
// decides everything; this package holds no state.
package meshclient

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
)

// ErrOffline is returned when the hub cannot be reached: not running, not
// answering in time, a private-directory check failing, or a malformed reply.
// The mesh is optional; callers carry on without it.
var ErrOffline = errors.New("mesh hub unreachable")

// DefaultTimeout is how long one request may take (the monomind client's 500 ms).
const DefaultTimeout = 500 * time.Millisecond

// Dir is the mesh directory: $MONOMIND_MESH_DIR or ~/.monomind/mesh.
func Dir() string {
	if d := os.Getenv("MONOMIND_MESH_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".monomind", "mesh")
}

// Client sends requests to the hub in Dir.
type Client struct {
	Dir     string        // "" = the default
	Timeout time.Duration // 0 = DefaultTimeout
}

// Call sends one request and returns the hub's reply object (its "ok" and
// "error" fields are the caller's to read). Any transport failure is ErrOffline.
func (c Client) Call(req map[string]any) (map[string]any, error) {
	dir := c.Dir
	if dir == "" {
		dir = Dir()
	}
	if dir == "" {
		return nil, fmt.Errorf("%w: no home directory", ErrOffline)
	}
	// Never hand a key to a socket in a directory somebody else could have made or entered.
	if problem := privateDirProblem(dir); problem != "" {
		return nil, fmt.Errorf("%w: %s", ErrOffline, problem)
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	conn, err := net.DialTimeout("unix", filepath.Join(dir, "hub.sock"), timeout)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOffline, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	line, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOffline, err)
	}
	reader := bufio.NewReaderSize(conn, 1<<20)
	raw, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOffline, err)
	}
	var reply map[string]any
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, fmt.Errorf("%w: malformed reply", ErrOffline)
	}
	return reply, nil
}

// sanitize replaces every character the hub does not accept in a name with "-",
// one dash per UTF-16 code unit as monomind's regexp does (an emoji gives two),
// so the ids computed here are the ones monomind computes.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteString(strings.Repeat("-", utf16.RuneLen(r)))
		}
	}
	return b.String()
}

// baseName is node's path.basename: "" for a root.
func baseName(p string) string {
	b := filepath.Base(p)
	if b == "/" || b == "." || b == string(filepath.Separator) {
		return ""
	}
	return b
}

// ProjectID is the hub's project id for a folder: <basename>-<16 hex of
// sha256(realpath)>, as monomind computes it (project-id.ts).
func ProjectID(projectPath string) string {
	real := projectPath
	if r, err := filepath.EvalSymlinks(projectPath); err == nil {
		real = r
	}
	name := sanitize(baseName(real))
	if len(name) > 64 {
		name = name[:64]
	}
	if name == "" {
		name = "project"
	}
	sum := sha256.Sum256([]byte(real))
	return name + "-" + hex.EncodeToString(sum[:])[:16]
}

// CoderAlias is the name a coder session gets when none is chosen:
// coder-<folder>-<YYYYMMDD>-<HHMMSS> in local time, short enough for the hub's
// 64-character limit with room for a "-NN" suffix.
func CoderAlias(projectPath string, started time.Time) string {
	name := sanitize(baseName(projectPath))
	if len(name) > 36 {
		name = name[:36]
	}
	if name == "" {
		name = "project"
	}
	return "coder-" + name + "-" + started.Format("20060102-150405")
}

// SafeID turns text into something the hub accepts as an address segment.
func SafeID(s string, max int) string {
	s = sanitize(s)
	if len(s) > max {
		s = s[:max]
	}
	if s == "" {
		return "x"
	}
	return s
}

// OneLine collapses whitespace and cuts s to max characters.
func OneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}
