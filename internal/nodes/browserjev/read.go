package browserjev

import (
	"context"
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
// a script in the page.
//
// It goes through the same session as the CLI's browser commands
// (browserctl.Open, then OpenTab), so the same checks apply: the level of
// its kind of agent in the user's browser permissions (off: refused), the
// site rules, the "allow this site?" approval at level ask, and the origin
// check on where the tab landed. The kind is that of the AI caller that
// started the run (aiCaller, policyFor: browser.jev's rules), or chat when
// the user started it. browserctl.Open never loosens it: an agent marker
// on the process binds when it is stricter.
type ReadNode struct {
	sender browserctl.Sender // nil: the running bridge; tests set a fake
}

// Text bounds: the default and the most a node may ask for.
const (
	defaultReadChars = 20000
	maxReadChars     = 200000
)

func (n *ReadNode) Type() string { return "browser.read" }

func (n *ReadNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	url := str(config, "url")
	if url == "" {
		return nil, fmt.Errorf("%w: browser.read requires \"url\"", workflow.ErrInvalidConfig)
	}
	maxChars := num(config, "max_chars", defaultReadChars)
	if maxChars <= 0 || maxChars > maxReadChars {
		return nil, fmt.Errorf("%w: browser.read max_chars must be 1..%d", workflow.ErrInvalidConfig, maxReadChars)
	}
	items := input.Items
	if len(items) == 0 {
		items = []workflow.Item{workflow.NewItem(map[string]interface{}{})}
	}
	out := make([]workflow.Item, 0, len(items))
	for _, item := range items {
		page, err := n.read(ctx, url, maxChars)
		if err != nil {
			return nil, fmt.Errorf("browser.read: %w", err)
		}
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

// read opens url, reads it and closes the tab.
func (n *ReadNode) read(ctx context.Context, url string, maxChars int) (map[string]interface{}, error) {
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
	s, err := browserctl.Open(browserctl.Options{
		Profile: profile, NoTab: true, Permissions: &perms, DB: db, Sender: n.sender, Caller: &who,
	})
	if err != nil {
		return nil, err
	}
	tabID, err := s.OpenTab(url, "")
	if tabID != 0 {
		defer func() { _ = s.CloseTab() }()
	}
	if err != nil {
		return nil, err
	}
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
	text, first := pageText(tree.Nodes)
	if title == "" {
		title = rootName(tree.Nodes)
	}
	cut := tree.Truncated
	if r := []rune(text); len(r) > maxChars {
		text, cut = string(r[:maxChars]), true
	}
	return map[string]interface{}{
		"url": s.URL(), "title": title, "text": text, "first_paragraph": first, "truncated": cut,
	}, nil
}

// readCaller is the caller the read is held as: the kind of the AI caller
// that started the run (an unapproved workflow's: the stricter of chat and
// other), else chat.
func readCaller(ctx context.Context) (browserctl.Caller, error) {
	who, ai := aiCaller(ctx)
	if !ai {
		return browserctl.Caller{Kind: browserctl.KindChat}, nil
	}
	pol, err := policyFor(ctx, who)
	if err != nil {
		return browserctl.Caller{}, err
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
