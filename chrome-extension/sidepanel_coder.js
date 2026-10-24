/**
 * MonoAgent Bridge — the side panel's Coder section (C6)
 *
 * Hands work about the page in front of you to a coder chat in MonoAgent:
 * what to do (copy an element's style, extract design tokens, fix an
 * element, report a bug, build an integration, ask), the element, a note
 * and the project folder. Queue sends only those (coder_bridge.js); the CLI
 * reads the page itself and builds the context. The extension never starts
 * the coder (decision 3): a queued job waits in the MonoAgent app until the
 * person presses Start there.
 *
 * Below the form: what is queued, the coder conversations, and a live mirror
 * of one of them, polled while the section is open and the panel visible:
 * every 1.5 s while a turn runs, every 6 s otherwise (each poll costs
 * MonoAgent a CLI run or two). Replies are drawn by chat_render.js (Markdown to DOM nodes, no
 * innerHTML), tools as small cards, screenshots from the data URLs the bridge
 * sends (coder_mirror.js). The follow-up box shows only for a conversation MonoAgent says may
 * take one from here; Stop shows while a turn runs.
 *
 * The right-click items and the highlight panel's button open the panel
 * here, filled in: coder_bridge.js writes PREFILL_KEY to session storage,
 * read on load and watched after.
 *
 * After sidepanel.js (`ask`, `profiles`, `page`, `here`, and its panel:*
 * events), chat_core.js, chat_render.js and coder_mirror.js.
 */

(function () {
  "use strict";

  const Core = globalThis.MonoChatCore;
  const Mirror = globalThis.MonoCoderMirror;
  const el = (id) => document.getElementById(id);

  const POLL_MS = 1500; // a turn is running: its reply is coming in
  const IDLE_POLL_MS = 6000; // nothing running: only a new turn to notice
  const LIST_MS = 5000;
  const PREFILL_KEY = "coderPrefill";
  const PREFILL_FRESH_MS = 60000;
  const NEEDS_ELEMENT = new Set(["copy-style", "fix-element"]);
  const NEEDS_NOTE = new Set(["fix-element", "ask", "extension-ask"]);
  // "Fill with coder" and "Ask coder": queued as AI tasks (coder_task), not as coder queue items.
  const TASK_KINDS = new Set(["extension-fill", "extension-ask"]);
  const KIND_LABELS = {
    "copy-style": "Copy style",
    tokens: "Design tokens",
    "fix-element": "Fix element",
    "bug-report": "Bug report",
    integration: "Integration",
    ask: "Question",
    "extension-fill": "Fill with coder",
    "extension-ask": "Ask coder",
  };
  // coder.queue's refusals that no other job would escape (the CLI's codes).
  const REFUSALS = new Set(["coder_disabled", "needs_monomind_update", "coder_runtime_unsupported"]);
  const NOTICES = {
    offline: "MonoAgent isn't connected, so nothing can go to the coder. This comes back when the bridge does.",
    update: "This MonoAgent can't take work for the coder yet. Update MonoAgent, then reopen this panel.",
  };

  const section = el("coder-panel");
  const countEl = el("coder-count");
  const notice = el("coder-notice");
  const kindSel = el("coder-kind");
  const selectorEl = el("coder-selector");
  const pickBtn = el("coder-pick");
  const clearBtn = el("coder-clear");
  const noteBox = el("coder-note");
  const reloadRow = el("coder-reload-row");
  const reloadBox = el("coder-reload");
  const cwdSel = el("coder-cwd");
  const queueBtn = el("coder-queue");
  const whyEl = el("coder-why");
  const msg = el("coder-msg");
  const lists = el("coder-lists");
  const queuedList = el("coder-queued");
  const queuedEmpty = el("coder-queued-empty");
  const convList = el("coder-convs");
  const convEmpty = el("coder-convs-empty");
  const mirror = el("coder-mirror");
  const mirrorTitle = el("coder-mirror-title");
  const mirrorClose = el("coder-mirror-close");
  const log = el("coder-log");
  const follow = el("coder-follow");
  const followBox = el("coder-follow-text");
  const sendBtn = el("coder-send");
  const stopBtn = el("coder-stop");
  const mirrorMsg = el("coder-mirror-msg");

  let availability = "checking"; // checking | ready | offline | update
  let refusedText = ""; // MonoAgent's own words when it will not take coder work (coder mode off)
  let target = null; // { tabId, key, selector }: an element, on the page it was taken from
  let projects = { projects: [], recent: [] };
  let projectsFor = null; // the profile those folders are for
  let chosenCwd = null; // the folder the person picked, or null for the page's own
  let items = [];
  let conversations = [];
  let watching = null; // { id, chat, shots, since, running }
  let queueing = false;
  let picking = false;
  let pollTimer = null;
  let listTimer = null;

  const pageNow = () => (typeof page !== "undefined" && page ? page : null);
  const pageOk = () => {
    const p = pageNow();
    return !!(p && p.tabId && /^(https?|file):\/\//i.test(p.url || ""));
  };
  const profileId = () => (typeof profiles !== "undefined" && profiles && profiles.current ? profiles.current.id || "" : "");
  const withProfile = (m) => (profileId() ? Object.assign(m, { profile: profileId() }) : m);
  const ready = () => availability === "ready";
  const usable = () => ready() && !refusedText;
  // Every coder.* request is about a profile, and the bridge refuses one that
  // names none: nothing is asked until the header has one (#822).
  const asking = () => ready() && !!profileId();
  const visible = () => section.open && document.visibilityState !== "hidden";
  const baseName = Mirror.baseName;

  function say(kind, text) {
    msg.textContent = text || "";
    if (text) msg.dataset.kind = kind;
    else delete msg.dataset.kind;
  }

  // ── the form ─────────────────────────────────────────────────────

  const cwdChoices = () => Mirror.folderChoices(projects, (pageNow() && pageNow().url) || "");

  function drawCwd() {
    const choices = cwdChoices();
    const want = chosenCwd !== null && choices.some((c) => c.value === chosenCwd) ? chosenCwd : choices[0].value;
    cwdSel.textContent = "";
    for (const c of choices) {
      const opt = document.createElement("option");
      opt.value = c.value;
      opt.textContent = c.label;
      opt.title = c.value;
      cwdSel.appendChild(opt);
    }
    cwdSel.value = want;
  }

  /** why says what is missing before Queue can be pressed, or "". */
  function why() {
    if (!usable()) return "unavailable";
    if (!profileId()) return "Choose a profile at the top first.";
    if (!pageOk()) return "Open a web page first.";
    if (NEEDS_ELEMENT.has(kindSel.value) && !(target && target.selector)) return "Pick the element first.";
    if (NEEDS_NOTE.has(kindSel.value) && !noteBox.value.trim()) return "Say what you want in the note.";
    return "";
  }

  function drawForm() {
    const shown = refusedText || NOTICES[availability] || "";
    notice.hidden = !shown;
    notice.textContent = shown;
    const on = usable();
    lists.hidden = !on;
    for (const c of [kindSel, noteBox, cwdSel, reloadBox]) c.disabled = !on;
    reloadRow.hidden = kindSel.value !== "bug-report";
    selectorEl.textContent = target && target.selector ? target.selector : "The whole page";
    selectorEl.dataset.empty = String(!(target && target.selector));
    pickBtn.disabled = !on || !pageOk() || picking;
    pickBtn.textContent = picking ? "Click it in the page…" : "Pick element";
    clearBtn.hidden = !(target && target.selector);
    const reason = why();
    queueBtn.disabled = !!reason || queueing;
    queueBtn.textContent = queueing ? "Queueing…" : "Queue for the coder";
    whyEl.textContent = reason === "unavailable" ? "" : reason;
    whyEl.hidden = !whyEl.textContent;
  }

  /**
   * setTarget binds a selector to the page it was taken on; moving off that
   * page drops it. A panel that has only just opened may not know its page
   * yet: the binding then waits for the first page it is told about.
   */
  function setTarget(tabId, selector) {
    const p = pageNow();
    target = selector ? { tabId, key: p && p.tabId === tabId ? p.key : null, selector } : null;
  }

  async function queue() {
    if (why() || queueing) return;
    const p = pageNow();
    queueing = true;
    say("", "");
    drawForm();
    const kind = kindSel.value;
    // A task carries only the tab and the person's note: no selector, folder
    // or reload, and never the page's text (the CLI reads the tab itself).
    const answer = await ask(
      withProfile(
        TASK_KINDS.has(kind)
          ? { type: "coder_task", kind, tabId: p.tabId, note: noteBox.value }
          : {
              type: "coder_queue",
              kind,
              tabId: p.tabId,
              // Only a bug report reloads the tab; an older bridge ignores the field.
              ...(kind === "bug-report" ? { reload: reloadBox.checked } : {}),
              selector: target && target.tabId === p.tabId ? target.selector : "",
              note: noteBox.value,
              cwd: cwdSel.value,
            }
      )
    );
    queueing = false;
    if (answer.ok) {
      noteBox.value = "";
      target = null;
      say("ok", TASK_KINDS.has(kind) ? "Queued as a task. Start it in the MonoAgent app: it never runs on its own." : "Queued. Start it in the MonoAgent app.");
      loadLists();
    } else if (REFUSALS.has(answer.code)) {
      // Coder mode off, or unable to run: true of every job, so said at the top.
      refusedText = answer.error || "MonoAgent will not take work for the coder right now.";
      if (TASK_KINDS.has(kind) && answer.code === "coder_disabled") {
        refusedText = "Coder mode is off in MonoAgent, so this was not queued. Fill form and the chat still work as quick one-shots; turn the coder on in MonoAgent's Settings to hand work to it.";
      }
    } else {
      say("err", answer.code === "offline" ? NOTICES.offline : answer.error || "MonoAgent did not queue it.");
    }
    drawForm();
  }

  async function pick() {
    const p = pageNow();
    if (!p || !pageOk() || picking) return;
    picking = true;
    say("", "");
    drawForm();
    const answer = await ask({ type: "coder_pick", tabId: p.tabId });
    picking = false;
    if (answer.ok && answer.selector) setTarget(p.tabId, answer.selector);
    else if (answer.error && !/cancelled/.test(answer.error)) say("err", `No element picked: ${answer.error}`);
    drawForm();
  }

  // ── what is queued, and the conversations ────────────────────────

  function row(title, detail, extra, address) {
    const li = document.createElement("li");
    li.className = "queue-item coder-item";
    const line = (cls, text) => {
      const n = document.createElement("div");
      n.className = cls;
      n.textContent = text;
      li.appendChild(n);
    };
    line("title", title);
    // The page chose its title; the address is the browser's. Shown whole,
    // so a title cannot pass one site off as another.
    if (address) line("coder-address", address);
    line("why", detail);
    if (extra) li.appendChild(extra);
    return li;
  }

  function drawLists() {
    const queued = items.filter((i) => i && (i.status || "queued") === "queued");
    queuedList.textContent = "";
    for (const i of queued) {
      const where = i.title || i.origin || "a page";
      const folder = i.cwd ? ` · ${baseName(i.cwd)}` : "";
      const address = `${typeof i.origin === "string" ? i.origin : ""}${typeof i.path === "string" ? i.path : ""}`;
      queuedList.appendChild(row(`${KIND_LABELS[i.kind] || i.kind}: ${where}`, `Waiting. Start it in the MonoAgent app${folder}.`, null, address));
    }
    queuedEmpty.hidden = queued.length > 0;
    convList.textContent = "";
    for (const c of conversations) {
      if (!c || typeof c.id !== "string") continue;
      const open = document.createElement("button");
      open.type = "button";
      open.className = "btn-secondary btn-tiny";
      open.textContent = watching && watching.id === c.id ? "Watching" : "Watch";
      open.disabled = !!(watching && watching.id === c.id);
      open.addEventListener("click", () => watch(c.id));
      const actions = document.createElement("div");
      actions.className = "actions";
      actions.appendChild(open);
      const state = c.running ? "Working" : "Idle";
      convList.appendChild(row(c.title || "Coder chat", `${state}${c.cwd ? ` · ${baseName(c.cwd)}` : ""}`, actions));
    }
    convEmpty.hidden = conversations.length > 0;
    countEl.textContent = queued.length ? `${queued.length} queued` : "";
  }

  async function loadLists() {
    if (!asking()) return;
    const [q, c] = await Promise.all([ask(withProfile({ type: "coder_queue_list" })), ask(withProfile({ type: "coder_list" }))]);
    if (q.ok) items = Array.isArray(q.items) ? q.items : [];
    if (c.ok) conversations = Array.isArray(c.conversations) ? c.conversations : [];
    drawLists();
    if (watching) drawFollow();
  }

  async function loadProjects() {
    projectsFor = profileId();
    const answer = await ask(withProfile({ type: "coder_projects" }));
    if (answer.ok) {
      projects = { projects: answer.projects || [], recent: answer.recent || [] };
      refusedText = "";
    } else if (answer.code === "offline") {
      availability = "offline";
    } else {
      // Coder mode off, or the coder root missing: MonoAgent's own words
      // say what to do about it better than a paraphrase here would.
      refusedText = answer.error || "MonoAgent will not take work for the coder right now.";
    }
    drawCwd();
  }

  async function load() {
    const answer = await ask({ type: "coder_ready" });
    availability = answer.ok ? answer.state || "offline" : "offline";
    refusedText = "";
    if (asking()) await loadProjects();
    drawForm();
    loadLists(); // the summary's count, open or not
    schedule();
  }

  // ── the live mirror ──────────────────────────────────────────────

  function conversation(id) {
    return conversations.find((c) => c && c.id === id) || null;
  }

  function drawFollow() {
    follow.hidden = !Mirror.followable(watching, watching && conversation(watching.id));
    sendBtn.disabled = !followBox.value.trim();
    stopBtn.hidden = !(watching && watching.running);
  }

  function drawMirror() {
    mirror.hidden = !watching;
    if (!watching) return;
    const c = conversation(watching.id);
    mirrorTitle.textContent = (c && c.title) || "Coder chat";
    const nearBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 40;
    log.textContent = "";
    log.appendChild(Mirror.render(document, watching));
    log.setAttribute("aria-busy", watching.running ? "true" : "false");
    if (nearBottom) log.scrollTop = log.scrollHeight;
    drawFollow();
  }

  /**
   * poll asks for the latest turn after the last event seen. seq counts per
   * turn, so when the latest turn is a new one, the end of the old one is
   * fetched first and the new one from its start: two more requests, only
   * when a turn begins.
   */
  async function poll() {
    clearTimeout(pollTimer);
    const w = watching;
    if (!w || !visible() || !asking()) return;
    const events = (extra) => ask(withProfile(Object.assign({ type: "coder_events", conversationId: w.id }, extra)));
    let answer = await events({ since: w.chat.lastSeq });
    let changed = false;
    if (watching === w && answer.ok && answer.turn && w.chat.turn && answer.turn !== w.chat.turn) {
      const tail = await events({ turn: w.chat.turn, since: w.chat.lastSeq });
      if (tail.ok) changed = Mirror.fold(w, tail);
      answer = await events({ turn: answer.turn });
    }
    if (watching !== w) return;
    if (answer.ok) {
      if (Mirror.fold(w, answer)) changed = true;
      const running = !!answer.turn_active;
      if (!running && w.running) Core.closeOpen(w.chat);
      if (changed || running !== w.running) {
        w.running = running;
        drawMirror();
      }
    }
    if (watching === w && visible()) pollTimer = setTimeout(poll, w.running ? POLL_MS : IDLE_POLL_MS);
  }

  function watch(id) {
    watching = Mirror.watchState(id);
    followBox.value = "";
    drawLists();
    drawMirror();
    poll();
  }

  async function send() {
    const text = followBox.value.trim();
    if (!text || !watching || !Mirror.followable(watching, conversation(watching.id))) return;
    sendBtn.disabled = true;
    delete mirrorMsg.dataset.kind;
    mirrorMsg.textContent = "";
    const answer = await ask(withProfile({ type: "coder_send", conversationId: watching.id, text }));
    if (answer.ok) {
      followBox.value = "";
      watching.running = true;
      drawMirror();
      poll();
    } else {
      mirrorMsg.dataset.kind = "err";
      mirrorMsg.textContent = answer.error || "MonoAgent did not take the message.";
      drawFollow();
    }
  }

  async function stop() {
    if (!watching) return;
    stopBtn.disabled = true;
    await ask(withProfile({ type: "coder_stop", conversationId: watching.id }));
    stopBtn.disabled = false;
  }

  /** schedule keeps the lists and the mirror fresh while anyone can see them, and only then. */
  function schedule() {
    clearInterval(listTimer);
    listTimer = null;
    clearTimeout(pollTimer);
    if (!visible() || !asking()) return;
    listTimer = setInterval(loadLists, LIST_MS);
    if (watching) poll();
  }

  // ── filled in from the menu or the highlight panel ───────────────

  async function prefill(p) {
    if (!p || typeof p !== "object" || typeof p.at !== "number" || Date.now() - p.at > PREFILL_FRESH_MS) return;
    // Another window's panel is not the one that was asked.
    const tab = await chrome.tabs.get(p.tabId).catch(() => null);
    if (!tab || (typeof here !== "undefined" && here.windowId !== null && tab.windowId !== here.windowId)) return;
    if (chrome.storage.session) chrome.storage.session.remove(PREFILL_KEY).catch(() => {});
    if (KIND_LABELS[p.kind]) kindSel.value = p.kind;
    setTarget(p.tabId, typeof p.selector === "string" ? p.selector : "");
    section.open = true;
    say("", "");
    drawForm();
    if (section.scrollIntoView) section.scrollIntoView({ block: "start" });
    noteBox.focus();
  }

  // ── events ───────────────────────────────────────────────────────

  queueBtn.addEventListener("click", queue);
  pickBtn.addEventListener("click", pick);
  clearBtn.addEventListener("click", () => {
    target = null;
    drawForm();
  });
  kindSel.addEventListener("change", drawForm);
  noteBox.addEventListener("input", drawForm);
  cwdSel.addEventListener("change", () => {
    chosenCwd = cwdSel.value;
  });
  followBox.addEventListener("input", drawFollow);
  followBox.addEventListener("keydown", (event) => {
    if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
      event.preventDefault();
      send();
    }
  });
  sendBtn.addEventListener("click", send);
  stopBtn.addEventListener("click", stop);
  mirrorClose.addEventListener("click", () => {
    clearTimeout(pollTimer);
    watching = null;
    drawMirror();
    drawLists();
  });
  section.addEventListener("toggle", schedule);
  document.addEventListener("visibilitychange", schedule);

  document.addEventListener("panel:page", () => {
    const p = pageNow();
    if (target && target.key === null && p && p.tabId === target.tabId) target.key = p.key;
    else if (target && (!p || p.key !== target.key)) target = null;
    drawCwd();
    drawForm();
  });
  document.addEventListener("panel:bridge-up", () => {
    if (!ready()) load();
  });
  document.addEventListener("panel:profiles", () => {
    if (!asking()) return drawForm();
    // The folders, lists and chats are the profile's: asked for once one is chosen, again when it changes.
    if (projectsFor !== profileId()) {
      watching = null;
      drawMirror();
      loadProjects().then(drawForm);
    }
    loadLists();
    schedule();
  });

  chrome.storage.onChanged.addListener((changes, area) => {
    if (area === "session" && changes[PREFILL_KEY]) prefill(changes[PREFILL_KEY].newValue);
  });

  drawCwd();
  drawForm();
  drawLists();
  load();
  if (chrome.storage.session) chrome.storage.session.get(PREFILL_KEY).then((got) => prefill(got && got[PREFILL_KEY]), () => {});
})();
