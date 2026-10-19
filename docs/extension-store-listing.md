# Extension store submission kit

Everything to paste into the Edge Add-ons (Partner Center) and Chrome Web Store
dashboards for **MonoAgent Bridge**. Build the upload with
`scripts/package-extension-store.sh` (manifest at the zip root, no tests).

Before each submission: bump `version` in `chrome-extension/manifest.json`
(the stores reject a repeated version), and host the privacy policy below at a
public URL on monoes.me (the stores require a URL, not text).

## Listing

**Name:** MonoAgent Bridge

**Short description (132 chars max):**
Connects your own browser to the MonoAgent app on your computer, to save pages, highlights and tasks, and run your automations.

**Category:** Productivity

**Detailed description:**

> MonoAgent Bridge is the browser half of MonoAgent, a workflow-automation app
> that runs on your own computer. It needs MonoAgent installed and running; on
> its own it does nothing.
>
> Once paired with your MonoAgent app (one token, pasted once), the extension
> lets MonoAgent work in the browser you are already signed in to:
>
> - Save the page, your selection, or a highlighted passage to your local MonoAgent library (Ctrl+Shift+S).
> - Add the selected text or page as a task (right-click, or Ctrl+Shift+K).
> - Ask questions about the page you are reading in a side-panel chat.
> - Run the browser steps of your own automations in your real, logged-in browser, instead of a separate automation profile.
> - Record the steps you take on a page and turn them into an automation.
>
> Everything stays on your computer. The extension talks only to the MonoAgent
> app on 127.0.0.1 and refuses any other server unless you override that for a
> single save. No account, no analytics, no ads, nothing sent to us.
>
> Get MonoAgent: https://monoes.me

**Single purpose (Chrome Web Store field):** Connect the user's browser to the
MonoAgent desktop app on the same computer, so the user's own automations and
saved items work in the browser they are signed in to.

## Permission justifications

| Permission | Why it is needed |
|---|---|
| `host_permissions: <all_urls>` and content scripts on `<all_urls>` | Automations, page capture, highlighting and the recorder run on whatever site the user points them at; there is no fixed list of sites. Nothing is read from a page unless the user triggers a save, a highlight, a recording, a chat question or an automation step. |
| `debugger` | Real keyboard and click events (typing into editors that ignore synthetic events), evaluation on pages whose CSP blocks scripts, and the DevTools-protocol relay the automation engine drives. Attached only to a tab being automated and detached after 30 seconds without traffic. |
| `scripting` | Injects the capture, highlight and recorder code into the active page and reads results back. |
| `tabs`, `tabGroups`, `webNavigation` | Open, find, group and wait for tabs during an automation; know when a page has finished loading. |
| `activeTab` | Capture and highlight the current page when the user presses the shortcut or the toolbar button. |
| `cookies` | Tell an automation whether the user is signed in to a site, and carry that session to the user's own local MonoAgent profile. Cookie values are never sent anywhere except to the local app. |
| `storage` | Remember the pairing token, the chosen profile, and the queue of saves and tasks waiting while the app is not running. |
| `alarms` | Keep the connection to the local app alive and retry it (the MV3 service worker sleeps). |
| `contextMenus` | The "Add selection as task / Add page as task" right-click entries. |
| `sidePanel` | The extension's main interface. |
| `commands` | Keyboard shortcuts for save, highlight and add-task. |

**Remote code disclosure.** The extension does not load code from the network.
It does run JavaScript that the user's own MonoAgent app sends over the paired
local connection (for example an automation step that reads a page). The
connection is loopback-only, authenticated with a token only the user's
machine holds, and the extension refuses an unpaired or non-loopback server.
State this plainly in the reviewer notes; do not describe it as "no remote code".

## Notes for the reviewer

Paste into the certification notes field:

> MonoAgent Bridge only works with the MonoAgent desktop app. To test: install
> MonoAgent from https://monoes.me, run `monoagentcli extension pair`, paste
> the printed token into the side panel's pairing box. After that the side
> panel shows "connected" and the shortcuts work (Ctrl+Shift+S saves the page
> to the local library). The extension makes no network requests other than to
> 127.0.0.1. [If a test build/video is provided, link it here.]

## Assets to prepare (not in the repo)

- 1280x800 (or 640x400) screenshots, at least 1: side panel connected, a saved page, the chat panel.
- Small promo tile 440x280 (Edge and Chrome Web Store).
- Support URL and homepage URL on monoes.me.
- Edge only: a "search terms" list (automation, browser, workflow, clipper, tasks).

## Privacy policy (host this text at a public URL)

**MonoAgent Bridge: Privacy Policy**

MonoAgent Bridge is a browser extension that connects your browser to the
MonoAgent app running on your own computer.

**What the extension handles.** When you save a page, highlight text, add a
task, ask the side-panel chat about a page, record steps, or run an automation,
the extension reads the relevant page content (its text, address, title,
selection, and, for sign-in checks and automations, cookies for that site). It
also stores a pairing token and your settings in the browser's extension
storage.

**Where it goes.** That information is sent only to the MonoAgent app on your
own computer, over a connection to 127.0.0.1. The extension rejects other
servers unless you explicitly allow one for a single save. We do not receive,
collect, sell or share this information, and the extension contains no
analytics, advertising or tracking.

**What MonoAgent does with it.** The app stores saved items locally under your
user profile. If you use the chat or summary features, the app sends the text
you chose to the AI provider you configured in MonoAgent; that is governed by
that provider's terms and your MonoAgent settings, not by the extension.

**Your control.** Remove the pairing in the side panel or with
`monoagentcli extension reset`; uninstalling the extension deletes its stored
data. Saved items live in `~/.monoagent/` and can be deleted from the app.

**Limited use.** The use of information received from browser APIs adheres to
the Chrome Web Store User Data Policy, including its Limited Use requirements.

**Contact.** <support email, set before publishing>

## Open before submitting

- Support email and the hosted policy URL (placeholders above).
- A test path for reviewers; the app is not installable from a store, so expect questions.
- `debugger` plus all-sites access plus app-supplied scripts is the likeliest cause of a rejection. If it is rejected, the fallback is a store build without `eval`/`eval_cdp` and the CDP relay, with the full bridge staying an unpacked install.
