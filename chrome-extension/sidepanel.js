/**
 * MonoAgent Bridge — Side panel script
 *
 * Connection status, the profile switcher, the save form (CLIP-07), batch
 * capture (CLIP-06) and the pending-capture list (CLIP-08). Deliberately
 * thin: the panel can be closed at any moment, so it asks the service
 * worker to do everything and only draws the answers. Anything here that
 * looked like state would be lost mid-capture.
 *
 * The judgement lives next door, where it can be tested without a DOM:
 * sidepanel_status.js turns a status word into a sentence and an action,
 * sidepanel_view.js turns a tab and a profile list into what the card and
 * the header say. This file only draws what they decide.
 *
 * What is new here, compared with the popup this grew out of: the panel
 * is one document per window that stays open while the person moves
 * between tabs. So "this page" is not fixed at open — it is the window's
 * active tab, followed through chrome.tabs events, and every capture,
 * lookup and batch names the tab and window it means instead of asking
 * the worker to guess from focus.
 *
 * Non-loopback servers are rejected unless the (unsafe, session-only)
 * override checkbox is enabled at save time.
 */

const $ = (id) => document.getElementById(id);

const Status = globalThis.MonoPanelStatus;
const View = globalThis.MonoPanelView;

const statusPill = $("status-pill");
const statusLabel = $("status-label");
const notice = $("notice");
const noticeTitle = $("notice-title");
const noticeBody = $("notice-body");
const noticeCommandRow = $("notice-command-row");
const noticeCommand = $("notice-command");
const noticeCopy = $("notice-copy");
const noticeActions = $("notice-actions");
const noticeAction = $("notice-action");

const wsUrlInput = $("ws-url");
const saveBtn = $("save-btn");
const wsSavedMsg = $("ws-saved-msg");
const wsErrorMsg = $("ws-error-msg");
const allowRemoteCheckbox = $("allow-remote");
const pairingTokenInput = $("pairing-token");
const pairBtn = $("pair-btn");
const pairSavedMsg = $("pair-saved-msg");
const settingsPanel = $("settings-panel");

const staleNotice = $("stale-notice");
const captureBtn = $("capture-btn");
const captureLabel = $("capture-label");
const captureMsg = $("capture-msg");
const shortcutKey = $("shortcut-key");
const hintNoShortcut = $("hint-noshortcut");
const shortcutSet = $("shortcut-set");

const pageIcon = $("page-icon");
const pageMonogram = $("page-monogram");
const pageTitle = $("page-title");
const pageHost = $("page-host");
const pageWhy = $("page-why");
const hintQueues = $("hint-queues");
const hintShortcut = $("hint-shortcut");
const noteInput = $("note");
const tagsInput = $("tags");
const collectionInput = $("collection");
const collectionList = $("collection-list");
const profileBtn = $("profile-btn");
const profileAvatar = $("profile-avatar");
const profileName = $("profile-name");
const profileMenu = $("profile-menu");
const profileOptions = $("profile-options");
const profileNote = $("profile-note");
const profileNoteText = $("profile-note-text");
const profileCommandRow = $("profile-command-row");
const profileCommand = $("profile-command");
const profileCopy = $("profile-copy");
const tagChips = $("tag-chips");

const batchWindowBtn = $("batch-window");
const batchGroupBtn = $("batch-group");
const batchCancelBtn = $("batch-cancel");
const batchProgress = $("batch-progress");
const batchLabel = $("batch-label");
const batchBar = $("batch-bar");
const batchMsg = $("batch-msg");
const batchGroupWhy = $("batch-group-why");
const batchDest = $("batch-dest");

const queuePanel = $("queue-panel");
const queueList = $("queue-list");
const queueCounts = $("queue-counts");
const queueClearBtn = $("queue-clear");
const queueRetryAllBtn = $("queue-retry-all");

// --- connection status -----------------------------------------------------

/**
 * What the panel knows about the connection. `since` is stamped here rather
 * than taken on faith from the worker, because the worker does not send one
 * today — and the grace period that stops a service-worker respawn flashing
 * red needs to know how long this state has been true. A status that has
 * not changed keeps its original stamp.
 */
const connection = {
  status: "",
  reason: "",
  detail: "",
  since: Date.now(),
  everConnected: false,
  wsUrl: "",
  // What the worker last said about the bridge's account: {refusing, state, reason}.
  account: null,
};

/** The last thing describe() returned, so the action button knows its job. */
let shown = null;

/**
 * The panel's two sources, kept apart so neither can silently overwrite the
 * other. The worker reports what its own socket did; the health probe says
 * whether a bridge process is running at all. They disagree constantly while
 * no bridge is running — the worker retries and broadcasts on every attempt
 * — so every repaint goes through Status.arbitrate, which lets each source
 * answer only the question it can actually know. Letting the last message
 * win made the panel flicker between two sentences for the same fact.
 */
let workerState = null;
let healthState = null;

function settle() {
  noteStatus(Status.arbitrate(workerState, healthState));
}

/**
 * noteStatus records a status update, preserving `since` across repeats of
 * the same state so that a worker re-broadcasting "disconnected" every few
 * seconds cannot hold the UI in its grace period forever.
 */
function noteStatus(update) {
  const next = update || {};
  const status = String(next.status || "");
  const reason = String(next.reason || "");
  const changed = status !== connection.status || reason !== connection.reason;

  connection.status = status;
  connection.reason = reason;
  connection.detail = next.detail || "";
  connection.account = next.account || null;
  if (typeof next.since === "number" && next.since > 0) {
    connection.since = next.since;
  } else if (changed) {
    connection.since = Date.now();
  }
  if (next.wsUrl) connection.wsUrl = next.wsUrl;
  if (status === "connected") connection.everConnected = true;

  drawStatus();
}

function drawStatus() {
  const was = shown && shown.tone;
  const view = Status.describe({
    status: connection.status,
    reason: connection.reason,
    detail: connection.detail,
    since: connection.since,
    everConnected: connection.everConnected,
    wsUrl: connection.wsUrl || wsUrlInput.value,
    account: connection.account,
  });
  shown = view;

  statusPill.dataset.tone = view.tone;
  statusPill.dataset.busy = String(view.busy);
  statusLabel.textContent = view.label;

  notice.dataset.open = String(!!view.title);
  notice.dataset.tone = view.tone === "busy" || view.tone === "ok" ? "idle" : view.tone;
  noticeTitle.textContent = view.title;
  noticeBody.textContent = view.body;
  noticeBody.hidden = !view.body;

  noticeCommand.textContent = view.command;
  noticeCommandRow.hidden = !view.command;
  noticeCopy.setAttribute("aria-label", `Copy the command ${view.command}`);

  // The button is hidden as well as its row: a button with no label is a
  // control our own accessibility checker is right to complain about, and
  // leaving it in the tree to be skipped by a wrapper is not an answer.
  noticeActions.hidden = !view.action;
  noticeAction.hidden = !view.action;
  if (view.action) {
    noticeAction.textContent = view.action.label;
    noticeAction.dataset.action = view.action.id;
  }

  // The button never lies about what it is about to do. A capture with no
  // bridge to send it to still succeeds — it is kept and sent later — and
  // saying so here is the difference between a queue and a surprise.
  hintQueues.hidden = !view.queues;
  hintShortcut.hidden = view.queues || !shortcutKey.textContent;
  hintNoShortcut.hidden = view.queues || !!shortcutKey.textContent || !shortcutKnown;

  // The popup was reopened for every look, so its profile list was always
  // as fresh as the bridge could make it. A panel that was opened while the
  // bridge was down would keep the cached list for good, so it asks again
  // the moment the bridge becomes reachable.
  if (view.tone === "ok" && was && was !== "ok") {
    loadFormState();
    document.dispatchEvent(new CustomEvent("panel:bridge-up"));
  }
}

/**
 * The grace period expires on a timer as well as on the next event: with no
 * further status broadcast, a socket that never came back would otherwise
 * sit on "Reconnecting…" for as long as the panel stayed open.
 */
setInterval(() => {
  if (shown && shown.key === "reconnecting") drawStatus();
}, 1000);

/**
 * copyCommand puts a command on the clipboard and says so on the button.
 * Clipboard refused (no permission, no focus): the command is selected
 * instead so the keyboard can finish the job — never a dead button.
 */
async function copyCommand(button, code, text) {
  if (!text) return;
  try {
    await navigator.clipboard.writeText(text);
    button.textContent = "Copied";
    setTimeout(() => {
      button.textContent = "Copy";
    }, 1600);
  } catch {
    const range = document.createRange();
    range.selectNodeContents(code);
    const selection = window.getSelection();
    selection.removeAllRanges();
    selection.addRange(range);
    button.textContent = "Press ⌘/Ctrl+C";
  }
}

noticeCopy.addEventListener("click", () => copyCommand(noticeCopy, noticeCommand, shown && shown.command));
profileCopy.addEventListener("click", () =>
  copyCommand(profileCopy, profileCommand, profileCommand.textContent)
);

noticeAction.addEventListener("click", async () => {
  const which = noticeAction.dataset.action;
  if (which === Status.ACTIONS.PAIR) {
    settingsPanel.open = true;
    pairingTokenInput.focus();
    pairingTokenInput.scrollIntoView({ block: "nearest" });
    return;
  }
  if (which === Status.ACTIONS.SETTINGS) {
    settingsPanel.open = true;
    wsUrlInput.focus();
    wsUrlInput.scrollIntoView({ block: "nearest" });
    return;
  }
  if (which === Status.ACTIONS.RETRY) {
    // Re-saving the address we already have is how the panel asks the
    // worker to drop the socket and dial again, using only the message the
    // worker already answers.
    noteStatus({ status: "connecting" });
    await ask({ type: "set_ws_url", url: wsUrlInput.value.trim() || Status.DEFAULT_WS_URL });
  }
});

function showError(message) {
  hide(wsSavedMsg);
  wsErrorMsg.textContent = message;
  wsErrorMsg.dataset.kind = "err";
}

function show(el, kind, text) {
  if (!text) {
    hide(el);
    return;
  }
  el.textContent = text;
  el.dataset.kind = kind;
}

function hide(el) {
  delete el.dataset.kind;
}

const showCapture = (kind, text) => show(captureMsg, kind, text);
const showBatch = (kind, text) => show(batchMsg, kind, text);

/** ask sends one message to the worker and resolves with its answer. */
function ask(message) {
  return new Promise((resolve) => {
    chrome.runtime.sendMessage(message, (response) => {
      if (chrome.runtime.lastError) {
        resolve({ ok: false, error: chrome.runtime.lastError.message });
        return;
      }
      resolve(response || { ok: false, error: "no answer from the extension" });
    });
  });
}

// The form always carries `profile`, including the empty string for "no
// profile": the worker treats its presence as this save's choice and makes
// it the sticky one, so the picker means the same thing whether it was
// touched or left where it was.
const form = () => ({
  note: noteInput.value,
  tags: tagsInput.value,
  collection: collectionInput.value,
  profile: profiles.current ? profiles.current.id : "",
});

// --- the profile switcher --------------------------------------------------

/** What the header last drew, from View.describeProfiles. */
let profiles = { current: null, options: [], interactive: false };
let lastFormState = null;

/**
 * paintAvatar draws a profile's initial on its own colour, or the inbox
 * glyph for "no profile". The hue is an identity (View.avatarHue), set as
 * a custom property so both themes derive their own lightness from it.
 */
function paintAvatar(el, option) {
  el.textContent = option && option.initial ? option.initial : "";
  el.dataset.kind = option && option.id ? "profile" : "inbox";
  if (option && option.hue >= 0) el.style.setProperty("--h", String(option.hue));
  else el.style.removeProperty("--h");
}

/**
 * drawProfiles fills the header's switcher from capture_form_state. The
 * choice is only ever pre-selected, never forced: a capture saves into
 * whatever the header shows, and the header shows what the worker will use
 * for the keyboard shortcut too.
 */
function drawProfiles(state) {
  lastFormState = state;
  profiles = View.describeProfiles(state);
  const { current, options, interactive, note } = profiles;

  profileName.textContent = View.displayName(current.name);
  paintAvatar(profileAvatar, current);
  profileBtn.disabled = !interactive;
  profileBtn.dataset.interactive = String(interactive);
  if (!interactive) closeProfileMenu(false);

  profileOptions.textContent = "";
  for (const option of options) {
    const row = document.createElement("label");
    row.className = "dest-option";

    const radio = document.createElement("input");
    radio.type = "radio";
    radio.name = "profile";
    radio.value = option.id;
    radio.checked = option.id === current.id;
    radio.className = "sr-only";
    row.appendChild(radio);

    const avatar = document.createElement("span");
    avatar.className = "avatar avatar-sm";
    avatar.setAttribute("aria-hidden", "true");
    paintAvatar(avatar, option);
    row.appendChild(avatar);

    const text = document.createElement("span");
    text.className = "dest-option-text";
    const name = document.createElement("span");
    name.className = "dest-option-name";
    name.textContent = option.name;
    text.appendChild(name);
    if (option.hint || option.isDefault) {
      const sub = document.createElement("span");
      sub.className = "dest-option-hint";
      // The bridge's "default" is monoagent's current profile (the one
      // `profile list` stars), not a profile called Default — which may
      // also exist, and would make "Default profile" a riddle.
      sub.textContent = option.isDefault ? "monoagent's current profile" : option.hint;
      text.appendChild(sub);
    }
    row.appendChild(text);

    const tick = document.createElement("span");
    tick.className = "tick";
    tick.setAttribute("aria-hidden", "true");
    row.appendChild(tick);

    profileOptions.appendChild(row);
  }

  profileNote.dataset.tone = note ? note.tone : "";
  profileNote.hidden = !note;
  profileNoteText.textContent = note ? note.text : "";
  profileCommandRow.hidden = !(note && note.command);
  profileCommand.textContent = (note && note.command) || "";
  profileCopy.setAttribute("aria-label", `Copy the command ${(note && note.command) || ""}`);

  // Mid-save or just saved, the button's own words win; flashDone puts
  // the save label back when it is done.
  if (!captureBtn.dataset.busy && !captureBtn.dataset.done) captureLabel.textContent = profiles.saveLabel;
  batchDest.textContent = current.id ? current.name : "the shared inbox";

  // The binding checklist offers the same list; redraw it when the list changes.
  if (typeof panelBinding !== "undefined") panelBinding.redraw();
  // The task box names the profile too (sidepanel_tasks.js).
  document.dispatchEvent(new CustomEvent("panel:profiles"));
}

function openProfileMenu() {
  if (profileBtn.disabled) return;
  profileMenu.hidden = false;
  profileBtn.setAttribute("aria-expanded", "true");
  const checked = profileOptions.querySelector("input:checked") || profileOptions.querySelector("input");
  if (checked) checked.focus();
}

function closeProfileMenu(returnFocus) {
  if (profileMenu.hidden) return;
  profileMenu.hidden = true;
  profileBtn.setAttribute("aria-expanded", "false");
  if (returnFocus) profileBtn.focus();
}

profileBtn.addEventListener("click", () => {
  if (profileMenu.hidden) openProfileMenu();
  else closeProfileMenu(true);
});

// Arrow keys move the choice (a radio group's own behaviour) and each move
// is applied at once: the header, the button and the stored sticky choice
// all follow, so there is never a "picked but not saved" state to explain.
profileOptions.addEventListener("change", async (event) => {
  const id = event.target && event.target.value;
  if (typeof id !== "string") return;
  const option = profiles.options.find((o) => o.id === id);
  if (!option) return;
  profiles.current = option;
  profileName.textContent = option.name;
  paintAvatar(profileAvatar, option);
  captureLabel.textContent = View.describeProfiles(
    Object.assign({}, lastFormState, { profile: id, profileChanged: false })
  ).saveLabel;
  batchDest.textContent = option.id ? option.name : "the shared inbox";
  // A choice made here supersedes a "your profile was deleted" note.
  if (profileNote.dataset.tone === "warn") profileNote.hidden = true;
  if (lastFormState) lastFormState = Object.assign({}, lastFormState, { profile: id, profileChanged: false });
  document.dispatchEvent(new CustomEvent("panel:profiles"));
  await ask({ type: "capture_profile_set", profile: id });
});

// Pressing on a row must not move focus: the rows are labels, so the press
// would blur the checked radio to <body>, the focusout below would close
// the menu, and the click would land on nothing — the choice silently lost.
profileOptions.addEventListener("mousedown", (event) => event.preventDefault());

// A pointer choice is a finished choice; so is Enter or Space on a row.
// Arrow keys alone are browsing, and leave the list open.
profileOptions.addEventListener("click", (event) => {
  if (event.target && event.target.matches("input[type=radio]") && event.detail > 0) closeProfileMenu(true);
});
profileOptions.addEventListener("keydown", (event) => {
  if (event.key === "Enter" || event.key === " ") {
    event.preventDefault();
    closeProfileMenu(true);
  }
});
$("dest").addEventListener("keydown", (event) => {
  if (event.key === "Escape" && !profileMenu.hidden) {
    event.preventDefault();
    closeProfileMenu(true);
  }
});
// Focus leaving the switcher closes it, so it is never left hanging open
// over the page card while someone types a note.
$("dest").addEventListener("focusout", (event) => {
  if (!$("dest").contains(event.relatedTarget)) closeProfileMenu(false);
});
document.addEventListener("pointerdown", (event) => {
  if (!$("dest").contains(event.target)) closeProfileMenu(false);
});

// The panel's own code is newer than the running worker's (see
// sidepanel_ai.js). Everything the older worker does know still works, so
// this is a banner rather than a blockade.
document.addEventListener("panel:stale-worker", () => {
  staleNotice.hidden = false;
});

// --- CLIP-07: the snapshot starts while the note is still being typed -----

let began = false;

/**
 * beginEarly asks the worker to photograph the page now. The page is only
 * going to get further from what the person is looking at, and the note
 * field is about to hold their attention for a while.
 *
 * Not a one-shot listener, as it was in the popup: a panel sees many pages
 * and many captures, so every page gets its own early snapshot. `began`
 * is cleared when the page changes (followTab) and after each save.
 */
function beginEarly() {
  if (began || !page.capturable) return;
  began = true;
  ask({ type: "capture_begin", options: { tabId: page.tabId, mode: saveMode().mode } });
}

for (const field of [noteInput, tagsInput, collectionInput]) {
  field.addEventListener("focus", beginEarly);
  field.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !captureBtn.disabled) captureBtn.click();
  });
}

function drawTagSuggestions(recent) {
  tagChips.textContent = "";
  const chosen = tagsInput.value;
  const query = chosen.split(/[,\s]+/).pop() || "";
  const suggestions = MonoSuggest(recent, query, chosen);
  for (const tag of suggestions) {
    const chip = document.createElement("button");
    chip.type = "button";
    chip.className = "chip";
    chip.textContent = tag;
    // Eight buttons that a screen reader reads as eight bare words are
    // eight guesses. The label says what pressing one does.
    chip.setAttribute("aria-label", `Add the tag ${tag}`);
    chip.addEventListener("click", () => {
      const parts = tagsInput.value.split(/,\s*/).filter(Boolean);
      // Replace the fragment being typed, if that is what the chip matched.
      if (query && !tagsInput.value.endsWith(", ") && parts.length) parts.pop();
      parts.push(tag);
      tagsInput.value = `${parts.join(", ")}, `;
      tagsInput.focus();
      drawTagSuggestions(recent);
    });
    tagChips.appendChild(chip);
  }
}

/**
 * MonoSuggest is the panel's copy of the ranking rule. The worker owns the
 * canonical one (capture_form.js); a panel page cannot importScripts, and this
 * is small enough that duplicating it beats a message round-trip per
 * keystroke.
 */
function MonoSuggest(recent, query, chosen) {
  const taken = new Set(
    String(chosen || "")
      .split(/[,\s]+/)
      .map((t) => t.replace(/^#+/, "").toLowerCase())
      .filter(Boolean)
  );
  const q = String(query || "").replace(/^#+/, "").trim().toLowerCase();
  const prefix = [];
  const contains = [];
  for (const tag of recent || []) {
    const key = String(tag).toLowerCase();
    if (!key || taken.has(key)) continue;
    if (!q || key.startsWith(q)) prefix.push(tag);
    else if (key.includes(q)) contains.push(tag);
  }
  return prefix.concat(contains).slice(0, 8);
}

// --- CLIP-08: the pending list --------------------------------------------

function drawQueue(state) {
  const items = (state.queued || []).concat(state.failed || []);
  queueList.textContent = "";
  if (!items.length) {
    queuePanel.hidden = true;
    queuePanel.open = false;
    return;
  }
  queuePanel.hidden = false;

  const counts = state.counts || { queued: 0, failed: 0 };
  const summary = Status.describeQueue(counts);
  if (summary) {
    queueCounts.textContent = summary.text;
    queueCounts.dataset.tone = summary.tone;
    // Captures that could not be sent are the only thing in this panel that
    // will not resolve itself, so that is the one case that opens its own
    // drawer. A queue merely waiting on a reconnect does not.
    if (summary.failed) queuePanel.open = true;
  }
  queueClearBtn.hidden = !counts.failed;

  // Every row has a "Retry now" and a "Delete", and read aloud in a list
  // they must say which capture they act on. The title alone is not
  // enough — two sites can share one, and the same page can be saved twice
  // — so the label carries the site and the time, and, when even those
  // collide, which of the identical rows it is.
  const whenOf = (item) => {
    const at = item.at ? new Date(item.at) : null;
    return at && !Number.isNaN(at.getTime())
      ? at.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })
      : "";
  };
  const nameOf = (item) => {
    const site = View.hostOf(item.url);
    const when = whenOf(item);
    return [item.title, site && `from ${site}`, when && `saved at ${when}`].filter(Boolean).join(", ");
  };
  const totals = new Map();
  for (const item of items) totals.set(nameOf(item), (totals.get(nameOf(item)) || 0) + 1);
  const seen = new Map();

  for (const item of items) {
    const when = whenOf(item);
    const base = nameOf(item);
    const nth = (seen.get(base) || 0) + 1;
    seen.set(base, nth);
    const which = totals.get(base) > 1 ? `${base} (${nth} of ${totals.get(base)})` : base;

    const li = document.createElement("li");
    li.className = `queue-item ${item.status}`;

    const title = document.createElement("div");
    title.className = "title";
    title.textContent = item.title;
    title.title = item.url || "";
    li.appendChild(title);

    const why = document.createElement("div");
    why.className = "why";
    why.textContent =
      (item.status === "failed"
        ? `Failed: ${item.reason || "no reason recorded"}`
        : item.reason
        ? `Waiting — last attempt: ${item.reason}`
        : "Waiting for the bridge to reconnect") + (when ? ` · ${when}` : "");
    li.appendChild(why);

    const actions = document.createElement("div");
    actions.className = "actions";
    const retry = document.createElement("button");
    retry.type = "button";
    retry.className = "btn-secondary btn-tiny";
    retry.textContent = "Retry now";
    retry.setAttribute("aria-label", `Retry sending ${which}`);
    retry.addEventListener("click", async () => {
      retry.disabled = true;
      const result = await ask({ type: "queue_retry", key: item.key });
      if (!result.ok) {
        why.textContent = `Could not send: ${result.reason || result.error}`;
        retry.disabled = false;
      }
      await refreshQueue();
    });
    const del = document.createElement("button");
    del.type = "button";
    del.className = "btn-secondary btn-tiny";
    del.textContent = "Delete";
    del.title = "Discard this capture without sending it";
    del.setAttribute("aria-label", `Discard ${which} without sending it`);
    del.addEventListener("click", async () => {
      await ask({ type: "queue_delete", key: item.key });
      await refreshQueue();
    });
    actions.appendChild(retry);
    actions.appendChild(del);
    li.appendChild(actions);

    queueList.appendChild(li);
  }
}

async function refreshQueue() {
  drawQueue(await ask({ type: "queue_state" }));
}

queueClearBtn.addEventListener("click", async () => {
  await ask({ type: "queue_clear_failures" });
  await refreshQueue();
});

queueRetryAllBtn.addEventListener("click", async () => {
  queueRetryAllBtn.disabled = true;
  const state = await ask({ type: "queue_state" });
  const items = (state.queued || []).concat(state.failed || []);
  for (const item of items) {
    await ask({ type: "queue_retry", key: item.key });
  }
  queueRetryAllBtn.disabled = false;
  await refreshQueue();
});

// --- saving ---------------------------------------------------------------

let doneTimer = 0;

/**
 * flashDone turns the save button green with a tick for a moment. The
 * outcome line under it is easy to miss; the button someone just pressed
 * is not.
 */
function flashDone(label) {
  captureBtn.dataset.done = "true";
  captureLabel.textContent = "✓ " + label.replace(/^Save\b/, "Saved");
  doneTimer = setTimeout(() => {
    delete captureBtn.dataset.done;
    captureLabel.textContent = profiles.saveLabel || label;
  }, 2500);
}

/** saveMode is the mode chosen beside Save (sidepanel_ai.js). */
function saveMode() {
  return globalThis.MonoPanelAI ? MonoPanelAI.captureMode() : { mode: "full", summarizes: false };
}

captureBtn.addEventListener("click", async () => {
  if (!page.capturable) return;
  const target = page;
  const mode = saveMode();
  const label = captureLabel.textContent;
  captureBtn.disabled = true;
  captureBtn.dataset.busy = "true";
  delete captureBtn.dataset.done;
  clearTimeout(doneTimer);
  captureLabel.textContent = "Saving…";
  showCapture("ok", "");

  // The tab is named, not left to the worker's idea of focus: with a panel
  // open, the last focused window may be another one entirely.
  const result = await ask({ type: "capture_commit", form: form(), options: { tabId: target.tabId, mode: mode.mode } });
  delete captureBtn.dataset.busy;
  captureBtn.disabled = !page.capturable;
  captureLabel.textContent = label;
  began = false;

  if (!result || result.ok === false) {
    showCapture("err", result?.error || result?.warnings?.join("; ") || "Capture failed");
    await refreshQueue();
    return;
  }

  const by = mode.summarizes && globalThis.MonoPanelAI ? MonoPanelAI.summaryBy() : "";
  const title = (result.title ? `Saved: ${result.title}` : "Saved") + (by ? ` — ${by} is writing the summary` : "");
  if (result.queued) {
    showCapture("warn", `${title} — waiting for the bridge`);
  } else if (result.warnings?.length) {
    showCapture("warn", `${title} — ${result.warnings.join("; ")}`);
  } else {
    showCapture("ok", `${title} — in ${profiles.current && profiles.current.id ? profiles.current.name : "the shared inbox"}`);
  }
  if (!result.queued) flashDone(label);
  noteInput.value = "";
  tagsInput.value = "";
  await Promise.all([refreshQueue(), loadFormState()]);
  // The worker re-asks the brain about this page a few seconds after a
  // capture lands; the "already saved" band catches up with it then.
  setTimeout(() => {
    if (page.key === target.key) document.dispatchEvent(new CustomEvent("panel:recheck"));
  }, 4500);
});

// --- CLIP-06: batch capture -----------------------------------------------

let batching = false;

function batchRunning(running) {
  batching = running;
  batchWindowBtn.disabled = running;
  batchGroupBtn.disabled = running || page.groupId === -1;
  batchProgress.dataset.running = String(running);
  batchCancelBtn.disabled = false;
  if (!running) batchBar.value = 0;
}

async function startBatch(scope) {
  batchRunning(true);
  batchLabel.textContent = "Starting…";
  showBatch("ok", "");

  const result = await ask({
    type: "capture_batch",
    scope,
    form: form(),
    windowId: here.windowId,
    tabId: page.tabId || undefined,
  });
  batchRunning(false);

  if (!result || result.ok === false) {
    showBatch("err", result?.error || "Batch capture failed");
  } else {
    const report = result.report || {};
    showBatch(report.failed?.length || report.skipped?.length ? "warn" : "ok", result.summary);
    drawSkips(report);
  }
  await Promise.all([refreshQueue(), loadFormState()]);
}

/** drawSkips names every tab that was not captured — a skip is never silent. */
function drawSkips(report) {
  const skipped = (report.skipped || []).concat(report.failed || []);
  if (!skipped.length) return;
  const details = document.createElement("details");
  const summary = document.createElement("summary");
  summary.textContent = `${skipped.length} not saved`;
  details.appendChild(summary);
  for (const item of skipped) {
    const line = document.createElement("div");
    line.textContent = `${item.title} — ${item.reason}`;
    details.appendChild(line);
  }
  batchMsg.appendChild(details);
}

batchWindowBtn.addEventListener("click", () => startBatch("window"));
batchGroupBtn.addEventListener("click", () => startBatch("group"));
batchCancelBtn.addEventListener("click", async () => {
  batchCancelBtn.disabled = true;
  batchLabel.textContent = "Cancelling after this tab…";
  await ask({ type: "capture_batch_cancel" });
});

// --- live messages from the worker ----------------------------------------

chrome.runtime.onMessage.addListener((msg) => {
  if (msg.type === "status") {
    workerState = msg;
    settle();
    return;
  }
  // A right-click menu capture (capture_bridge.js announce): its outcome
  // lands in the same line as a save from this panel.
  if (msg.type === "capture_menu_result" && msg.feedback) {
    const level = { ok: "ok", warn: "warn", error: "err" }[msg.feedback.level] || "ok";
    showCapture(level, msg.feedback.text);
    if (msg.result && msg.result.ok) refreshQueue();
    return;
  }
  if (msg.type === "capture_batch_progress") {
    const s = msg.state || {};
    batchRunning(s.phase !== "done");
    if (s.total) {
      batchBar.max = s.total;
      batchBar.value = s.phase === "done" ? s.total : s.completed || 0;
    }
    batchLabel.textContent =
      s.phase === "done"
        ? `${s.done} of ${s.total} saved`
        : `${s.completed} of ${s.total}${s.title ? ` — ${s.title}` : ""}`;
  }
});

// --- startup --------------------------------------------------------------

let recentTags = [];
tagsInput.addEventListener("input", () => drawTagSuggestions(recentTags));

async function loadFormState() {
  const state = await ask({ type: "capture_form_state" });
  if (!state || state.ok === false) return;
  recentTags = state.recentTags || [];
  drawTagSuggestions(recentTags);
  drawProfiles(state);
  collectionList.textContent = "";
  for (const name of state.collections || []) {
    const option = document.createElement("option");
    option.value = name;
    collectionList.appendChild(option);
  }
  drawQueue(state);
}

/**
 * How often the panel re-asks the bridge while it is not connected. A probe
 * taken only at open went stale the moment someone started the bridge in a
 * terminal with the panel still showing, which is exactly when they are
 * looking at it. Loopback and short, so it costs nothing.
 */
const HEALTH_POLL_MS = 2000;

/** The storage hints the probe was first given, reused by every poll. */
let probeHints = {};
let probing = false;

/**
 * checkHealth asks the bridge directly whether it is running, and is the
 * authoritative answer to the only question this panel's status exists to
 * answer. It cannot fail: a probe that finds nothing IS the "no bridge"
 * result, which is the most common state and has to feel instant, so there
 * is no error path and nothing to wait on.
 */
async function checkHealth() {
  if (probing) return;
  probing = true;
  try {
    const health = await MonoBridgeHealth.probe({ known: probeHints });
    const before = healthState;
    healthState = MonoBridgeHealth.toStatus(health);
    settle();
    // A bridge that has just appeared: ask the worker to dial now, the same
    // way "Try again" does, rather than leave it to its next scheduled
    // retry — which can be half a minute away once the fast retries are
    // spent, and is exactly the wait that made the panel look broken.
    if (before && before.reason === "no_bridge" && healthState.reason !== "no_bridge") {
      ask({ type: "set_ws_url", url: wsUrlInput.value.trim() || Status.DEFAULT_WS_URL });
    }
  } finally {
    probing = false;
  }
}

setInterval(() => {
  if (connection.status !== "connected") checkHealth();
}, HEALTH_POLL_MS);

async function init() {
  // The non-loopback override is session-scoped and must be re-enabled
  // each time — reset it whenever the panel opens.
  try {
    await chrome.storage.session.set({ allowNonLoopback: false });
  } catch {
    // storage unavailable — background treats a missing flag as no override
  }

  drawStatus();

  chrome.runtime.sendMessage({ type: "get_status" }, (response) => {
    // A stand-in until the probe answers, and only when it says something
    // the probe cannot. A worker that has just been woken to answer this
    // reports its boot-time "disconnected" with no reason — true, stale, and
    // exactly what used to flash "Not connected" at someone opening the
    // panel. Hold "Checking…" instead; the probe is already on its way.
    if (workerState || chrome.runtime.lastError || !response?.status) return;
    const informative =
      response.status === "connected" || response.status === "unpaired" || !!response.reason;
    if (!informative) return;
    workerState = response;
    settle();
  });

  const stored = await chrome.storage.local.get(["wsUrl", "pairedWsUrl", "workingWsUrl"]);
  wsUrlInput.value = stored.wsUrl || Status.DEFAULT_WS_URL;
  connection.wsUrl = wsUrlInput.value;

  // Ask the bridge itself, rather than asking our own socket about it. This
  // is what makes "no bridge is running" a statement instead of a guess —
  // and it is deliberately not awaited before the form loads, so the panel
  // is never waiting on the network to become usable.
  probeHints = stored;
  checkHealth();

  drawShortcut();
  await Promise.all([startFollowing(), loadFormState()]);
}

// A capture begun for a note that was never saved is dropped rather than
// left pinned in the worker's memory. Nothing was promised: only a
// committed capture is a capture.
window.addEventListener("pagehide", () => {
  if (began) chrome.runtime.sendMessage({ type: "capture_discard" });
});

// --- the page this panel is following ---------------------------------------

/** The window this panel belongs to. Set once: a panel never changes window. */
const here = { windowId: null };

/** What the card is showing, from View.describeTab. */
let page = View.describeTab(null);

/**
 * drawPage paints the card's head for the tab in front of the person, and
 * gates the button on whether that page can be saved at all.
 */
function drawPage(view) {
  pageTitle.textContent = view.title;
  pageTitle.title = view.url || "";
  pageHost.textContent = view.loading && view.host ? `${view.host} · loading…` : view.host;
  pageHost.hidden = !view.host;
  pageWhy.textContent = view.why;
  pageWhy.hidden = !view.why;

  pageMonogram.textContent = View.initialOf(view.host || view.title);
  drawFavicon(view.favicon);

  captureBtn.disabled = !view.capturable || captureBtn.dataset.busy === "true";
  batchGroupBtn.disabled = batching || view.groupId === -1;
  batchGroupWhy.hidden = view.groupId !== -1;
}

/**
 * drawFavicon puts the site's icon over its letter, but only once the icon
 * has actually loaded: a broken image in the one place the page is
 * identified looks like a broken page. An icon that arrives after the
 * person has moved to another tab is dropped.
 */
function drawFavicon(url) {
  const current = pageIcon.querySelector("img");
  if (current && current.dataset.src === url) return;
  if (current) current.remove();
  if (!url) return;
  const img = new Image(18, 18);
  img.alt = "";
  img.dataset.src = url;
  img.addEventListener("load", () => {
    if (page.favicon === url && !pageIcon.querySelector("img")) pageIcon.appendChild(img);
  });
  img.src = url;
}

/**
 * followTab makes `tab` the page this panel is about. When it is a
 * different page from before, everything learned about the old one goes:
 * the early snapshot (it is of the wrong page), the capture message (it was
 * about the wrong page), and the "already saved" band, which
 * sidepanel_recall.js re-asks for on the event below.
 */
function followTab(tab, recheck) {
  const next = View.describeTab(tab);
  const moved = next.key !== page.key;
  page = next;
  drawPage(next);
  if (!moved && !recheck) return;

  if (moved) {
    if (began) ask({ type: "capture_discard" });
    began = false;
    showCapture("ok", "");
    captureMsg.textContent = "";
  }
  document.dispatchEvent(new CustomEvent("panel:page", { detail: next }));
}

async function refreshActiveTab(recheck) {
  if (here.windowId === null) return;
  const [tab] = await chrome.tabs.query({ active: true, windowId: here.windowId });
  followTab(tab || null, recheck);
}

async function startFollowing() {
  try {
    const win = await chrome.windows.getCurrent();
    here.windowId = win.id;
  } catch {
    // No window (the panel is being torn down). Nothing to follow.
    return;
  }

  // Another tab in this window came to the front.
  chrome.tabs.onActivated.addListener(({ tabId, windowId }) => {
    if (windowId !== here.windowId) return;
    chrome.tabs.get(tabId).then(
      (tab) => followTab(tab, true),
      () => refreshActiveTab(true)
    );
  });

  // The tab in front navigated, finished loading, or was retitled.
  chrome.tabs.onUpdated.addListener((tabId, change, tab) => {
    if (tabId !== page.tabId) return;
    const what = View.tabChange(change);
    if (what.redraw) followTab(tab, what.recheck);
  });

  // A tab dragged into or out of this window changes what is in front.
  chrome.tabs.onAttached.addListener((tabId, info) => {
    if (info.newWindowId === here.windowId) refreshActiveTab(true);
  });
  chrome.tabs.onDetached.addListener((tabId, info) => {
    if (info.oldWindowId === here.windowId) refreshActiveTab(true);
  });

  await refreshActiveTab(true);
}

/** The panel's window and tab, for sidepanel_recall.js. */
globalThis.MonoPanelPage = {
  current: () => page,
  windowId: () => here.windowId,
};

// --- things that change behind the panel's back -----------------------------

// Captures also arrive through the shortcut and the context menu, and the
// profile can be changed from another window's panel. The popup never saw
// either — it was closed — but a panel is open while they happen.
chrome.storage.onChanged.addListener((changes, area) => {
  if (area !== "local") return;
  if (changes.captureQueue || changes.captureFailures) refreshQueue();
  // The binding can change while this panel is open (`extension bind`, the
  // app's Settings → Browsers). Redraw it, or the next Save would write the
  // stale choice back and silently undo that bind.
  if ((changes[MonoBrowserBinding.PROFILE_KEY] || changes[MonoBrowserBinding.PROFILE_IDS_KEY] || changes[MonoBrowserBinding.LABEL_KEY]) && typeof panelBinding !== "undefined") {
    panelBinding.refresh().catch(() => {});
  }
  if (changes.captureProfile && lastFormState) {
    const id = changes.captureProfile.newValue || "";
    if (!profiles.current || profiles.current.id !== id) {
      drawProfiles(Object.assign({}, lastFormState, { profile: id, profileChanged: false }));
    }
  }
});

/**
 * drawShortcut shows the capture shortcut the browser actually has, which
 * is whatever the person set in the browser's shortcut settings — or none,
 * in which case the sentence is not drawn rather than naming a key that
 * does nothing.
 */
let shortcutKnown = false;

async function drawShortcut() {
  try {
    const commands = await chrome.commands.getAll();
    const capture = commands.find((c) => c.name === "capture-page");
    shortcutKey.textContent = (capture && capture.shortcut) || "";
    shortcutKnown = true;
  } catch {
    shortcutKey.textContent = "";
  }
  drawStatus();
}

// The browser's own shortcuts page. chrome:// is the address every
// Chromium browser answers to, Edge included (it shows edge://).
shortcutSet.addEventListener("click", () => {
  chrome.tabs.create({ url: "chrome://extensions/shortcuts" });
});

// Changed on that page, and the panel is still open: pick it up on return.
window.addEventListener("focus", drawShortcut);

// --- connection settings ---------------------------------------------------

// Which profile this browser runs automations for (sidepanel_binding.js).
// Its checklist reuses the capture picker's profile list.
const panelBinding = MonoPanelBinding.install({
  doc: document,
  storage: chrome.storage.local,
  userAgent: navigator.userAgent,
  profiles: () => (lastFormState && Array.isArray(lastFormState.profiles) ? lastFormState.profiles : []),
});
panelBinding.refresh().catch(() => {});

pairBtn.addEventListener("click", async () => {
  const value = pairingTokenInput.value.trim();
  if (!value) {
    pairingTokenInput.focus();
    showError("Paste the token printed by: " + Status.PAIR_COMMAND);
    return;
  }

  // Sends the token typed into the field, never reads it back out of
  // storage, so the panel never displays a previously-saved secret.
  chrome.runtime.sendMessage({ type: "set_pairing_token", value }, (response) => {
    if (chrome.runtime.lastError) {
      showError(chrome.runtime.lastError.message);
      return;
    }
    if (!response || response.ok === false) {
      showError(response?.error || "Failed to save pairing token");
      return;
    }
    hide(wsErrorMsg);
    pairingTokenInput.value = "";
    show(pairSavedMsg, "ok", "Saved — reconnecting…");
    noteStatus({ status: "connecting" });
    setTimeout(() => hide(pairSavedMsg), 2000);
  });
});

saveBtn.addEventListener("click", async () => {
  const url = wsUrlInput.value.trim();
  if (!url) return;

  // Session-only override flag — mirrors the checkbox, never persisted to
  // local storage, and cleared again when the panel reopens.
  try {
    await chrome.storage.session.set({ allowNonLoopback: allowRemoteCheckbox.checked });
  } catch {
    // ignore — background will treat as no override
  }

  chrome.runtime.sendMessage({ type: "set_ws_url", url }, (response) => {
    if (chrome.runtime.lastError) {
      showError(chrome.runtime.lastError.message);
      return;
    }
    if (!response || response.ok === false) {
      showError(response?.error || "Failed to save URL");
      return;
    }
    hide(wsErrorMsg);
    connection.wsUrl = url;
    show(wsSavedMsg, "ok", "Saved — reconnecting…");
    noteStatus({ status: "connecting" });
    setTimeout(() => hide(wsSavedMsg), 2000);
  });
});

init();
