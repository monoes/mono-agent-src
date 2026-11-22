/**
 * MonoAgent Bridge — the form filler's worker half
 *
 *   form_fill  {profile, windowId}  ->  {ok, filled, found, sources?}
 *   card_fill  {profile, windowId}  ->  {ok, saved, filled}
 *   files_fill {profile, windowId}  ->  {ok, attached, found, names?}
 *   form_run   {profile, tabId, what: fill|files, generate?}  (bridge command, from the command line)
 *
 * form_fill scans the active tab's form fields (form_page.js), asks the bridge
 * which of them the profile's Info page and documents can answer (`form.fill`),
 * and writes the answers into the page. card_fill and files_fill do the two
 * things form_fill never does, each on its own button: the saved payment card
 * (`form.card`) and the saved photos and scans (`form.photos`). None of them
 * submits anything: once a field was changed, the page asks the person to
 * review the form and press Submit themselves (decision 4,
 * MonoCoderBridge.promptUserToSubmit). Runtime and model are the panel's "AI for summaries"
 * choice, as for chat, and only form_fill uses them.
 *
 * A card or a photo of an ID is only put on a page the browser calls secure
 * (https, or this computer's own address).
 *
 * After ask.js, chat_core.js and summary_ai.js; recall_bridge.js installs
 * ask.js itself, so install() here never does.
 */

(function (root) {
  "use strict";

  const PAGE_FILE = "form_page.js";
  const FILL_TIMEOUT_MS = 180000;
  const QUICK_TIMEOUT_MS = 60000;
  const SUBMIT_MESSAGE = "Review the form and press Submit yourself";
  let running = false;

  async function activeTab(msg) {
    // The command line names its tab (`browser fill`); the side panel never does.
    if (Number.isInteger(msg.tabId)) return (await chrome.tabs.get(msg.tabId).catch(() => null)) || null;
    const query = Number.isInteger(msg.windowId) ? { active: true, windowId: msg.windowId } : { active: true, lastFocusedWindow: true };
    const [tab] = await chrome.tabs.query(query);
    return tab || null;
  }

  const inPage = async (tabId, func, args) => {
    const [res] = await chrome.scripting.executeScript({ target: { tabId }, func, args: args || [] });
    return res && res.result;
  };

  /** Safe for a card or an ID photo: https, or this computer's own address. */
  function secureUrl(url) {
    try {
      const u = new URL(url);
      return u.protocol === "https:" || (u.protocol === "http:" && (u.hostname === "localhost" || u.hostname === "127.0.0.1"));
    } catch {
      return false;
    }
  }

  const profileOf = (msg) => {
    const profile = typeof msg.profile === "string" && root.MonoChatCore.validProfile(msg.profile) ? msg.profile.trim() : "";
    if (!profile) throw new Error("choose a profile first");
    return profile;
  };

  /**
   * One run on the active tab: the page half is put in, then `work` runs with
   * the tab. Only one run at a time, and a page that was never scanned is left
   * as it was (apply with nothing clears the marks the scan left).
   */
  async function onTab(msg, { secure }, work) {
    const profile = profileOf(msg);
    if (running) throw new Error("a form is already being filled");
    const tab = await activeTab(msg);
    if (!tab || !tab.id) throw new Error("there is no page to fill");
    if (secure && !secureUrl(tab.url)) throw new Error("this is only done on secure (https) pages");
    running = true;
    try {
      try {
        await chrome.scripting.executeScript({ target: { tabId: tab.id }, files: [PAGE_FILE] });
      } catch {
        throw new Error("this page cannot be filled (the browser does not let extensions in)");
      }
      return await work(tab, profile);
    } finally {
      running = false;
    }
  }

  /** After a change to the page: the person submits, nothing here does. No selector: the banner finds the form the last changed field is in. */
  async function askToSubmit(tabId, changed) {
    if (changed > 0 && root.MonoCoderBridge) await root.MonoCoderBridge.promptUserToSubmit(tabId, { message: SUBMIT_MESSAGE });
  }

  const clearMarks = (tabId) => inPage(tabId, () => globalThis.MonoFormPage.apply([])).catch(() => {});

  async function fill(msg) {
    return onTab(msg, { secure: false }, async (tab, profile) => {
      const scan = await inPage(tab.id, () => globalThis.MonoFormPage.scan());
      const asked = ((scan && scan.fields) || []).filter((f) => !f.filled);
      if (!asked.length) {
        await clearMarks(tab.id);
        return { ok: true, filled: 0, found: 0, none: true };
      }
      const params = { profile, url: scan.url, fields: asked.map(({ filled, ...rest }) => rest) };
      if (msg.generate === true) params.generate = true;
      let ai = {};
      try {
        ai = (await root.MonoSummaryAI.sticky(chrome.storage.local)) || {};
      } catch {
        ai = {};
      }
      if (ai.runtime) params.runtime = ai.runtime;
      if (ai.runtime && ai.model) params.model = ai.model;
      let data;
      try {
        data = await root.MonoAsk.request("form.fill", params, { timeoutMs: FILL_TIMEOUT_MS, idleTimeoutMs: FILL_TIMEOUT_MS });
      } catch (err) {
        await clearMarks(tab.id);
        throw err;
      }
      const values = (data && Array.isArray(data.values) ? data.values : []).filter((v) => v && typeof v.id === "string" && typeof v.value === "string");
      const done = await inPage(tab.id, (vals) => globalThis.MonoFormPage.apply(vals), [values.map(({ id, value }) => ({ id, value }))]);
      const sources = [...new Set(values.map((v) => v.source).filter(Boolean))];
      const filled = (done && done.filled) || 0;
      await askToSubmit(tab.id, filled);
      const generated = values.filter((v) => v.generated).length;
      return { ok: true, filled, found: asked.length, sources, generated };
    });
  }

  async function card(msg) {
    return onTab(msg, { secure: true }, async (tab, profile) => {
      const scan = await inPage(tab.id, () => globalThis.MonoFormPage.scan());
      const asked = ((scan && scan.fields) || []).filter((f) => !f.filled);
      let data;
      try {
        data = await root.MonoAsk.request("form.card", { profile, url: scan.url, fields: asked.map(({ filled, ...rest }) => rest) }, { timeoutMs: QUICK_TIMEOUT_MS, idleTimeoutMs: QUICK_TIMEOUT_MS });
      } catch (err) {
        await clearMarks(tab.id);
        throw err;
      }
      const values = (data && Array.isArray(data.values) ? data.values : []).filter((v) => v && typeof v.id === "string" && typeof v.value === "string");
      const done = await inPage(tab.id, (vals) => globalThis.MonoFormPage.apply(vals), [values.map(({ id, value }) => ({ id, value }))]);
      const filled = (done && done.filled) || 0;
      await askToSubmit(tab.id, filled);
      return { ok: true, saved: !!(data && data.saved), filled };
    });
  }

  async function files(msg) {
    return onTab(msg, { secure: true }, async (tab, profile) => {
      const scan = await inPage(tab.id, () => globalThis.MonoFormPage.scanFiles());
      const boxes = (scan && scan.files) || [];
      if (!boxes.length) return { ok: true, attached: 0, found: 0 };
      let data;
      try {
        data = await root.MonoAsk.request("form.photos", { profile, fields: boxes }, { timeoutMs: FILL_TIMEOUT_MS, idleTimeoutMs: FILL_TIMEOUT_MS });
      } catch (err) {
        await clearMarks(tab.id);
        throw err;
      }
      const list = (data && Array.isArray(data.files) ? data.files : []).filter((f) => f && typeof f.id === "string" && typeof f.data === "string");
      const done = await inPage(tab.id, (fs) => globalThis.MonoFormPage.applyFiles(fs), [list.map(({ id, name, mime, data: d }) => ({ id, name, mime, data: d }))]);
      const attached = (done && done.attached) || 0;
      await askToSubmit(tab.id, attached);
      return { ok: true, attached, found: boxes.length, names: list.map((f) => f.key) };
    });
  }

  const handlers = { form_fill: fill, card_fill: card, files_fill: files };

  /**
   * The command line's way in (`monoagentcli browser fill`, bridge command
   * form_run): the text fields or the photo and scan boxes of one tab. The
   * card is never done from here, only by its own button. A failure throws,
   * which the bridge reports as the command's error.
   */
  async function run(msg) {
    if (!Number.isInteger(msg.tabId)) throw new Error("form_run needs a tab");
    if (msg.what !== "fill" && msg.what !== "files") throw new Error("form_run: what is fill or files");
    const out = await (msg.what === "files" ? files : fill)(msg);
    return out;
  }

  function install() {
    chrome.runtime.onMessage.addListener((msg, sender, respond) => {
      const handler = msg && handlers[msg.type];
      if (!handler) return false;
      // Only this extension's own pages: a web page cannot fill a form or the saved card.
      if (!sender || sender.id !== chrome.runtime.id || !String(sender.url || "").startsWith(chrome.runtime.getURL(""))) return false;
      handler(msg)
        .then(respond)
        .catch((err) => respond({ ok: false, error: err.message || String(err), code: err.code }));
      return true;
    });
  }

  root.MonoForm = { install, fill, card, files, run };
})(globalThis);
