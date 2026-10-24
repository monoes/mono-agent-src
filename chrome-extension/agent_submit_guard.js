/**
 * MonoAgent Bridge — submit_guard, the worker's half (plan C1a)
 *
 * Before an agent acts on a page whose submits need a person's approval, the
 * CLI arms this tab's guard; every submit the page then tries is stopped in
 * the page (agent_submit_page.js) and recorded here, where the CLI reads it
 * (status) and turns it into an approval item. Once approved, allow_once
 * lets exactly the next submit through.
 *
 *   arm         inject the guard into every frame, and into every document
 *               the tab commits from now on on the same origin
 *               (webNavigation.onCommitted)
 *   disarm      stop blocking; the guards stay in their pages, inert
 *   allow_once  the next submit passes, in the frame named by `frameId`,
 *               else the frame of the latest blocked submit of the form at
 *               `selector`, else that of the latest blocked submit
 *   status      what all four answer:
 *               {armed, origin, blocked:[{seq,ts,url,form,frameId}]}
 *
 * A guard belongs to the origin its tab was on when it was armed. The tab's
 * page leaving that origin (the person going elsewhere in the same tab)
 * ends it: the tab is disarmed and its records dropped, so nothing the
 * person submits on another site is stopped or reported. Frames inside a
 * page on the armed origin are guarded whatever their own origin (a payment
 * iframe is exactly what must not submit unapproved). A report that arrives
 * while the page is on another origin is dropped.
 *
 * While a tab is armed its page shows a bar saying so, with a button that
 * stops the guard, and a toast for each held submit (agent_guard_bar.js):
 * the guard cannot tell the person's own submit from an agent's.
 *
 * The armed tabs and their blocked submits are kept in chrome.storage.session
 * as well as here, so a worker restart neither forgets to re-arm a tab on its
 * next navigation nor loses an attempt the CLI has not read yet. A browser
 * restart clears them. A tab keeps its last MAX_BLOCKED attempts.
 *
 * Node-tested against a fake chrome (agent_submit_guard.test.mjs).
 */

(function (root) {
  "use strict";

  const ACTIONS = ["arm", "disarm", "allow_once", "status"];
  const STORE_KEY = "agentSubmitGuard";
  // Nothing of the guard is on the page's window: it is controlled over a per-tab
  // random event name (st.ctl, see installGuard) that only this worker knows.
  const MESSAGE = "agent_submit_blocked";
  const MAX_BLOCKED = 50;

  let tabs = {}; // String(tabId) -> { armed, seq, blocked: [{seq, ts, url, form, frameId}] }
  let ready = null;

  function load() {
    if (!ready) {
      ready = Promise.resolve()
        .then(() => chrome.storage.session.get(STORE_KEY))
        .then(
          (got) => {
            const saved = got && got[STORE_KEY];
            if (saved && typeof saved === "object") tabs = Object.assign(saved, tabs);
          },
          () => {}
        );
    }
    return ready;
  }

  function save() {
    try {
      chrome.storage.session.set({ [STORE_KEY]: tabs }).catch(() => {});
    } catch {
      // no session storage: the state lives as long as this worker
    }
  }

  function view(st) {
    return {
      armed: !!st.armed,
      origin: st.origin || "",
      blocked: st.blocked.map((b) => ({ seq: b.seq, ts: b.ts, url: b.url, form: b.form, frameId: b.frameId })),
    };
  }

  function nonce() {
    const bytes = new Uint8Array(12);
    crypto.getRandomValues(bytes);
    return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
  }

  /** inject puts the relay, then the guard, into the target frames. */
  async function inject(tabId, frames, immediately, ctl) {
    const page = root.MonoAgentSubmitPage;
    const target = Object.assign({ tabId }, frames);
    const event = `monoagent-submit-${nonce()}`;
    await chrome.scripting.executeScript({ target, func: page.installRelay, args: [event], injectImmediately: !!immediately });
    const results = await chrome.scripting.executeScript({
      target,
      world: "MAIN",
      func: page.installGuard,
      args: [ctl, event],
      injectImmediately: !!immediately,
    });
    return (results || []).some((r) => r && r.result === true);
  }

  /** ui runs one of agent_guard_bar.js's functions in the tab's top frame, once the page is there. */
  function ui(tabId, fn) {
    return chrome.scripting.executeScript({ target: { tabId, frameIds: [0] }, func: fn }).catch(() => {});
  }

  /** control runs one guard action in the target frames; true when a guard took it. */
  async function control(tabId, frames, action) {
    const st = tabs[tabId];
    if (!st || !st.ctl) return false;
    const results = await chrome.scripting.executeScript({
      target: Object.assign({ tabId }, frames),
      world: "MAIN",
      args: [st.ctl, action],
      func: (name, a) => {
        // Answered only by a guard listening on this tab's name.
        const box = { action: a, result: false };
        try {
          document.dispatchEvent(new CustomEvent(name, { detail: box }));
        } catch {
          return false;
        }
        return box.result === true;
      },
    });
    return (results || []).some((r) => r && r.result === true);
  }

  /** handle answers submit_guard. */
  async function handle(params) {
    const tabId = root.MonoAgentCdp.tabOf(params, "submit_guard");
    const action = params.action;
    if (!ACTIONS.includes(action)) throw new Error(`submit_guard: action must be one of ${ACTIONS.join(", ")}`);
    await load();
    const st = tabs[tabId] || { armed: false, origin: "", seq: 0, blocked: [] };
    if (action === "arm") {
      const tab = await chrome.tabs.get(tabId);
      if (tab.pendingUrl) throw new Error("submit_guard: the tab is navigating; arm it once the page has loaded");
      const origin = originOf(tab.url);
      if (!/^https?:/.test(origin)) throw new Error("submit_guard: the tab has no web page to guard");
      st.ctl = st.ctl || `monoagent-ctl-${nonce()}`;
      if (!(await inject(tabId, { allFrames: true }, false, st.ctl))) {
        throw new Error("submit_guard: the guard could not be installed in this page");
      }
      if (st.origin !== origin) st.blocked = []; // records belong to the origin they were made on
      st.armed = true;
      st.origin = origin;
      tabs[tabId] = st;
      save();
      await ui(tabId, root.MonoAgentGuardBar.show);
    } else if (action === "disarm") {
      st.armed = false;
      if (tabs[tabId]) save();
      await control(tabId, { allFrames: true }, "disarm").catch(() => false);
      await ui(tabId, root.MonoAgentGuardBar.hide);
    } else if (action === "allow_once") {
      if (!st.armed) throw new Error("submit_guard: the guard is not armed on this tab");
      const frameId = allowFrame(st, params);
      const ok = await control(tabId, { frameIds: [frameId] }, "allow_once").catch(() => false);
      if (!ok) throw new Error(`submit_guard: no guard in frame ${frameId} to let a submit through (it navigated away; arm it again)`);
    }
    return view(st);
  }

  /**
   * allowFrame is the frame allow_once opens: the one the caller names, else
   * the one the latest blocked submit of `selector` (or the latest of all)
   * came from. Never a guess at another frame: one that went away is an error.
   */
  function allowFrame(st, params) {
    if (params.frameId !== undefined && params.frameId !== null) {
      if (!Number.isInteger(params.frameId) || params.frameId < 0) throw new Error("submit_guard: frameId must be a frame id");
      return params.frameId;
    }
    let pick = st.blocked[st.blocked.length - 1];
    if (typeof params.selector === "string" && params.selector) {
      pick = [...st.blocked].reverse().find((b) => b.form.selector === params.selector);
      if (!pick) throw new Error(`submit_guard: no blocked submit of ${params.selector} to let through`);
    }
    return pick && Number.isInteger(pick.frameId) ? pick.frameId : 0;
  }

  function originOf(u) {
    try {
      return new URL(u).origin;
    } catch {
      return "";
    }
  }

  const str = (v, n) => (typeof v === "string" ? v.slice(0, n) : "");

  /**
   * normalize re-checks a report from the page: it is page-written data. Its
   * URLs lose their secret-looking query values (agent_redact.js), as do any
   * URLs in the field values.
   */
  function normalize(detail) {
    const R = root.MonoAgentRedact;
    let d;
    try {
      d = JSON.parse(detail);
    } catch {
      return null;
    }
    if (!d || typeof d !== "object") return null;
    const f = d.form && typeof d.form === "object" ? d.form : {};
    const fields = Array.isArray(f.fields) ? f.fields.slice(0, 100) : [];
    return {
      url: R.url(str(d.url, 2048)),
      form: {
        action: R.url(str(f.action, 2048)),
        method: str(f.method, 16),
        fields: fields.map((x) => ({
          name: str(x && x.name, 200),
          type: str(x && x.type, 40),
          label: str(x && x.label, 200),
          value: R.text(str(x && x.value, 500)),
        })),
        submitter: str(f.submitter, 1000),
        selector: str(f.selector, 1000),
      },
    };
  }

  async function record(tabId, frameId, frameUrl, tabUrl, detail) {
    await load();
    const st = tabs[tabId];
    if (!st || !st.armed) return;
    if (originOf(tabUrl) !== st.origin) return; // never a report from a page on another origin
    const report = normalize(detail);
    if (!report) return;
    st.seq += 1;
    st.blocked.push({
      seq: st.seq,
      ts: Date.now(),
      url: root.MonoAgentRedact.url(str(frameUrl, 2048)) || report.url,
      form: report.form,
      frameId: Number.isInteger(frameId) ? frameId : 0,
    });
    if (st.blocked.length > MAX_BLOCKED) st.blocked.shift();
    save();
    await ui(tabId, root.MonoAgentGuardBar.show);
    await ui(tabId, root.MonoAgentGuardBar.toast);
  }

  async function committed(details) {
    await load();
    const st = tabs[details.tabId];
    if (!st || !st.armed) return;
    if (details.frameId === 0) {
      if (originOf(details.url) !== st.origin) {
        // The page left the armed origin: the guard ends with it.
        delete tabs[details.tabId];
        save();
        return;
      }
    } else {
      const tab = await chrome.tabs.get(details.tabId).catch(() => null);
      if (!tab || originOf(tab.url) !== st.origin) return;
    }
    // As early as Chrome allows: before the page's own scripts, usually.
    if (!st.ctl) {
      st.ctl = `monoagent-ctl-${nonce()}`;
      save();
    }
    await inject(details.tabId, { frameIds: [details.frameId] }, true, st.ctl).catch(() => false);
    // The bar waits for the page: executeScript's default timing.
    if (details.frameId === 0) await ui(details.tabId, root.MonoAgentGuardBar.show);
  }

  async function removed(tabId) {
    await load();
    if (!tabs[tabId]) return;
    delete tabs[tabId];
    save();
  }

  /** armedTabs lists the armed tabs, for the idle release (agent_commands.js). */
  async function armedTabs() {
    await load();
    return Object.keys(tabs)
      .filter((k) => tabs[k].armed)
      .map(Number);
  }

  /** install registers the three listeners; called once, at worker start. */
  function install() {
    chrome.webNavigation.onCommitted.addListener((details) => {
      committed(details).catch(() => {});
    });
    chrome.runtime.onMessage.addListener((msg, sender) => {
      if (!msg || msg.type !== MESSAGE) return false;
      const tabId = sender && sender.tab && sender.tab.id;
      if (!Number.isInteger(tabId) || (sender.id && sender.id !== chrome.runtime.id)) return false;
      record(tabId, sender.frameId, sender.url, sender.tab.url, msg.detail).catch(() => {});
      return false;
    });
    chrome.tabs.onRemoved.addListener((tabId) => {
      removed(tabId).catch(() => {});
    });
    load();
  }

  root.MonoAgentSubmitGuard = { install, handle, normalize, armedTabs };
})(globalThis);
