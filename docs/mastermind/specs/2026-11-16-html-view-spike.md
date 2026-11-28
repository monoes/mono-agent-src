# HTML view spike: sandboxed model-written HTML in chat

Status: spike, default OFF (`localStorage['chat.htmlViews']='true'`). `ChatHosts`
mounts `HtmlViewContext`: `onSend` is confirmed with the user before sending and
`onAppLink` goes through the same app-link opener as block links.

## What it is

A fenced block with info string `html-view` renders model-written HTML in an
`<iframe sandbox="allow-scripts" srcdoc=...>` instead of a code block, only when
`localStorage['chat.htmlViews'] === 'true'` (any other value, or no storage,
is off; the flag has no UI, it is set by hand for the spike). Flag off: the
block renders as the ordinary code block.

Files (`wails-app/frontend/src/components/chat/`):

- `htmlViewContract.js` pure helpers: CSP, bridge script, `buildSrcdoc`,
  `validateFrameMessage`, `createHostGate`, flag read.
- `SandboxedHtmlFrame.jsx` the component.
- `htmlView.jsx` `htmlViewForPre` (the one hook `ChatMarkdown.jsx`'s `MdPre`
  calls; 3 lines changed there) and `HtmlViewContext` (`onSend`, `onAppLink`).
- `htmlView.render.test.jsx` 17 tests.

## Design

- sandbox is exactly `allow-scripts`: no same-origin, top-navigation, forms,
  popups, modals. Opaque origin, so no storage, cookies or DOM access to the host.
- srcdoc begins with a CSP `<meta>`, before the bridge and before model markup:
  `default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline';
  img-src data:; connect-src 'none'; frame-src 'none'; form-action 'none';
  base-uri 'none'; object-src 'none'`.
- Bridge (inside the frame) exposes frozen `host.resize(h)`, `host.send(text)`,
  `host.open(monoagent://kind/id)`, plus a trusted-click (`isTrusted`) signal.
- Host: accepts a `message` only if `event.source === iframe.contentWindow`;
  strict shape (`v:1`, exact key set, plain object); caps (send 4000 chars,
  height 40..600, HTML 64 KB else source-only fallback); app-link regex
  `^monoagent://[a-z][a-z0-9-]{0,31}/[A-Za-z0-9._:-]{1,128}$`; 30 messages/s
  rate limit; send/open need a fresh (2 s), one-shot gesture.
- Collapsed by default (shows source; button "Show interactive view");
  always-visible badge "sandboxed, no network".

### Known limit of the gesture signal

The bridge lives inside the frame, so hostile script can post a fake
`gesture`. The frame cannot be trusted to attest to its own clicks. Mitigation
implemented: a frame `gesture` only counts if the HOST window lost focus
(`blur`) while the iframe was the active element, within the last 2 s, and each
blur backs one message. That stops background/automatic sends, but after a
person clicks or tabs into the frame, hostile script can still fire one
send/open without the click having meant it. `onSend` therefore never sends on
the frame's say-so: the host shows the text and sends only on a click on its own
Send button. `onAppLink` has no such confirmation.

## Verified

jsdom (vitest), run here: `npx vitest run src/components/chat/htmlView.render.test.jsx`
17/17 pass; whole `src/components/chat` suite 41 files / 810 tests pass.
This proves only our own attribute/validation/gate logic: iframe `sandbox`
attribute value, CSP meta position in the srcdoc string, message validation,
gesture/rate/source gating, flag-off fallback. jsdom does not enforce sandbox,
CSP or origin isolation, so it says nothing about browser behaviour.

Real WKWebView (macOS 27.2, arm64, CommandLineTools Swift), small harness, not
the Wails app. Harness: a `WKWebView` loading a page from a custom
`wails://wails/` scheme handler (same scheme/host Wails uses), with a
`WKScriptMessageHandler` named `h` registered on the `WKUserContentController`
(like Wails' `external`), `window.go` and `window.runtime` set on the host page
as stand-ins, and the iframe built with the real `buildSrcdoc()` containing a
probe script. Harness source lives in the session scratchpad only (not
committed). Results (one run; the `h` handler call was a second run):

| Probe inside frame | Result |
| --- | --- |
| `location.origin` | `"null"` (opaque) |
| `window.host` | object (bridge present) |
| `window.go`, `window.runtime` | `undefined` |
| `parent.go`, `parent.runtime`, `top.document` | throw `SecurityError` |
| `localStorage` | throws `SecurityError`; `document.cookie` is `""` |
| `top.location = 'https://...'` | throws `SecurityError` (no navigation; the nav delegate saw only `wails://wails/` and `about:srcdoc`) |
| `window.open(...)` | returns `null` |
| `fetch('https://example.com/')` | blocked, CSP `connect-src` violation event |
| remote `Image` | blocked, CSP `img-src` violation event |
| `data:` `Image` | loads |
| `parent.postMessage` from frame | arrives with `event.origin === "null"` and `event.source === iframe.contentWindow` (true) |
| `host.resize(123)` through the bridge | arrives as `{v:1,t:'resize',h:123}` |
| `window.webkit.messageHandlers.h.postMessage(...)` from frame | **reaches the native handler** (see below) |

So on WKWebView: sandboxed srcdoc iframe, opaque-origin postMessage with
`source` identity check, and a CSP `<meta>` placed first all behave as designed.

### Important finding: native message handlers are reachable from the frame

`window.webkit.messageHandlers.<name>` exists inside the sandboxed frame, and a
handler registered the normal way is callable from it. Wails 2.17.1 (the
version in `wails-app/go.mod`) registers `external` this way
(`WailsContext.m:298`) and it carries Go binding calls. By reading Wails source
(NOT by running Wails): `didReceiveScriptMessage` forwards `frameInfo.isMainFrame`
and `startBindingsMessageProcessor` (`frontend.go:174`) drops any message with
`isMainFrame == false` ("Blocked request from not main frame"), and checks the
origin allow-list for main-frame messages. That would stop bound-method calls
from our frame. Two caveats from the same file: the `"drag"` message is handled
in ObjC before that check (it can only start a window drag if a mouse event is
pending), and every blocked message logs an error, so a hostile frame can spam
the log. The whole defence rests on this Wails check; it is not our code.
Verify against the real app before enabling the flag for anyone (see below).

## UNVERIFIED (not run, do not assume)

- The real Wails app. Not built or launched; Wails bindings
  (`window.go`, `window.runtime`) reachability was only checked against
  stand-in globals in the harness, and the "non-main-frame binding calls are
  dropped" claim is from reading Wails 2.17.1 source. Test to run: in the Wails
  dev build, with the flag on, render an `html-view` that calls
  `window.webkit.messageHandlers.external.postMessage('C...')` for a real bound
  method and confirm the Go method does not run and the log shows "Blocked
  request from not main frame".
- Wails' injected scripts run in all frames (`forMainFrameOnly:false`,
  `WailsContext.m:311`); the one inspected references `window.wails`, which
  would throw a TypeError in the frame. Not observed.
- Windows WebView2 (and Linux WebKitGTK): not tested at all, this is a Mac. Open
  questions: sandbox and CSP-meta enforcement in `srcdoc` iframes (expected to
  work, it is Chromium), postMessage `source`/opaque-origin behaviour, and, most
  important, whether Wails' WebView2 bridge (`window.chrome.webview.postMessage`)
  is reachable from a sandboxed child frame and how Wails filters it
  (`frontend/desktop/windows`). Not read, not run.
- macOS versions other than 27.2; behaviour of older WKWebView (CSP meta in
  srcdoc, sandbox) is unknown.
- CSP `form-action`, `frame-src`, `base-uri`, `object-src` effects: present in the
  policy, not probed in WKWebView.
- Real user clicks: no real mouse click was driven in WKWebView, so the
  `isTrusted` click handler, blur gating, and `host.send/open` through
  a real gesture are untested outside jsdom. Auto-resize with real content
  height was not measured (the frame only sends a height when its script calls
  `host.resize`; there is no ResizeObserver in the bridge).
- Release-build behaviours (devtools off), accessibility (screen reader on the
  iframe, keyboard), and dark/light theming of the frame.

## Open questions

1. (Resolved: `ChatHosts` mounts it; send is user-confirmed, app links use the shared opener.)
2. (Resolved: the gesture gate now uses host window blur. Known limit: repeated clicks in an already-focused frame do not send again until focus leaves and returns.)
3. Should the model be told the `html-view` convention (prompt change)? Not in
   this spike.
4. Add a ResizeObserver to the bridge, or keep manual `host.resize`?
