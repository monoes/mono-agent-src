/**
 * MonoAgent Bridge — the chat in the side panel (issue #451, slice 2)
 *
 * Thin on purpose, like the rest of the panel. The judgement is in
 * chat_core.js (events, storage, caps) and chat_render.js (safe Markdown);
 * the long request runs in the worker (chat_bridge.js), which relays
 * progress as `chat_progress` messages. This file draws, and keeps the
 * promises a panel has to keep:
 *
 *   - Closing and reopening it loses nothing. The transcript and the last
 *     seen event number are stored per profile; on open it asks the backend
 *     for what happened after that (`chat.events`) and, while a turn is
 *     still running there, keeps asking until it ends.
 *   - The live stream and a replay overlap, and a repeated event is dropped
 *     by its number (chat_core applyEvent), so neither can double a reply.
 *   - An older MonoAgent that has no chat says so, once, in the section,
 *     instead of the panel breaking.
 *
 * After sidepanel.js: it uses that file's `ask`, `profiles` and `page`, and
 * redraws on the `panel:profiles`, `panel:page` and `panel:bridge-up`
 * events it sends. The profile is the header's; runtime and model are the
 * "AI for summaries" choice, which the worker reads itself.
 */

(function () {
  "use strict";

  const Core = globalThis.MonoChatCore;
  const Render = globalThis.MonoChatRender;
  const el = (id) => document.getElementById(id);

  const log = el("chat-log");
  const empty = el("chat-empty");
  const input = el("chat-input");
  const sendBtn = el("chat-send");
  const stopBtn = el("chat-stop");
  const newBtn = el("chat-new");
  const fillBtn = el("chat-fill");
  const summarizeBtn = el("chat-summarize");
  const pageChip = el("chat-page");
  const notice = el("chat-notice");
  const stateEl = el("chat-state");

  const SUMMARIZE_PROMPT = "Summarize this page.";
  // "fill the form" typed in the box does what the Fill form button does: the
  // chat's own model has no access to the profile, so it would only refuse.
  const FILL_INTENT = /^\s*(please\s+)?(auto-?)?fill(\s+(in|out|up))?\s+(the|this|my)?\s*(form|page|application)\b/i;
  const POLL_MS = 1500;
  const NOTICES = {
    offline: "MonoAgent isn't connected, so chat is unavailable. It comes back when the bridge does.",
    update: "This MonoAgent is too old for chat. Update MonoAgent, then reopen this panel.",
  };

  let profile = null; // the profile the transcript on screen belongs to
  let chat = Core.newState(null);
  let availability = "checking"; // checking | ready | offline | update
  let active = null; // { tag } while a send from this panel is running
  let remote = false; // a turn is running in the backend that this panel did not start
  let pollTimer = null;
  let loadSeq = 0;
  // Sharing the page is on for the first message of a conversation only: a
  // later message goes out without a page unless the chip is turned on again,
  // so switching tabs mid-conversation never sends the new page unnoticed.
  let usePage = true;
  let refusal = ""; // why the last send was not made (the page changed under the chip)
  let drawQueued = false;
  const nodes = new Map(); // message id -> { root, sig }

  const currentProfile = () => (typeof profiles !== "undefined" && profiles && profiles.current ? profiles.current.id || "" : "");
  const pageNow = () => (typeof page !== "undefined" && page ? page : null);
  const pageShareable = () => {
    const p = pageNow();
    return !!(p && p.tabId && /^(https?|file):\/\//i.test(p.url || ""));
  };
  const busy = () => !!active || remote;

  // ── drawing ──────────────────────────────────────────────────────

  function signature(m) {
    return `${m.role}|${m.text.length}|${m.open ? 1 : 0}|${(m.tools || []).map((t) => t.status).join(",")}`;
  }

  function buildMessage(m) {
    const box = document.createElement("div");
    box.className = "chat-msg";
    box.dataset.role = m.role;
    if (m.error) box.dataset.error = "true";
    if (m.role === "assistant") {
      if (m.tools.length) {
        const row = document.createElement("div");
        row.className = "chat-tool-row";
        for (const t of m.tools) {
          const chip = document.createElement("span");
          chip.className = "chat-tool";
          chip.dataset.status = t.status;
          chip.textContent = t.status === "failed" ? `${t.name} failed` : t.name;
          row.appendChild(chip);
        }
        box.appendChild(row);
      }
      if (m.text) box.appendChild(Render.toDom(document, Render.parse(m.text)));
      else if (m.open && !m.tools.length) box.appendChild(document.createTextNode("Thinking…"));
    } else if (m.role === "user") {
      const text = document.createElement("div");
      text.className = "chat-text";
      text.textContent = m.text;
      box.appendChild(text);
      if (m.page) {
        const about = document.createElement("span");
        about.className = "chat-about";
        about.textContent = `Using this page: ${m.page}`;
        box.appendChild(about);
      }
    } else {
      box.textContent = m.text;
    }
    return box;
  }

  function drawLog() {
    const nearBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 40;
    const keep = new Set(chat.messages.map((m) => m.id));
    for (const [id, n] of nodes) {
      if (!keep.has(id)) {
        n.root.remove();
        nodes.delete(id);
      }
    }
    // `empty` is always the first child; messages follow it in order.
    let cursor = empty.nextSibling;
    for (const m of chat.messages) {
      let n = nodes.get(m.id);
      const sig = signature(m);
      if (!n) {
        n = { root: buildMessage(m), sig };
        nodes.set(m.id, n);
      } else if (n.sig !== sig) {
        const fresh = buildMessage(m);
        n.root.replaceWith(fresh);
        n = { root: fresh, sig };
        nodes.set(m.id, n);
      }
      if (cursor === n.root) cursor = cursor.nextSibling;
      else log.insertBefore(n.root, cursor);
    }
    empty.hidden = chat.messages.length > 0;
    log.setAttribute("aria-busy", busy() ? "true" : "false");
    if (nearBottom) log.scrollTop = log.scrollHeight;
  }

  function scheduleDraw() {
    if (typeof requestAnimationFrame !== "function") return drawLog();
    if (drawQueued) return;
    drawQueued = true;
    requestAnimationFrame(() => {
      drawQueued = false;
      drawLog();
    });
  }

  function drawControls() {
    const ready = availability === "ready";
    const shown = NOTICES[availability] || refusal;
    notice.hidden = !shown;
    notice.textContent = shown || "";
    stateEl.textContent = busy() ? "Replying…" : "";
    input.disabled = !ready;
    sendBtn.disabled = !ready || busy();
    sendBtn.hidden = busy();
    stopBtn.hidden = !busy();
    stopBtn.disabled = !chat.conversation && !active;
    summarizeBtn.disabled = !ready || busy() || !pageShareable();
    fillBtn.disabled = !ready || busy() || filling || !pageShareable();
    newBtn.disabled = busy() || (!chat.messages.length && !chat.conversation);
    const shareable = pageShareable();
    pageChip.disabled = !shareable;
    pageChip.setAttribute("aria-pressed", String(shareable && usePage));
    const title = (pageNow() && pageNow().title) || "";
    pageChip.textContent = !shareable ? "No page to share" : usePage ? `Using this page: ${title}${pageNow() && Core.isVideoPage(pageNow().url) ? " (with captions, if available)" : ""}` : "Not sharing this page";
    pageChip.title = shareable ? (usePage ? "Click to leave this page out of the next message" : "Click to share this page with the next message") : "";
  }

  function draw() {
    scheduleDraw();
    drawControls();
  }

  // ── storage and the backend ──────────────────────────────────────

  const save = () => Core.saveState(chrome.storage.local, profile, chat);

  /** applyAll folds events in and tells whether anything changed. */
  function applyAll(events) {
    let changed = false;
    for (const ev of events) if (Core.applyEvent(chat, ev)) changed = true;
    return changed;
  }

  async function sync() {
    const mine = loadSeq;
    // Nothing to ask about, or no answer: whatever was mid-stream when the
    // panel closed is over as far as this panel can tell.
    const giveUp = () => {
      remote = false;
      Core.closeOpen(chat);
      draw();
    };
    if (!chat.conversation) return giveUp();
    const answer = await ask({ type: "chat_events", profile, conversation: chat.conversation, after_seq: chat.lastSeq, turn: chat.turn });
    if (mine !== loadSeq || active) return;
    if (!answer.ok) return giveUp();
    if (applyAll(answer.events || [])) save();
    remote = !!answer.turn_active;
    if (!remote) {
      Core.closeOpen(chat);
      save();
    }
    draw();
    clearTimeout(pollTimer);
    if (remote) pollTimer = setTimeout(sync, POLL_MS);
  }

  async function load() {
    const mine = ++loadSeq;
    clearTimeout(pollTimer);
    profile = currentProfile();
    active = null;
    remote = false;
    chat = await Core.loadState(chrome.storage.local, profile);
    if (mine !== loadSeq) return;
    usePage = !chat.messages.length; // only a conversation's first message shares the page by default
    refusal = "";
    nodes.forEach((n) => n.root.remove());
    nodes.clear();
    draw();

    const ready = await ask({ type: "chat_ready" });
    if (mine !== loadSeq) return;
    if (ready.ok === false && !ready.state) {
      // A worker older than this document has never heard of chat.
      availability = "checking";
      stateEl.textContent = "";
      document.dispatchEvent(new CustomEvent("panel:stale-worker"));
      return;
    }
    availability = ready.state || "offline";
    draw();
    if (availability === "ready") await sync();
  }

  // ── sending ──────────────────────────────────────────────────────

  /**
   * pageContext reads the page the chip showed. The worker is told that tab
   * and address and refuses when the active tab is no longer them: the person
   * switched tabs after the chip was drawn, and what they wrote was about the
   * page they saw. -> { context } or { refused: why }.
   */
  async function pageContext() {
    const p = pageNow();
    const answer = await ask({
      type: "chat_page_context",
      windowId: typeof here !== "undefined" ? here.windowId : undefined,
      expect: p ? { tabId: p.tabId, url: p.url } : undefined,
    });
    if (answer.ok === false && answer.code === "page_changed") {
      return { refused: answer.error || "The page changed after you wrote this. Nothing was sent." };
    }
    if (answer.ok && answer.context) return { context: answer.context };
    return { context: p && pageShareable() ? Core.buildContext({ url: p.url, title: p.title, text: "", selection: "" }) : null };
  }

  let filling = false;

  /** Fills the active tab's form from the profile; the result is told in the notice line. */
  async function fillForm() {
    if (filling || busy() || availability !== "ready") return;
    filling = true;
    refusal = "Filling the form…";
    drawControls();
    try {
      const r = await ask({ type: "form_fill", profile: currentProfile(), windowId: typeof here !== "undefined" ? here.windowId : undefined });
      if (!r.ok) refusal = r.error || "The form could not be filled.";
      else if (r.none) refusal = "No empty fields to fill on this page.";
      else if (!r.filled) refusal = "Nothing in this profile's documents answers this form.";
      else {
        const from = r.sources && r.sources.length ? ` from ${r.sources.slice(0, 3).join(", ")}` : "";
        refusal = `Filled ${r.filled} of ${r.found} fields${from}. Check them, then submit yourself.`;
      }
    } catch (err) {
      refusal = (err && err.message) || "The form could not be filled.";
    } finally {
      filling = false;
      drawControls();
    }
  }

  async function send(text, forcePage) {
    const message = Core.checkMessage(text);
    if (!message || busy() || availability !== "ready") return;
    if (FILL_INTENT.test(message)) {
      input.value = "";
      return fillForm();
    }
    const sharing = pageShareable() && (forcePage || usePage);
    const tag = `c${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
    const sentFrom = profile;
    active = { tag };
    refusal = "";
    input.value = "";
    draw();

    let context = null;
    if (sharing) {
      const got = await pageContext();
      if (!active || active.tag !== tag) return;
      if (got.refused) {
        active = null;
        refusal = got.refused;
        input.value = text; // nothing was sent: give back what was typed
        draw();
        return;
      }
      context = got.context;
    }
    if (!active || active.tag !== tag) return;

    Core.startTurn(chat, message, Core.pageLabel(context));
    await save();
    draw();

    const answer = await ask({
      type: "chat_send", tag, profile: sentFrom, conversation: chat.conversation, message, context,
    });
    if (!active || active.tag !== tag) return; // the panel moved to another profile
    active = null;
    if (answer.ok) {
      const data = answer.data || {};
      if (typeof data.conversation === "string" && data.conversation) {
        if (chat.conversation && chat.conversation !== data.conversation) chat.lastSeq = 0;
        chat.conversation = data.conversation;
      }
      Core.finishTurn(chat, data.text, data.turn);
    } else {
      Core.failTurn(chat, describeFailure(answer));
    }
    usePage = false; // the page was for this message; a follow-up shares nothing unless asked
    await save();
    draw();
    // The turn may have outlived this request (the worker slept, the socket
    // dropped): ask the backend where it stands.
    if (!answer.ok && chat.conversation) sync();
  }

  function describeFailure(answer) {
    if (answer.code === "offline") return "MonoAgent went offline before the reply finished.";
    if (answer.code === "timeout") return "The reply stopped coming. You can send another message to carry on.";
    return answer.error || "The reply failed.";
  }

  async function stop() {
    if (!chat.conversation) return;
    stopBtn.disabled = true;
    await ask({ type: "chat_stop", profile, conversation: chat.conversation });
  }

  async function newChat() {
    if (busy()) return;
    ++loadSeq;
    chat = Core.newState(null);
    usePage = true; // a new conversation shares its first page
    refusal = "";
    await save(); // an empty conversation id: the next send starts a fresh one
    nodes.forEach((n) => n.root.remove());
    nodes.clear();
    draw();
    input.focus();
  }

  // ── events ───────────────────────────────────────────────────────

  chrome.runtime.onMessage.addListener((msg) => {
    if (!msg || msg.type !== "chat_progress" || !active || msg.tag !== active.tag) return;
    const ev = Core.parseProgress(msg.progress);
    if (ev && Core.applyEvent(chat, ev)) draw();
  });

  input.addEventListener("keydown", (event) => {
    if (event.key !== "Enter" || event.shiftKey || event.isComposing) return;
    event.preventDefault();
    send(input.value, false);
  });
  sendBtn.addEventListener("click", () => send(input.value, false));
  stopBtn.addEventListener("click", stop);
  newBtn.addEventListener("click", newChat);
  fillBtn.addEventListener("click", fillForm);
  summarizeBtn.addEventListener("click", () => send(SUMMARIZE_PROMPT, true));
  pageChip.addEventListener("click", () => {
    usePage = !usePage;
    drawControls();
  });

  let lastProfile = null;
  document.addEventListener("panel:profiles", () => {
    const next = currentProfile();
    if (next === lastProfile) return;
    lastProfile = next;
    load();
  });
  document.addEventListener("panel:page", drawControls);
  document.addEventListener("panel:bridge-up", () => {
    if (availability !== "ready") load();
  });

  draw();
  // The header's first drawing may already have happened.
  if (typeof profiles !== "undefined" && profiles && profiles.current) {
    lastProfile = currentProfile();
    load();
  }
})();
