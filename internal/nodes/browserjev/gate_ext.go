package browserjev

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/vault"
)

// guardPage is what the gate needs of the run's tab; *extension.ExtensionPage
// is one.
type guardPage interface {
	SubmitGuard(action string) (*extension.SubmitGuardState, error)
	Screenshot(o extension.ScreenshotOptions) (*extension.Screenshot, error)
	TabID() int
}

var (
	_ guardPage                             = (*extension.ExtensionPage)(nil)
	_ interface{ GetURL() (string, error) } = (*extension.ExtensionPage)(nil) // TabURL
)

// resubmitPage repeats a held submit on a local or dev page, which needs no
// approval (browserctl.ResubmitPage); tests replace it.
var resubmitPage = func(p guardPage, f browserctl.FormSummary) error {
	ep, ok := p.(*extension.ExtensionPage)
	if !ok {
		return fmt.Errorf("%w: a %T page can't repeat a submit", browserctl.ErrNotSent, p)
	}
	return browserctl.ResubmitPage(ep, f)
}

// resubmitItem repeats an approved submit once the page still shows what was
// approved: its origin, path and form (browserctl.ResubmitItem, as `browser
// submit --hil` does); tests replace it.
var resubmitItem = func(p guardPage, it browserctl.SubmitItem) error {
	ep, ok := p.(*extension.ExtensionPage)
	if !ok {
		return fmt.Errorf("%w: a %T page can't repeat a submit", browserctl.ErrNotSent, p)
	}
	return browserctl.ResubmitItem(ep, it)
}

// TabURL is the tab's URL from the browser (page_info: chrome.tabs), not the
// page's own location.
func (d extDriver) TabURL() (string, error) {
	u, ok := d.page.(interface{ GetURL() (string, error) })
	if !ok {
		return "", errors.New("the page can't say its address (it needs the extension bridge)")
	}
	return u.GetURL()
}

// ArmSubmitGuard arms the extension's submit guard (C1 submit_guard) on the
// run's tab.
func (d extDriver) ArmSubmitGuard(ctx context.Context) (submitGuard, error) {
	if d.page == nil {
		return nil, errors.New("the page has no submit guard (it needs the extension bridge)")
	}
	return armGuard(ctx, d.page)
}

// extGuard is the submit guard of an extension tab. Its approvals are the
// browser.submit items `monoagentcli browser` makes (internal/browserctl),
// so `hil list`, the HIL page and `browser submit --hil` all see them.
type extGuard struct {
	page    guardPage
	db      *sql.DB
	profile string
	seen    int64  // the newest guard record handled
	origin  string // the origin it was armed on, as the extension reports it
}

func armGuard(ctx context.Context, p guardPage) (*extGuard, error) {
	db := vault.DBFromContext(ctx)
	if db == nil {
		return nil, errors.New("there is no database to hold its approvals")
	}
	profile, err := runProfile(ctx) // before arming: no guard nobody can approve through
	if err != nil {
		return nil, err
	}
	st, err := p.SubmitGuard(extension.SubmitGuardArm)
	if err != nil {
		return nil, err
	}
	g := &extGuard{page: p, db: db, profile: profile, origin: browserctl.Origin(st.Origin)}
	for _, b := range st.Blocked {
		g.seen = max(g.seen, b.Seq)
	}
	return g, nil
}

func (g *extGuard) Blocked(ctx context.Context) ([]blockedSubmit, error) {
	st, err := g.page.SubmitGuard(extension.SubmitGuardStatus)
	if err != nil {
		return nil, fmt.Errorf("read the submit guard: %w", err)
	}
	if !st.Armed { // the extension restarted, the page left its origin, or the tab sat idle: guard it again
		re, err := g.page.SubmitGuard(extension.SubmitGuardArm)
		if err != nil {
			return nil, fmt.Errorf("re-arm the submit guard: %w", err)
		}
		g.origin = browserctl.Origin(re.Origin)
	}
	var newest int64
	for _, b := range st.Blocked {
		newest = max(newest, b.Seq)
	}
	if newest < g.seen {
		g.seen = 0 // its records began again
	}
	var out []blockedSubmit
	for _, b := range st.Blocked {
		if b.Seq > g.seen {
			out = append(out, blockedSubmit{Seq: int(b.Seq), URL: b.URL, FrameID: b.FrameID, Form: formSummary(b.Form)})
		}
	}
	g.seen = max(g.seen, newest)
	return out, nil
}

func (g *extGuard) Follow(ctx context.Context, pageURL string) error {
	if pageURL == "" || pageURL == "about:blank" {
		return nil
	}
	origin := browserctl.Origin(pageURL)
	if origin == g.origin {
		return nil
	}
	if _, err := g.page.SubmitGuard(extension.SubmitGuardArm); err != nil {
		return fmt.Errorf("re-arm the submit guard on %s: %w", origin, err)
	}
	g.origin = origin
	return nil
}

func formSummary(f extension.SubmitForm) browserctl.FormSummary {
	out := browserctl.FormSummary{Action: f.Action, Method: f.Method, Submitter: f.Submitter, Selector: f.Selector,
		Fields: make([]browserctl.FormField, 0, len(f.Fields))}
	for _, x := range f.Fields {
		out.Fields = append(out.Fields, browserctl.FormField{Name: x.Name, Type: x.Type, Label: x.Label, Value: x.Value})
	}
	return out
}

func (g *extGuard) Ask(ctx context.Context, b blockedSubmit, by hilsend.PreparedBy) (string, error) {
	// The item is bound to the form as the page shows it now, so an approval
	// sends only that (and `browser submit --hil` can use it after a timeout).
	ep, _ := g.page.(*extension.ExtensionPage)
	it, err := browserctl.CreateSubmitItem(ctx, g.db, browserctl.SubmitRequest{RequestedBy: &by, Page: ep,
		Profile: g.profile, URL: b.URL, Form: b.Form, Screenshot: g.cardScreenshot(), Via: "guard", TabID: g.TabID(), Seq: b.Seq,
		FrameID: b.FrameID,
	})
	return it.ID, err
}

// Decision reads the item. Only the user's own approval counts (`hil
// approve` or the HIL page, which refuse an agent): one set another way is
// approved_elsewhere.
func (g *extGuard) Decision(ctx context.Context, hilID string) (string, error) {
	it, err := browserctl.GetSubmitItem(ctx, g.db, g.profile, hilID)
	if it.Status == "approved" && it.ApprovedBy != browserctl.ApprovedByOperator {
		return "approved_elsewhere", err
	}
	return it.Status, err
}

func (g *extGuard) Submit(ctx context.Context, b blockedSubmit, hilID string) error {
	var err error
	switch {
	case hilID == "" && b.FrameID != 0:
		// ResubmitPage repeats in the top frame only.
		return fmt.Errorf("%w: the form is in a frame of the page, where a held submit can't be repeated", browserctl.ErrNotSent)
	case hilID == "":
		err = resubmitPage(g.page, b.Form)
	default:
		it, cerr := browserctl.ClaimSubmitItem(ctx, g.db, g.profile, hilID)
		if cerr != nil {
			var used *browserctl.SubmitStateError
			if errors.As(cerr, &used) && used.Status == "consumed" {
				return nil // `browser submit --hil` sent it first
			}
			return cerr
		}
		err = resubmitItem(g.page, it)
	}
	var idle *browserctl.NoReactionError
	if errors.As(err, &idle) {
		err = nil // the click went out; the loop's "three actions in a row changed nothing" stop (node.go) watches the page
	}
	if err != nil {
		_, _ = g.page.SubmitGuard(extension.SubmitGuardArm) // drop an unused allow_once
		err = fmt.Errorf("repeat the held submit: %w", err)
	}
	switch {
	case hilID == "":
	case errors.Is(err, browserctl.ErrNotSent):
		_ = browserctl.ReleaseSubmitItem(ctx, g.db, g.profile, hilID) // nothing reached the page
	default:
		_ = browserctl.FinishSubmitItem(ctx, g.db, g.profile, hilID, err)
	}
	return err
}

// TabID is the guarded tab, for the item and the run's output.
func (g *extGuard) TabID() int { return g.page.TabID() }

// cardScreenshot saves the tab's viewport for the approval card under
// ~/.monoagent/browser/hil/, where `monoagentcli browser` keeps its own;
// "" when it can't.
func (g *extGuard) cardScreenshot() string {
	shot, err := g.page.Screenshot(extension.ScreenshotOptions{})
	if err != nil {
		return ""
	}
	data, err := shot.Bytes()
	home, herr := os.UserHomeDir()
	if err != nil || herr != nil || len(data) == 0 {
		return ""
	}
	dir := filepath.Join(home, ".monoagent", "browser", "hil")
	if os.MkdirAll(dir, 0o700) != nil {
		return ""
	}
	ext := ".png"
	if shot.MIME == "image/jpeg" {
		ext = ".jpg"
	}
	path := filepath.Join(dir, "jev-"+uuid.NewString()+ext)
	if os.WriteFile(path, data, 0o600) != nil {
		return ""
	}
	return path
}
