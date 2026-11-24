// Package agentinstall installs AI agent runtimes (Claude Code, Codex, …)
// from the install hint monomind reports for each one in `agent scan`.
// monomind stays the single owner of the runtime list; this package only
// executes what it says, and only in the two shapes it can do safely:
// `npm install -g <pkgs>` and a vendor `curl -fsSL https://… | bash`
// installer. Anything else is shown to the user as a manual step.
package agentinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/nodemgr"
)

// Kind is how a runtime gets installed.
type Kind string

const (
	KindNpm    Kind = "npm"
	KindScript Kind = "script"
	KindManual Kind = "manual"
)

// Recipe is a parsed install hint.
type Recipe struct {
	Kind      Kind     `json:"kind"`
	Packages  []string `json:"packages,omitempty"`   // npm
	ScriptURL string   `json:"script_url,omitempty"` // script
	Shell     string   `json:"shell,omitempty"`      // script: bash | sh
	// ScriptSHA256 is the hex sha-256 of the script as reviewed; a download that
	// hashes differently is not run. Empty when monomind sent none.
	ScriptSHA256 string `json:"script_sha256,omitempty"`
	// IgnoreScripts runs npm with --ignore-scripts (monomind says the package installs without them).
	IgnoreScripts bool `json:"ignore_scripts,omitempty"`
	Hint      string   `json:"hint"`                 // the original text
}

var (
	npmPkg      = regexp.MustCompile(`^(@[a-z0-9][\w.-]*/)?[a-z0-9][\w.-]*(@[\w.^~<>=*-]+)?$`)
	curlInstall = regexp.MustCompile(`^curl\s+-fsSL\s+(https://\S+)\s*\|\s*(bash|sh)$`)
)

// ScriptHosts are the hosts whose install scripts may run: the vendors in
// monomind's recipes today. A script from anywhere else is shown as a
// manual step — monomind names the URL, but what runs is decided here.
var ScriptHosts = map[string]bool{
	"antigravity.google":            true,
	"hermes-agent.nousresearch.com": true,
}

// validPackage is an npm registry package spec. npm reads a spec ending in
// .tgz/.tar/.tar.gz as a tarball path (a local file or URL), not a registry
// package, so those are refused although the name pattern allows dots.
func validPackage(p string) bool {
	if !npmPkg.MatchString(p) {
		return false
	}
	lower := strings.ToLower(p)
	for _, ext := range []string{".tgz", ".tar", ".tar.gz"} {
		if strings.HasSuffix(lower, ext) {
			return false
		}
	}
	return true
}

// validScript reports whether a script URL may run: https, from a host
// in ScriptHosts, and not on Windows (no bash there).
func validScript(raw, shell string) bool {
	if runtime.GOOS == "windows" || (shell != "bash" && shell != "sh") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return false
	}
	// Host includes any port: an entry names the default https port only.
	return ScriptHosts[strings.ToLower(u.Host)]
}

// Parse turns a hint into a recipe; unrecognized hints are KindManual.
func Parse(hint string) Recipe {
	hint = strings.TrimSpace(hint)
	r := Recipe{Kind: KindManual, Hint: hint}
	fields := strings.Fields(hint)
	if len(fields) >= 4 && fields[0] == "npm" && fields[1] == "install" && (fields[2] == "-g" || fields[2] == "--global") {
		for _, p := range fields[3:] {
			if !validPackage(p) {
				return r
			}
		}
		r.Kind, r.Packages = KindNpm, fields[3:]
		return r
	}
	if m := curlInstall.FindStringSubmatch(hint); m != nil && validScript(m[1], m[2]) {
		r.Kind, r.ScriptURL, r.Shell = KindScript, m[1], m[2]
	}
	return r
}

// ForEntry is the recipe for a scanned runtime: monomind's structured
// `install` (protocol rev 9) when it sent one, re-validated here, else the
// parsed hint. Anything that doesn't validate is manual.
func ForEntry(e monomind.ScanEntry) Recipe {
	in := e.Install
	if in == nil {
		return Parse(e.InstallHint)
	}
	r := Recipe{Kind: KindManual, Hint: e.InstallHint}
	switch in.Kind {
	case "npm":
		if len(in.Packages) == 0 {
			return r
		}
		for _, p := range in.Packages {
			if !validPackage(p) {
				return r
			}
		}
		r.Kind, r.Packages, r.IgnoreScripts = KindNpm, in.Packages, in.IgnoreScripts
	case "script":
		if !validScript(in.URL, in.Shell) {
			return r
		}
		r.Kind, r.ScriptURL, r.Shell, r.ScriptSHA256 = KindScript, in.URL, in.Shell, in.SHA256
	}
	return r
}

// Installer runs recipes.
type Installer struct {
	Node *nodemgr.Manager
	HTTP *http.Client
}

// New returns an Installer using the managed-Node-aware npm.
func New() *Installer {
	return &Installer{Node: nodemgr.New(), HTTP: &http.Client{Timeout: 2 * time.Minute, CheckRedirect: checkScriptRedirect}}
}

// checkScriptRedirect keeps a redirect from an allowed host inside the
// allowlist: https, a ScriptHosts host, at most 3 hops.
func checkScriptRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > 3 {
		return errors.New("too many redirects")
	}
	if req.URL.Scheme != "https" || req.URL.User != nil || !ScriptHosts[strings.ToLower(req.URL.Host)] {
		return fmt.Errorf("refusing redirect to %s: not an allowed installer host", req.URL.Host)
	}
	return nil
}

// ErrManual is returned for recipes that need the user.
var ErrManual = errors.New("this runtime can't be installed automatically")

// InstallTimeout bounds one install. Vendor installers can wait on a
// person (a prompt, sudo); run with no terminal they fail instead, and this
// is the backstop for anything that still hangs.
var InstallTimeout = 15 * time.Minute

// Install runs a recipe, streaming output to progress.
func (in *Installer) Install(ctx context.Context, r Recipe, progress func(string)) error {
	ctx, cancel := context.WithTimeout(ctx, InstallTimeout)
	defer cancel()
	switch r.Kind {
	case KindNpm:
		for _, p := range r.Packages {
			if !validPackage(p) {
				return fmt.Errorf("%w — %q is not an npm registry package", ErrManual, p)
			}
		}
		pkgs := append([]string(nil), r.Packages...)
		if r.IgnoreScripts {
			pkgs = append(pkgs, "--ignore-scripts")
		}
		for _, p := range r.Packages {
			if !pinned(p) {
				progress("warning: " + p + " is not pinned to a version")
			}
		}
		_, err := in.Node.InstallGlobal(ctx, progress, pkgs...)
		return err
	case KindScript:
		// Checked again here: a Recipe can be built by hand, not only by
		// Parse/ForEntry.
		if !validScript(r.ScriptURL, r.Shell) {
			return fmt.Errorf("%w — %s is not a vendor installer monoagent runs", ErrManual, r.ScriptURL)
		}
		return in.runScript(ctx, r, progress)
	default:
		return fmt.Errorf("%w — %s", ErrManual, r.Hint)
	}
}

// NpmBinDir is where an npm recipe's executables would land now (the
// system prefix when writable, else ~/.monoagent/npm-global/bin).
func (in *Installer) NpmBinDir(ctx context.Context) (string, error) {
	plan, err := in.Node.PlanGlobalInstall(ctx)
	if err != nil {
		return "", err
	}
	return plan.BinDir, nil
}

// pinned says whether an npm package spec names a version or tag.
func pinned(spec string) bool {
	return strings.Contains(strings.TrimPrefix(spec, "@"), "@")
}

// installerEnvKeys is the part of the caller's environment a vendor installer
// sees: no API tokens, cloud or git credentials.
var installerEnvKeys = []string{
	"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "TERM", "SHELL", "TMPDIR",
	"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
	"SystemRoot", "USERPROFILE",
}

func installerEnv() []string {
	var env []string
	for _, k := range installerEnvKeys {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// runScript downloads the vendor installer with Go (no dependency on curl
// being installed), reports its size and SHA-256 so the log shows exactly
// what ran, and executes it with the named shell.
func (in *Installer) runScript(ctx context.Context, r Recipe, progress func(string)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.ScriptURL, nil)
	if err != nil {
		return err
	}
	resp, err := in.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("downloading installer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading installer: %s: HTTP %d", r.ScriptURL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	got := hex.EncodeToString(sum[:])
	progress(fmt.Sprintf("downloaded installer %s (%d bytes, sha256 %s)", r.ScriptURL, len(body), got))
	if want := strings.ToLower(strings.TrimSpace(r.ScriptSHA256)); want != "" && want != got {
		return fmt.Errorf("installer %s has sha256 %s, not the reviewed %s: not run", r.ScriptURL, got, want)
	}

	f, err := os.CreateTemp("", "monoagent-installer-*.sh")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(body); err != nil {
		f.Close()
		return err
	}
	f.Close()

	progress("running installer with " + r.Shell)
	cmd := exec.CommandContext(ctx, r.Shell, f.Name())
	cmd.Stdin = nil // installers must not wait for input
	cmd.Env = installerEnv()
	return nodemgr.StreamCmd(cmd, progress)
}
