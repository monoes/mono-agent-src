//go:build !nosocial

package x

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	botpkg "github.com/monoes/mono-agent/internal/bot"
	"github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/fsconfine"
)

// composerJS finds a tweet composer — the /compose/post dialog first, else
// (unless want is "dialog") the inline one on /home — and marks its text
// box, its Post button
// (tweetButton in the dialog, tweetButtonInline inline) and its file input.
const composerJS = `(attr, tok, want) => {
	let mode = '', scope = null, box = null;
	const dlg = [...document.querySelectorAll("[role='dialog']")].find((d) => d.querySelector("[data-testid='tweetTextarea_0']"));
	if (dlg) { mode = 'dialog'; scope = dlg; box = dlg.querySelector("[data-testid='tweetTextarea_0']"); }
	else if (want !== 'dialog') {
		box = document.querySelector("[data-testid='primaryColumn'] [data-testid='tweetTextarea_0']");
		if (box) {
			mode = 'inline';
			scope = box;
			while (scope && !scope.querySelector("[data-testid='tweetButtonInline']")) scope = scope.parentElement;
		}
	}
	if (!mode || !scope) return { mode: '' };
	box.setAttribute(attr, tok + '-box');
	scope.setAttribute(attr, tok + '-scope');
	const btn = scope.querySelector(mode === 'dialog' ? "[data-testid='tweetButton']" : "[data-testid='tweetButtonInline']");
	if (btn) btn.setAttribute(attr, tok + '-btn');
	const file = scope.querySelector("input[data-testid='fileInput'], input[type='file']");
	if (file) file.setAttribute(attr, tok + '-file');
	return { mode, btn: !!btn, file: !!file };
}`

type composerInfo struct {
	Mode string `json:"mode"`
	Btn  bool   `json:"btn"`
	File bool   `json:"file"`
}

// attachmentsJS counts the media attachments shown in the composer scope.
const attachmentsJS = `(scope) => {
	const s = document.querySelector(scope);
	if (!s) return 0;
	const a = s.querySelector("[data-testid='attachments']");
	return a ? Math.max(1, a.querySelectorAll('img, video').length) : 0;
}`

// openComposer opens a composer at u and marks its parts; want "dialog"
// waits for the composer dialog only (on /compose/post the inline composer
// of the page behind it renders first).
func openComposer(ctx context.Context, p browser.PageInterface, u, want, tok string, timeout time.Duration) (composerInfo, error) {
	var ci composerInfo
	if err := navigate(p, u); err != nil {
		return ci, err
	}
	err := poll(ctx, timeout, func() (bool, error) {
		if err := botpkg.EvalJSON(p, composerJS, &ci, markAttr, tok, want); err != nil {
			return false, nil
		}
		return ci.Mode != "", nil
	})
	return ci, err
}

// PublishPost publishes a post with text (and optional media files, which
// must pass the run's file confinement) through X's composer — the
// /compose/post dialog, else the inline composer on /home — and confirms the
// text left the composer.
func (b *XBot) PublishPost(ctx context.Context, p browser.PageInterface, text string, media []string) (map[string]interface{}, error) {
	if strings.TrimSpace(text) == "" {
		// Text is also how the send is verified (it must leave the composer).
		return nil, errors.New("x: post text is required")
	}
	// Uploaded files leave the machine: check them before touching the page.
	files, err := fsconfine.Paths(ctx, media)
	if err != nil {
		return nil, fmt.Errorf("x: media: %w", err)
	}
	defer clearMarks(p)

	tok := token()
	ci, err := openComposer(ctx, p, "https://x.com/compose/post", "dialog", tok, loadTimeout/2)
	if errors.Is(err, errNotLoggedIn) {
		return nil, err
	}
	if err != nil || ci.Mode == "" {
		ci, err = openComposer(ctx, p, "https://x.com/home", "", tok, loadTimeout)
		if err != nil {
			return nil, fmt.Errorf("x: no post composer on /compose/post or /home: %w", err)
		}
	}
	scope := markedSelector(tok + "-scope")

	if len(files) > 0 {
		if !ci.File {
			return nil, errors.New("x: the composer has no media file input")
		}
		in, err := markedElement(p, tok+"-file")
		if err != nil {
			return nil, fmt.Errorf("x: media input: %w", err)
		}
		if err := in.SetFiles(files); err != nil {
			return nil, fmt.Errorf("x: attach media: %w", err)
		}
		var n int
		if err := poll(ctx, 30*time.Second, func() (bool, error) {
			if err := botpkg.EvalJSON(p, attachmentsJS, &n, scope); err != nil {
				return false, nil
			}
			return n > 0, nil
		}); err != nil {
			return nil, errors.New("x: attached media never showed in the composer")
		}
	}

	boxSel := markedSelector(tok + "-box")
	box, err := markedElement(p, tok+"-box")
	if err != nil {
		return nil, fmt.Errorf("x: composer text box: %w", err)
	}
	if err := typeAndCheck(ctx, p, box, boxSel, text); err != nil {
		return nil, fmt.Errorf("x: publish: %w", err)
	}

	var btn browser.ElementHandle
	btnSel := markedSelector(tok + "-btn")
	if ci.Btn {
		if btn, err = markedElement(p, tok+"-btn"); err != nil {
			return nil, fmt.Errorf("x: Post button: %w", err)
		}
	} else {
		pick, _, jerr := b.jevPick(ctx, p, "click", "the Post button that publishes the post being composed", "", scope)
		if jerr != nil {
			return nil, fmt.Errorf("x: could not find the composer's Post button (jev fallback: %v)", jerr)
		}
		defer pick.Release()
		btn, btnSel = pick.Element, pick.Selector
	}
	link, err := submitAndVerify(ctx, p, btn, btnSel, text)
	if err != nil {
		return nil, fmt.Errorf("x: publish %w", err)
	}
	res := map[string]interface{}{"posted": true, "post_text": text, "composer": ci.Mode}
	if link != "" {
		res["post_url"] = link
	}
	return res, nil
}
