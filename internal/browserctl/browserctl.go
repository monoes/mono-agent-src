// Package browserctl drives the user's browser through the extension bridge
// for `monoagentcli browser …` and the packages built on it (styles, visual
// checks, the coder queue). It finds the running bridge, picks the browser
// and the tab, and applies the agent origin policy (policy.go) and the
// submit gate (gate.go, hil.go) on every command an agent runs.
//
// The bridge commands it sends are the extension's (C1 in
// docs/mastermind/plans/2026-10-15-coder-browser.md); it only relays
// through a bridge that is already running and never starts one, because a
// short-lived command that took the bridge port would cut the browser off
// when it exits.
package browserctl

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/cli/bridge"
	"github.com/monoes/mono-agent/internal/extension"
)

// Sender is the bridge a Session talks to; *extension.RemoteSender is one.
type Sender interface {
	SendCommandTo(t extension.Target, cmd *extension.Command, timeout time.Duration) (*extension.Response, error)
	ResolveTarget(t extension.Target) (extension.ConnInfo, error)
	Browsers() ([]extension.ConnInfo, error)
}

var _ Sender = (*extension.RemoteSender)(nil)

// Errors Open and the Session methods return.
var (
	ErrNoBridge = errors.New("no browser bridge is running: start the daemon or `monoagentcli extension serve`, and open a browser that has the MonoAgent Bridge extension")
	ErrNoTab    = errors.New("no tab to act on: open one with `monoagentcli --profile <id> browser open <url>`, or pass --tab (see `monoagentcli --profile <id> browser tabs`)")
)

// BrowserNotFoundError is a --browser that matches no attached browser.
type BrowserNotFoundError struct{ Query string }

func (e *BrowserNotFoundError) Error() string {
	return fmt.Sprintf("no attached browser matches %q: see `monoagentcli browser browsers`", e.Query)
}

// DefaultTimeout bounds one bridge command unless the caller names another.
const DefaultTimeout = 30 * time.Second

// FindBridge returns a sender for the bridge already running at the
// MONOAGENT_EXTENSION_PORT port, else at 9222 or its 9323 fallback.
func FindBridge() (Sender, error) {
	_, base, ok := bridge.FindRunning(context.Background(), bridge.ProbeAddrs())
	if !ok {
		return nil, ErrNoBridge
	}
	return extension.NewRemoteSender(base), nil
}

// Options choose the browser and the tab a Session acts on.
type Options struct {
	// Browser is an attached browser's instance id (or 4+ leading
	// characters) or label. "" routes by Profile.
	Browser string
	// Profile is the monoagent profile id whose bound browser runs the
	// commands; "" is the bridge's default browser.
	Profile string
	// Tab is the tab to act on. 0: the context's last tab, else the active
	// tab of the agent's group (an agent) or of the browser (a human).
	Tab int
	// NoTab opens a Session without a tab (listing, opening a new one).
	NoTab bool
	// Permissions are the user's browser permissions (LoadPermissions);
	// nil: every kind at its default level.
	Permissions *Permissions
	// Allowlist adds allow rules on top of the permissions' own.
	Allowlist []string
	// DB, when set, lets the session raise the HIL item a site needs at
	// level ask (AskSite); without it that is a *SiteNeedsApprovalError.
	DB *sql.DB
	// HoldLocalSubmits holds submits on local pages too (Policy.HoldLocalSubmits).
	HoldLocalSubmits bool
	// PinOrigin, when set, is the one origin the session may act on: any page
	// of another origin is refused (*PinnedOriginError) BEFORE the site policy
	// looks at it, so a page that navigated away raises no "allow this site"
	// approval for an origin it chose.
	PinOrigin string
	// Sender overrides finding the bridge (tests).
	Sender Sender
}

// Session is one command's handle on one browser and, usually, one tab.
type Session struct {
	sender  Sender
	target  extension.Target
	info    extension.ConnInfo
	policy  Policy
	db      *sql.DB
	key     string
	state   State
	profile string
	tabID   int
	url     string
	// blankTab: an agent's tab with no page yet (origin_guard.go).
	blankTab bool
	// pinOrigin: see Options.PinOrigin.
	pinOrigin string
}

// Open finds the bridge and the browser, and unless o.NoTab the tab, which
// an agent may act on only when its page is allowed (Policy.Check).
func Open(o Options) (*Session, error) {
	sender := o.Sender
	if sender == nil {
		var err error
		if sender, err = FindBridge(); err != nil {
			return nil, err
		}
	}
	perms := DefaultPermissions()
	if o.Permissions != nil {
		perms = *o.Permissions
	}
	policy := perms.PolicyForCaller(AgentCaller())
	policy.Allowlist = append(policy.Allowlist, o.Allowlist...)
	policy.HoldLocalSubmits = o.HoldLocalSubmits
	if policy.Agent && policy.level() == LevelOff {
		return nil, &BrowserOffError{Kind: policy.Kind, Declared: policy.Declared} // no command at all, a listing included
	}
	s := &Session{
		sender:    sender,
		policy:    policy,
		db:        o.DB,
		key:       ContextKey(),
		profile:   o.Profile,
		pinOrigin: o.PinOrigin,
	}
	s.state = LoadState(s.key)
	if err := s.route(o); err != nil {
		return nil, err
	}
	if o.NoTab {
		return s, nil
	}
	if err := s.pickTab(o.Tab); err != nil {
		return nil, err
	}
	return s, nil
}

// route resolves the browser and pins its instance.
func (s *Session) route(o Options) error {
	t := extension.Target{Profile: o.Profile}
	if q := strings.TrimSpace(o.Browser); q != "" {
		infos, err := s.sender.Browsers()
		if err != nil {
			return fmt.Errorf("listing browsers: %w", err)
		}
		b, err := matchBrowser(infos, q)
		if err != nil {
			return err
		}
		t = extension.Target{Instance: b.Instance}
	}
	info, err := s.sender.ResolveTarget(t)
	switch {
	case errors.Is(err, extension.ErrBridgeTooOld):
		s.target, s.info = extension.Target{}, extension.ConnInfo{Legacy: true}
		return nil
	case err != nil:
		return err
	}
	s.info = info
	s.target = extension.Target{Instance: info.Instance}
	return nil
}

// matchBrowser finds one browser by instance id, id prefix (4+ chars) or
// label (case-insensitive).
func matchBrowser(infos []extension.ConnInfo, q string) (extension.ConnInfo, error) {
	for _, b := range infos {
		if b.Instance == q {
			return b, nil
		}
	}
	var hits []extension.ConnInfo
	for _, b := range infos {
		if (len(q) >= 4 && strings.HasPrefix(b.Instance, q)) || strings.EqualFold(b.Label, q) {
			hits = append(hits, b)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return extension.ConnInfo{}, &BrowserNotFoundError{Query: q}
	}
	return extension.ConnInfo{}, fmt.Errorf("%q matches %d browsers: use more of the id", q, len(hits))
}

// pickTab chooses the tab: explicit, else the context's last one if it is
// still open in this browser, else the active tab of the agent's group or
// (a human) of the browser. When the policy refuses the last one's page
// (refusedSite), that refusal is the answer.
func (s *Session) pickTab(explicit int) error {
	if explicit > 0 {
		return s.UseTab(explicit)
	}
	if s.state.TabID > 0 && s.state.Instance == s.info.Instance {
		if err := s.UseTab(s.state.TabID); err == nil || refusedSite(err) {
			return err // a refusal is the agent's to read, not a reason to act elsewhere
		}
	}
	tabs, err := s.ListTabs()
	if err != nil {
		return err
	}
	if id := defaultTab(tabs, s.policy.Agent, GroupTitle()); id > 0 {
		return s.UseTab(id)
	}
	return ErrNoTab
}

// refusedSite reports whether err is the policy refusing a tab's page: not
// allowed, blocked, or waiting for (or refused) the user's approval, which
// may have raised a HIL item.
func refusedSite(err error) bool {
	return errors.As(err, new(*NotAllowedError)) || errors.As(err, new(*SiteBlockedError)) ||
		errors.As(err, new(*SiteApprovalPendingError)) || errors.As(err, new(*SiteNeedsApprovalError)) ||
		errors.As(err, new(*SiteRejectedError)) || errors.As(err, new(*SiteAsksFullError))
}

// defaultTab is the active tab of the agent's group (any tab of it when
// none is active), or for a human the first active tab.
func defaultTab(tabs []Tab, agent bool, group string) int {
	if agent {
		first := 0
		for _, t := range tabs {
			if t.GroupTitle != group {
				continue
			}
			if t.Active {
				return t.TabID
			}
			if first == 0 {
				first = t.TabID
			}
		}
		return first
	}
	for _, t := range tabs {
		if t.Active {
			return t.TabID
		}
	}
	return 0
}

// UseTab makes id the session's tab once its page passes the policy, and
// remembers it as the context's last tab.
func (s *Session) UseTab(id int) error {
	u, isBlank, err := s.checkTab(id)
	if err != nil {
		return err
	}
	s.tabID, s.url, s.blankTab = id, u, isBlank
	s.state.TabID, s.state.Instance = id, s.info.Instance
	return s.saveState()
}

// blank is a tab with no page yet, which an agent may only navigate or close.
func blank(u string) bool { return u == "" || u == "about:blank" }

type pageInfo struct {
	TabID int    `json:"tabId"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

func (s *Session) pageInfo(tabID int) (pageInfo, error) {
	var info pageInfo
	err := s.call(tabID, extension.CmdPageInfo, nil, DefaultTimeout, &info)
	return info, err
}

// Refresh re-reads the tab's URL (after a click may have navigated it).
func (s *Session) Refresh() (string, error) {
	info, err := s.pageInfo(s.tabID)
	if err != nil {
		return "", err
	}
	s.url = info.URL
	return s.url, nil
}

func (s *Session) saveState() error { return SaveState(s.key, s.state) }

// Page is the session's tab as an extension page (CDP, EvalCDP, …). The
// policy was checked when the tab was chosen, and an agent's commands
// through it carry expectOrigin like every other.
func (s *Session) Page() *extension.ExtensionPage {
	return extension.NewExtensionPage(pinned{s}, s.tabID)
}

// TabID is the session's tab; 0 when it was opened with NoTab.
func (s *Session) TabID() int { return s.tabID }

// URL is the tab's URL as last read.
func (s *Session) URL() string { return s.url }

// Target is where the session's commands go.
func (s *Session) Target() extension.Target { return s.target }

// Browser is the browser the session resolved to.
func (s *Session) Browser() extension.ConnInfo { return s.info }

// Policy is the origin policy the session enforces.
func (s *Session) Policy() Policy { return s.policy }

// Agent reports whether an agent runs the session.
func (s *Session) Agent() bool { return s.policy.Agent }

// Profile is the monoagent profile the session routes by.
func (s *Session) Profile() string { return s.profile }

// Send sends one bridge command to the session's tab and returns the
// extension's response.
func (s *Session) Send(cmdType string, params map[string]interface{}, timeout time.Duration) (*extension.Response, error) {
	return s.send(s.tabID, cmdType, params, timeout)
}

// Call is Send that decodes the response's data into out (nil: ignore it).
func (s *Session) Call(cmdType string, params map[string]interface{}, timeout time.Duration, out any) error {
	return s.call(s.tabID, cmdType, params, timeout, out)
}

func (s *Session) send(tabID int, cmdType string, params map[string]interface{}, timeout time.Duration) (*extension.Response, error) {
	if params == nil {
		params = map[string]interface{}{}
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	cmd := &extension.Command{Type: cmdType, TabID: tabID, Params: params}
	if err := s.guardCmd(cmd); err != nil {
		return nil, err
	}
	return s.sender.SendCommandTo(s.target, cmd, timeout)
}

func (s *Session) call(tabID int, cmdType string, params map[string]interface{}, timeout time.Duration, out any) error {
	resp, err := s.send(tabID, cmdType, params, timeout)
	if err != nil {
		return err
	}
	if out == nil || resp == nil || resp.Data == nil {
		return nil
	}
	raw, err := json.Marshal(resp.Data)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s: unexpected answer: %w", cmdType, err)
	}
	return nil
}

// pinned sends an ExtensionPage's commands to the session's browser.
type pinned struct{ s *Session }

func (p pinned) SendCommand(cmd *extension.Command, timeout time.Duration) (*extension.Response, error) {
	if err := p.s.guardCmd(cmd); err != nil {
		return nil, err
	}
	return p.s.sender.SendCommandTo(p.s.target, cmd, timeout)
}
