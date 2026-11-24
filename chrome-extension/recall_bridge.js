/**
 * MonoAgent Bridge — in-page recall wiring (RCL-02, RCL-04, RCL-05)
 *
 * The Chrome half of the three recall stories, kept out of background.js the
 * way capture_bridge.js keeps the capture half out of it. Everything with
 * judgement in it lives elsewhere and is node-tested:
 *
 *   ask.js         the request channel — asking the backend a question
 *   saved.js       RCL-02's debounce, cache and badge policy
 *   highlights.js  RCL-04's model, store and anchor arithmetic
 *
 * What is left here is the wiring those three cannot do for themselves:
 * which Chrome events drive them, and what the popup and the content script
 * are allowed to ask for.
 *
 * background.js calls install() with the socket it owns; the dispatch it
 * adds is one line in `onmessage` and one each in `onopen`/`onclose`.
 */

(function (root) {
  "use strict";

  let deps = null;

  const storage = () => (deps && deps.storage) || chrome.storage.local;

  /**
   * install takes send(frame) / isConnected() from background.js. Everything
   * else it reaches for through the chrome.* APIs the worker already has.
   */
  function install(d) {
    deps = d;
    root.MonoAsk.install({ send: d.send, isConnected: d.isConnected });
    // "Already saved" is asked about the profile captures are being saved
    // into, so every lookup carries it.
    root.MonoSaved.install({
      ask: async (method, params, opts) =>
        root.MonoAsk.request(method, withProfile(params, await stickyProfile()), opts),
      badge: paintBadge,
    });
    registerTabs();
    registerMessages();
    registerCommands();
    registerProfileChanges();
  }

  // ── Which brain a question is about ──────────────────────────────

  // Captures saved into a profile are searchable from that profile only, so
  // every recall question names the profile the side panel is "Saving
  // into", and the backend refuses a question that names none. With no
  // choice stored that is the default profile (capture_profile.js decides).
  // The key is read directly only as a fallback, because this group also
  // runs without capture_profile.js (tests, and a worker that failed to
  // load it), in which case no profile is sent.
  const PROFILE_KEY = "captureProfile";

  function validProfile(id) {
    if (root.MonoCaptureProfile) return root.MonoCaptureProfile.isValidProfileId(id);
    const v = typeof id === "string" ? id.trim() : "";
    return !!v && !v.includes("/") && !v.includes("\\") && !v.includes("..");
  }

  // Every lookup asks for the profile, and a bridge that cannot list profiles
  // would be asked again for each one: concurrent callers share one question,
  // and an answer of "none" is kept for a few seconds.
  const NO_PROFILE_MEMORY_MS = 10000;
  let resolving = null;
  let noProfileUntil = 0;

  /** stickyProfile is the remembered "Saving into" choice, else the default
   *  profile, or "" when there is neither. */
  async function stickyProfile() {
    if (Date.now() < noProfileUntil) return "";
    if (!resolving) {
      resolving = resolveStickyProfile()
        .then((id) => {
          if (!id) noProfileUntil = Date.now() + NO_PROFILE_MEMORY_MS;
          return id;
        })
        .finally(() => {
          resolving = null;
        });
    }
    return resolving;
  }

  async function resolveStickyProfile() {
    try {
      const P = root.MonoCaptureProfile;
      if (P && P.stickyOrAsk) {
        const chosen = await P.stickyOrAsk(root.MonoAsk, storage(), !!(deps && deps.isConnected && deps.isConnected()));
        return validProfile(chosen) ? chosen.trim() : "";
      }
      const got = (await storage().get(PROFILE_KEY)) || {};
      const id = got[PROFILE_KEY];
      return validProfile(id) ? id.trim() : "";
    } catch {
      return "";
    }
  }

  /** profileFor prefers the profile a message names (the side panel sends
   *  the one it shows, "" included for the shared inbox) over the stored
   *  one, which is what the badge path and older panels rely on. */
  async function profileFor(msg) {
    if (msg && typeof msg.profile === "string") {
      return validProfile(msg.profile) ? msg.profile.trim() : "";
    }
    return stickyProfile();
  }

  /** withProfile adds `profile` to a request's params when there is one;
   *  no profile leaves the params exactly as they were. */
  function withProfile(params, profile) {
    return profile ? Object.assign({}, params, { profile }) : params;
  }

  /** A new "Saving into" choice changes every "already saved" answer: the
   *  cache belongs to the old profile, so it goes, and the tab in front is
   *  asked again. So does a new profile list: the profile those answers were
   *  about may be gone (capture_profile.js recheck). */
  const PROFILES_KEY = "captureProfilesCache";
  function registerProfileChanges() {
    if (!chrome.storage || !chrome.storage.onChanged) return;
    chrome.storage.onChanged.addListener((changes, area) => {
      if (area !== "local" || !(changes[PROFILE_KEY] || changes[PROFILES_KEY])) return;
      noProfileUntil = 0;
      root.MonoSaved.reset();
      checkActiveTab();
    });
  }

  /**
   * paintBadge is RCL-02's badge, scoped to the tab it describes. The
   * capture badge (capture_bridge.js) is global and transient; a per-tab
   * value wins over it for that one tab, which is why MonoSaved clears its
   * own badge before a capture and repaints after.
   */
  function paintBadge(spec) {
    const target = { tabId: spec.tabId };
    chrome.action.setBadgeText(Object.assign({ text: spec.text }, target)).catch(() => {});
    if (spec.text) {
      chrome.action.setBadgeBackgroundColor(Object.assign({ color: spec.color }, target)).catch(() => {});
    }
    chrome.action
      .setTitle(Object.assign({ title: spec.title || "MonoAgent Bridge" }, target))
      .catch(() => {});
  }

  // ── The socket's edges ───────────────────────────────────────────

  /** connected is called from ws.onopen: find out what this backend can
   *  answer, and re-check whatever tab the user is looking at. */
  function connected() {
    root.MonoAsk.probe().catch(() => {});
    checkActiveTab();
  }

  /** disconnected is called from ws.onclose. Every question in flight is
   *  settled — a panel waiting on a promise nobody will ever settle spins
   *  forever — and the badges go, because "saved" is no longer something
   *  this extension knows. */
  function disconnected(reason) {
    root.MonoAsk.disconnected(reason);
  }

  async function checkActiveTab() {
    try {
      const [tab] = await chrome.tabs.query({ active: true, lastFocusedWindow: true });
      if (tab && tab.id && tab.url) root.MonoSaved.check(tab.id, tab.url);
    } catch {
      // No tab to check (a window closing, the worker starting) — nothing
      // to do and nothing to report.
    }
  }

  // ── Chrome events ────────────────────────────────────────────────

  function registerTabs() {
    if (!chrome.tabs || !chrome.tabs.onUpdated) return;

    // A navigation fires this several times. MonoSaved debounces; this
    // only has to avoid asking about a tab that has no URL yet.
    chrome.tabs.onUpdated.addListener((tabId, change, tab) => {
      const url = change.url || (change.status === "complete" && tab && tab.url);
      if (!url) return;
      root.MonoSaved.check(tabId, url);
      restoreHighlights(tabId, url);
    });

    // Switching tabs is not a navigation, so the answer is almost always
    // cached; this is what repaints the badge for the tab now in front.
    chrome.tabs.onActivated.addListener(async ({ tabId }) => {
      try {
        const tab = await chrome.tabs.get(tabId);
        if (tab && tab.url) root.MonoSaved.check(tabId, tab.url);
      } catch {
        // The tab went away between the event and the lookup.
      }
    });

    chrome.tabs.onRemoved.addListener((tabId) => root.MonoSaved.forget(tabId));
  }

  /** restoreHighlights hands a freshly-loaded page its stored highlights.
   *  The content script also asks for them on load; this covers a same-page
   *  navigation, where it never runs again. */
  async function restoreHighlights(tabId, url) {
    // Signed out nothing is painted (gate.js); the highlights stay stored.
    // Fail closed: without a signedIn dependency there is no proof of a sign-in.
    if (!deps || typeof deps.signedIn !== "function" || !deps.signedIn()) return;
    const records = await root.MonoHighlights.load(storage(), url);
    if (!records.length) return;
    try {
      await chrome.tabs.sendMessage(tabId, { type: "highlight_restore", records });
    } catch {
      // No content script in that tab (a PDF viewer, the web store, a page
      // that loaded before the extension). Nothing is painted; nothing is
      // lost — the highlights are still stored.
    }
  }

  function registerCommands() {
    if (!chrome.commands) return;
    chrome.commands.onCommand.addListener(async (command) => {
      if (command !== "highlight-selection") return;
      try {
        const [tab] = await chrome.tabs.query({ active: true, lastFocusedWindow: true });
        if (tab && tab.id) await chrome.tabs.sendMessage(tab.id, { type: "highlight_selection" });
      } catch (err) {
        console.error("[monoagent] highlight shortcut failed:", err.message);
      }
    });
  }

  // ── What the popup and the page may ask for ──────────────────────

  // Handlers keyed by message type. Each returns a promise; the dispatch
  // below turns a rejection into {ok: false, error} rather than letting it
  // become a dropped sendMessage the caller waits out.
  const handlers = {
    // RCL-02 ---------------------------------------------------------
    saved_get: async (msg, sender) => {
      const tabId = msg.tabId || (sender.tab && sender.tab.id);
      const record = root.MonoSaved.get(tabId);
      if (record) return { ok: true, record, source: "cache" };
      // The popup opened before the navigation lookup finished, or after
      // its cache entry expired. Ask now, without the debounce.
      const tab = tabId ? await chrome.tabs.get(tabId).catch(() => null) : null;
      if (!tab || !tab.url) return { ok: true, record: null };
      const result = await root.MonoSaved.check(tabId, tab.url, { debounceMs: 0, force: msg.force });
      return { ok: true, record: result.record, source: result.source };
    },

    saved_open: async (msg) => {
      // "Open the archived copy". A local path is not something the popup
      // can navigate to, so the worker does it.
      const path = String(msg.path || "");
      if (!path) return { ok: false, error: "no archived copy to open" };
      const url = path.startsWith("file://") ? path : `file://${path}`;
      await chrome.tabs.create({ url });
      return { ok: true };
    },

    // RCL-05 ---------------------------------------------------------
    ask_brain: async (msg) => {
      const answer = await root.MonoAsk.request(
        "doc.ask",
        withProfile({ q: msg.q, limit: msg.limit || 4 }, await profileFor(msg)),
        {
          timeoutMs: 40000,
          idleTimeoutMs: 15000,
          // Relayed to whoever is watching. The popup is a document that
          // can close at any moment, so a failed post is not an error.
          onProgress: (progress) =>
            chrome.runtime.sendMessage({ type: "ask_progress", progress }).catch(() => {}),
        }
      );
      return { ok: true, answer };
    },

    ask_related: async (msg) => ({
      ok: true,
      related: await root.MonoAsk.request(
        "doc.related",
        withProfile({ url: msg.url, limit: msg.limit || 3 }, await profileFor(msg))
      ),
    }),

    ask_ready: async () => ({ ok: true, methods: await root.MonoAsk.probe() }),

    // RCL-04 ---------------------------------------------------------
    highlight_list: async (msg, sender) => ({
      ok: true,
      records: await root.MonoHighlights.load(storage(), pageUrl(msg, sender)),
    }),

    highlight_add: async (msg, sender) => {
      const url = pageUrl(msg, sender);
      const { record, records } = await root.MonoHighlights.add(storage(), url, msg.highlight);
      return { ok: !!record, record, count: records.length };
    },

    highlight_update: async (msg, sender) => {
      const { record } = await root.MonoHighlights.update(
        storage(),
        pageUrl(msg, sender),
        msg.id,
        msg.patch
      );
      return { ok: !!record, record };
    },

    highlight_remove: async (msg, sender) => {
      const { removed, records } = await root.MonoHighlights.remove(
        storage(),
        pageUrl(msg, sender),
        msg.id
      );
      return { ok: removed, count: records.length };
    },

    highlight_pages: async () => ({ ok: true, pages: await root.MonoHighlights.listPages(storage()) }),
  };

  /** pageUrl prefers the sender tab's own URL over whatever the message
   *  claims: a content script's idea of where it is beats a popup's. */
  function pageUrl(msg, sender) {
    if (sender && sender.tab && sender.tab.url) return sender.tab.url;
    return msg.url || "";
  }

  // Messages only this extension's own pages (the side panel) may send. The
  // highlight_* ones a content script on a page legitimately sends, and
  // saved_get / ask_brain / ask_related are still open to any sender here
  // (their tests send with no sender): see the report on r04-04.
  const PANEL_ONLY = new Set(["saved_open", "ask_ready", "highlight_pages"]);

  /** fromExtensionPage: sent by one of this extension's own pages, not a content script on a site. */
  function fromExtensionPage(sender) {
    return !!sender && sender.id === chrome.runtime.id && String(sender.url || "").startsWith(chrome.runtime.getURL(""));
  }

  function registerMessages() {
    chrome.runtime.onMessage.addListener((msg, sender, respond) => {
      if (!msg || typeof msg.type !== "string") return false;
      const handler = handlers[msg.type];
      if (!handler) return false;
      if (PANEL_ONLY.has(msg.type) && !fromExtensionPage(sender)) return false;
      handler(msg, sender)
        .then((result) => respond(result))
        .catch((err) => respond({ ok: false, error: err.message || String(err), code: err.code }));
      return true; // async response
    });
  }

  // ── The capture hook (RCL-04) ────────────────────────────────────

  /**
   * highlightsArtifact is handed to the capture context, so every capture
   * of a page that has highlights carries them as `highlights.json`. Null
   * when there are none — an empty artifact in every envelope would be
   * noise in the inbox and a false signal to the ingest side.
   */
  async function highlightsArtifact(url) {
    try {
      const records = await root.MonoHighlights.load(storage(), url);
      return root.MonoHighlights.artifact(url, records);
    } catch {
      return null; // a capture must never fail because of its highlights
    }
  }

  /** capturedTab is called after a capture lands, so the badge stops saying
   *  "not saved" about a page that now is. */
  function capturedTab(tabId, url) {
    if (!tabId || !url) return;
    root.MonoSaved.capturedNow(tabId, url).catch(() => {});
  }

  root.MonoRecall = {
    install,
    connected,
    disconnected,
    highlightsArtifact,
    capturedTab,
    checkActiveTab,
  };
})(globalThis);
