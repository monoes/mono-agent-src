/**
 * MonoAgent Bridge — the chat's worker half (issue #451, slice 2)
 *
 * The side panel is a document that can close at any moment, so the long
 * request (`chat.send`, minutes) is made here, over ask.js, and the panel is
 * only told about it: progress frames are relayed as `chat_progress`
 * messages that nobody has to be listening to. Whatever a closed panel
 * misses it gets back from `chat.events` when it reopens (chat_core.js).
 *
 * Messages the panel may send (all answered {ok, ...}):
 *   chat_ready         {state: "ready" | "offline" | "update"}
 *   chat_page_context  {context}  url, title, readable text, selection of the
 *                      panel's active tab, capped. The page is untrusted; it is
 *                      read as data and nothing in it is ever run.
 *   chat_send          {data}     {conversation, turn, text} when the turn ends;
 *                      `images` ([{mime,data}], chat_look.js) go with it, re-checked here
 *   chat_stop, chat_events
 *   chat_ui_action     {conversationId, action}  an A2UI button press -> chat.ui_action -> {ok, outcome, delivered?, message?, reason?}
 *   chat_ui_sync       {conversationId}          surface/resolve pending approvals -> chat.ui_sync -> {ok, surfaced, resolved}
 *   chat_ui_journal    {conversationId, seen}    cards written outside a running turn -> chat.ui_journal -> {ok, turns, more}
 *
 * Runtime and model are the side panel's "AI for summaries" choice
 * (summary_ai.js), read from storage here so the panel and the right-click
 * menus keep one picker.
 *
 * After ask.js, chat_core.js and summary_ai.js; recall_bridge.js installs
 * ask.js itself, so install() here never does.
 */

(function (root) {
  "use strict";

  const Core = () => root.MonoChatCore;
  const CONTEXT_TIMEOUT_MS = 8000;
  const THIN_TEXT = 250; // below this the article reader found little: try every frame, forms included
  const CAPTIONS_TIMEOUT_MS = 20000; // a caption track, and the mobile-client retry when the first comes back empty
  const PAGE_FILES =["markdown.js", "readable.js"];

  let deps = null;
  // One turn at a time per profile: a second send while one is running would
  // interleave two replies in one conversation.
  const sending = new Set();

  const storage = () => (deps && deps.storage) || chrome.storage.local;
  const profileOf = (msg) => (Core().validProfile(msg && msg.profile) ? msg.profile.trim() : "");
  const withProfile = (params, profile) => (profile ? Object.assign({}, params, { profile }) : params);

  // These decide approvals or read the cards: only the side panel may ask.
  const PANEL_ONLY = new Set(["chat_ui_action", "chat_ui_sync", "chat_ui_journal"]);

  /** fromPanel: this extension's side panel (Chromium names it by id and url only). */
  function fromPanel(sender) {
    if (!sender || !chrome.runtime.id || sender.id !== chrome.runtime.id) return false;
    const panel = chrome.runtime.getURL("sidepanel.html");
    const url = String(sender.url || "");
    return !!panel && (url === panel || url.startsWith(`${panel}?`) || url.startsWith(`${panel}#`));
  }

  function install(d) {
    deps = d || {};
    chrome.runtime.onMessage.addListener((msg, sender, respond) => {
      if (!msg || typeof msg.type !== "string") return false;
      const handler = handlers[msg.type];
      if (!handler || (PANEL_ONLY.has(msg.type) && !fromPanel(sender))) return false;
      handler(msg)
        .then((result) => respond(result))
        .catch((err) => respond({ ok: false, error: err.message || String(err), code: err.code }));
      return true;
    });
  }

  /** The page, read inside the tab. Serialized by executeScript: closes over nothing. */
  function readPage() {
    const R = globalThis.MonoReadable;
    let text = "";
    try {
      const tree = R.snapshot(document.documentElement, globalThis);
      text = R.extract(tree, { baseUrl: document.baseURI || location.href }).text || "";
    } catch {
      text = "";
    }
    let selection = "";
    try {
      selection = String(globalThis.getSelection ? globalThis.getSelection() : "");
    } catch {
      selection = "";
    }
    return { url: location.href, title: document.title, text, selection };
  }

  /**
   * The fallback reader: what a person sees, form controls and all. Run in
   * every frame; walks open and closed shadow roots. Serialized: closes over nothing.
   */
  function readVisible() {
    const out = [];
    const walk = (node) => {
      // chrome.dom reaches closed shadow roots too; a page cannot hide a form from its own reader.
      const root = (globalThis.chrome && chrome.dom && chrome.dom.openOrClosedShadowRoot ? chrome.dom.openOrClosedShadowRoot(node) : null) || node.shadowRoot;
      if (root) out.push(Array.from(root.children).map((c) => c.innerText || "").join("\n"));
      for (const el of node.children || []) walk(el);
    };
    let text = "";
    try {
      text = document.body ? document.body.innerText || "" : "";
      if (document.body) walk(document.body);
    } catch {
      /* keep what was read */
    }
    return { url: location.href, text: [text].concat(out).join("\n").trim() };
  }

  /** Runs `func` in the page's own world (YouTube's globals and its cookies live there). */
  async function mainWorld(tabId, func, args) {
    const results = await chrome.scripting.executeScript({ target: { tabId }, world: "MAIN", func, args });
    if (!results || !results.length) throw new Error("the page did not answer");
    return results[0].result;
  }

  /** Serialized into the page: closes over nothing (same as capture_bridge.js). */
  async function pageFetchText(url, init) {
    const res = await fetch(url, Object.assign({ credentials: "include" }, init || {}));
    return { status: res.status, text: await res.text() };
  }

  /**
   * The video summary's own reader (youtube_transcript.js collect), reused:
   * the video's record and its caption track, fetched from the person's tab
   * with the requests the player itself makes. Never throws; when there are
   * no captions, or they do not come in time, `transcript` is "" and the
   * server tells the model it is unavailable. The cues become plain text
   * within the size cap (Core().buildTranscript).
   */
  async function readCaptions(tab) {
    const V = root.MonoYouTubeVideo;
    const T = root.MonoYouTubeTranscript;
    const out = { video: true, video_id: V.videoIdOf(tab.url), transcript: "" };
    if (!T) return out;
    const limit = (deps && deps.captionsTimeoutMs) || CAPTIONS_TIMEOUT_MS;
    let timer = null;
    try {
      const run = T.collect({
        url: tab.url,
        readPage: () => mainWorld(tab.id, V.readPageState, []),
        fetchText: (url, init) => mainWorld(tab.id, pageFetchText, [url, init || null]),
      });
      run.catch(() => {});
      const timeout = new Promise((_, reject) => {
        timer = setTimeout(() => reject(new Error("the captions did not come")), limit);
      });
      const got = await Promise.race([run, timeout]);
      const f = (got && got.fields) || {};
      out.channel = f.channel || "";
      out.description = f.description || "";
      out.transcript = Core().buildTranscript(got && got.transcript && got.transcript.cues);
    } catch {
      // Not fatal: the page text is still sent.
    } finally {
      clearTimeout(timer);
    }
    return out;
  }

  async function targetTab(msg) {
    const query = Number.isInteger(msg.windowId)
      ? { active: true, windowId: msg.windowId }
      : { active: true, lastFocusedWindow: true };
    const [tab] = await chrome.tabs.query(query);
    return tab || null;
  }

  /**
   * The page changed under the chip: `expect` is the tab id and address the
   * panel showed the person. Comparing addresses ignores query and fragment,
   * which are never sent anyway.
   */
  const sameAddress = (a, b) => {
    const x = Core().stripUrl(a);
    if (!x || x !== Core().stripUrl(b)) return false;
    // Two videos share /watch: the id in the query tells them apart.
    const V = root.MonoYouTubeVideo;
    return !V || V.videoIdOf(a) === V.videoIdOf(b);
  };
  const pageChanged = () => ({
    ok: false,
    code: "page_changed",
    error: "The page changed after you wrote this: it is not the one shown above the message. Nothing was sent.",
  });
  const movedFrom = (expect, tab) =>
    !!expect && (typeof expect !== "object" || expect.tabId !== tab.id || !sameAddress(tab.url, expect.url));

  // A frame from another origin is content the page does not control: it can carry instructions meant for the model.
  const FOREIGN_FRAME_CHARS = 2000;
  const originOf = (u) => {
    try {
      return new URL(u).origin;
    } catch {
      return "";
    }
  };

  /**
   * Every frame's visible text, the page's own first; `fallback` when none can
   * be read. Frames of another origin are capped and labelled as untrusted.
   */
  async function readFrames(tabId, fallback, pageUrl) {
    try {
      const results = await chrome.scripting.executeScript({ target: { tabId, allFrames: true }, func: readVisible });
      const home = originOf(pageUrl);
      const parts = (results || [])
        .map((r) => {
          const text = (r && r.result && r.result.text) || "";
          if (!text) return "";
          const there = originOf(r.result.url);
          if (!home || there === home) return text;
          // Nothing inside the frame may close the fence early or open another one.
          const body = text.slice(0, FOREIGN_FRAME_CHARS).replace(/\[\s*(?:\/\s*frame\s+content|frame\s+origin\s*:)/gi, "(");
          return `[frame origin: ${there || "unknown"} \u2014 untrusted frame content]\n${body}\n[/frame content]`;
        })
        .filter(Boolean);
      const joined = parts.join("\n\n");
      return joined.length > (fallback || "").length ? joined : fallback || "";
    } catch {
      return fallback || "";
    }
  }

  async function pageContext(msg) {
    const tab = await targetTab(msg);
    if (!tab || !tab.id) return { ok: true, context: null };
    if (movedFrom(msg.expect, tab)) return pageChanged();
    const tabInfo = { id: tab.id, url: Core().stripUrl(tab.url) };
    // What the tab itself says is the fallback: a page we cannot read (the
    // web store, a PDF viewer, chrome://) still has an address and a title.
    const fallback = Core().buildContext({ url: tab.url, title: tab.title, text: "", selection: "" });
    let timer = null;
    try {
      await chrome.scripting.executeScript({ target: { tabId: tab.id }, files: PAGE_FILES });
      const run = chrome.scripting.executeScript({ target: { tabId: tab.id }, func: readPage });
      const timeout = new Promise((_, reject) => {
        timer = setTimeout(() => reject(new Error("the page did not answer")), CONTEXT_TIMEOUT_MS);
      });
      const [res] = await Promise.race([run, timeout]);
      // The page may have navigated by itself (history API) since the tab
      // was queried: what was read must still be the page that was shown.
      if (msg.expect && res && res.result && !sameAddress(res.result.url, msg.expect.url)) return pageChanged();
      let raw = res && res.result;
      if (raw && (raw.text || "").length < THIN_TEXT) raw = Object.assign({}, raw, { text: await readFrames(tab.id, raw.text, tab.url) });
      if (raw && root.MonoYouTubeVideo && root.MonoYouTubeVideo.isVideoUrl(tab.url)) {
        raw = Object.assign({}, raw, await readCaptions(tab));
        // The captions took a while: they belong to the page that was shown.
        const now = await chrome.tabs.get(tab.id).catch(() => null);
        if (!now || !sameAddress(now.url, tab.url)) return pageChanged();
      }
      return { ok: true, context: Core().buildContext(raw) || fallback, tab: tabInfo };
    } catch {
      return { ok: true, context: fallback, tab: tabInfo };
    } finally {
      clearTimeout(timer);
    }
  }

  async function ready() {
    if (!deps || !deps.isConnected || !deps.isConnected()) return { ok: true, state: "offline" };
    const methods = await root.MonoAsk.probe();
    if (root.MonoAsk.known() === null) return { ok: true, state: "offline" };
    return { ok: true, state: methods.indexOf(Core().METHOD_SEND) !== -1 ? "ready" : "update" };
  }

  async function choice() {
    try {
      return (await root.MonoSummaryAI.sticky(storage())) || {};
    } catch {
      return {};
    }
  }

  async function send(msg) {
    const message = Core().checkMessage(msg.message);
    if (!message) throw new Error("there is nothing to send");
    const profile = profileOf(msg);
    const key = profile || "_";
    if (sending.has(key)) throw new Error("a reply is still being written");

    const params = { message };
    if (typeof msg.conversation === "string" && msg.conversation) params.conversation = msg.conversation;
    const ai = await choice();
    if (ai.runtime) params.runtime = ai.runtime;
    if (ai.runtime && ai.model) params.model = ai.model;
    // Re-capped here whatever the panel sent: the cap is the worker's rule.
    const context = Core().buildContext(msg.context);
    if (context) params.context = context;

    // Pictures: the worker's rule is chat_look.js's, whatever the panel sent.
    if (!root.MonoChatLook && msg.images) throw new Error("this extension cannot send pictures; reload it");
    const images = root.MonoChatLook ? root.MonoChatLook.checkImages(msg.images) : [];
    if (images.length) params.images = images;

    sending.add(key);
    let remembered = false;
    try {
      const data = await root.MonoAsk.request("chat.send", withProfile(params, profile), {
        timeoutMs: 600000,
        idleTimeoutMs: 60000,
        onProgress: (progress) => {
          // The conversation may be named before the turn ends (the progress
          // wrapper's `conversation`); keep it even if nobody is listening
          // (a closed panel), once.
          if (!remembered) {
            const ev = Core().parseProgress(progress);
            const c = ev && (ev.conversation || ev.payload.conversation || ev.payload.conversation_id);
            if (typeof c === "string" && c) {
              remembered = true;
              Core().rememberConversation(storage(), profile, c);
            }
          }
          chrome.runtime.sendMessage({ type: "chat_progress", tag: msg.tag || "", progress }).catch(() => {});
        },
      });
      // Persisted here, not by the panel: it may have been closed on a
      // first turn, and the next one must continue the same conversation.
      await Core().rememberConversation(storage(), profile, data && data.conversation);
      return { ok: true, data: data || {} };
    } finally {
      sending.delete(key);
    }
  }

  const handlers = {
    chat_ready: ready,
    chat_page_context: pageContext,
    chat_send: send,
    chat_stop: async (msg) => {
      const params = { conversation: String(msg.conversation || "") };
      if (!params.conversation) throw new Error("there is no conversation to stop");
      await root.MonoAsk.request("chat.stop", withProfile(params, profileOf(msg)), { timeoutMs: 15000, idleTimeoutMs: 15000 });
      return { ok: true };
    },
    chat_events: async (msg) => {
      const conversation = String(msg.conversation || "");
      if (!conversation) throw new Error("there is no conversation to read");
      const params = { conversation };
      // seq is per turn, so after_seq only means something with its turn.
      if (msg.turn !== undefined && msg.turn !== null && msg.turn !== "") params.turn = msg.turn;
      if (Number.isFinite(msg.after_seq) && msg.after_seq > 0 && params.turn !== undefined) params.after_seq = msg.after_seq;
      const data = await root.MonoAsk.request("chat.events", withProfile(params, profileOf(msg)), { timeoutMs: 20000, idleTimeoutMs: 20000 });
      const turn = data && data.turn !== undefined && data.turn !== null ? String(data.turn) : "";
      return { ok: true, turn, events: Core().normalizeEvents(data && data.events, turn), turn_active: !!(data && data.turn_active) };
    },
  };

  // A2UI surfaces (a2ui_render.js): the press of a button, and the approvals waiting in the conversation.
  const uiCall = async (method, msg, extra, ms = 20000) => {
    const conversation = String(msg.conversationId || msg.conversation || "");
    if (!conversation) throw new Error("there is no conversation");
    const params = Object.assign({ conversation }, extra);
    const data = await root.MonoAsk.request(method, withProfile(params, profileOf(msg)), { timeoutMs: ms, idleTimeoutMs: ms });
    return Object.assign({}, data && typeof data === "object" ? data : {}, { ok: true });
  };
  handlers.chat_ui_action = (msg) => {
    if (!msg.action || typeof msg.action !== "object") return Promise.reject(new Error("there is no action to send"));
    // Longer than the bridge waits for a press before it answers "in progress".
    return uiCall("chat.ui_action", msg, { action: msg.action }, 35000);
  };
  handlers.chat_ui_sync = (msg) => uiCall("chat.ui_sync", msg, {});
  handlers.chat_ui_journal = async (msg) => {
    const seen = (Array.isArray(msg.seen) ? msg.seen : []).filter((id) => typeof id === "string").slice(-200);
    const r = await uiCall("chat.ui_journal", msg, { seen });
    const turns = (Array.isArray(r.turns) ? r.turns : []).map((t) => {
      const turn = String((t && t.turn) || "");
      return { turn, events: Core().normalizeEvents(t && t.events, turn) };
    });
    return { ok: true, turns: turns.filter((t) => t.turn), more: !!r.more };
  };

  root.MonoChat = { install, readPage, handlers };
})(globalThis);
