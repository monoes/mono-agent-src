package browserctl

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/profiledir"
)

// The submit gate (C2): an agent's form submit on an allowed origin that is
// not local waits for a person. Before an agent acts on such a page the
// page's submit_guard is armed; a click on a submit-like control is held
// instead of performed, and submits the guard blocked (Enter in a field,
// script) are reported on the command that caused them or the next one.
// Each becomes a HIL item (hil.go); after approval the agent runs
// `browser submit --hil <id>`. Fetch/XHR-only "forms" are not caught.

// AwaitingApproval is a submit held for a person's approval. The CLI
// reports it as {status:"awaiting_approval",hil_id} with exit code 3.
type AwaitingApproval struct {
	HILID  string `json:"hil_id"`
	Origin string `json:"origin"`
	URL    string `json:"url"`
	Via    string `json:"via"`
	// Profile is the profile the item is in, for the commands to run.
	Profile string `json:"profile,omitempty"`
}

func (a *AwaitingApproval) Error() string {
	if a.Via == ViaFill {
		return fmt.Sprintf("the form fill on %s waits for a person's approval (HIL item %s): ask the user to approve it with `monoagentcli %s hil approve %s` or on the Human in Loop page, then run `monoagentcli %s browser fill --hil %s`",
			a.Origin, a.HILID, ProfileFlag(a.Profile), a.HILID, ProfileFlag(a.Profile), a.HILID)
	}
	if a.Via == ViaUpload {
		return fmt.Sprintf("the file upload on %s waits for a person's approval (HIL item %s): ask the user to approve it with `monoagentcli %s hil approve %s` or on the Human in Loop page, then run `monoagentcli %s browser upload --hil %s`",
			a.Origin, a.HILID, ProfileFlag(a.Profile), a.HILID, ProfileFlag(a.Profile), a.HILID)
	}
	return fmt.Sprintf("the form submit on %s waits for a person's approval (HIL item %s): ask the user to approve it with `monoagentcli %s hil approve %s` or on the Human in Loop page, then run `monoagentcli %s browser submit --hil %s`",
		a.Origin, a.HILID, ProfileFlag(a.Profile), a.HILID, ProfileFlag(a.Profile), a.HILID)
}

// ProfileFlag is the --profile part of a command to copy: profile, or a
// placeholder when it is not known.
func ProfileFlag(profile string) string {
	if profile == "" {
		profile = "<id>"
	}
	return "--profile " + profile
}

// Gated reports whether the gate applies to the session's tab now.
func (s *Session) Gated() bool { return s.tabID > 0 && s.policy.SubmitGated(s.url) }

// guardStatus is submit_guard's answer.
type guardStatus struct {
	Armed bool `json:"armed"`
	// Origin is the origin the guard was armed on; its records belong to
	// it, and the extension drops them when the top page leaves it.
	Origin  string         `json:"origin"`
	Blocked []blockedEntry `json:"blocked"`
}

type blockedEntry struct {
	Seq     int         `json:"seq"`
	TS      float64     `json:"ts"`
	URL     string      `json:"url"`
	FrameID int         `json:"frameId"`
	Form    FormSummary `json:"form"`
}

func (s *Session) guard(action string) (guardStatus, error) {
	var st guardStatus
	err := s.Call(extension.CmdSubmitGuard, map[string]interface{}{"action": action}, DefaultTimeout, &st)
	return st, err
}

// BeforeAct arms the guard on a gated tab and reports a submit it blocked
// since the last command (an *AwaitingApproval). It fails closed: a gated
// tab whose guard cannot be armed is not acted on.
func (s *Session) BeforeAct(ctx context.Context, db *sql.DB) error {
	if s.policy.Agent && s.policy.level() == LevelFullNoApprovals && s.tabID > 0 && !blank(s.url) {
		// No guard at this level: one armed earlier (another level, another
		// agent) is let go, with its bar.
		_, _ = s.guard(extension.SubmitGuardDisarm)
		return nil
	}
	if !s.Gated() {
		return nil
	}
	if err := s.arm(); err != nil {
		return err
	}
	return s.takeBlocked(ctx, db)
}

// arm arms the tab's submit guard; it is armed again before every action,
// since the extension disarms a tab whose top page changes origin. A tab
// still navigating is given time to load, re-read (it may have left the
// allowed pages) and armed once more.
func (s *Session) arm() error {
	_, err := s.guard(extension.SubmitGuardArm)
	if err != nil && strings.Contains(err.Error(), "navigating") {
		_, _ = s.sendRaw(s.tabID, extension.CmdWaitLoad, map[string]interface{}{"timeout": loadTimeout.Milliseconds()}, loadTimeout+5*time.Second)
		if err := s.UseTab(s.tabID); err != nil {
			return err
		}
		if !s.Gated() {
			return nil
		}
		_, err = s.guard(extension.SubmitGuardArm)
	}
	if err != nil {
		return fmt.Errorf("this page needs approval before any form is submitted, and its submit guard could not be armed: %w", err)
	}
	return nil
}

// AfterAct reports a submit the guard blocked during the action. The
// action may have navigated the tab, so its URL is read again first.
func (s *Session) AfterAct(ctx context.Context, db *sql.DB) error {
	if s.tabID == 0 {
		return nil
	}
	was := s.Gated()
	if _, err := s.Refresh(); err != nil {
		return nil // the tab went away or is navigating; the next command checks again
	}
	if !s.policy.Allowed(s.url) {
		return nil // the action took the tab off the allowed pages: nothing more is sent to it
	}
	if !was && !s.Gated() {
		return nil
	}
	return s.takeBlocked(ctx, db)
}

// takeBlocked turns guard records newer than the last reported one into
// HIL items and returns the first as an *AwaitingApproval.
func (s *Session) takeBlocked(ctx context.Context, db *sql.DB) error {
	st, err := s.guard(extension.SubmitGuardStatus)
	if err != nil {
		return fmt.Errorf("reading the page's submit guard: %w", err)
	}
	// Per origin: the records of another origin were dropped, and seqs may
	// start over.
	key := tabKey(s.info.Instance, s.tabID) + "|" + st.Origin
	last := s.state.Reported[key]
	maxSeq := 0
	for _, b := range st.Blocked {
		if b.Seq > maxSeq {
			maxSeq = b.Seq
		}
	}
	if maxSeq < last {
		last = 0 // the extension restarted and its seq began again
	}
	var first *AwaitingApproval
	for _, b := range st.Blocked {
		if b.Seq <= last {
			continue
		}
		u := b.URL
		if u == "" {
			u = s.url
		}
		a, err := s.Hold(ctx, db, u, b.Form, "guard", b.Seq, b.FrameID)
		if err != nil {
			return err
		}
		if first == nil {
			first = a
		}
	}
	if maxSeq != s.state.Reported[key] {
		if s.state.Reported == nil {
			s.state.Reported = map[string]int{}
		}
		s.state.Reported[key] = maxSeq
		_ = s.saveState()
	}
	if first != nil {
		return first
	}
	return nil
}

// Hold records a held submit on pageURL (in frameID, 0 the top frame) as a
// HIL item, with a screenshot of the page for the card, and returns it as
// an *AwaitingApproval.
func (s *Session) Hold(ctx context.Context, db *sql.DB, pageURL string, form FormSummary, via string, seq, frameID int) (*AwaitingApproval, error) {
	it, err := CreateSubmitItem(ctx, db, SubmitRequest{
		Profile: s.profile, URL: pageURL, Form: form, Via: via, TabID: s.tabID, Instance: s.info.Instance, Seq: seq,
		FrameID: frameID, Page: s.Page(),
	})
	if err != nil {
		return nil, err
	}
	if path := s.cardScreenshot(it.ID); path != "" {
		it.Screenshot = path
		_ = setSubmitScreenshot(ctx, db, s.profile, it.ID, path)
	}
	return &AwaitingApproval{HILID: it.ID, Origin: it.Origin, URL: it.URL, Via: via, Profile: s.profile}, nil
}

// cardScreenshot saves the tab's viewport for a HIL card under
// ~/.monoagent/browser/hil/<id>.png; "" when it cannot.
func (s *Session) cardScreenshot(id string) string {
	shot, err := s.Screenshot(ScreenshotOptions{})
	if err != nil {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, ".monoagent", "browser", "hil")
	if os.MkdirAll(dir, 0o700) != nil {
		return ""
	}
	path := filepath.Join(dir, safeKey(id)+shot.Ext())
	if os.WriteFile(path, shot.Data, 0o600) != nil {
		return ""
	}
	return path
}

func setSubmitScreenshot(ctx context.Context, db *sql.DB, profile, id, path string) error {
	if profile == "" {
		return profiledir.ErrNoProfile
	}
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT readonly_data FROM hil_pending WHERE id = ? AND profile_id = ?`, id, profile).Scan(&raw); err != nil {
		return err
	}
	var card submitCard
	if err := json.Unmarshal([]byte(raw), &card); err != nil {
		return err
	}
	card.Screenshot = path
	b, err := json.Marshal(card)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `UPDATE hil_pending SET readonly_data = ? WHERE id = ? AND profile_id = ?`, string(b), id, profile)
	return err
}

// SubmitTarget is what a click on selector would do to a form.
type SubmitTarget struct {
	Found  bool        `json:"found"`
	Submit bool        `json:"submit"`
	Why    string      `json:"why,omitempty"`
	Form   FormSummary `json:"form"`
}

// ProbeSubmit reports whether clicking selector is submit-like: an
// [type=submit] control, a button inside a <form>, or a control whose text
// says submit, send, post, publish, pay, buy, confirm, order, sign up or
// apply. The form it would send is summarized with sensitive values masked.
func (s *Session) ProbeSubmit(selector string) (SubmitTarget, error) {
	var t SubmitTarget
	res, err := evalIsolated(s.Page(), pageScript(probeSubmitJS, "", selector, ""))
	if err != nil {
		return t, err
	}
	raw, err := json.Marshal(res)
	if err != nil {
		return t, err
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return t, fmt.Errorf("checking the click target: %w", err)
	}
	if t.Form.Fields == nil {
		t.Form.Fields = []FormField{}
	}
	t.Form.Submitter = selector
	return t, nil
}

// ErrNotSent wraps a Resubmit failure that happened before anything
// reached the page, so the approval can be handed back.
var ErrNotSent = errors.New("nothing was submitted")

// Resubmit performs an approved submit item on the session's tab once the
// page still shows what was approved (ResubmitItem).
func (s *Session) Resubmit(it SubmitItem) error { return ResubmitItem(s.Page(), it) }

// ResubmitPage repeats a held submit of the page's top frame on page p
// (resubmit) without checking what it sends: callers repeating an approved
// item use ResubmitItem.
func ResubmitPage(p *extension.ExtensionPage, f FormSummary) error { return resubmit(p, f, 0, "") }

// resubmit repeats a held submit: the guard is armed and lets exactly one
// submit through in frameID, then one evaluation in the page checks that
// what would be sent still has the digest expect ("": no check) and clicks
// the submit control, or requestSubmit()s the form when there was none. A
// failure wrapping ErrNotSent reached nothing on the page; once the guard
// was opened, any failure closes it again.
func resubmit(p *extension.ExtensionPage, f FormSummary, frameID int, expect string) error {
	if f.Submitter == "" && f.Selector == "" {
		return fmt.Errorf("%w: the held submit recorded neither its submit control nor its form, so it cannot be repeated", ErrNotSent)
	}
	if frameID != 0 {
		return fmt.Errorf("%w: the form is in a frame of the page, where a held submit cannot be repeated", ErrNotSent)
	}
	if _, err := p.SubmitGuard(extension.SubmitGuardArm); err != nil {
		return fmt.Errorf("%w: arming the page's submit guard: %v", ErrNotSent, err)
	}
	if _, err := p.SubmitGuardAllowOnce(frameID, ""); err != nil {
		return fmt.Errorf("%w: letting the submit through: %v", ErrNotSent, err)
	}
	reclose := func() {
		_, _ = p.SubmitGuard(extension.SubmitGuardDisarm)
		_, _ = p.SubmitGuard(extension.SubmitGuardArm)
	}
	before, _ := p.GetURL()
	ctxID, err := newIsolatedWorld(p)
	if err != nil {
		reclose()
		return err
	}
	res, err := evalIn(p, ctxID, pageScript(triggerJS, f.Selector, f.Submitter, expect))
	if err != nil {
		reclose()
		return err
	}
	raw, _ := json.Marshal(res)
	var out struct {
		OK       bool   `json:"ok"`
		Click    bool   `json:"click"`
		X        int    `json:"x"`
		Y        int    `json:"y"`
		Changed  bool   `json:"changed"`
		Disabled bool   `json:"disabled"`
		Covered  bool   `json:"covered"`
		Missing  string `json:"missing"`
	}
	_ = json.Unmarshal(raw, &out)
	switch {
	case out.OK && !out.Click:
		return nil // the form was submitted by the page script
	case out.OK:
	case out.Changed:
		reclose()
		return &SubmitChangedError{What: "what the form would send"}
	case out.Missing != "":
		reclose()
		return fmt.Errorf("%w: %s is no longer on the page", ErrNotSent, out.Missing)
	case out.Disabled:
		reclose()
		return fmt.Errorf("%w: the submit control is disabled, so the page would ignore a click", ErrNotSent)
	case out.Covered:
		reclose()
		return fmt.Errorf("%w: another element covers the submit control", ErrNotSent)
	default:
		reclose()
		return fmt.Errorf("%w: the page did not answer the submit (%s)", ErrNotSent, raw)
	}
	// The click is a trusted mouse event, as a person's is: a page's own
	// handlers can ignore the synthetic click of a script. Whatever happens
	// from here, the watch the trigger left in the page is taken down.
	endWatch := func() { _, _ = evalIn(p, ctxID, collectJS) }
	mouse := func(typ string) error {
		ev := map[string]interface{}{"type": typ, "x": out.X, "y": out.Y, "button": "left", "clickCount": 1}
		if typ == "mouseMoved" {
			ev["button"], ev["clickCount"] = "none", 0
		}
		_, err := p.CDP("Input.dispatchMouseEvent", ev)
		return err
	}
	abort := func(err error) error {
		endWatch()
		reclose()
		return err
	}
	var done struct {
		Fired       bool `json:"fired"`
		Blocked     bool `json:"blocked"`
		Misdirected bool `json:"misdirected"`
		Changed     bool `json:"changed"`
		Gone        bool `json:"gone"`
	}
	readWatch := func(js string) error {
		fin, err := evalIn(p, ctxID, js)
		if err != nil {
			return err
		}
		raw, _ = json.Marshal(fin)
		done.Fired, done.Blocked, done.Misdirected, done.Changed, done.Gone = false, false, false, false, false
		if err := json.Unmarshal(raw, &done); err != nil {
			return err
		}
		if done.Gone {
			return errors.New("the click watch is gone: its page context was destroyed")
		}
		return nil
	}
	if err := mouse("mouseMoved"); err != nil {
		return abort(fmt.Errorf("%w: moving to the submit control: %v", ErrNotSent, err))
	}
	// The move can open a menu or shift the page: check once more, right
	// before the press.
	if v, err := evalIn(p, ctxID, recheckJS); err != nil {
		return abort(fmt.Errorf("%w: checking the submit control before the click: %v", ErrNotSent, err))
	} else {
		raw, _ = json.Marshal(v)
		var again struct {
			OK      bool `json:"ok"`
			Changed bool `json:"changed"`
			Covered bool `json:"covered"`
		}
		_ = json.Unmarshal(raw, &again)
		switch {
		case again.Changed:
			return abort(&SubmitChangedError{What: "what the form would send"})
		case again.Covered:
			return abort(fmt.Errorf("%w: another element now covers the submit control", ErrNotSent))
		case !again.OK:
			return abort(fmt.Errorf("%w: the page did not answer the pre-click check (%s)", ErrNotSent, raw))
		}
	}
	if err := mouse("mousePressed"); err != nil {
		_ = mouse("mouseReleased") // never leave the button held down
		// A press that failed on the way back may still have been
		// delivered: if the click reached the control, the approval is used.
		if peekErr := readWatch(peekJS); peekErr == nil && done.Fired {
			endWatch()
			reclose()
			return fmt.Errorf("the mouse press reported an error (%v), but the click reached the submit control; check the page before trying again", err)
		}
		return abort(fmt.Errorf("%w: clicking the submit control: %v", ErrNotSent, err))
	}
	if err := mouse("mouseReleased"); err != nil {
		if err = mouse("mouseReleased"); err != nil {
			endWatch()
			reclose()
			return fmt.Errorf("pressed the submit control but could not release the mouse button: %v", err)
		}
	}
	// Give the page a moment to answer; stop waiting at the first sign.
	var readErr error
	for waited := time.Duration(0); ; waited += reactionPoll {
		if readErr = readWatch(peekJS); readErr != nil || done.Changed || done.Blocked || done.Misdirected || waited >= reactionWait {
			break
		}
		time.Sleep(reactionPoll)
	}
	if readErr != nil {
		// A page that moved on is the usual sign of a submit that worked,
		// but only if the tab's address really changed: any other failure
		// to read is "unknown".
		if now, uerr := p.GetURL(); pageGone(readErr) && uerr == nil && now != before {
			return nil
		}
		reclose()
		return fmt.Errorf("clicked the submit control, but could not read how the page took it (%v); check the page before trying again", readErr)
	}
	endWatch()
	// Fired first: a stray click elsewhere that was cancelled does not
	// undo a click that reached the control.
	switch {
	case done.Blocked:
		reclose()
		return &SubmitChangedError{What: "what the form would send"}
	case !done.Fired && done.Misdirected:
		reclose()
		return fmt.Errorf("%w: the click landed on another element and was stopped", ErrNotSent)
	case !done.Fired:
		reclose()
		return fmt.Errorf("%w: the click did not reach the submit control", ErrNotSent)
	case !done.Changed:
		_, _ = p.SubmitGuard(extension.SubmitGuardArm) // drop an allowance the page did not use
		return &NoReactionError{}
	}
	return nil
}

// pageGone reports a read that failed because the page navigated or closed
// under it, which is what a submit that worked looks like.
func pageGone(err error) bool {
	m := strings.ToLower(err.Error())
	for _, w := range []string{"context", "navigat", "detached", "destroyed", "target closed", "no tab"} {
		if strings.Contains(m, w) {
			return true
		}
	}
	return false
}

// NoReactionError reports a replayed submit whose click reached the control
// but after which nothing on the page changed: it may not have gone through.
// The approval is used up either way.
type NoReactionError struct{}

func (*NoReactionError) Error() string {
	return "clicked the submit control, but the page did not react (nothing on the page changed); whether it went through is unknown, so check the page before trying again"
}

// reactionWait is how long a replayed click gets to change the page before
// it counts as unanswered, read every reactionPoll.
const (
	reactionWait = 1200 * time.Millisecond
	reactionPoll = 150 * time.Millisecond
)

// settle gives a page a moment to act on a click before AfterAct reads it.
const settle = 300 * time.Millisecond

// Settle waits briefly so a submit the action set off reaches the guard.
func Settle() { time.Sleep(settle) }
