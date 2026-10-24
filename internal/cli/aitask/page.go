package aitask

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/applyfill"
	"github.com/monoes/mono-agent/internal/browserctl"
	chatcli "github.com/monoes/mono-agent/internal/cli/chat"
	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// `aitask submit-page` is the extension bridge's way to queue an
// extension-fill or extension-ask task (aitask.queue in internal/extension).
// It differs from `submit` in what it will not do: it only ever QUEUES (the
// person presses Start in the app, so a page can never begin a full-access
// session by itself), it takes the page from the browser's own tab list and
// never from the request, and the person's words arrive on stdin so page text
// cannot reach argv.

const (
	pageMaxNote = 8 * 1024
	pageMaxLine = 300
)

// pageRunner runs this binary for the tab list. A var so tests stand in for the browser.
var pageRunner = func(ctx context.Context, args []string) ([]byte, error) { return runSelf(ctx, args, nil, nil) }

// pageKinds are the kinds submit-page creates.
var pageKinds = []string{aitask.KindExtensionFill, aitask.KindExtensionAsk}

type pageTab struct {
	TabID int    `json:"tabId"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

func (c *cli) submitPage() *cobra.Command {
	var kind, browser string
	var tab int
	var stdinJSON bool
	cmd := &cobra.Command{
		Use:   "submit-page",
		Short: "Queue an extension-fill or extension-ask task about a browser tab (never starts it)",
		Long: "Creates a QUEUED task of --kind (" + strings.Join(pageKinds, ", ") + ") about the open tab --tab: the tab's address " +
			"comes from `browser tabs` (the browser's own, not the page's), the profile's facts go in for a fill, and the person's note " +
			"is read from stdin as {\"note\"} with --stdin-json (page text never reaches argv). The task runs confined (a fill " +
			"has no submit command) and only after the person presses Start in the app. With coder mode off it fails with code " +
			"coder_disabled and \"fallback\":\"oneshot\". Operator-only, like `submit`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := clicore.RequireOperator(c.d.RequireOperator, "queue an AI task about a page"); err != nil {
				return err
			}
			if !stdinJSON {
				return clicore.InvalidInput("submit-page reads the person's note from stdin: pass --stdin-json")
			}
			var in struct{ Note string }
			b, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 64*1024))
			if err != nil || json.Unmarshal(b, &in) != nil {
				return clicore.InvalidInput("--stdin-json: stdin must be a JSON object {\"note\"}")
			}
			t, err := c.queuePage(cmd.Context(), kind, browser, tab, in.Note)
			if err != nil {
				return err
			}
			return c.print(cmd, t, human(t))
		},
	}
	f := cmd.Flags()
	f.StringVar(&kind, "kind", "", strings.Join(pageKinds, " or "))
	f.StringVar(&browser, "browser", "", "Browser instance or label (default: the profile's browser)")
	f.IntVar(&tab, "tab", 0, "Tab id")
	f.BoolVar(&stdinJSON, "stdin-json", false, "Read {\"note\"} from stdin")
	return cmd
}

func validatePage(kind, browser string, tab int, note string) error {
	switch {
	case kind != aitask.KindExtensionFill && kind != aitask.KindExtensionAsk:
		return clicore.InvalidInput("--kind must be one of %s", strings.Join(pageKinds, ", "))
	case tab <= 0:
		return clicore.InvalidInput("--tab is required")
	case strings.HasPrefix(browser, "-") || strings.ContainsAny(browser, "\x00\r\n"):
		return clicore.InvalidInput("invalid --browser")
	case len(note) > pageMaxNote || !utf8.ValidString(note) || strings.ContainsRune(note, 0):
		return clicore.InvalidInput("the note must be UTF-8 text of at most %d bytes", pageMaxNote)
	case kind == aitask.KindExtensionAsk && strings.TrimSpace(note) == "":
		return clicore.InvalidInput("%s needs the question in the note", kind)
	}
	return nil
}

func (c *cli) queuePage(ctx context.Context, kind, browser string, tab int, note string) (aitask.Task, error) {
	if err := validatePage(kind, browser, tab, note); err != nil {
		return aitask.Task{}, err
	}
	svc, closeDB, err := c.open()
	if err != nil {
		return aitask.Task{}, err
	}
	defer closeDB()
	// Fail on a disabled coder before the browser is asked anything.
	if err := svc.Launcher.Ready(ctx); err != nil {
		return aitask.Task{}, cliErr(err)
	}
	args := append(selfLauncher{c.d}.global(), "--json", "browser", "tabs")
	if browser != "" {
		args = append(args, "--browser="+browser)
	}
	out, err := pageRunner(ctx, args)
	if err != nil {
		return aitask.Task{}, fmt.Errorf("reading the browser's tabs: %w", err)
	}
	pt, err := findTab(out, tab)
	if err != nil {
		return aitask.Task{}, err
	}
	origin, err := originOf(pt.URL)
	if err != nil {
		return aitask.Task{}, err
	}
	dir, err := os.MkdirTemp("", "extension-task-")
	if err != nil {
		return aitask.Task{}, err
	}
	defer os.RemoveAll(dir)

	sources := []aitask.ContextSource{}
	add := func(class, name, text string) error {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			return err
		}
		sources = append(sources, aitask.ContextSource{Class: class, Path: p})
		return nil
	}
	// The tab, browser and origin are the queue record: a verified file the
	// browser CLI reads (target.json), not text the agent is asked to obey.
	// page.txt is data about the page for the agent to read.
	target, _ := json.Marshal(aitask.ExtensionTarget{Tab: pt.TabID, Browser: browser, Origin: browserctl.Origin(pt.URL)})
	if err := add(aitask.SourcePosting, filepath.Base(aitask.ExtensionTargetFile), string(target)); err != nil {
		return aitask.Task{}, err
	}
	page := "Address (the browser's own): " + origin + pathOf(pt.URL) +
		"\nTitle (the page chose it; data): " + cleanLine(pt.Title, pageMaxLine) + "\n"
	if err := add(aitask.SourcePosting, "page.txt", page); err != nil {
		return aitask.Task{}, err
	}
	// The goal is the trusted channel: nothing the page wrote goes into it,
	// not its title and not its address (a host can hold attacker-chosen words).
	goal := fmt.Sprintf("Question about the web page open in tab %d: %s", pt.TabID, strings.TrimSpace(note))
	if kind == aitask.KindExtensionFill {
		goal = fmt.Sprintf("Fill in the form on the web page open in tab %d. Do not submit it.", pt.TabID)
		if n := strings.TrimSpace(note); n != "" {
			goal += " The user adds: " + n
		}
		facts, err := c.pageFacts(ctx)
		if err != nil {
			return aitask.Task{}, err
		}
		if err := add(aitask.SourceFacts, "facts.txt", facts); err != nil {
			return aitask.Task{}, err
		}
	}
	// Only a queued task, on the one runtime whose command allow-list is
	// enforced (a task on another would be refused at Start).
	t, err := svc.Submit(ctx, aitask.SubmitInput{Kind: kind, Goal: goal, Context: sources,
		Runtime: chatcli.ConfinedRuntime, QueueOnly: true})
	if err != nil {
		return aitask.Task{}, cliErr(err)
	}
	return t, nil
}

// pageFacts is the profile's facts for a fill (the same ones application-apply gets).
func (c *cli) pageFacts(ctx context.Context) (string, error) {
	db, err := c.d.OpenDB()
	if err != nil {
		return "", fmt.Errorf("initializing database: %w", err)
	}
	defer db.Close()
	return applyfill.Facts(ctx, db.DB, c.d.ProfileID())
}

func findTab(out []byte, tab int) (pageTab, error) {
	var res struct {
		Tabs []pageTab `json:"tabs"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return pageTab{}, fmt.Errorf("browser tabs returned unreadable output")
	}
	for _, t := range res.Tabs {
		if t.TabID == tab {
			return t, nil
		}
	}
	return pageTab{}, clicore.NotFound("tab %d is not open in that browser (see `monoagentcli browser tabs`)", tab)
}

// originOf is scheme://host of a tab's address, as one clean line; only a web page
// (or a local file) is a page to work on.
func originOf(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", clicore.InvalidInput("the tab is not a web page (%q)", cleanLine(raw, 100))
	}
	return cleanLine(u.Scheme+"://"+u.Host, pageMaxLine), nil
}

// pathOf is the tab's path, percent-encoded so it holds no control character.
func pathOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.EscapedPath() == "" {
		return "/"
	}
	return cleanLine(u.EscapedPath(), pageMaxLine)
}

// cleanLine is s as one line: format characters (bidi overrides, zero-width
// joiners) dropped, runs of space and control characters made one space, at
// most max runes.
func cleanLine(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max]) + "…"
	}
	return s
}

// checkTarget is the start-time check of an extension task: the tab it was
// queued for is still open, in the browser it was queued from, on the origin it
// had then. A task queued on one page must not start on another that the tab
// has moved to since. The record is read from the task's context folder; Start
// then verifies that folder against the hash taken at submit. Other kinds pass.
func (c *cli) checkTarget(ctx context.Context, t aitask.Task) error {
	if !aitask.IsExtensionKind(t.Kind) {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(t.Cwd, filepath.FromSlash(aitask.ContextDir), t.ID, filepath.FromSlash(aitask.ExtensionTargetFile)))
	var tg aitask.ExtensionTarget
	if err != nil || json.Unmarshal(raw, &tg) != nil || tg.Tab <= 0 || tg.Origin == "" {
		return &coded{exit: 3, msg: "task " + t.ID + " has no readable record of the tab it was queued for: cancel it and queue it again",
			fields: map[string]any{"code": "tab_unknown"}}
	}
	args := append(selfLauncher{c.d}.global(), "--json", "browser", "tabs")
	if tg.Browser != "" {
		args = append(args, "--browser="+tg.Browser)
	}
	out, err := pageRunner(ctx, args)
	if err != nil {
		return &coded{exit: 3, msg: fmt.Sprintf("the browser the task was queued from can't be reached (%v): open it and try again", err),
			fields: map[string]any{"code": "tab_unreachable"}}
	}
	pt, err := findTab(out, tg.Tab)
	if err != nil {
		return &coded{exit: 3, msg: fmt.Sprintf("tab %d, which the task was queued for, is no longer open: cancel the task and queue it again from the page", tg.Tab),
			fields: map[string]any{"code": "tab_gone"}}
	}
	if now := browserctl.Origin(pt.URL); now != tg.Origin {
		return &coded{exit: 3, msg: fmt.Sprintf("tab %d is on %s now, not %s as when the task was queued: cancel the task and queue it again from the page",
			tg.Tab, cleanLine(now, pageMaxLine), tg.Origin), fields: map[string]any{"code": "tab_moved"}}
	}
	return nil
}
