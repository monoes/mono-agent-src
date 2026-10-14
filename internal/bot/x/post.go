//go:build !nosocial

package x

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	botpkg "github.com/monoes/mono-agent/internal/bot"
	"github.com/monoes/mono-agent/internal/browser"
)

// findPostJS marks the article of post id: the article whose OWN timestamp
// link points at /status/<id> (a quote card's timestamp, inside a
// div[role=link], does not count). Articles that merely quote or link the
// post do not match, and nothing else on the page is ever substituted.
const findPostJS = `(id, attr, tok) => {
	const re = new RegExp('/status/' + id + '(?:$|[/?#])');
	const own = (a) => [...a.querySelectorAll('a[href]')].some((l) => re.test(l.getAttribute('href') || '') && l.querySelector('time') &&
		l.closest('article') === a && !l.closest("div[role='link']"));
	const matches = [...document.querySelectorAll('article')].filter(own);
	if (!matches.length) {
		return { found: false, unavailable: !!document.querySelector("[data-testid='primaryColumn'] [data-testid='emptyState'], [data-testid='error-detail']") };
	}
	const a = matches.find((x) => x.getAttribute('data-testid') === 'tweet') || matches[0];
	a.setAttribute(attr, tok);
	return { found: true, unavailable: false };
}`

type findPostResult struct {
	Found       bool `json:"found"`
	Unavailable bool `json:"unavailable"`
}

// openPost navigates to a post and marks its article; it returns the
// normalised URL and the article's scope selector.
func (b *XBot) openPost(ctx context.Context, p browser.PageInterface, target string) (string, string, error) {
	u, id, err := b.postURL(target)
	if err != nil {
		return "", "", err
	}
	if err := navigate(p, u); err != nil {
		return "", "", err
	}
	tok := token()
	var r findPostResult
	err = poll(ctx, loadTimeout, func() (bool, error) {
		if err := botpkg.EvalJSON(p, findPostJS, &r, id, markAttr, tok); err != nil {
			return false, nil
		}
		return r.Found || r.Unavailable, nil
	})
	if err == nil && !r.Found {
		err = errors.New("the post is unavailable (deleted, protected or not found)")
	}
	if err != nil {
		if lerr := checkLoginRedirect(p); lerr != nil {
			return "", "", lerr
		}
		return "", "", fmt.Errorf("x: post %s not found on its page: %w", u, err)
	}
	return u, markedSelector(tok), nil
}

// ---------------------------------------------------------------------------
// like_post
// ---------------------------------------------------------------------------

// likeStateJS inspects the post's own like control. data-testid="unlike"
// means the viewer already liked it; that control is never clicked.
const likeStateJS = `(scope, attr, tok) => {
	const a = document.querySelector(scope);
	if (!a) return { state: 'gone' };
	if (a.querySelector("[data-testid='unlike']")) return { state: 'liked' };
	const btn = a.querySelector("[data-testid='like']");
	if (!btn) return { state: 'missing' };
	btn.setAttribute(attr, tok);
	return { state: 'marked' };
}`

// likedJS reports whether the post now shows as liked.
const likedJS = `(scope, pick) => {
	const a = document.querySelector(scope);
	if (!a) return false;
	if (a.querySelector("[data-testid='unlike']")) return true;
	const e = pick ? document.querySelector(pick) : null;
	if (!e) return false;
	const t = e.closest('[data-testid]');
	return e.getAttribute('aria-pressed') === 'true' || (t && t.getAttribute('data-testid') === 'unlike');
}`

type stateResult struct {
	State string `json:"state"`
}

var alreadyLikedRe = regexp.MustCompile(`(?i)\bunlike\b|\bliked\b`)

// alreadyLiked reports whether a like control's state says "already liked".
func alreadyLiked(info pickInfo) bool {
	return info.TestID == "unlike" || info.Pressed == "true" || alreadyLikedRe.MatchString(info.Label)
}

// LikePost likes the post at postURL. A post the viewer already liked is
// left alone (reported as already_liked); the Unlike control is never
// clicked. Only the post's own article is searched; when its Like button is
// not found by data-testid, Jev may pick it inside that article. The like
// counts only when the post then shows as liked.
func (b *XBot) LikePost(ctx context.Context, p browser.PageInterface, postURL string) (map[string]interface{}, error) {
	u, scope, err := b.openPost(ctx, p, postURL)
	if err != nil {
		return nil, err
	}
	defer clearMarks(p)
	result := map[string]interface{}{"url": u, "liked": true, "already_liked": false}

	btnTok := token()
	var st stateResult
	// The action bar can render a beat after the article.
	_ = poll(ctx, 3*time.Second, func() (bool, error) {
		if err := botpkg.EvalJSON(p, likeStateJS, &st, scope, markAttr, btnTok); err != nil {
			return false, nil
		}
		return st.State != "missing", nil
	})
	var btn browser.ElementHandle
	pickSel := ""
	switch st.State {
	case "liked":
		result["already_liked"] = true
		return result, nil
	case "marked":
		if btn, err = markedElement(p, btnTok); err != nil {
			return nil, fmt.Errorf("x: like button of %s: %w", u, err)
		}
	default:
		pick, info, jerr := b.jevPick(ctx, p, "click",
			"the Like (heart) button in the action bar of the post "+u+" — the post's own Like button, not one on another post or reply",
			"X gives it data-testid=\"like\"; a control labelled \"Unlike\" or \"Liked\" means the post is already liked.", scope)
		if jerr != nil {
			return nil, fmt.Errorf("x: could not find the Like button of %s (jev fallback: %v)", u, jerr)
		}
		defer pick.Release()
		if alreadyLiked(info) {
			result["already_liked"] = true
			return result, nil
		}
		btn, pickSel = pick.Element, pick.Selector
	}
	if err := botpkg.ClickTrusted(p, btn); err != nil {
		return nil, fmt.Errorf("x: click Like on %s: %w", u, err)
	}
	var liked bool
	err = poll(ctx, verifyTimeout, func() (bool, error) {
		if err := botpkg.EvalJSON(p, likedJS, &liked, scope, pickSel); err != nil {
			return false, nil
		}
		return liked, nil
	})
	if err != nil {
		return nil, fmt.Errorf("x: like on %s was not confirmed (the post does not show as liked): %w", u, err)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// reply_post / publish_post: the tweet composer
// ---------------------------------------------------------------------------

// composerTextJS returns the collapsed text of the composer marked by sel.
const composerTextJS = `(sel) => {
	const e = document.querySelector(sel);
	if (!e) return { present: false, text: '' };
	const v = e.value !== undefined && e.tagName !== 'DIV' ? e.value : (e.innerText || e.textContent || '');
	return { present: true, text: v.replace(/\s+/g, ' ').trim() };
}`

type composerText struct {
	Present bool   `json:"present"`
	Text    string `json:"text"`
}

// postedJS checks whether text left every tweet composer, and reads a toast.
const postedJS = `(snippet) => {
	const norm = (s) => (s || '').replace(/\s+/g, ' ').trim();
	const boxes = [...document.querySelectorAll("[data-testid^='tweetTextarea_']")];
	const still = boxes.some((b) => norm(b.innerText || b.textContent || b.value).includes(snippet));
	const toast = document.querySelector("[data-testid='toast']");
	const link = toast ? toast.querySelector("a[href*='/status/']") : null;
	return {
		still,
		toast: toast ? norm(toast.innerText || toast.textContent) : '',
		link: link ? new URL(link.getAttribute('href'), location.origin).href : '',
	};
}`

type postedState struct {
	Still bool   `json:"still"`
	Toast string `json:"toast"`
	Link  string `json:"link"`
}

// buttonEnabledJS reports whether the marked submit button is enabled.
const buttonEnabledJS = `(sel) => {
	const e = document.querySelector(sel);
	if (!e) return false;
	return !e.disabled && e.getAttribute('aria-disabled') !== 'true';
}`

// verifySnippet is the whitespace-collapsed start of text used to find it
// in a composer.
func verifySnippet(text string) string {
	s := strings.Join(strings.Fields(text), " ")
	r := []rune(s)
	if len(r) > 40 {
		r = r[:40]
	}
	return string(r)
}

// typeAndCheck types text into the composer box and confirms it arrived.
func typeAndCheck(ctx context.Context, p browser.PageInterface, box browser.ElementHandle, boxSel, text string) error {
	if err := botpkg.TypeInto(ctx, p, box, text); err != nil {
		return err
	}
	snippet := verifySnippet(text)
	var ct composerText
	err := poll(ctx, 3*time.Second, func() (bool, error) {
		if err := botpkg.EvalJSON(p, composerTextJS, &ct, boxSel); err != nil {
			return false, nil
		}
		return strings.Contains(ct.Text, snippet), nil
	})
	if err != nil {
		return fmt.Errorf("typed text did not appear in the composer (it shows %q)", truncateForError(ct.Text, 60))
	}
	return nil
}

// submitAndVerify clicks the marked submit button once it is enabled, then
// waits until text left every composer. A toast that shows while the text
// is still in the composer (e.g. "You already said that") is the failure
// reason. It returns the new post's URL when the toast links it.
func submitAndVerify(ctx context.Context, p browser.PageInterface, btn browser.ElementHandle, btnSel, text string) (string, error) {
	var enabled bool
	if err := poll(ctx, verifyTimeout, func() (bool, error) {
		if err := botpkg.EvalJSON(p, buttonEnabledJS, &enabled, btnSel); err != nil {
			return false, nil
		}
		return enabled, nil
	}); err != nil {
		return "", errors.New("the submit button stayed disabled")
	}
	if err := botpkg.ClickTrusted(p, btn); err != nil {
		return "", fmt.Errorf("click submit: %w", err)
	}
	snippet := verifySnippet(text)
	var st postedState
	err := poll(ctx, verifyTimeout, func() (bool, error) {
		if err := botpkg.EvalJSON(p, postedJS, &st, snippet); err != nil {
			return false, nil
		}
		return !st.Still, nil
	})
	if err != nil {
		if st.Toast != "" {
			return "", fmt.Errorf("not posted: X said %q", truncateForError(st.Toast, 120))
		}
		return "", errors.New("not confirmed: the text is still in the composer")
	}
	// The success toast can trail the composer closing by a moment.
	if st.Link == "" {
		_ = poll(ctx, 2*time.Second, func() (bool, error) {
			if err := botpkg.EvalJSON(p, postedJS, &st, snippet); err != nil {
				return false, nil
			}
			return st.Link != "", nil
		})
	}
	return st.Link, nil
}

// markInJS marks the first element matching sel inside scope (document when
// scope is empty) with tok; false when there is none.
const markInJS = `(scope, sel, attr, tok) => {
	const root = scope ? document.querySelector(scope) : document;
	const e = root ? root.querySelector(sel) : null;
	if (!e) return false;
	e.setAttribute(attr, tok);
	return true;
}`

// findMarked polls for sel inside scope and returns the element.
func findMarked(ctx context.Context, p browser.PageInterface, scope, sel string, timeout time.Duration) (browser.ElementHandle, string, error) {
	tok := token()
	var ok bool
	err := poll(ctx, timeout, func() (bool, error) {
		if err := botpkg.EvalJSON(p, markInJS, &ok, scope, sel, markAttr, tok); err != nil {
			return false, nil
		}
		return ok, nil
	})
	if err != nil {
		return nil, "", err
	}
	el, err := markedElement(p, tok)
	if err != nil {
		return nil, "", err
	}
	return el, markedSelector(tok), nil
}

// ReplyPost replies text to the post at postURL through the post's own
// Reply button and the reply dialog, and confirms the reply left the
// composer.
func (b *XBot) ReplyPost(ctx context.Context, p browser.PageInterface, postURL, text string) (map[string]interface{}, error) {
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("x: reply text is required")
	}
	u, scope, err := b.openPost(ctx, p, postURL)
	if err != nil {
		return nil, err
	}
	defer clearMarks(p)

	replyBtn, _, err := findMarked(ctx, p, scope, "[data-testid='reply']", 3*time.Second)
	if err != nil {
		pick, _, jerr := b.jevPick(ctx, p, "click",
			"the Reply (speech bubble) button in the action bar of the post "+u+" — the post's own Reply button, not one on another post",
			"", scope)
		if jerr != nil {
			return nil, fmt.Errorf("x: could not find the Reply button of %s (jev fallback: %v)", u, jerr)
		}
		defer pick.Release()
		replyBtn = pick.Element
	}
	if err := botpkg.ClickTrusted(p, replyBtn); err != nil {
		return nil, fmt.Errorf("x: click Reply on %s: %w", u, err)
	}

	const dialog = "[role='dialog']"
	box, boxSel, err := findMarked(ctx, p, dialog, "[data-testid='tweetTextarea_0']", loadTimeout/2)
	if err != nil {
		pick, _, jerr := b.jevPick(ctx, p, "fill", "the reply text box in the open reply dialog", "", dialog)
		if jerr != nil {
			return nil, fmt.Errorf("x: reply dialog for %s did not open (jev fallback: %v)", u, jerr)
		}
		defer pick.Release()
		box, boxSel = pick.Element, pick.Selector
	}
	if err := sleep(ctx, actionPause); err != nil {
		return nil, err
	}
	if err := typeAndCheck(ctx, p, box, boxSel, text); err != nil {
		return nil, fmt.Errorf("x: reply to %s: %w", u, err)
	}

	btn, btnSel, err := findMarked(ctx, p, dialog, "[data-testid='tweetButton']", 3*time.Second)
	if err != nil {
		pick, _, jerr := b.jevPick(ctx, p, "click", "the Reply button that sends the reply typed in the open reply dialog", "", dialog)
		if jerr != nil {
			return nil, fmt.Errorf("x: could not find the reply dialog's Reply button (jev fallback: %v)", jerr)
		}
		defer pick.Release()
		btn, btnSel = pick.Element, pick.Selector
	}
	link, err := submitAndVerify(ctx, p, btn, btnSel, text)
	if err != nil {
		return nil, fmt.Errorf("x: reply to %s %w", u, err)
	}
	res := map[string]interface{}{"url": u, "replied": true, "reply_text": text}
	if link != "" {
		res["reply_url"] = link
	}
	return res, nil
}
