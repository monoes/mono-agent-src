/**
 * MonoAgent Bridge — what the side panel asks the worker to do (CLIP-06/07/08)
 *
 * The panel is a document that can be closed at any moment; the service
 * worker is the only thing that outlives it. So every action with
 * consequences lives here, and sidepanel.js is left doing nothing but
 * drawing.
 *
 * The one piece of timing worth explaining is CLIP-07's. A note is typed
 * *before* a capture is saved, and waiting for someone to finish typing
 * before photographing the page would mean the archive is of whatever the
 * page looked like thirty seconds later — by which time an infinite feed
 * has moved on. So the snapshot starts the moment the note field is
 * touched (`capture_begin`) and waits, finished, while the typing goes on;
 * `capture_commit` stamps the note onto the meta of a capture that was
 * already taken. Nothing about the page's bytes depends on how long
 * someone thought about the sentence.
 *
 * A capture begun this way and never committed is dropped after
 * PENDING_TTL_MS. That is not a violation of CLIP-08's "never silently
 * drop": nobody asked for it to be saved. Only a committed capture is a
 * capture, and from commit onwards MonoCaptureQueue guarantees it.
 */

(function (root) {
  "use strict";

  // A capture the popup started but never committed. Long enough to write
  // a sentence, short enough that a forgotten popup does not pin a
  // multi-megabyte envelope in the worker's memory.
  const PENDING_TTL_MS = 120000;
  const PUSH_TYPE = "page_capture";

  let deps = null;
  let pending = null; // { id, result, startedAt, timer }
  let batch = null; // { cancelled }

  const storage = () => deps.storage || chrome.storage.local;

  function newId() {
    const uuid = globalThis.crypto && crypto.randomUUID && crypto.randomUUID();
    return `ext-${uuid || `${Date.now()}-${Math.random().toString(16).slice(2)}`}`;
  }

  /**
   * send frames one envelope onto the socket. The framing lives in capture.js.
   *
   * Throws when a frame does not reach the wire, which is what lets
   * MonoCaptureQueue tell a delivery from a no-op: background.js's send
   * writes only while the socket is OPEN and returns false otherwise, and a
   * queue that cannot see the difference deletes captures it never sent.
   */
  function send(envelope) {
    const messages = root.MonoCapture.planMessages(
      envelope.id,
      envelope.meta,
      envelope.artifacts,
      envelope.warnings,
      { type: PUSH_TYPE, maxMessageBytes: deps.maxMessageBytes }
    );
    for (const message of messages) {
      if (deps.send(message) === false) throw new Error("the bridge disconnected mid-send");
    }
  }

  // prepare files a capture that names no profile as it is sent, or holds it
  // when no profile exists (capture_profile.js preparer; D2). opts.verify
  // also re-checks a profile it names: a retry may name a deleted one.
  const bridge = (opts) => {
    const P = root.MonoCaptureProfile;
    const prepare = P ? P.preparer(root.MonoAsk, storage(), opts) : null;
    return Object.assign({ send, isConnected: () => deps.isConnected() }, prepare ? { prepare } : {});
  };

  /**
   * capture takes one page, through the injected capture function in tests.
   * A named mode (the panel's Full page / Screenshot / Summary) is expanded
   * into its params here, exactly as the right-click menu expands it.
   */
  async function capture(params) {
    const p = params && params.mode && root.MonoCaptureModes ? root.MonoCaptureModes.paramsFor(params) : params;
    if (deps.capture) return deps.capture(p);
    return root.MonoCapture.pageCapture(p, root.MonoCaptureBridge.context());
  }

  async function activeTabId() {
    const [tab] = await chrome.tabs.query({ active: true, lastFocusedWindow: true });
    return tab && tab.id;
  }

  // --- CLIP-07: capture now, annotate while it happens ---------------------

  function clearPending() {
    if (pending && pending.timer) clearTimeout(pending.timer);
    pending = null;
  }

  /**
   * begin starts a speculative capture of the active tab. Called when the
   * note field is first touched, so the snapshot is of the page the person
   * is looking at, not the page as it is when they stop typing.
   */
  async function begin(options) {
    clearPending();
    const params = Object.assign({}, options || {});
    if (!params.tabId) params.tabId = await activeTabId();
    if (!params.tabId) throw new Error("no active tab");

    const id = newId();
    const record = { id, tabId: params.tabId, mode: params.mode || "", result: null, error: null, startedAt: Date.now() };
    record.promise = capture(params).then(
      (result) => {
        record.result = result;
        return result;
      },
      (err) => {
        record.error = err;
        return null;
      }
    );
    record.timer = setTimeout(() => {
      if (pending === record) pending = null;
    }, PENDING_TTL_MS);
    // Node's timers keep the process alive; the worker's do not care.
    if (record.timer && record.timer.unref) record.timer.unref();
    pending = record;
    return { ok: true, id, tabId: params.tabId };
  }

  /**
   * commit finishes the capture the popup asked for: the pending one when
   * it is the same tab, a fresh one otherwise. From here the capture
   * belongs to the queue, which never loses it.
   */
  async function commit(message) {
    const form = (message && message.form) || {};
    const options = (message && message.options) || {};
    const tabId = options.tabId || (await activeTabId());

    let id;
    let result;
    // A snapshot begun in another mode (the person switched from Full page
    // to Screenshot while typing) is not what they are saving now.
    if (pending && (!tabId || pending.tabId === tabId) && pending.mode === (options.mode || "")) {
      const use = pending;
      clearPending();
      await use.promise;
      if (use.error) throw use.error;
      id = use.id;
      result = use.result;
    } else {
      clearPending();
      id = newId();
      result = await capture(Object.assign({ tabId }, options));
    }

    return finish(id, result, form);
  }

  /**
   * applyProfile settles which profile this capture is filed into.
   *
   * The popup sends `profile` with every save, so a picked profile (or a
   * deliberate "no profile") wins for this save AND becomes the sticky
   * choice for the next one — including the keyboard shortcut, which never
   * opens a popup and reads that same stored value. A caller that sends no
   * `profile` at all (the batch path, a test) inherits the sticky choice
   * without changing it.
   */
  async function applyProfile(meta, form) {
    const profiles = root.MonoCaptureProfile;
    if (!profiles) return "";
    const override = form && Object.prototype.hasOwnProperty.call(form, "profile");
    const id = override
      ? form.profile
      : await profiles.stickyOrAsk(root.MonoAsk, storage(), deps.isConnected());
    profiles.applyToMeta(meta, id);
    if (override) await profiles.remember(storage(), id);
    return meta.profile || "";
  }

  /** finish stamps the form onto a finished capture and hands it to the queue. */
  async function finish(id, result, form) {
    root.MonoCaptureForm.applyToMeta(result.meta, form);
    await root.MonoCaptureForm.remember(storage(), form);
    await applyProfile(result.meta, form);
    // The AI that writes the summary is read at commit, not at the early
    // snapshot, so a choice changed while the note was typed still counts.
    if (root.MonoSummaryAI && result.meta.summarize) {
      root.MonoSummaryAI.applyToMeta(result.meta, await root.MonoSummaryAI.sticky(storage()));
    }

    const envelope = { id, meta: result.meta, artifacts: result.artifacts, warnings: result.warnings };
    const delivery = await root.MonoCaptureQueue.deliver(bridge(), storage(), envelope);
    await root.MonoCaptureQueue.paintBadge(storage());

    return {
      ok: delivery.sent || delivery.queued,
      queued: !!delivery.queued,
      held: !!delivery.held,
      title: result.meta.title,
      note: result.meta.note || null,
      tags: result.meta.tags || [],
      collection: result.meta.collection || null,
      // Filed as it was sent, if it named no profile (D2).
      profile: delivery.profile || result.meta.profile || null,
      warnings: (result.warnings || []).concat(delivery.failed ? [delivery.reason] : []),
      error: delivery.failed ? delivery.reason : undefined,
    };
  }

  // --- CLIP-06: a window, or a group, as one collection --------------------

  /**
   * tabsFor lists what a batch covers. `where` is the side panel's own
   * window and the tab it is showing: a panel stays open while focus moves
   * to other windows, so "the last focused window" can be a different one
   * from the window whose panel the button was pressed in. Absent (a test,
   * an older caller), it falls back to the last focused window as before.
   */
  async function tabsFor(scope, where) {
    const at = where || {};
    const windowQuery = at.windowId ? { windowId: at.windowId } : { lastFocusedWindow: true };
    if (scope === "group") {
      const active = at.tabId
        ? await chrome.tabs.get(at.tabId).catch(() => null)
        : (await chrome.tabs.query(Object.assign({ active: true }, windowQuery)))[0];
      const groupId = active && active.groupId;
      if (groupId === undefined || groupId === null || groupId === -1) {
        throw new Error("this tab is not in a tab group");
      }
      let groupTitle = "";
      try {
        const group = await chrome.tabGroups.get(groupId);
        groupTitle = (group && group.title) || "";
      } catch {
        // tabGroups is unavailable on older Chrome; the date-only name is fine.
      }
      return { tabs: await chrome.tabs.query({ groupId }), scope: { kind: "group", groupTitle } };
    }
    return {
      tabs: await chrome.tabs.query(windowQuery),
      scope: { kind: "window" },
    };
  }

  function announce(state) {
    try {
      const sent = chrome.runtime.sendMessage({ type: "capture_batch_progress", state });
      if (sent && sent.catch) sent.catch(() => {});
    } catch {
      // The popup is closed. The batch carries on regardless — that is the
      // point of running it in the worker.
    }
  }

  async function runBatch(message) {
    if (batch) return { ok: false, error: "a batch capture is already running" };
    // Claimed before the first await: two clicks on the button land in the
    // same task queue, and an async guard would let both through.
    batch = { cancelled: false };
    try {
      const { tabs, scope } = await tabsFor((message && message.scope) || "window", {
        windowId: message && message.windowId,
        tabId: message && message.tabId,
      });
      const form = (message && message.form) || {};
      const plan = root.MonoCaptureBatch.planBatch(tabs, {
        scope,
        collection: form.collection || (message && message.collection),
      });
      const report = await root.MonoCaptureBatch.runBatch(plan, {
        cancelled: () => batch.cancelled,
        onProgress: announce,
        capture: async ({ tabId, collection }) => {
          const result = await capture({ tabId });
          return finish(newId(), result, Object.assign({}, form, { collection }));
        },
      });
      await root.MonoCaptureQueue.paintBadge(storage());
      return { ok: true, report, summary: root.MonoCaptureBatch.summarize(report) };
    } finally {
      batch = null;
    }
  }

  // --- dispatch ------------------------------------------------------------

  const HANDLERS = {
    capture_form_state: async () => {
      const lists = await root.MonoCaptureForm.load(storage());
      const state = await root.MonoCaptureQueue.pending(storage());
      // The profile list comes from the Go side, so it is asked for here
      // (the worker owns the socket) rather than from the popup. It can
      // fail in every ordinary way and still returns something drawable.
      const profile = root.MonoCaptureProfile
        ? await root.MonoCaptureProfile.refresh(root.MonoAsk, storage())
        : { profiles: [], id: "", changed: false, reason: "", offline: true };
      return Object.assign({ ok: true }, lists, state, {
        profiles: profile.profiles,
        profile: profile.id,
        profileName: profile.name || "",
        profileChanged: profile.changed,
        profileReason: profile.reason,
        profilesOffline: profile.offline,
      });
    },
    // The side panel's picker is always on screen, so choosing in it is
    // the choice — remembered now, not at the next save, so the keyboard
    // shortcut agrees with the header the moment it changes.
    capture_profile_set: async (msg) => {
      const profiles = root.MonoCaptureProfile;
      if (!profiles) return { ok: false, error: "profiles are not available" };
      return { ok: true, profile: await profiles.remember(storage(), msg.profile) };
    },
    // The side panel's "AI for summaries" picker (summary_ai.js). The lists
    // come from the bridge, so the worker, which owns the socket, asks.
    summary_ai_state: async (msg) =>
      Object.assign({ ok: true }, await root.MonoSummaryAI.state(root.MonoAsk, storage(), !!msg.live)),
    summary_ai_models: async (msg) =>
      Object.assign({ ok: true }, await root.MonoSummaryAI.modelsFor(root.MonoAsk, storage(), msg.runtime)),
    summary_ai_set: async (msg) =>
      ({ ok: true, choice: await root.MonoSummaryAI.remember(storage(), { runtime: msg.runtime, model: msg.model }) }),
    capture_begin: (msg) => begin(msg.options),
    capture_commit: (msg) => commit(msg),
    capture_discard: async () => {
      clearPending();
      return { ok: true };
    },
    capture_batch: (msg) => runBatch(msg),
    capture_batch_cancel: async () => {
      if (batch) batch.cancelled = true;
      return { ok: true, cancelling: !!batch };
    },
    queue_state: async () => Object.assign({ ok: true }, await root.MonoCaptureQueue.pending(storage())),
    queue_retry: async (msg) => {
      const result = await root.MonoCaptureQueue.retry(bridge({ verify: true }), storage(), msg.key);
      await root.MonoCaptureQueue.paintBadge(storage());
      return Object.assign({ ok: result.ok }, result);
    },
    queue_delete: async (msg) => {
      const result = await root.MonoCaptureQueue.remove(storage(), msg.key);
      await root.MonoCaptureQueue.paintBadge(storage());
      return result;
    },
    queue_clear_failures: async () => {
      const result = await root.MonoCaptureQueue.clearFailures(storage());
      await root.MonoCaptureQueue.paintBadge(storage());
      return result;
    },
  };

  /** handle runs one popup request. Exported so the tests skip chrome.runtime. */
  async function handle(message) {
    const handler = HANDLERS[message && message.type];
    if (!handler) return null;
    try {
      return await handler(message);
    } catch (err) {
      return { ok: false, error: err.message };
    }
  }

  function install(d) {
    deps = d;
    chrome.runtime.onMessage.addListener((msg, sender, respond) => {
      if (!msg || !HANDLERS[msg.type]) return false;
      // Only this extension's own pages (the side panel): a content script on a site must not start or commit a capture.
      if (!sender || sender.id !== chrome.runtime.id || !String(sender.url || "").startsWith(chrome.runtime.getURL(""))) return false;
      handle(msg).then(respond);
      return true; // async response
    });
    // The badge outlives the popup: a worker that just restarted still has
    // to show what is waiting.
    root.MonoCaptureQueue.paintBadge(storage()).catch(() => {});

    // Captures also arrive through the shortcut and the context menu, which
    // go through capture_bridge.js and never touch this file. Watching the
    // queue keys means the count is right however the capture got there.
    if (chrome.storage && chrome.storage.onChanged) {
      chrome.storage.onChanged.addListener((changes, area) => {
        if (area !== "local") return;
        if (!changes[root.MonoCaptureQueue.QUEUE_KEY] && !changes[root.MonoCaptureQueue.FAILURES_KEY]) return;
        root.MonoCaptureQueue.paintBadge(storage()).catch(() => {});
      });
    }
  }

  root.MonoCaptureActions = { install, handle, begin, commit, runBatch, applyProfile, PENDING_TTL_MS };
})(globalThis);
