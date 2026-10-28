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
 *   - What the chat proposes (a task, a recording) is a card; only the
 *     person's click on it does anything (chat_proposals.js, and
 *     sidepanel_chat_cards.js for what each click does).
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
  const Proposals = globalThis.MonoChatProposals;
  const Look = globalThis.MonoChatLook;
  const el = (id) => document.getElementById(id);

  const log = el("chat-log");
  const empty = el("chat-empty");
  const input = el("chat-input");
  const sendBtn = el("chat-send");
  const stopBtn = el("chat-stop");
  const newBtn = el("chat-new");
  const fillBtn = el("chat-fill");
  const cardBtn = el("chat-card");
  const filesBtn = el("chat-files");
  const summarizeBtn = el("chat-summarize");
  const pageChip = el("chat-page");
  const attachBtn = el("chat-attach");
  const attachInput = el("chat-attach-input");
  const attachedRow = el("chat-attached");
  const notice = el("chat-notice");
  const stateEl = el("chat-state");

  const SUMMARIZE_PROMPT = "Summarize this page.";
  // What the person's turn says when the model asked to see the page and they agreed (the picture itself is the attachment).
  const LOOK_MESSAGE = "Here is the screenshot of the page that you asked to see.";
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
  // Pictures the person picked for the next message ({name, image}): shrunk when picked, kept in memory only.
  let pending = [];
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
    const cards = (m.proposals || []).map((p) => `${p.state}:${(p.feedback || "").length}`).join(",");
    return `${m.role}|${m.text.length}|${m.open ? 1 : 0}|${(m.tools || []).map((t) => t.status).join(",")}|${cards}`;
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
      // What the chat proposed: a card each, sent on only by the person's click.
      for (const p of (Proposals && m.proposals) || []) {
        const key = Proposals.keyOf(p);
        box.appendChild(Proposals.card(document, p, { add: (value) => cards.confirm(key, value), dismiss: () => cards.dismiss(key), review: () => cards.review(key), run: () => cards.run(key), coder: () => cards.coder(key), send: () => cards.send(key), thumb: () => cards.thumb(key) }));
      }
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
      if (m.attached) {
        const att = document.createElement("span");
        att.className = "chat-about";
        att.textContent = `With: ${m.attached}`;
        box.appendChild(att);
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
        // The node it replaces may be where the next one goes: follow it.
        if (cursor === n.root) cursor = fresh;
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
    cardBtn.disabled = fillBtn.disabled;
    filesBtn.disabled = fillBtn.disabled;
    newBtn.disabled = busy() || (!chat.messages.length && !chat.conversation);
    const shareable = pageShareable();
    attachBtn.disabled = !ready || busy() || pending.length >= Look.MAX_IMAGES;
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

  // What a click on a card does; the recording cards follow the Record section.
  const record = globalThis.MonoPanelRecord || null;
  const cards = globalThis.MonoChatCards.create({ chat: () => chat, ask: (m) => ask(m), profile: () => profile, current: () => loadSeq, save, draw, record, page: pageNow, acted: () => { usePage = true; drawControls(); }, sendLook: (image) => send(LOOK_MESSAGE, false, { images: [image], noPage: true, label: "a screenshot of the page" }) });
  if (record) record.onChange((event) => cards.follow(event));

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
    const unread = Core.unreadAfterFailure(chat);
    if (applyAll(answer.events || [])) save();
    // The reply the failure hid has come: the retry's page sharing is spent.
    if (unread && !Core.unreadAfterFailure(chat)) usePage = false;
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
    // Only a conversation's first message shares the page by default, and a
    // first message whose reply failed was never read: its retry shares it too.
    usePage = !chat.messages.length || Core.unreadAfterFailure(chat);
    refusal = "";
    pending = [];
    drawAttached();
    nodes.forEach((n) => n.root.remove());
    nodes.clear();
    draw();
    if (record) cards.follow({ type: "state", state: record.state() });

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

  const FILL_CARD_INTENT = /^\s*(please\s+)?fill(\s+in)?\s+(the\s+|my\s+)?(card|payment)/i;
  const FILES_INTENT = /^\s*(please\s+)?(attach|upload)\s+(the\s+|my\s+)?(photos?|files?|documents?|ids?|scans?)/i;

  // What each fill reports, in the notice line.
  const FILLS = {
    form_fill: {
      start: "Filling the form…",
      done: (r) => {
        if (r.none) return "No empty fields to fill on this page.";
        if (!r.filled) return "Nothing in this profile's Info or documents answers this form.";
        const from = r.sources && r.sources.length ? ` from ${r.sources.slice(0, 3).join(", ")}` : "";
        return `Filled ${r.filled} of ${r.found} fields${from}. Check them, then submit yourself.`;
      },
    },
    card_fill: {
      start: "Filling the card…",
      done: (r) => {
        if (!r.saved) return "No card is saved. Add one on the Info page in the MonoAgent app.";
        if (!r.filled) return "No card fields found on this page.";
        return `Filled ${r.filled} card field${r.filled === 1 ? "" : "s"} from your saved card. Check them, then pay yourself.`;
      },
    },
    files_fill: {
      start: "Attaching files…",
      done: (r) => {
        if (!r.found) return "This page has no upload boxes.";
        if (!r.attached) return "None of your saved photos match this page's upload boxes.";
        return `Attached ${r.attached} file${r.attached === 1 ? "" : "s"}${r.names && r.names.length ? ` (${r.names.join(", ")})` : ""}. Check them, then continue yourself.`;
      },
    },
  };

  /** Fills the active tab from the profile (form, card or files); the result is told in the notice line. */
  async function runFill(type) {
    if (filling || busy() || availability !== "ready") return;
    filling = true;
    refusal = FILLS[type].start;
    drawControls();
    try {
      const r = await ask({ type, profile: currentProfile(), windowId: typeof here !== "undefined" ? here.windowId : undefined });
      refusal = r.ok ? FILLS[type].done(r) : r.error || "That could not be done.";
    } catch (err) {
      refusal = (err && err.message) || "That could not be done.";
    } finally {
      filling = false;
      drawControls();
    }
  }
  const fillForm = () => runFill("form_fill");

  /**
   * send sends one message. opts: images (picture attachments, default the
   * ones the person picked), noPage (never the page's text with it), label
   * (what the bubble says came with it). -> { sent } once the reply is over, or { sent:false, error }.
   */
  async function send(text, forcePage, opts) {
    const message = Core.checkMessage(text);
    if (!message) return { sent: false, error: "there is nothing to send" };
    if (busy()) return { sent: false, error: "a reply is still being written" };
    if (availability !== "ready") return { sent: false, error: "chat is not available" };
    const own = !opts || !opts.images;
    const images = own ? pending.map((a) => a.image) : opts.images;
    const label = (opts && opts.label) || (images.length ? `${images.length} image${images.length === 1 ? "" : "s"}` : "");
    // Card first: "fill the card" must not be taken for "fill the form".
    for (const [re, type] of [[FILL_CARD_INTENT, "card_fill"], [FILES_INTENT, "files_fill"], [FILL_INTENT, "form_fill"]]) {
      if (re.test(message)) {
        input.value = "";
        return runFill(type);
      }
    }
    const sharing = !(opts && opts.noPage) && pageShareable() && (forcePage || usePage);
    const tag = `c${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
    const sentFrom = profile;
    active = { tag };
    refusal = "";
    input.value = "";
    const took = own ? pending : [];
    if (own) pending = [];
    drawAttached();
    draw();

    let context = null;
    if (sharing) {
      const got = await pageContext();
      if (!active || active.tag !== tag) return { sent: false, error: "the panel moved on" };
      if (got.refused) {
        active = null;
        refusal = got.refused;
        input.value = text; // nothing was sent: give back what was typed
        pending = took;
        drawAttached();
        draw();
        return { sent: false, error: got.refused };
      }
      context = got.context;
    }
    if (!active || active.tag !== tag) return { sent: false, error: "the panel moved on" };

    const shown = pageNow(); // the reply's cards are bound to this page
    Core.startTurn(chat, message, Core.pageLabel(context), context != null, shown && { tabId: shown.tabId, url: shown.url }, label);
    await save();
    draw();

    const answer = await ask({
      type: "chat_send", tag, profile: sentFrom, conversation: chat.conversation, message, context, images,
    });
    if (!active || active.tag !== tag) return { sent: false, error: "the panel moved on" }; // the panel moved to another profile
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
    // The page was for this message; a follow-up shares nothing unless asked.
    // A failed reply read nothing, so the retry shares the page again.
    if (answer.ok) usePage = false;
    await save();
    draw();
    // The turn may have outlived this request (the worker slept, the socket
    // dropped): ask the backend where it stands.
    if (!answer.ok && chat.conversation) sync();
    return answer.ok ? { sent: true } : { sent: false, error: describeFailure(answer) };
  }

  // ── pictures the person attaches ──────────────────────────────────

  function drawAttached() {
    attachedRow.replaceChildren();
    attachedRow.hidden = pending.length === 0;
    pending.forEach((a, i) => {
      const chip = document.createElement("span");
      chip.className = "chat-attached-chip";
      chip.textContent = a.name;
      const drop = document.createElement("button");
      drop.type = "button";
      drop.className = "chat-attached-drop";
      drop.textContent = "\u00d7";
      drop.setAttribute("aria-label", `Remove ${a.name}`);
      drop.addEventListener("click", () => {
        pending.splice(i, 1);
        drawAttached();
        drawControls();
      });
      chip.appendChild(drop);
      attachedRow.appendChild(chip);
    });
  }

  async function attachPicked() {
    const files = Array.from(attachInput.files || []);
    attachInput.value = "";
    refusal = "";
    for (const f of files) {
      if (pending.length >= Look.MAX_IMAGES) {
        refusal = `At most ${Look.MAX_IMAGES} pictures go with a message.`;
        break;
      }
      if (!/^image\/(png|jpeg|gif|webp)$/.test(f.type)) {
        refusal = `${f.name} is not a PNG, JPEG, GIF or WebP picture.`;
        continue;
      }
      try {
        pending.push({ name: f.name.slice(0, 60), image: await Look.shrink(f, Look.realEnv()) });
      } catch (err) {
        refusal = `${f.name}: ${(err && err.message) || "it could not be attached"}`;
      }
    }
    drawAttached();
    drawControls();
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
    pending = [];
    drawAttached();
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
  cardBtn.addEventListener("click", () => runFill("card_fill"));
  filesBtn.addEventListener("click", () => runFill("files_fill"));
  summarizeBtn.addEventListener("click", () => send(SUMMARIZE_PROMPT, true));
  attachBtn.addEventListener("click", () => attachInput.click());
  attachInput.addEventListener("change", attachPicked);
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
