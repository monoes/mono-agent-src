package agent

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/agentinstall"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/shellpath"
)

func (d Deps) newAgentInstallCmd() *cobra.Command {
	var yes, force bool
	var approveScript string
	cmd := &cobra.Command{
		Use:   "install <runtime>",
		Short: "Install an AI agent runtime (claude, codex, opencode, …)",
		Long: `Installs one of the runtimes ` + "`agent scan`" + ` lists, using the install
recipe monomind reports for it:

  npm packages      npm install -g, through a suitable system Node or the
                    managed one (monoagentcli nodejs install); into
                    ~/.monoagent/npm-global when the system prefix needs root
  vendor installer  the vendor's https install script, downloaded and run;
                    asks first (or pass --yes)
  anything else     printed as manual instructions

With --json, progress is streamed as NDJSON like ` + "`doctor fix`" + `.`,
		Example: `  monoagentcli agent install claude
  monoagentcli agent install antigravity --yes --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			progress := d.ProgressWriter(out, d.JSONOutput())
			confirmScript := scriptConsent(yes, approveScript, !d.JSONOutput() && d.StdinIsTerminal(), cmd.InOrStdin(), out)
			msg, err := d.installRuntime(cmd.Context(), realRuntimeMachine(), args[0], force, confirmScript, progress)
			return d.FinishStreamed(out, d.JSONOutput(), err, msg)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Run vendor install scripts without asking")
	cmd.Flags().StringVar(&approveScript, "approve-script", "", "Run the vendor install script only if it is this exact URL (what the person was shown)")
	cmd.Flags().BoolVar(&force, "force", false, "Install even if the runtime is already installed (upgrade)")
	return cmd
}

// runtimeMachine is what installRuntime needs from this machine; tests
// fake it.
type runtimeMachine struct {
	scan    func(ctx context.Context) (*monomind.ScanResult, error)
	install func(ctx context.Context, r agentinstall.Recipe, progress func(string)) error
	// npmBinDir is where an npm install would put executables now.
	npmBinDir func(ctx context.Context) (string, error)
	// loginPath is the user's login-shell PATH ("" when unknown).
	loginPath func(ctx context.Context) string
	// nodeDirFor is the folder of the `node` a Node-script binary runs
	// with here, "" for a binary that isn't one.
	nodeDirFor func(bin string) string
}

// realRuntimeMachine is the machine installRuntime runs on outside tests.
func realRuntimeMachine() runtimeMachine {
	in := agentinstall.New()
	return runtimeMachine{
		scan:      monomind.Scan,
		install:   in.Install,
		npmBinDir: in.NpmBinDir,
		loginPath: func(ctx context.Context) string {
			p, _ := shellpath.LoginPath(ctx)
			return p
		},
		nodeDirFor: nodeDirFor,
	}
}

// installRuntime installs one runtime by its `agent scan` id and verifies
// it with a re-scan. confirmScript gates vendor install scripts.
func (d Deps) installRuntime(ctx context.Context, m runtimeMachine, id string, force bool, confirmScript func(agentinstall.Recipe) bool,
	progress func(string)) (string, error) {
	scan, err := m.scan(ctx)
	if err != nil {
		return "", err
	}
	entry := scan.Find(id)
	if entry == nil {
		ids := make([]string, 0, len(scan.Agents))
		for _, a := range scan.Agents {
			ids = append(ids, a.ID)
		}
		sort.Strings(ids)
		return "", d.NotFound("unknown runtime %q (known: %s)", id, strings.Join(ids, ", "))
	}
	if entry.Installed && !force {
		return fmt.Sprintf("%s is already installed (%s at %s) — nothing to do; use --force to reinstall",
			id, strPtr(entry.Version, "unknown version"), strPtr(entry.Binary, "?")), nil
	}

	recipe := agentinstall.ForEntry(*entry)
	switch recipe.Kind {
	case agentinstall.KindManual:
		return "", d.InvalidInput("%s can't be installed automatically — %s", id, entry.InstallHint)
	case agentinstall.KindScript:
		if !confirmScript(recipe) {
			return "", d.InvalidInput("%s installs by running the vendor script %s — confirm, or pass --yes", id, recipe.ScriptURL)
		}
	}
	binDir := ""
	if recipe.Kind == agentinstall.KindNpm {
		if binDir, err = m.npmBinDir(ctx); err != nil {
			return "", err
		}
	}
	before := strPtr(entry.Binary, "")
	// Reinstalling with npm when the runtime came from elsewhere (mise,
	// Homebrew, a vendor installer) adds a second copy the first one still
	// shadows on PATH: nothing would change but the report.
	if entry.Installed && before != "" && binDir != "" && !sameDir(filepath.Dir(before), binDir) {
		return "", d.InvalidInput("%s at %s was not installed with npm into %s — --force would add a second copy there "+
			"instead of updating this one; update it the way it was installed (mise, Homebrew, …), or remove it and run this again",
			id, before, binDir)
	}
	if err := m.install(ctx, recipe, progress); err != nil {
		return "", err
	}

	after, err := m.scan(ctx)
	if err != nil {
		return "", fmt.Errorf("installed, but re-scanning failed: %w", err)
	}
	got := after.Find(id)
	if got == nil || !got.Installed {
		return "", fmt.Errorf("the installer finished but monomind still doesn't see %s — open a new terminal, or check the output above", id)
	}
	bin := strPtr(got.Binary, id)
	if binDir != "" && got.Binary != nil && !sameDir(filepath.Dir(bin), binDir) {
		progress(fmt.Sprintf("note: %s comes first on PATH, so the copy just installed into %s is not the one used", bin, binDir))
	}
	progress("sign in: " + signInHint(bin, strPtr(got.LoginHint, ""), m.loginPath(ctx), m.nodeDirFor(bin)))
	oldV, newV := strPtr(entry.Version, ""), strPtr(got.Version, "")
	switch {
	case !entry.Installed:
		return fmt.Sprintf("%s %s installed at %s", id, newV, bin), nil
	case oldV != "" && oldV == newV && bin == before:
		return fmt.Sprintf("%s reinstalled — still %s at %s (nothing newer was found)", id, newV, bin), nil
	default:
		return fmt.Sprintf("%s updated from %s to %s at %s", id, strPtr(entry.Version, "unknown version"), strPtr(got.Version, "unknown version"), bin), nil
	}
}

// sameDir compares two folders after resolving symlinks.
func sameDir(a, b string) bool {
	if ra, err := filepath.EvalSymlinks(a); err == nil {
		a = ra
	}
	if rb, err := filepath.EvalSymlinks(b); err == nil {
		b = rb
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// signInHint is the sign-in command to show for a runtime just installed
// at bin. The user runs it in their own terminal, whose PATH (loginPath)
// may lack bin's folder (~/.monoagent/npm-global/bin) and the folder of the
// `node` it runs with (a managed Node): then the full path is shown, with
// node's folder put in front of PATH.
func signInHint(bin, loginHint, loginPath, nodeDir string) string {
	onPath := func(dir string) bool {
		if loginPath == "" {
			return false // unknown: say it in full
		}
		for _, p := range filepath.SplitList(loginPath) {
			if p != "" && sameDir(p, dir) {
				return true
			}
		}
		return false
	}
	name := filepath.Base(bin)
	command := loginHint
	if command == "" {
		command = strings.TrimSuffix(name, filepath.Ext(name))
		if runtime.GOOS != "windows" {
			command = name
		}
	}
	if filepath.IsAbs(bin) && !onPath(filepath.Dir(bin)) {
		first, rest, _ := strings.Cut(command, " ")
		if first == name || first == strings.TrimSuffix(name, filepath.Ext(name)) {
			command = strings.TrimSpace(shellQuote(bin) + " " + rest)
		} else {
			command += " (" + name + " is at " + bin + ")"
		}
	}
	if nodeDir != "" && !onPath(nodeDir) {
		if runtime.GOOS == "windows" {
			command += " (with " + nodeDir + " on PATH)"
		} else {
			command = "PATH=" + shellQuote(nodeDir) + ":\"$PATH\" " + command
		}
	}
	if loginHint != "" {
		return command
	}
	return "run `" + command + "` once in a terminal if it asks you to log in"
}

// shellQuote quotes s for a POSIX shell when it needs it.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t'\"$`\\!*?[]{}()<>|&;#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// nodeDirFor returns the folder of the `node` bin runs with, when bin is a
// `#!/usr/bin/env node` script (an npm package) — the `node` on this
// process's PATH, which may be the managed one. "" otherwise.
func nodeDirFor(bin string) string {
	f, err := os.Open(bin)
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 128)
	n, _ := f.Read(head)
	line, _, _ := strings.Cut(string(head[:n]), "\n")
	if !strings.HasPrefix(line, "#!") || !strings.Contains(line, "node") {
		return ""
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return ""
	}
	return filepath.Dir(node)
}

func strPtr(s *string, def string) string {
	if s == nil || *s == "" {
		return def
	}
	return *s
}

// scriptConsent decides whether a vendor install script may run: --yes
// approves any; --approve-script approves exactly the URL the person was
// shown (the GUI's confirmation), and a recipe that now names another is
// refused, since consent was for what was shown, not for whatever runs;
// otherwise a person is asked, and with nobody to ask it is refused.
func scriptConsent(yes bool, approveURL string, canAsk bool, in io.Reader, out io.Writer) func(agentinstall.Recipe) bool {
	return func(r agentinstall.Recipe) bool {
		switch {
		case yes:
			return true
		case approveURL != "":
			return r.ScriptURL == approveURL
		case !canAsk:
			return false
		}
		fmt.Fprintf(out, "Run the vendor installer %s with %s? [y/N] ", r.ScriptURL, r.Shell)
		ans, _ := bufio.NewReader(in).ReadString('\n')
		a := strings.ToLower(strings.TrimSpace(ans))
		return a == "y" || a == "yes"
	}
}
