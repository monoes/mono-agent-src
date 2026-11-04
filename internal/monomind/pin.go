package monomind

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// pinned is a binary Find resolved through any shim, with the directory of
// the node that runs it ("" when it isn't a node script or no node exists).
type pinned struct {
	bin     string
	nodeDir string
	at      time.Time
}

var pins struct {
	sync.Mutex
	byFound map[string]pinned // Find's candidate path → its pin
	nodeDir map[string]string // pinned bin → nodeDir
}

// resetPins drops every cached pin (ResetCapabilityCache).
func resetPins() {
	pins.Lock()
	pins.byFound, pins.nodeDir = nil, nil
	pins.Unlock()
	resetRuntimePins()
	resetBinPaths()
}

// pin resolves found (an absolute candidate path) to the binary that runs
// no matter the working directory, cached for capabilityTTL.
func pin(found string) (pinned, error) {
	pins.Lock()
	defer pins.Unlock()
	if p, ok := pins.byFound[found]; ok && time.Since(p.at) < capabilityTTL {
		if _, err := os.Stat(p.bin); err == nil {
			return p, nil
		}
	}
	bin, err := pinPath(found, "monomind")
	if err != nil {
		return pinned{}, err
	}
	nodeDir, err := pinNodeDir(bin)
	if err != nil {
		return pinned{}, err
	}
	p := pinned{bin: bin, nodeDir: nodeDir, at: time.Now()}
	if pins.byFound == nil {
		pins.byFound, pins.nodeDir = map[string]pinned{}, map[string]string{}
	}
	pins.byFound[found] = p
	pins.nodeDir[bin] = nodeDir
	return p, nil
}

// pinPath returns path itself, or for a version-manager shim the binary
// named name that the manager picks from a neutral directory, validated to
// sit under that manager's installs dir.
func pinPath(path, name string) (string, error) {
	kind := shimManager(path)
	if kind == "" {
		return path, nil
	}
	return resolveShim(kind, path, name)
}

// pinNodeDir finds the node that runs bin when bin is a node script: the
// one next to it, else the one on PATH, resolved through a shim the same
// way. "" when bin doesn't run on node or no node is installed (the exec
// then fails as it always did).
func pinNodeDir(bin string) (string, error) {
	if !runsOnNode(bin) {
		return "", nil
	}
	node := filepath.Join(filepath.Dir(bin), "node")
	if runtime.GOOS == "windows" {
		node += ".exe"
	}
	if !isExecutableFile(node) {
		p, err := exec.LookPath("node")
		if err != nil {
			return "", nil
		}
		if node, err = filepath.Abs(p); err != nil {
			node = p
		}
	}
	node, err := pinPath(node, "node")
	if err != nil {
		return "", err
	}
	return filepath.Dir(node), nil
}

// runsOnNode reports whether bin is a script whose interpreter line names
// node (`#!/usr/bin/env node`), or an npm .cmd wrapper on Windows.
func runsOnNode(bin string) bool {
	if strings.EqualFold(filepath.Ext(bin), ".cmd") {
		return true
	}
	real, err := filepath.EvalSymlinks(bin)
	if err != nil {
		real = bin
	}
	head := fileHead(real, 256)
	if !strings.HasPrefix(head, "#!") {
		return false
	}
	line, _, _ := strings.Cut(head, "\n")
	return strings.Contains(line, "node")
}

// nodeDirFor is bin's pinned node dir, pinned now when bin didn't come
// from Find (a caller-supplied path).
func nodeDirFor(bin string) string {
	pins.Lock()
	nd, ok := pins.nodeDir[bin]
	pins.Unlock()
	if ok {
		return nd
	}
	nd, _ = pinNodeDir(bin)
	return nd
}

// Command is exec.Command for a monomind binary: its environment is this
// process's through PinEnv. Callers that set cmd.Env themselves pass it
// through PinEnv.
func Command(bin string, args ...string) *exec.Cmd {
	cmd := exec.Command(bin, args...)
	cmd.Env = PinEnv(ScrubDestinations(os.Environ()), bin)
	return cmd
}

// CommandContext is Command with exec.CommandContext.
func CommandContext(ctx context.Context, bin string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = PinEnv(ScrubDestinations(os.Environ()), bin)
	return cmd
}

// CheckOutside fails when bin, or the node pinned to run it, lies inside
// any of roots (by path or by what it resolves to): a project-local binary
// is the project's to swap, so it never runs for that project.
func CheckOutside(bin string, roots ...string) error {
	paths := []string{bin}
	if nd := nodeDirFor(bin); nd != "" {
		paths = append(paths, nd)
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			rootAbs = root
		}
		rootReal, err := filepath.EvalSymlinks(rootAbs)
		if err != nil {
			rootReal = rootAbs
		}
		for _, p := range paths {
			abs, err := filepath.Abs(p)
			if err != nil {
				abs = p
			}
			real, err := filepath.EvalSymlinks(abs)
			if err != nil {
				real = abs
			}
			for _, r := range []string{rootAbs, rootReal} {
				if within(abs, r) || within(real, r) {
					return fmt.Errorf("monomind resolved to a project-local binary (%s is inside %s); refusing to run it — choose a project folder that doesn't contain the monomind install (a subfolder)", p, root)
				}
			}
		}
	}
	return nil
}

// EnsureIn is Ensure for a command that runs in projectRoot: the project
// check comes before the handshake, which already runs the binary.
func EnsureIn(ctx context.Context, projectRoot string) (string, error) {
	bin, err := findIn(projectRoot)
	if err != nil {
		return "", err
	}
	if _, err := Handshake(ctx, bin); err != nil {
		return "", err
	}
	return bin, nil
}

// findIn is Find for a command that runs in projectRoot.
func findIn(projectRoot string) (string, error) {
	bin, err := Find()
	if err != nil {
		return "", err
	}
	if err := CheckOutside(bin, projectRoot); err != nil {
		return "", err
	}
	return bin, nil
}

// within reports whether path is dir or inside it (dir resolved through
// symlinks too, so a symlinked data dir still matches).
func within(path, dir string) bool {
	check := func(d string) bool {
		rel, err := filepath.Rel(d, path)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
	}
	if check(filepath.Clean(dir)) {
		return true
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		return check(real)
	}
	return false
}

func sameDir(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

func fileHead(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, n)
	k, _ := io.ReadFull(f, buf)
	return string(buf[:k])
}

func isExecutableFile(path string) bool {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return false
	}
	return runtime.GOOS == "windows" || st.Mode()&0o111 != 0
}
