package browserjev

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// ReadNode is browser.read: it opens a URL in a new tab of the browser bound
// to the run's profile and returns the page's title and visible text. It
// reads the text the way `monoagentcli browser a11y` does (the extension's
// accessibility tree), then closes the tab. It never clicks, types or runs
// a script in the page, so it arms no submit guard.
//
// It goes through the same session as the CLI's browser commands
// (browserctl.Open, then OpenTab), so the same permission checks apply: the
// level of its kind of agent in the user's browser permissions (off:
// refused), the site rules, the "allow this site?" approval at level ask,
// and the origin check on where the tab landed. The kind is that of the AI
// caller that started the run (aiCaller, policyFor: browser.jev's rules), or
// chat when the user started it. browserctl.Open never loosens it: an agent
// marker on the process binds when it is stricter.
type ReadNode struct {
	sender browserctl.Sender // nil: the running bridge; tests set a fake
}

// Text bounds: the default and the most a node may ask for.
const (
	defaultReadChars = 20000
	maxReadChars     = 200000
)

func (n *ReadNode) Type() string { return "browser.read" }

// Execute reads the page once (url is the node's, the same for every item)
// and adds what it read to each input item.
func (n *ReadNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	url := str(config, "url")
	if url == "" {
		return nil, fmt.Errorf("%w: browser.read requires \"url\"", workflow.ErrInvalidConfig)
	}
	maxChars, err := readMaxChars(config)
	if err != nil {
		return nil, err
	}
	page, err := n.read(ctx, url, maxChars)
	if err != nil {
		return nil, fmt.Errorf("browser.read: %w", err)
	}
	items := input.Items
	if len(items) == 0 {
		items = []workflow.Item{workflow.NewItem(map[string]interface{}{})}
	}
	out := make([]workflow.Item, 0, len(items))
	for _, item := range items {
		merged := make(map[string]interface{}, len(item.JSON)+len(page))
		for k, v := range item.JSON {
			merged[k] = v
		}
		for k, v := range page {
			merged[k] = v
		}
		out = append(out, workflow.Item{JSON: merged})
	}
	return []workflow.NodeOutput{{Handle: "main", Items: out}}, nil
}

// readMaxChars is max_chars: a number 1..maxReadChars, default
// defaultReadChars; anything else is a config error.
func readMaxChars(config map[string]interface{}) (int, error) {
	n := defaultReadChars
	switch v := config["max_chars"].(type) {
	case nil:
	case float64:
		n = int(v)
	case int:
		n = v
	default:
		n = 0
	}
	if n <= 0 || n > maxReadChars {
		return 0, fmt.Errorf("%w: browser.read max_chars must be a number 1..%d", workflow.ErrInvalidConfig, maxReadChars)
	}
	return n, nil
}

// read opens url, reads it and closes the tab, stopping between steps once
// ctx is done. A tab it opened is always closed, and a failed close is
// returned with the result's error.
func (n *ReadNode) read(ctx context.Context, url string, maxChars int) (_ map[string]interface{}, err error) {
	profile, err := runProfile(ctx)
	if err != nil {
		return nil, err
	}
	who, err := readCaller(ctx)
	if err != nil {
		return nil, err
	}
	db := vault.DBFromContext(ctx)
	perms := browserctl.DefaultPermissions()
	if db != nil {
		if perms, err = browserctl.LoadPermissions(db); err != nil {
			return nil, fmt.Errorf("read the browser permissions: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s, err := browserctl.Open(browserctl.Options{
		Profile: profile, NoTab: true, Permissions: &perms, DB: db, Sender: n.sender, Caller: &who,
	})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tabID, err := s.OpenTab(url, "")
	if tabID != 0 {
		defer func() {
			if cerr := s.CloseOpenedTab(tabID); cerr != nil {
				err = errors.Join(err, fmt.Errorf("the tab it opened is still open: %w", cerr))
			}
		}()
	}
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	checked := s.URL()
	var tree extension.A11yTreeResult
	if err := s.Call(extension.CmdA11yTree, nil, browserctl.DefaultTimeout, &tree); err != nil {
		return nil, fmt.Errorf("read the page: %w", err)
	}
	title := ""
	if tabs, err := s.ListTabs(); err == nil {
		for _, t := range tabs {
			if t.TabID == tabID {
				title = t.Title
			}
		}
	}
	if title == "" {
		title = rootName(tree.Nodes)
	}
	// The address the browser has now, if still on the origin that was
	// checked and read (a same-origin move); else the checked one.
	pageURL := checked
	if now, err := s.Refresh(); err == nil && browserctl.Origin(now) == browserctl.Origin(checked) {
		pageURL = now
	}
	text, first := pageText(tree.Nodes)
	text, cutText := cutRunes(text, maxChars)
	title, cutTitle := cutRunes(title, maxChars)
	first, cutFirst := cutRunes(first, maxChars)
	return map[string]interface{}{
		"url": pageURL, "title": title, "text": text, "first_paragraph": first,
		"truncated": tree.Truncated || cutText || cutTitle || cutFirst,
	}, nil
}

// cutRunes cuts s to max runes.
func cutRunes(s string, max int) (string, bool) {
	if r := []rune(s); len(r) > max {
		return string(r[:max]), true
	}
	return s, false
}

// readCaller is the caller the read is held as: the kind of the AI caller
// that started the run (an unapproved workflow's: the stricter of chat and
// other), else chat. A kind it cannot name is held as other, never as no
// kind (which would be the user's own, unrestricted, terminal).
func readCaller(ctx context.Context) (browserctl.Caller, error) {
	who, ai := aiCaller(ctx)
	if !ai {
		return browserctl.Caller{Kind: browserctl.KindChat}, nil
	}
	pol, err := policyFor(ctx, who)
	if err != nil {
		return browserctl.Caller{}, err
	}
	if pol.Kind == "" {
		return browserctl.Caller{Kind: browserctl.KindOther, Declared: pol.Declared}, nil
	}
	return browserctl.Caller{Kind: pol.Kind, Declared: pol.Declared}, nil
}

// pageText is the page's visible text, one line per text node in page
// order, and the text of its first paragraph. The extension already drops
// text that only repeats its parent's name.
func pageText(nodes []extension.A11yNode) (text, first string) {
	var lines []string
	var walk func(ns []extension.A11yNode, inPara *[]string)
	walk = func(ns []extension.A11yNode, inPara *[]string) {
		for _, n := range ns {
			name := strings.TrimSpace(n.Name)
			leaf := len(n.Children) == 0
			if name != "" && (leaf || n.Role == "heading") && n.Role != "RootWebArea" {
				lines = append(lines, name)
				if inPara != nil {
					*inPara = append(*inPara, name)
				}
			}
			if n.Role == "paragraph" && first == "" && inPara == nil {
				var para []string
				if leaf && name != "" {
					para = append(para, name)
				}
				walk(n.Children, &para)
				first = strings.Join(para, " ")
				continue
			}
			if n.Role == "heading" {
				continue // its text is its name
			}
			walk(n.Children, inPara)
		}
	}
	walk(nodes, nil)
	return strings.Join(lines, "\n"), first
}

// rootName is the document's accessible name (its title), when the tab
// list had none.
func rootName(nodes []extension.A11yNode) string {
	for _, n := range nodes {
		if n.Role == "RootWebArea" {
			return strings.TrimSpace(n.Name)
		}
	}
	return ""
}
