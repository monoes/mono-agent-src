/**
 * MonoAgent Bridge — handing work to the coder, the worker's half (C5/C6)
 *
 * The side panel's Coder section queues a coder chat: a kind of job
 * (copy-style, tokens, fix-element, bug-report, integration, ask), the tab,
 * perhaps one element's selector, a note and a project folder. This file
 * answers the panel's coder_* messages over ask.js.
 *
 * THE RULE: what goes to MonoAgent is those fields and nothing else. The
 * page's text never rides a coder request: the CLI builds the context
 * bundle itself, from the tab, with its own browser commands (`coder queue
 * add`), so a page cannot put words into the request. A selector is the
 * one page-derived string, and it is only ever a selector.
 *
 * The extension never starts a coder turn (decision 3). It queues one, and
 * the person presses Start in the MonoAgent app. A follow-up from here is
 * only for a conversation whose first turn the app started; MonoAgent
 * refuses the rest (coder.send), and the panel offers the box only then.
 *
 * Messages (all answered {ok, ...}; failures carry error and code):
 *   coder_ready       -> {state: "ready" | "offline" | "update"}
 *   coder_projects    -> {projects, recent}
 *   coder_queue       {kind, tabId, selector?, note?, cwd?, reload?, profile?} -> {item}
 *                     reload: bug-report only, reload the tab to catch its load's errors
 *   coder_task        {kind, tabId, note?, profile?} -> {task}
 *                     "Fill with coder" / "Ask coder" (kind extension-fill | extension-ask): queues an AI
 *                     task about the tab. Like coder_queue it starts nothing, and no page text rides it.
 *   coder_queue_list  -> {items}
 *   coder_list        -> {conversations}
 *   coder_events      {conversationId, turn?, since?} -> {turn, events, turn_active, images}
 *                     like chat.events: seq counts per turn, no turn means the latest
 *   coder_send        {conversationId, text}
 *   coder_stop        {conversationId}
 *   coder_pick        {tabId} -> {selector}   the element picker, then a selector
 *   coder_from_page   the highlight panel's coder button (a content script)
 *
 * Who may send them is checked as task_bridge.js checks its own: only this
 * extension, its own pages (the side panel) for every message, and a tab's
 * content script for coder_from_page alone, which names nothing: the worker
 * reads the element back from the page itself.
 *
 * The right-click items (coder_menu.js) and the highlight panel's button
 * both end in handoff(): the side panel opens on its Coder section, filled
 * in with the kind, the tab and the element's selector (PREFILL_KEY).
 *
 * After ask.js; recall_bridge.js installs ask.js itself, so install() here
 * never does.
 */

(function (root) {
  "use strict";

  const KINDS = ["copy-style", "tokens", "fix-element", "bug-report", "integration", "ask"];
  // The AI-task kinds ("Fill with coder", "Ask coder"). They queue through
  // coder.task, not coder.queue; extension-ask needs the person's question.
  const TASK_KINDS = ["extension-fill", "extension-ask"];
  const PREFILL_KEY = "coderPrefill";
  const SELECTOR_CAP = 1024;
  const NOTE_CAP = 4000;
  const CWD_CAP = 4096;
  const TEXT_CAP = 16 * 1024;
  const ID = /^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$/; // the bridge's own conversation id rule
  // Building the bundle runs the CLI's screenshot/styles/console commands
  // against the tab, which can take a while on a heavy page.
  const QUEUE_TIMEOUT_MS = 180000;
  const QUERY_TIMEOUT_MS = 20000;
  const PICK_TIMEOUT_MS = 120000;
  const PICK_PROMPT = "Click the element to send to the coder";

  let deps = null;

  const str = (v) => (typeof v === "string" ? v : "");
  const fail = (message, code) => {
    const err = new Error(message);
    if (code) err.code = code;
    return err;
  };
  const query = (method, params, ms) =>
    root.MonoAsk.request(method, params, { timeoutMs: ms || QUERY_TIMEOUT_MS, idleTimeoutMs: ms || QUERY_TIMEOUT_MS });

  /** isExtensionPage: one of this extension's own pages, wherever it is open (task_bridge.js). */
  function isExtensionPage(sender) {
    const base = chrome.runtime.getURL ? chrome.runtime.getURL("") : "";
    return !!base && String(sender.url || "").startsWith(base);
  }

  /** allowed: this extension's pages may send every coder_* message, a tab's content script only coder_from_page. */
  function allowed(type, sender) {
    if (!sender || sender.id !== chrome.runtime.id) return false;
    return isExtensionPage(sender) || (type === "coder_from_page" && !!sender.tab);
  }

  function install(d) {
    deps = d || {};
    chrome.runtime.onMessage.addListener((msg, sender, respond) => {
      if (!msg || typeof msg.type !== "string") return false;
      const handler = handlers[msg.type];
      if (!handler || !allowed(msg.type, sender)) return false;
      // Called now, not on a later tick: coder_from_page opens the side
      // panel, which Chrome allows only inside the click's own handler.
      let out;
      try {
        out = handler(msg, sender);
      } catch (err) {
        out = Promise.reject(err);
      }
      Promise.resolve(out)
        .then((result) => respond(result))
        .catch((err) => respond({ ok: false, error: err.message || String(err), code: err.code }));
      return true;
    });
  }

  /** withProfile adds the header's profile when the panel sent a sane one. */
  function withProfile(params, msg) {
    const p = str(msg && msg.profile).trim();
    return p && p.length <= 128 ? Object.assign({}, params, { profile: p }) : params;
  }

  function conversationOf(msg) {
    const id = str(msg && msg.conversationId);
    if (!ID.test(id)) throw fail("there is no conversation to talk to", "invalid");
    return id;
  }

  /**
   * queueParams is the whole of a coder.queue request, built from the
   * panel's message field by field: whatever else the message carried (a
   * page's text, its title, a context block) is not copied.
   */
  function queueParams(msg) {
    const kind = str(msg.kind);
    if (KINDS.indexOf(kind) === -1) throw fail(`unknown kind "${kind}"`, "invalid");
    if (!Number.isInteger(msg.tabId) || msg.tabId <= 0) throw fail("there is no tab to send", "invalid");
    const params = { kind, tabId: msg.tabId };
    const selector = str(msg.selector).trim();
    if (selector.length > SELECTOR_CAP) throw fail("that selector is too long", "invalid");
    if (selector) params.selector = selector;
    const note = str(msg.note).trim().slice(0, NOTE_CAP);
    if (note) params.note = note;
    const cwd = str(msg.cwd).trim();
    if (cwd.length > CWD_CAP) throw fail("that folder path is too long", "invalid");
    if (cwd) params.cwd = cwd;
    if (kind === "bug-report" && typeof msg.reload === "boolean") params.reload = msg.reload;
    return withProfile(params, msg);
  }

  /**
   * taskParams is the whole of a coder.task request, built field by field like
   * queueParams: the person's note and the tab, never the page's text, title or
   * selector. The note is the person's own words.
   */
  function taskParams(msg) {
    const kind = str(msg.kind);
    if (TASK_KINDS.indexOf(kind) === -1) throw fail(`unknown kind "${kind}"`, "invalid");
    if (!Number.isInteger(msg.tabId) || msg.tabId <= 0) throw fail("there is no tab to send", "invalid");
    const params = { kind, tabId: msg.tabId };
    const note = str(msg.note).trim().slice(0, NOTE_CAP);
    if (kind === "extension-ask" && !note) throw fail("say what you want to ask", "invalid");
    if (note) params.note = note;
    return withProfile(params, msg);
  }

  async function ready() {
    if (!deps || !deps.isConnected || !deps.isConnected()) return { ok: true, state: "offline" };
    const methods = await root.MonoAsk.probe();
    if (root.MonoAsk.known() === null) return { ok: true, state: "offline" };
    return { ok: true, state: methods.indexOf("coder.queue") !== -1 ? "ready" : "update" };
  }

  /** selectorOf is a picker fingerprint's best unique CSS candidate, else its structural path. */
  function selectorOf(fp) {
    if (!fp || typeof fp !== "object") return "";
    const list = Array.isArray(fp.candidates) ? fp.candidates : [];
    const best = list.find((c) => c && c.kind === "css" && c.unique && typeof c.value === "string");
    const sel = best ? best.value : str(fp.css);
    return sel.length <= SELECTOR_CAP ? sel : "";
  }

  async function pick(msg) {
    if (!Number.isInteger(msg.tabId) || msg.tabId <= 0) throw fail("there is no tab to pick in", "invalid");
    if (!deps || typeof deps.pickElement !== "function") throw fail("the element picker is not available", "unavailable");
    const got = await deps.pickElement({ tabId: msg.tabId, prompt: PICK_PROMPT, timeoutMs: PICK_TIMEOUT_MS });
    const selector = selectorOf(got && got.fingerprint);
    if (!selector) throw fail("could not describe that element");
    return { ok: true, selector };
  }

  // ── The element under the right-click or beside the highlight panel ──

  /** Run in the page (serialized): closes over nothing. Is the selector library already there? */
  function hasSelectors() {
    return globalThis.MonoRecorderSelectors !== undefined;
  }

  /**
   * Run in the page (serialized): the selector of the element
   * highlight_panel.js remembered ("context" or "panel"), or "". Removes
   * the selector library again when this call is what brought it.
   */
  function selectorOfRemembered(which, drop) {
    let out = "";
    try {
      const P = globalThis.MonoHighlightPanel;
      const S = globalThis.MonoRecorderSelectors;
      const el = P && P.remembered && P.remembered(which);
      if (el && el.isConnected && S) out = S.bestCss(el, S.browserEnv(document));
    } catch {
      out = "";
    }
    if (drop) delete globalThis.MonoRecorderSelectors;
    return typeof out === "string" ? out : "";
  }

  /**
   * rememberedSelector reads that element back in the tab's top frame, in
   * the content scripts' own world, with recorder_selectors.js: the same
   * ranking the picker and the recorder use. A selector from a subframe
   * would name an element of another document, so none is taken there.
   */
  async function rememberedSelector(tabId, frameId, which) {
    if (frameId) return "";
    const target = { tabId, frameIds: [0] };
    try {
      const [before] = await chrome.scripting.executeScript({ target, func: hasSelectors });
      await chrome.scripting.executeScript({ target, files: ["recorder_selectors.js"] });
      const drop = !(before && before.result);
      const [res] = await chrome.scripting.executeScript({ target, func: selectorOfRemembered, args: [which, drop] });
      const sel = str(res && res.result);
      return sel.length <= SELECTOR_CAP ? sel : "";
    } catch {
      return ""; // a page that refuses scripts: the whole page, then
    }
  }

  /** A dev server's page is most likely the person's own: fix it. Anyone else's: copy from it. */
  function elementKind(url) {
    let host = "";
    try {
      host = new URL(url).hostname.replace(/^\[|\]$/g, "");
    } catch {
      return "copy-style";
    }
    const local =
      /^(localhost|::1)$/.test(host) ||
      /\.(localhost|local|test)$/.test(host) ||
      /^(127|10)\.\d+\.\d+\.\d+$/.test(host) ||
      /^192\.168\.\d+\.\d+$/.test(host) ||
      /^172\.(1[6-9]|2\d|3[01])\.\d+\.\d+$/.test(host);
    return local ? "fix-element" : "copy-style";
  }

  /** toast tells the page what happened when there is no side panel to tell it in. */
  function toast(tabId, text, level) {
    const C = root.MonoCaptureBridge;
    if (C && C.toast) C.toast(tabId, text, level);
  }

  /**
   * panelShown: is a side panel of ours already open in that window? A click
   * in the page reaches the worker as a message, which carries no gesture,
   * so open() is refused there even when the panel is right beside it.
   */
  async function panelShown(windowId) {
    try {
      const found = await chrome.runtime.getContexts({ contextTypes: ["SIDE_PANEL"], windowIds: [windowId] });
      return Array.isArray(found) && found.length > 0;
    } catch {
      return false;
    }
  }

  /**
   * handoff opens the side panel on its Coder section, filled in. Chrome
   * opens a side panel only inside the gesture's own handler, before
   * anything is awaited, so open() is called first, synchronously; the
   * selector is worked out after and reaches the panel through session
   * storage, which it reads on load and watches. `which` names the
   * remembered element ("context", "panel"), or nothing for the page.
   */
  function handoff(tab, opts) {
    const o = opts || {};
    let opening;
    try {
      opening = Promise.resolve(chrome.sidePanel.open({ windowId: tab.windowId }));
    } catch (err) {
      opening = Promise.reject(err);
    }
    const opened = opening.then(() => true, () => false);
    return (async () => {
      const selector = o.which ? await rememberedSelector(tab.id, o.frameId, o.which) : "";
      const kind = o.kind || elementKind(tab.url);
      const prefill = { at: Date.now(), kind, tabId: tab.id, selector };
      if (chrome.storage && chrome.storage.session) await chrome.storage.session.set({ [PREFILL_KEY]: prefill });
      if (!(await opened) && !(await panelShown(tab.windowId))) {
        // Worded for both cases: some browsers do not list an open panel.
        toast(tab.id, "Filled in for the coder in MonoAgent's side panel. Open it from the toolbar button if it is closed.", "warn");
      }
      return prefill;
    })();
  }

  function fromPage(msg, sender) {
    const tab = sender && sender.tab;
    if (!tab || !Number.isInteger(tab.id)) throw fail("there is no tab to send", "invalid");
    return handoff(tab, { which: "panel", frameId: sender.frameId }).then(() => ({ ok: true }));
  }

  /**
   * promptUserToSubmit is decision 4 for a form the extension filled: it is
   * never submitted for the person. The page shows its "review and press
   * Submit yourself" banner and outlines the submit control (C1
   * submit_prompt, agent_submit_prompt.js); nothing is pressed. Resolves
   * what the page answered, {shown, submitSelector?}, or {shown:false} when
   * it could not be shown. `selector` names the form, when known.
   */
  async function promptUserToSubmit(tabId, opts) {
    const S = root.MonoAgentSubmitPrompt;
    if (!S || typeof S.show !== "function" || !Number.isInteger(tabId)) return { shown: false };
    const o = opts || {};
    const params = { tabId };
    if (typeof o.selector === "string" && o.selector) params.selector = o.selector;
    if (typeof o.message === "string" && o.message) params.message = o.message;
    try {
      return await S.show(params);
    } catch {
      return { shown: false };
    }
  }

  const handlers = {
    coder_ready: ready,
    coder_projects: async (msg) => {
      const data = await query("coder.projects", withProfile({}, msg));
      return { ok: true, projects: (data && data.projects) || [], recent: (data && data.recent) || [] };
    },
    coder_queue: async (msg) => {
      const params = queueParams(msg);
      const data = await query("coder.queue", params, QUEUE_TIMEOUT_MS);
      return { ok: true, item: (data && data.item) || null };
    },
    coder_task: async (msg) => {
      const params = taskParams(msg);
      const data = await query("coder.task", params, QUEUE_TIMEOUT_MS);
      return { ok: true, task: (data && data.task) || null };
    },
    coder_queue_list: async (msg) => {
      const data = await query("coder.queue.list", withProfile({}, msg));
      return { ok: true, items: (data && data.items) || [] };
    },
    coder_list: async (msg) => {
      const data = await query("coder.list", withProfile({}, msg));
      return { ok: true, conversations: (data && data.conversations) || [] };
    },
    coder_events: async (msg) => {
      const params = { conversationId: conversationOf(msg) };
      const turn = str(msg.turn);
      if (turn && !ID.test(turn)) throw fail("invalid turn id", "invalid");
      if (turn) params.turn = turn;
      if (Number.isInteger(msg.since) && msg.since > 0) params.since = msg.since;
      const data = (await query("coder.events", withProfile(params, msg))) || {};
      return {
        ok: true,
        turn: str(data.turn),
        events: Array.isArray(data.events) ? data.events : [],
        turn_active: !!data.turn_active,
        images: Array.isArray(data.images) ? data.images : [],
      };
    },
    coder_send: async (msg) => {
      const text = str(msg.text).trim();
      if (!text) throw fail("there is nothing to send", "invalid");
      if (new TextEncoder().encode(text).length > TEXT_CAP) throw fail("that message is too long", "invalid");
      const data = await query("coder.send", withProfile({ conversationId: conversationOf(msg), text }, msg));
      return { ok: true, data: data || {} };
    },
    coder_stop: async (msg) => {
      await query("coder.stop", withProfile({ conversationId: conversationOf(msg) }, msg));
      return { ok: true };
    },
    coder_pick: pick,
    coder_from_page: fromPage,
  };

  root.MonoCoderBridge = {
    install,
    handlers,
    handoff,
    queueParams,
    taskParams,
    selectorOf,
    elementKind,
    promptUserToSubmit,
    KINDS,
    TASK_KINDS,
    PREFILL_KEY,
  };
})(globalThis);
