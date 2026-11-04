package chat

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/cli/clicore"
)

const (
	queueMaxNote     = 8 * 1024
	queueMaxSelector = 1024
	queueMaxTitle    = 200
	queueStepTimeout = 90 * time.Second
)

// queueExec runs one monoagentcli command (argv without the binary) and
// returns its stdout: the `browser …` commands `coder queue add` gathers a
// page's context with. A var so tests stand in for the browser.
type queueExec interface {
	Run(ctx context.Context, args []string) ([]byte, error)
}

var queueRunner queueExec = selfExec{}

// selfExec runs this binary. Every agent-context marker is removed from the
// child's env: the person asked for this page, so the agent-only origin
// policy of `browser` must not apply to it.
type selfExec struct{}

func (selfExec) Run(ctx context.Context, args []string) ([]byte, error) {
	bin, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("cannot locate monoagentcli: %w", err)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = withoutAgentMarkers(os.Environ())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var refusal struct{ Error string }
		lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
		_ = json.Unmarshal(lines[len(lines)-1], &refusal)
		msg := refusal.Error
		if msg == "" {
			msg = strings.TrimSpace(stderr.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(msg)
	}
	return out, nil
}

// withoutAgentMarkers is env minus every variable browserctl.IsAgentContext
// reads.
func withoutAgentMarkers(env []string) []string {
	markers := browserctl.AgentContextEnv()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(markers, k) {
			out = append(out, kv)
		}
	}
	return out
}

// queueStep is one command of an item's context: its output lands at file
// (bundle-relative), through --out or, when stdout, as the command's JSON.
// A step with no file only acts on the tab; settle waits queueReloadSettle
// after it.
type queueStep struct {
	file   string
	args   []string // after `browser`; "{out}" is replaced by file's path
	stdout bool
	settle bool
}

// queueReloadSettle is how long a reloaded tab gets after its load event to
// log what it logs while it starts (late requests, errors in timers).
var queueReloadSettle = 2 * time.Second

// queueSteps is what each kind gathers. A selector goes after `--` or as
// --flag=value, so it never reads as a flag. reload (bug-report only) turns
// the tab's console and network capture on and reloads it before the logs
// are read: a tab nobody watched has none.
func queueSteps(kind, sel string, reload bool) []queueStep {
	steps := []queueStep{{file: "page.png", args: []string{"screenshot", "--out", "{out}"}}}
	element := queueStep{file: "element.png", args: []string{"screenshot", "--element=" + sel, "--out", "{out}"}}
	switch kind {
	case "copy-style":
		steps = append(steps, queueStep{file: "styles", args: []string{"styles", "--out", "{out}", "--", sel}})
	case "tokens":
		steps = append(steps, queueStep{file: "tokens.json", args: []string{"tokens", "--out", "{out}"}})
	case "fix-element":
		steps = append(steps, element, queueStep{file: "source.json", args: []string{"source", "--", sel}, stdout: true})
	case "bug-report":
		if reload {
			// Reading the console turns the capture on (browserctl's Console).
			steps = append(steps, queueStep{args: []string{"console", "--limit", "1"}},
				queueStep{args: []string{"reload"}, settle: true})
		}
		steps = append(steps,
			queueStep{file: "console.json", args: []string{"console", "--level", "error,warning"}, stdout: true},
			queueStep{file: "network.json", args: []string{"network", "--failed"}, stdout: true})
	case "integration":
		steps = append(steps, queueStep{file: "network.json", args: []string{"network"}, stdout: true})
	case "ask":
		if sel != "" {
			steps = append(steps, element)
		}
	}
	return steps
}

// queueAttachments are the images the first turn attaches, element first.
var queueAttachments = []string{"styles/element.png", "element.png", "page.png"}

type queueAddInput struct {
	kind, selector, note, cwd, browser string
	tab                                int
	reload                             bool
}

func newCoderQueueAddCmd(cfg *Deps) *cobra.Command {
	var in queueAddInput
	var stdinJSON bool
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Queue a coder chat about a browser tab (refused inside an agent)",
		Long: "Gathers the tab's context with the `browser` commands (a screenshot always; styles, design tokens, " +
			"source mapping, console errors or network calls as --kind needs) into a queued chat the desktop app " +
			"starts on your click. --stdin-json reads {\"selector\",\"note\",\"reload\"} from stdin instead of the flags " +
			"(what the extension bridge passes, so page text never reaches argv). Kinds: " +
			strings.Join(queueKinds, ", ") + ".",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireUser(cfg, "queue a coder chat"); err != nil {
				return err
			}
			if stdinJSON {
				if cmd.Flags().Changed("selector") || cmd.Flags().Changed("note") {
					return clicore.InvalidInput("--stdin-json replaces --selector and --note")
				}
				var v struct {
					Selector, Note string
					Reload         bool
				}
				b, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 64*1024))
				if err != nil || json.Unmarshal(b, &v) != nil {
					return clicore.InvalidInput("--stdin-json: stdin must be a JSON object {\"selector\",\"note\"}")
				}
				in.selector, in.note, in.reload = v.Selector, v.Note, in.reload || v.Reload
			}
			it, err := addQueueItem(cmd, cfg, in)
			if err != nil {
				return err
			}
			if cfg.JSONOutput() {
				return printQueueJSON(cmd, it)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "queued %s (%s) for %s — start it in the MonoAgent app\n", it.ID, it.Kind, it.Cwd)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&in.kind, "kind", "", "copy-style, tokens, fix-element, bug-report, integration or ask")
	f.StringVar(&in.browser, "browser", "", "Browser instance or label (default: the profile's browser)")
	f.IntVar(&in.tab, "tab", 0, "Tab id (default: the browser's active tab)")
	f.StringVar(&in.selector, "selector", "", "CSS selector of the element (copy-style and fix-element need one)")
	f.StringVar(&in.note, "note", "", "What you want done")
	f.StringVar(&in.cwd, "cwd", "", "Folder the chat works in (default: the folder mapped to the page's origin, else the coder root)")
	f.BoolVar(&in.reload, "reload", false, "bug-report: turn the tab's console and network capture on, reload it, and read the logs after it loads")
	f.BoolVar(&stdinJSON, "stdin-json", false, "Read {\"selector\",\"note\",\"reload\"} from stdin")
	return cmd
}

func validateQueueAdd(in queueAddInput) error {
	if !slices.Contains(queueKinds, in.kind) {
		return clicore.InvalidInput("--kind must be one of %s", strings.Join(queueKinds, ", "))
	}
	if (in.kind == "copy-style" || in.kind == "fix-element") && in.selector == "" {
		return clicore.InvalidInput("--kind %s needs --selector", in.kind)
	}
	if len(in.selector) > queueMaxSelector || strings.ContainsAny(in.selector, "\x00\r\n") {
		return clicore.InvalidInput("the selector must be one line of at most %d bytes", queueMaxSelector)
	}
	if len(in.note) > queueMaxNote || !utf8.ValidString(in.note) {
		return clicore.InvalidInput("the note must be UTF-8 text of at most %d bytes", queueMaxNote)
	}
	if in.tab < 0 || strings.HasPrefix(in.browser, "-") || strings.ContainsAny(in.browser, "\x00\r\n") {
		return clicore.InvalidInput("invalid --tab or --browser")
	}
	return nil
}

func addQueueItem(cmd *cobra.Command, cfg *Deps, in queueAddInput) (queueItem, error) {
	if err := validateQueueAdd(in); err != nil {
		return queueItem{}, err
	}
	db, err := cfg.OpenDB()
	if err != nil {
		return queueItem{}, fmt.Errorf("initializing database: %w", err)
	}
	settings, err := loadCoderSettings(db.DB)
	var projects []coderProject
	if err == nil {
		projects, err = loadCoderProjects(db.DB)
	}
	db.Close()
	if err != nil {
		return queueItem{}, err
	}
	if !settings.Enabled {
		return queueItem{}, errCoderDisabled()
	}
	id := newQueueID(time.Now())
	it := queueItem{ID: id, Kind: in.kind, CreatedAt: time.Now().UTC().Format(time.RFC3339), Status: queueStatusQueued,
		Profile: cfg.ProfileID(), Note: in.note, Selector: in.selector, Browser: in.browser, TabID: in.tab}

	root := queueRoot(cfg)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return queueItem{}, err
	}
	tmp := filepath.Join(root, ".tmp-"+id)
	if err := os.Mkdir(tmp, 0o700); err != nil {
		return queueItem{}, err
	}
	done := false
	defer func() {
		if !done {
			_ = os.RemoveAll(tmp)
		}
	}()

	base := []string{"--json", "--db-path=" + cfg.DBPath(), "--profile=" + it.Profile, "browser"}
	common := []string{}
	if in.browser != "" {
		common = append(common, "--browser="+in.browser)
	}
	run := func(sub []string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(cmd.Context(), queueStepTimeout)
		defer cancel()
		args := append(append(append([]string{}, base...), sub[0]), common...)
		return queueRunner.Run(ctx, append(args, sub[1:]...))
	}

	// The tab and its address come from the browser (coder_queue_page.go),
	// and every later command names that tab.
	out, err := run([]string{"tabs"})
	if err != nil {
		return queueItem{}, fmt.Errorf("reading the browser's tabs: %w", err)
	}
	tab, err := pickQueueTab(out, in.tab)
	if err != nil {
		return queueItem{}, err
	}
	if it.Origin, it.Path, err = pageOf(tab.URL); err != nil {
		return queueItem{}, err
	}
	it.TabID, it.Title = tab.TabID, cleanTitle(tab.Title)
	common = append(common, "--tab="+strconv.Itoa(tab.TabID))

	for _, s := range queueSteps(in.kind, in.selector, in.reload) {
		target := filepath.Join(tmp, filepath.FromSlash(s.file))
		sub := make([]string, len(s.args))
		for i, a := range s.args {
			sub[i] = strings.ReplaceAll(a, "{out}", target)
		}
		out, err := run(sub)
		if err != nil {
			return queueItem{}, fmt.Errorf("browser %s: %w", s.args[0], err)
		}
		if s.settle {
			time.Sleep(queueReloadSettle)
		}
		if s.stdout {
			if err := os.WriteFile(target, out, 0o600); err != nil {
				return queueItem{}, err
			}
		}
	}

	if in.kind == "bug-report" {
		console, cok := logCount(tmp, "console.json")
		network, nok := logCount(tmp, "network.json")
		it.Reloaded = in.reload
		it.LogsEmpty = cok && nok && console == 0 && network == 0
		it.NetworkEmpty = nok && network == 0
	}
	if it.Cwd, err = queueCwd(in.cwd, it.Origin, projects, settings); err != nil {
		return queueItem{}, err
	}
	it.Attachments = []string{}
	for _, a := range queueAttachments {
		if fi, err := os.Stat(filepath.Join(tmp, filepath.FromSlash(a))); err == nil && fi.Mode().IsRegular() {
			it.Attachments = append(it.Attachments, a)
		}
	}
	it.Prompt = queuePrompt(it)
	if it.Files, err = queueFiles(tmp); err != nil {
		return queueItem{}, err
	}
	readme := queueReadme(it)
	if err := os.WriteFile(filepath.Join(tmp, queueReadmeFile), []byte(readme), 0o600); err != nil {
		return queueItem{}, err
	}
	it.Files = append(it.Files, queueFile{Name: queueReadmeFile, Size: int64(len(readme))})
	if err := writeQueueItem(tmp, it); err != nil {
		return queueItem{}, err
	}
	if err := os.Rename(tmp, filepath.Join(root, id)); err != nil {
		return queueItem{}, err
	}
	done = true
	return it, nil
}

func newQueueID(now time.Time) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return now.UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

// queueCwd is --cwd, else the folder mapped to origin, else the coder root.
func queueCwd(flag, origin string, projects []coderProject, s coderSettings) (string, error) {
	if flag != "" {
		dir, err := filepath.Abs(expandHome(flag))
		if err != nil || !dirExists(dir) {
			return "", clicore.InvalidInput("--cwd %s is not a folder", flag)
		}
		return dir, nil
	}
	if dir := projectDirFor(projects, strings.ToLower(origin)); dir != "" {
		return dir, nil
	}
	return expandHome(s.WorkspaceRoot), nil
}

// logCount is how many entries a bug report's console.json or network.json
// holds; ok is false when it can't be read, and the prompt then says
// nothing about it.
func logCount(dir, name string) (n int, ok bool) {
	var v struct {
		Entries *[]json.RawMessage `json:"entries"`
	}
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil || json.Unmarshal(b, &v) != nil || v.Entries == nil {
		return 0, false
	}
	return len(*v.Entries), true
}

// queueFiles lists dir's files (not item.json), slash-separated.
func queueFiles(dir string) ([]queueFile, error) {
	out := []queueFile{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if rel == queueItemFile || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, queueFile{Name: filepath.ToSlash(rel), Size: fi.Size()})
		return nil
	})
	return out, err
}
