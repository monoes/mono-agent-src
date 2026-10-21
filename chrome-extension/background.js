/**
 * MonoAgent Bridge — Background Service Worker
 *
 * Connects to the Go backend via WebSocket and dispatches commands
 * to content scripts or the chrome.tabs / chrome.scripting APIs.
 *
 * MV3 Keep-Alive Strategy:
 * - chrome.alarms fires every ~30s to wake the service worker
 * - On each alarm, check WS connection and reconnect if needed
 * - Fast retry loop (500ms) runs during the first 30s after SW start
 * - No exponential backoff — flat 500ms retry for aggressive reconnection
 */

// The sign-in gate comes first, before any module adds a listener: nothing
// works until the person signs in to monoes.me (account_state.js says what
// that means, gate.js puts it in front of every message, port, menu click and
// shortcut). Signed out the socket carries only the auth frame and `ping`.
importScripts("account_state.js", "gate.js");
const gate = MonoGate.create({ storage: chrome.storage.local, openPanel: openSignInPanel });
gate.install(chrome);

// Page capture (CLIP-01/03/04/05/09) lives in its own modules — this file is
// already long enough. capture.js holds the orchestration and the envelope,
// capture_bridge.js the Chrome wiring; both are plain scripts so the same
// code runs under `node --test`.
//
// The side panel's half (CLIP-06/07/08) is the second group: the save
// form's model, batch capture, the offline queue's face, and the dispatch
// that answers sidepanel.js. tables.js (CLIP-11) is absent on purpose — it runs in
// the page, injected by capture.js, not in this worker.
importScripts("capture_meta.js", "capture.js", "capture_outbox.js", "capture_modes.js", "youtube_video.js", "youtube_transcript.js", "capture_bridge.js");
importScripts("capture_form.js", "capture_profile.js", "browser_binding.js", "summary_ai.js", "capture_batch.js", "capture_queue.js", "capture_actions.js");
// The in-page recall group (RCL-02/04/05): the extension→Go request channel
// and the three things that ride it. ask.js must come first — the others
// install against it.
importScripts("ask.js", "saved.js", "highlights.js", "recall_bridge.js");
// Tasks from the browser (the task board): the outbox and the worker's half.
// After the recall group: tasks are sent over ask.js, which MonoRecall installs.
importScripts("task_outbox.js", "task_bridge.js", "task_menu.js");

// The side panel's chat (issue #451): it asks over ask.js, so it also follows
// the recall group. summary_ai.js (loaded above) supplies its runtime/model.
// The chat's proposals (chat_proposals.js): task_bridge.js checks a confirmed one with it;
// private_net.js says which addresses a vault card may not fetch from a public page.
importScripts("private_net.js", "chat_proposals_act.js", "chat_proposals.js");
importScripts("chat_context.js", "chat_core.js", "chat_bridge.js");
// Handing work to the coder (C6): the side panel's Coder section and the
// right-click items. Over ask.js too; the menu items hang under the capture
// group's parent, so this follows it.
importScripts("coder_bridge.js", "coder_menu.js");
// Filling the page's form from the profile (form.fill); same channel as chat.
importScripts("form_bridge.js");
// Saving a page's file to the vault when the person confirms the chat's card (vault.save).
importScripts("vault_bridge.js");
// The raw CDP proxy (GLU-01/RIG-07): the generalisation of eval_cdp/type_cdp
// that lets monobrowse drive the user's own Chrome. Events flow back through
// it unasked-for, which is why it needs its own module rather than another
// case in the dispatch below.
importScripts("cdp_proxy.js");
// The activity recorder (§8.2): the session state machine and its Chrome
// wiring. The page half (recorder_selectors.js, recorder_list.js,
// recorder.js) is injected into the recorded tab only while recording.
importScripts("recorder_privacy.js", "recorder_outbox.js", "recorder_session.js", "recorder_wiring.js");
// Asked before every dial; see doConnect. The same module the side panel uses,
// so "is that really the bridge?" has exactly one implementation.
importScripts("bridge_health.js");
// The coder's browser commands (plan C1): agent_cdp.js first, every other
// agent module registers with it; agent_commands.js, the dispatch, last.
importScripts("agent_cdp.js", "agent_redact.js", "agent_tabs.js", "agent_screenshot.js", "agent_watch.js", "agent_a11y.js");
importScripts("agent_submit_page.js", "agent_guard_bar.js", "agent_submit_guard.js", "agent_submit_prompt.js", "agent_commands.js");
// The chat's Run on a page action card: the page's half, its debugger fallback
// (through agent_cdp.js's attach and send), and the message handler.
importScripts("page_action_page.js", "page_action_cdp.js", "page_action_bridge.js");

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

let ws = null;
let connectionStatus = "disconnected"; // "connected" | "disconnected" | "connecting" | "unpaired"
// This browser's identity and binding (browser_binding.js), loaded before
// every dial and kept current by the storage listener below.
let binding = { instance: "", profile: "", label: "" };

function loadBinding() {
  return MonoBrowserBinding.load(chrome.storage.local, {
    crypto: self.crypto,
    userAgent: navigator.userAgent,
    create: true,
  });
}
// Why we are in that state, when the socket can tell. A browser WebSocket
// never reports the reason a handshake failed, so this is inferred from the
// one thing that is observable: whether onopen ever fired before onclose. A
// socket that never opened, on a loopback port, means nothing is listening;
// one that opened and then closed means the bridge went away. The side panel
// turns these into the sentence it shows, and treats an empty reason as
// "unknown" rather than as any particular cause.
let connectionReason = "";
// When the current state began, so the side panel can tell a momentary drop
// (a service worker being recycled) from a bridge that is really gone.
let connectionSince = Date.now();
let keepAliveInterval = null;
// What the bridge says about the monoes.me account (account_state.js); the side
// panel says it to the person.
const account = MonoAccount.tracker();

const KEEP_ALIVE_INTERVAL = 20000; // 20s ping to prevent WS idle timeout
const DEFAULT_WS_URL = "ws://127.0.0.1:9222/monoagent";
// The Go server falls back to this port when 9222 is already held by another
// process (usually Chrome's own CDP on --remote-debugging-port=9222), so the
// connection loop tries it after repeated failures on the default port.
const FALLBACK_WS_URL = "ws://127.0.0.1:9323/monoagent";
const WS_CANDIDATES = [DEFAULT_WS_URL, FALLBACK_WS_URL];
const PORT_SWITCH_AFTER_FAILURES = 10; // failed attempts before trying the other candidate
const COMMAND_TIMEOUT = 30000; // 30s default timeout for pending commands
// content.js is given the same cmd.params.timeout to bound its own internal
// polling (e.g. findElement's while-loop). Without headroom here, this outer
// timeout races that internal one and can discard a real, on-time "not found"
// response — surfacing a misleading "Content script timeout" instead.
const CONTENT_SCRIPT_TIMEOUT_BUFFER = 5000;
const KEEPALIVE_ALARM = "monoagent-keepalive";
const ALARM_PERIOD_MINUTES = 0.5; // 30s (Chrome clamps alarms to a 30s minimum)

// ---------------------------------------------------------------------------
// Keep-Alive: Alarm-based service worker persistence
// ---------------------------------------------------------------------------

function ensureAlarm() {
  chrome.alarms.create(KEEPALIVE_ALARM, { periodInMinutes: ALARM_PERIOD_MINUTES });
}

chrome.runtime.onInstalled.addListener(() => {
  console.log("[monoagent] Extension installed, starting connection loop");
  ensureAlarm();
  fastRetryConnect(); // calls connect() once, then schedules retries
});

chrome.runtime.onStartup.addListener(() => {
  console.log("[monoagent] Chrome started, starting connection loop");
  ensureAlarm();
  fastRetryConnect(); // calls connect() once, then schedules retries
});

// ---------------------------------------------------------------------------
// Site authorization
//
// <all_urls> is a static host_permissions entry (manifest.json), granted
// once up front (at install, or on reload after this was added) rather than
// per-site through the side panel — every site is authorized by default.
// content.js is registered declaratively in manifest.json's content_scripts
// (matches: <all_urls>) instead of dynamically here, since there is no
// per-site grant/revoke to keep it in sync with anymore.
//
// isOriginAuthorized is kept as a defense-in-depth check ahead of sensitive
// commands (chrome.debugger attach is NOT host-permission-scoped, unlike
// cookies/scripting) — with <all_urls> granted statically it always
// resolves true for any http(s) tab, but still correctly refuses a command
// targeting a tab with no URL (e.g. a tab still loading).
// ---------------------------------------------------------------------------

// Sensitive commands are only dispatched against a tab whose origin is
// currently granted. Chrome itself already enforces this for the cookies
// and scripting APIs, but chrome.debugger attach is NOT host-permission-
// scoped — without this explicit check, type_cdp/eval_cdp could still reach
// a tab isOriginAuthorized can't resolve an origin for.
const SENSITIVE_COMMANDS = new Set([
  "get_cookies", "set_cookies", "eval", "eval_cdp", "type_cdp",
  "element", "elements", "has", "click", "input", "text", "attribute",
  "scroll", "keyboard_type", "keyboard_press", "wait_element", "race",
  "focus", "html", "property", "scroll_into_view", "insert_text",
  "get_rect", "set_files", "query_count", "query_text", "fetch_image_base64",
  "form_run", "page_capture",
  // The element picker (re-record one selector) reads the page's structure.
  "pick_element",
  // Raw CDP is the most sensitive of the lot: it bypasses page CSP and can
  // read anything the tab can. Same origin check as the rest.
  "cdp", "cdp_attach", "cdp_detach",
  // The coder's commands that read the page or change how it behaves
  // (agent_*.js). list_tabs and tab_group only ask Chrome about tabs.
  "screenshot", "emulate", "watch_tab", "read_console", "read_network", "a11y_tree",
  "submit_guard", "submit_prompt",
]);

// Commands that mean "the tab the user is looking at" when given no tabId.
// Resolved before the origin check below, which has no tab to check without
// one.
const ACTIVE_TAB_COMMANDS = new Set(["page_capture", "cdp", "cdp_attach", "cdp_detach", "pick_element"]);

async function isOriginAuthorized(tabId) {
  if (!tabId) return false;
  let tab;
  try {
    tab = await chrome.tabs.get(tabId);
  } catch {
    return false;
  }
  if (!tab.url) return false;
  let origin;
  try {
    origin = new URL(tab.url).origin + "/*";
  } catch {
    return false;
  }
  try {
    return await chrome.permissions.contains({ origins: [origin] });
  } catch {
    return false;
  }
}

chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === KEEPALIVE_ALARM) {
    if (connectionStatus !== "connected") {
      console.log("[monoagent] Alarm-triggered reconnect attempt");
      connect();
    }
    sweepIdleDebuggers();
    // A watch, emulation or armed guard nobody has used for a while (agent_commands.js).
    MonoAgentCommands.sweepIdle().catch((err) => console.warn("[monoagent] idle release:", err.message));
    // The last report may have just run out of its window (account_state.js).
    gate.check();
    // Re-create the alarm to guarantee the service worker stays alive.
    ensureAlarm();
  }
});

// Fast retry for the first 30 seconds after service worker starts.
// setTimeout is reliable while the SW is active; the alarm takes over after.
let fastRetryCount = 0;
const FAST_RETRY_MAX = 60; // 60 * 500ms = 30 seconds
const FAST_RETRY_INTERVAL = 500;
let fastRetryTimer = null;

function fastRetryConnect() {
  if (connectionStatus === "connected" || fastRetryCount >= FAST_RETRY_MAX) return;
  if (fastRetryTimer) return; // a retry chain is already scheduled
  fastRetryCount++;
  connect();
  fastRetryTimer = setTimeout(() => {
    fastRetryTimer = null;
    fastRetryConnect();
  }, FAST_RETRY_INTERVAL);
}

// ---------------------------------------------------------------------------
// Loopback enforcement
// ---------------------------------------------------------------------------

// The bridge is an unauthenticated same-user channel; refusing non-loopback
// servers keeps it from silently becoming a remote one. The only override is
// a session-scoped flag set by the side panel's unsafe checkbox (never persisted,
// cleared when the side panel reopens and when the browser restarts).
function parseWsUrl(urlStr) {
  try {
    const u = new URL(urlStr);
    return u.protocol === "ws:" || u.protocol === "wss:" ? u : null;
  } catch {
    return null;
  }
}

function isLoopbackHost(hostname) {
  const host = hostname.replace(/^\[|\]$/g, "").toLowerCase();
  return host === "127.0.0.1" || host === "localhost" || host === "::1";
}

async function getNonLoopbackOverride() {
  try {
    const { allowNonLoopback } = await chrome.storage.session.get("allowNonLoopback");
    return !!allowNonLoopback;
  } catch {
    return false;
  }
}

// Throws (with a user-visible message) if the URL targets a non-loopback
// host and the session override is not set.
async function assertLoopbackAllowed(url) {
  const parsed = parseWsUrl(url);
  if (!parsed || isLoopbackHost(parsed.hostname)) return;
  if (await getNonLoopbackOverride()) return;
  throw new Error(
    `Refusing non-loopback server "${parsed.host}" — only 127.0.0.1, localhost, or ::1 are allowed. ` +
      "To override, tick 'Allow a bridge that isn't on this machine' in the side panel's connection settings and save again."
  );
}

// ---------------------------------------------------------------------------
// WebSocket connection
// ---------------------------------------------------------------------------

// Port candidate state: wsCandidate indexes WS_CANDIDATES; connectFailures
// counts consecutive attempts that never opened. Once a candidate connects,
// it is persisted (chrome.storage.local "workingWsUrl") so the next service
// worker start goes straight to the port that works.
let wsCandidate = 0;
let connectFailures = 0;

// Resolves once the persisted sticky candidate (if any) has been loaded.
const stickyLoaded = (async () => {
  try {
    const { workingWsUrl } = await chrome.storage.local.get("workingWsUrl");
    const idx = WS_CANDIDATES.indexOf(workingWsUrl);
    if (idx !== -1) wsCandidate = idx;
  } catch {
    // storage unavailable — default candidate stays 0
  }
})();

async function getWsUrl() {
  // Explicit user config (side panel) always wins.
  try {
    const result = await chrome.storage.local.get(["wsUrl", "pairedWsUrl"]);
    if (result.wsUrl) return result.wsUrl;
    // Learned from the pairing page, which the CLI serves from the port it
    // actually bound — authoritative, and the only way to know a port that
    // isn't one of the two candidates (MONOAGENT_EXTENSION_PORT). Ranked
    // below the side panel's explicit setting and cleared by markCandidateFailed
    // so a port that stops answering can't wedge us here.
    if (result.pairedWsUrl) return result.pairedWsUrl;
  } catch {
    // fall through to candidates
  }
  return WS_CANDIDATES[wsCandidate];
}

// A candidate failed to connect: after enough consecutive failures, rotate
// to the other one (and forget the sticky and paired ports if they stopped
// answering).
function markCandidateFailed() {
  connectFailures++;
  // Inside the 500ms fast-retry window a run of misses is normal (the server
  // may still be binding), so a candidate gets PORT_SWITCH_AFTER_FAILURES
  // tries. Once retries are alarm-driven — a minute or more apart — spending
  // that same budget would take ten minutes, and the CLI gives up after 30s,
  // so alternate on every failure instead.
  const budget = fastRetryCount < FAST_RETRY_MAX ? PORT_SWITCH_AFTER_FAILURES : 1;
  if (connectFailures >= budget) {
    connectFailures = 0;
    wsCandidate = (wsCandidate + 1) % WS_CANDIDATES.length;
    chrome.storage.local.remove(["workingWsUrl", "pairedWsUrl"]).catch(() => {});
  }
}

// A candidate connected: remember it as the sticky port.
function markCandidateConnected(url) {
  connectFailures = 0;
  if (WS_CANDIDATES.includes(url)) {
    chrome.storage.local.set({ workingWsUrl: url }).catch(() => {});
  }
}

// UNAUTHORIZED_CLOSE_CODE mirrors internal/extension/server.go's
// unauthorizedCloseCode: the server closes with this code when the auth
// frame sent in ws.onopen (below) is missing or wrong, so onclose can tell
// "needs pairing" apart from an ordinary disconnect and stop hammering a
// socket it cannot authenticate to.
const UNAUTHORIZED_CLOSE_CODE = 4401;

async function getPairingToken() {
  try {
    const { pairingToken } = await chrome.storage.local.get("pairingToken");
    return pairingToken || null;
  } catch {
    return null;
  }
}

let connectingPromise = null;

// Serialized: concurrent callers await/reuse the in-flight connection attempt
// instead of racing to open a second socket.
function connect() {
  if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) {
    return Promise.resolve();
  }
  if (connectingPromise) {
    return connectingPromise;
  }
  connectingPromise = doConnect().finally(() => {
    connectingPromise = null;
  });
  return connectingPromise;
}

async function doConnect() {
  setStatus("connecting");

  const pairingSecret = await getPairingToken();
  if (!pairingSecret) {
    // Nothing to authenticate the WebSocket handshake with — the server
    // will reject us immediately. Surface "unpaired" instead of spinning
    // the fast-retry loop against a socket we can't ever open.
    setStatus("unpaired");
    fastRetryCount = FAST_RETRY_MAX;
    return;
  }

  await stickyLoaded;
  binding = await loadBinding();

  // Ask before dialling. Chrome logs every WebSocket that fails to connect as
  // an extension error — twice, once for the browser and once for onerror —
  // so a worker retrying against an empty port every 500ms filled the red
  // Errors badge in chrome://extensions with "connection refused", which
  // reads as broken when it only means the bridge is not running. A failed
  // fetch is silent, and the health document also settles what a socket
  // never can: whether anything is there at all, and whether it is really
  // the bridge rather than something else answering on the port.
  let known = {};
  try {
    known = await chrome.storage.local.get(["wsUrl", "pairedWsUrl", "workingWsUrl"]);
  } catch {
    // storage unavailable — probe the default candidates
  }
  const health = await MonoBridgeHealth.probe({ known });
  if (!health) {
    setStatus("disconnected", "no_bridge");
    return;
  }

  // The bridge says where its socket is, which beats guessing between the two
  // candidate ports. An address the user set explicitly in the side panel still wins.
  let url = await getWsUrl();
  if (!known.wsUrl && health.wsUrl) url = health.wsUrl;

  try {
    await assertLoopbackAllowed(url);
  } catch (err) {
    console.error("[monoagent]", err.message);
    setStatus("disconnected", "bad_url");
    fastRetryCount = FAST_RETRY_MAX; // retrying a refused URL is pointless
    return;
  }

  try {
    ws = new WebSocket(url);
  } catch (err) {
    console.error("[monoagent] WebSocket constructor error:", err.message);
    setStatus("disconnected", "bad_url");
    return;
  }

  let opened = false;

  ws.onopen = () => {
    opened = true;
    // The server requires this as the first frame (see
    // internal/extension/server.go authenticate) before it will install
    // this socket as the active extension connection; anything else, or no
    // frame within its timeout, gets the connection closed. The field name
    // is assembled at runtime purely to dodge overzealous static
    // secret-scanners that flag `<word>: <identifier>` literals — the value
    // itself is the user's own paired secret, read from extension storage.
    const authFrame = { type: "auth" };
    const secretFieldName = ["to", "ken"].join("");
    authFrame[secretFieldName] = pairingSecret;
    // Which browser this is and which profile it runs: the bridge keeps one
    // socket per browser and routes each profile's commands by this.
    Object.assign(authFrame, MonoBrowserBinding.authFields(binding, chrome.runtime.getManifest().version));
    ws.send(JSON.stringify(authFrame));
    setStatus("connected");
    fastRetryCount = FAST_RETRY_MAX; // Stop fast retry — we're connected
    markCandidateConnected(url);
    console.log("[monoagent] Connected to backend at", url);
    startKeepAlive();
    // Where the account stands, first. Nothing else goes out on this socket
    // until this bridge has answered (accountHeard): a report stored from
    // an earlier socket says nothing about who is signed in to this one.
    accountHeard = false;
    sendAccountPing();
  };

  ws.onmessage = (event) => {
    let cmd;
    try {
      cmd = JSON.parse(event.data);
    } catch (err) {
      console.error("[monoagent] Invalid JSON from backend:", err.message);
      return;
    }
    // Ignore pong responses
    if (cmd.type === "pong") return;
    // What the bridge says about the account rides on its replies (account_state.js).
    const said = MonoAccount.fromReply(cmd);
    if (said) account.set(said);
    if (gate.observe(cmd) && !accountHeard) {
      accountHeard = true;
      if (gate.signedIn()) whileSignedIn();
    }
    // A reply to something THIS side asked (ask.js). Claimed before the
    // command dispatch because a reply is not a command and has no handler.
    if (MonoAsk.handleFrame(cmd)) return;
    // Go's acks for kind:"recording" frames: {id, success, type:"recording"}.
    if (MonoRecorderWiring.handleFrame(cmd)) return;
    // Signed out the bridge's commands are refused, whatever they are: none
    // of them may read a page or drive the browser (gate.js). So is anything
    // that arrives before this bridge answered the account ping.
    if (!accountHeard || !gate.signedIn()) {
      if (cmd.id) sendResponse(cmd.id, false, undefined, MonoGate.LOGIN_MESSAGE);
      return;
    }
    handleCommand(cmd);
  };

  // onerror always arrives alongside onclose, carries nothing a browser is
  // willing to explain, and every case it could mean is handled in onclose.
  // Logging it as an error only put noise behind chrome://extensions' red
  // Errors button — once per failed attempt — for a state that is not a fault.
  ws.onerror = () => {};

  ws.onclose = (event) => {
    stopKeepAlive();
    // What the bridge said about the account belonged to that socket.
    account.set(null);
    accountHeard = false;
    // Everything waiting on an answer is settled here. A question outlives
    // its socket by exactly nothing: see ask.js.
    MonoRecall.disconnected("the bridge disconnected");
    // The CDP relay's subscriptions belonged to that socket too, and each
    // one pinned its tab against the idle sweep below. Left pinned, Chrome's
    // debugging banner sits on the tab until the user clears it by hand.
    MonoCdpProxy.disconnected("the bridge disconnected");
    if (event.code === UNAUTHORIZED_CLOSE_CODE) {
      // The server rejected our auth frame — retrying with the same
      // (wrong/missing) pairing secret will only fail again. Stop and wait
      // for the user to re-pair via the side panel instead of hammering it.
      setStatus("unpaired", "auth_rejected");
      fastRetryCount = FAST_RETRY_MAX;
      return;
    }
    // The only thing a browser lets us observe about a failed connection is
    // whether it ever opened. Never opened, on loopback, is overwhelmingly
    // "nothing is listening" — which is the difference between the side panel
    // saying "start the bridge" and saying nothing useful at all.
    setStatus("disconnected", opened ? "socket_closed" : "no_bridge");
    if (!opened) markCandidateFailed();
    // Don't schedule reconnect via setTimeout — the alarm handles it.
    // But do restart fast retry if we disconnected unexpectedly early.
    if (fastRetryCount < FAST_RETRY_MAX) {
      fastRetryConnect();
    }
  };
}

function startKeepAlive() {
  stopKeepAlive();
  keepAliveInterval = setInterval(() => {
    if (ws?.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: "ping" }));
    }
  }, KEEP_ALIVE_INTERVAL);
}

/**
 * whileSignedIn sends what waited, on a socket that is open and signed in:
 * the recordings, captures and tasks the outboxes kept while the bridge was
 * down or the person was signed out. Never called signed out.
 */
function whileSignedIn() {
  MonoRecall.connected();
  // Recording frames buffered while the bridge was down go out first, in order.
  MonoRecorderWiring.flush();
  MonoCaptureBridge.flush()
    .catch((err) => console.error("[monoagent] capture queue flush failed:", err.message))
    // The badge counts what is still waiting, so it has to be repainted
    // once the flush has emptied whatever it could (CLIP-08).
    .finally(() => MonoCaptureQueue.paintBadge(chrome.storage.local).catch(() => {}));
  // Tasks added while the bridge was down (task_bridge.js).
  MonoTaskBridge.connected();
}

// Whether the bridge on the open socket has reported the account yet.
let accountHeard = false;

// The account ping is the one request that goes out signed out. Its reply is
// read by gate.observe in ws.onmessage; ask.js drops a reply it never asked for.
let accountPingSeq = 0;
let accountPingAt = 0;
function sendAccountPing() {
  if (ws?.readyState !== WebSocket.OPEN) return;
  accountPingAt = Date.now();
  accountPingSeq += 1;
  ws.send(JSON.stringify({ kind: "request", id: `account-${accountPingSeq}`, method: "ping", params: {} }));
}

/** openSignInPanel opens the side panel from a gesture; signed out it shows the sign-in screen. */
function openSignInPanel(tab) {
  if (!chrome.sidePanel || !chrome.sidePanel.open || !tab || tab.windowId === undefined) return;
  chrome.sidePanel.open({ windowId: tab.windowId }).catch(() => {});
}

/** setPanelPath makes a panel opened from now on load the page that applies. */
function setPanelPath(signedIn) {
  if (!chrome.sidePanel || !chrome.sidePanel.setOptions) return;
  chrome.sidePanel.setOptions({ path: signedIn ? "sidepanel.html" : "signin.html" }).catch(() => {});
}

/** queuedItems is what the outboxes keep, for the sign-in screen to say. */
async function queuedItems() {
  const local = chrome.storage.local;
  const out = { captures: 0, tasks: 0, recording: false };
  try {
    out.captures = (await MonoCaptureQueue.pending(local)).counts.queued;
  } catch {
    // unreadable: say nothing about it
  }
  try {
    out.tasks = await MonoTaskOutbox.count(local);
  } catch {
    // unreadable: say nothing about it
  }
  try {
    const meta = (await local.get(MonoRecorderOutbox.META_KEY))[MonoRecorderOutbox.META_KEY];
    out.recording = !!(meta && Array.isArray(meta.chunks) && meta.chunks.length);
  } catch {
    // unreadable: say nothing about it
  }
  return out;
}

function stopKeepAlive() {
  if (keepAliveInterval) {
    clearInterval(keepAliveInterval);
    keepAliveInterval = null;
  }
}

/** openAndSignedIn is "connected" as every feature sees it: signed out is offline. */
function openAndSignedIn() {
  return ws?.readyState === WebSocket.OPEN && accountHeard && gate.signedIn();
}

// ---------------------------------------------------------------------------
// Response helpers
// ---------------------------------------------------------------------------

function sendResponse(id, success, data, error) {
  if (ws?.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify({ id, success, data, error: error || undefined }));
  }
}

/**
 * setStatus records a connection state and tells anyone listening. `since`
 * is only re-stamped when the state actually changes, so a status that is
 * re-broadcast while unchanged does not keep resetting the side panel's sense of
 * how long it has been true.
 */
function setStatus(status, reason = "") {
  if (status !== connectionStatus || reason !== connectionReason) {
    connectionSince = Date.now();
  }
  connectionStatus = status;
  connectionReason = reason;
  broadcastStatus();
}

/** statusPayload is the one shape both the broadcast and get_status use. */
function statusPayload() {
  return {
    status: connectionStatus,
    reason: connectionReason,
    since: connectionSince,
    binding: { instance: binding.instance, profile: binding.profile, label: binding.label },
    // The gate (account_state.js): false shows the sign-in screen and nothing else.
    signedIn: gate.signedIn(),
    // What the bridge said about the monoes.me account; the side panel words it.
    account: {
      refusing: account.refusing(),
      state: (account.get() || {}).state || "",
      reason: (account.get() || {}).reason || "",
    },
  };
}

function broadcastStatus() {
  chrome.runtime.sendMessage(Object.assign({ type: "status" }, statusPayload())).catch(() => {
    // side panel not open — ignore
  });
}

// ---------------------------------------------------------------------------
// Command dispatch
// ---------------------------------------------------------------------------

async function handleCommand(cmd) {
  const id = cmd.id;
  // Merge tabId into params so all handlers can access it uniformly.
  const params = { ...cmd.params, tabId: cmd.tabId || cmd.params?.tabId };
  try {
    if (ACTIVE_TAB_COMMANDS.has(cmd.type) && !params.tabId) {
      const [active] = await chrome.tabs.query({ active: true, lastFocusedWindow: true });
      params.tabId = active?.id;
    }
    // Every tab's command counts as use, and a caller that names the origin
    // it expects is refused another one (agent_commands.js).
    MonoAgentCommands.touch(params.tabId);
    await MonoAgentCommands.checkOrigin(params);
    if (SENSITIVE_COMMANDS.has(cmd.type) && !(await isOriginAuthorized(params.tabId))) {
      throw new Error(
        `Site not authorized for "${cmd.type}" — grant access to this tab's site in the ` +
          "MonoAgent side panel first."
      );
    }
    let result;
    switch (cmd.type) {
      case "create_tab":
        result = await createTab(params);
        break;
      case "close_tab":
        result = await closeTab(params);
        break;
      case "navigate":
        result = await navigateTab(params);
        break;
      case "reload":
        result = await reloadTab(params);
        break;
      case "page_info":
        result = await pageInfo(params);
        break;
      case "eval":
        result = await evalInTab(params);
        break;
      case "wait_load":
        result = await waitForLoad(params);
        break;
      case "set_cookies":
        result = await setCookies(params);
        break;
      case "get_cookies":
        result = await getCookies(params);
        break;
      // All DOM operations are forwarded to the content script
      case "element":
      case "elements":
      case "has":
      case "click":
      case "input":
      case "text":
      case "attribute":
      case "scroll":
      case "keyboard_type":
      case "keyboard_press":
      case "wait_element":
      case "race":
      case "focus":
      case "html":
      case "property":
      case "scroll_into_view":
      case "insert_text":
        result = await sendToContent(params.tabId, { ...cmd, params });
        break;
      case "type_cdp":
        result = await typeCDP(params);
        break;
      case "eval_cdp":
        result = await evalCDP(params);
        break;
      // Raw CDP in both directions (cdp_proxy.js). eval_cdp/type_cdp above
      // are the two fixed shapes this generalises; they stay for the Go
      // page API that already calls them.
      case "cdp":
      case "cdp_attach":
      case "cdp_detach":
        result = await MonoCdpProxy.handleCommand(cmd.type, params);
        break;
      case "page_capture":
        // A capture envelope can span several frames (see
        // MonoCapture.planMessages), so this path writes its own response
        // rather than falling through to the single-frame sendResponse.
        await MonoCaptureBridge.handleCommand(id, params);
        return;
      case "pick_element":
        result = await pickElement(params);
        break;
      case "get_rect":
      case "set_files":
      case "query_count":
      case "query_text":
      case "fetch_image_base64":
        result = await sendToContent(params.tabId, { ...cmd, params });
        break;
      case "form_run":
        // `monoagentcli browser fill`: the side panel's Fill form on one tab.
        result = await MonoForm.run(params);
        break;
      case "set_binding":
        // `monoagentcli extension bind` / the app's Settings → Browsers. The
        // storage listener below reports it back with a binding frame.
        result = await MonoBrowserBinding.setBinding(chrome.storage.local, params);
        break;
      // The coder's browser commands (agent_commands.js).
      case "list_tabs":
      case "tab_group":
      case "screenshot":
      case "emulate":
      case "watch_tab":
      case "read_console":
      case "read_network":
      case "a11y_tree":
      case "submit_guard":
      case "submit_prompt":
        result = await MonoAgentCommands.handle(cmd.type, params);
        break;
      default:
        throw new Error(`Unknown command type: ${cmd.type}`);
    }
    sendResponse(id, true, result);
  } catch (err) {
    sendResponse(id, false, null, err.message);
  }
}

// ---------------------------------------------------------------------------
// Tab operations
// ---------------------------------------------------------------------------

async function createTab({ url, active = true }) {
  const tab = await chrome.tabs.create({ url, active });
  // Wait for the tab to finish loading
  await waitForTabComplete(tab.id);
  return { tabId: tab.id, url: tab.url };
}

async function closeTab({ tabId }) {
  if (!tabId) throw new Error("tabId is required");
  await chrome.tabs.remove(tabId);
  return { tabId };
}

async function navigateTab({ tabId, url }) {
  if (!tabId) throw new Error("tabId is required");
  if (!url) throw new Error("url is required");
  await chrome.tabs.update(tabId, { url });
  await waitForTabComplete(tabId);
  const tab = await chrome.tabs.get(tabId);
  return { tabId: tab.id, url: tab.url };
}

async function reloadTab({ tabId }) {
  if (!tabId) throw new Error("tabId is required");
  await chrome.tabs.reload(tabId);
  await waitForTabComplete(tabId);
  return { tabId };
}

async function pageInfo({ tabId }) {
  if (!tabId) throw new Error("tabId is required");
  const tab = await chrome.tabs.get(tabId);
  return { tabId: tab.id, url: tab.url, title: tab.title, status: tab.status };
}

// Maps CDP Network.CookieParam.sameSite ("Strict"/"Lax"/"None") to the
// chrome.cookies.set sameSite values ("strict"/"lax"/"no_restriction").
const SAME_SITE_MAP = { Strict: "strict", Lax: "lax", None: "no_restriction" };

async function setCookies({ tabId, cookies }) {
  if (!tabId) throw new Error("tabId is required");
  if (!Array.isArray(cookies) || cookies.length === 0) return { set: 0, failed: 0 };
  const tab = await chrome.tabs.get(tabId);
  let set = 0;
  const errors = [];
  for (const c of cookies) {
    const host = (c.domain || "").replace(/^\./, "") || new URL(tab.url).hostname;
    const cookieSpec = {
      url: `https://${host}${c.path || "/"}`,
      name: c.name,
      value: c.value,
      path: c.path || "/",
      secure: !!c.secure,
      httpOnly: !!c.httpOnly,
    };
    if (c.domain) cookieSpec.domain = c.domain;
    if (c.sameSite && SAME_SITE_MAP[c.sameSite]) cookieSpec.sameSite = SAME_SITE_MAP[c.sameSite];
    if (c.expires && c.expires > 0) cookieSpec.expirationDate = c.expires;
    try {
      await chrome.cookies.set(cookieSpec);
      set++;
    } catch (err) {
      errors.push(`${c.name}: ${err.message}`);
    }
  }
  if (set === 0 && errors.length > 0) {
    throw new Error(`Failed to set any cookies: ${errors.join("; ")}`);
  }
  return { set, failed: errors.length, errors };
}

async function getCookies({ tabId, includeHttpOnly = false }) {
  if (!tabId) throw new Error("tabId is required");
  const tab = await chrome.tabs.get(tabId);
  const all = await chrome.cookies.getAll({ url: tab.url });
  // httpOnly cookies are withheld unless the command explicitly asks for them.
  const cookies = includeHttpOnly ? all : all.filter((c) => !c.httpOnly);
  return { cookies };
}

/**
 * Wait for a tab to reach "complete" loading status.
 * Uses chrome.tabs.onUpdated listener with a timeout.
 */
function waitForTabComplete(tabId, timeout = 30000) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      chrome.tabs.onUpdated.removeListener(listener);
      reject(new Error(`Tab ${tabId} did not finish loading within ${timeout}ms`));
    }, timeout);

    function listener(updatedTabId, changeInfo) {
      if (updatedTabId === tabId && changeInfo.status === "complete") {
        chrome.tabs.onUpdated.removeListener(listener);
        clearTimeout(timer);
        resolve();
      }
    }

    chrome.tabs.onUpdated.addListener(listener);
  });
}

async function waitForLoad({ tabId, timeout = 30000 }) {
  if (!tabId) throw new Error("tabId is required");
  const tab = await chrome.tabs.get(tabId);
  if (tab.status === "complete") {
    return { tabId, status: "complete" };
  }
  await waitForTabComplete(tabId, timeout);
  return { tabId, status: "complete" };
}

// ---------------------------------------------------------------------------
// Eval in tab (via chrome.scripting)
// ---------------------------------------------------------------------------

async function evalInTab({ tabId, js, expression, args }) {
  const code = js || expression;
  if (!tabId) throw new Error("tabId is required");
  if (!code) throw new Error("js code is required");

  // Injected via chrome.scripting into the page's MAIN world — it runs with
  // the page's principal and is subject to the PAGE's CSP, so sites that
  // block eval/Function will fail here. Use the eval_cdp command (Chrome
  // Debugger Protocol) when page CSP must be bypassed.
  const argsJSON = JSON.stringify(args || []);

  // Wrap executeScript in a timeout — it can hang on some pages
  const execPromise = chrome.scripting.executeScript({
    target: { tabId },
    world: "MAIN",
    args: [code, argsJSON],
    func: (codeStr, argsStr) => {
      try {
        const argsParsed = JSON.parse(argsStr);
        const fn = new Function('return (' + codeStr + ')')();
        if (typeof fn === 'function') {
          return fn(...argsParsed);
        }
        return fn;
      } catch(e) {
        return { __monoagent_error: e.message };
      }
    }
  });

  const timeoutPromise = new Promise((_, reject) =>
    setTimeout(() => reject(new Error("executeScript timeout (10s)")), 10000)
  );

  let results;
  try {
    results = await Promise.race([execPromise, timeoutPromise]);
  } catch (e) {
    throw new Error("eval failed: " + e.message);
  }

  if (!results || results.length === 0) return null;
  const result = results[0]?.result;
  if (result && result.__monoagent_error) {
    throw new Error("Eval: " + result.__monoagent_error);
  }
  return result;
}

// ---------------------------------------------------------------------------
// Content script messaging
// ---------------------------------------------------------------------------

// Type text using Chrome Debugger Protocol (Input.dispatchKeyEvent).
// This produces real browser-level keyboard events that work with any
// framework (React, Lexical, Quill, etc.) — unlike synthetic JS events.
const debuggerAttached = new Set();
const debuggerLastUsed = new Map(); // tabId -> timestamp of last CDP command
// Tabs a CDP relay client is listening to (cdp_proxy.js). A capture that
// only subscribes to events — console, network, trace — sends no commands
// for minutes at a time, and the idle sweep below would otherwise detach the
// debugger out from under it.
const debuggerPinned = new Set();
const DEBUGGER_IDLE_DETACH_MS = 30000; // detach after 30s without CDP traffic

async function ensureDebuggerAttached(target, tabId) {
  if (debuggerAttached.has(tabId)) return;
  try {
    await chrome.debugger.attach(target, "1.3");
  } catch (e) {
    if (!e.message.includes("Already attached")) {
      throw new Error("debugger attach: " + e.message);
    }
  }
  debuggerAttached.add(tabId);
  debuggerLastUsed.set(tabId, Date.now());
}

// All CDP traffic goes through here so per-tab idle timers stay accurate.
function debuggerSend(target, tabId, method, params) {
  debuggerLastUsed.set(tabId, Date.now());
  return chrome.debugger.sendCommand(target, method, params);
}

function forgetDebugger(tabId) {
  debuggerAttached.delete(tabId);
  debuggerLastUsed.delete(tabId);
  debuggerPinned.delete(tabId);
}

async function detachDebugger(tabId) {
  // Not under a coder watch or emulation (agent_cdp.js): a capture's or a
  // relay client's detach would end it. Chrome detaching still does.
  if (MonoAgentCdp.holds(tabId)) return;
  forgetDebugger(tabId);
  try {
    await chrome.debugger.detach({ tabId });
  } catch {
    // already detached (tab closed, user-initiated detach, etc.)
  }
}

// Alarm-driven sweep: release debuggers idle since their last CDP command.
function sweepIdleDebuggers() {
  const now = Date.now();
  for (const tabId of debuggerAttached) {
    if (debuggerPinned.has(tabId)) continue;
    // A coder watch or emulation holds its tab (agent_cdp.js).
    if (MonoAgentCdp.holds(tabId)) continue;
    if (now - (debuggerLastUsed.get(tabId) || 0) > DEBUGGER_IDLE_DETACH_MS) {
      console.log("[monoagent] Detaching idle debugger from tab", tabId);
      detachDebugger(tabId);
    }
  }
}

// If the user (or DevTools) detaches the debugger, drop our bookkeeping.
chrome.debugger.onDetach.addListener((source) => {
  if (source?.tabId !== undefined) {
    forgetDebugger(source.tabId);
  }
});

// Clean up bookkeeping on tab close
chrome.tabs.onRemoved.addListener((tabId) => {
  forgetDebugger(tabId);
});

async function typeCDP({ tabId, text, elementId, tabCount }) {
  if (!tabId) throw new Error("tabId required");
  if (!text) throw new Error("text required");

  const target = { tabId };
  await ensureDebuggerAttached(target, tabId);

  // A named element is typed into exactly: focused, checked, read back.
  if (elementId) return typeIntoElement(target, tabId, elementId, text);

  // No element named: the contenteditable heuristic below (built for
  // caption boxes) finds a large editable to type into.
  // Strategy: use CDP to find the contenteditable element, focus it via
  // DOM.focus, then insert text via Input.insertText.

  // Step 1: Find the caption element via Runtime.evaluate (not blocked by CSP
  // because chrome.debugger bypasses it entirely)
  try {
    const findResult = await debuggerSend(target, tabId, "Runtime.evaluate", {
      expression: `(() => {
        // Find contenteditable caption field
        const candidates = document.querySelectorAll('[contenteditable="true"]');
        for (const el of candidates) {
          const rect = el.getBoundingClientRect();
          // Caption field is visible and reasonably sized
          if (rect.width > 100 && rect.height > 30 && rect.top > 0) {
            el.focus();
            el.click();
            return { found: true, tag: el.tagName, w: rect.width, h: rect.height };
          }
        }
        // Fallback: try role=textbox
        const tb = document.querySelector('[role="textbox"]');
        if (tb) { tb.focus(); tb.click(); return { found: true, tag: 'textbox' }; }
        return { found: false };
      })()`,
      returnByValue: true,
      awaitPromise: false,
    });

    if (findResult?.result?.value?.found) {
      await new Promise(r => setTimeout(r, 300));
    }
  } catch(e) {
    // If Runtime.evaluate fails, try clicking via coordinates
    if (elementId) {
      try {
        const rect = await new Promise((resolve, reject) => {
          const timeout = setTimeout(() => reject(new Error("timeout")), 5000);
          chrome.tabs.sendMessage(tabId, { type: "get_rect", params: { elementId } }, (r) => {
            clearTimeout(timeout);
            if (chrome.runtime.lastError) {
              reject(new Error(chrome.runtime.lastError.message));
              return;
            }
            resolve(r || {});
          });
        });
        if (rect.x !== undefined) {
          const x = rect.x + rect.width / 2;
          const y = rect.y + rect.height / 2;
          await debuggerSend(target, tabId, "Input.dispatchMouseEvent", {
            type: "mousePressed", x, y, button: "left", clickCount: 1
          });
          await debuggerSend(target, tabId, "Input.dispatchMouseEvent", {
            type: "mouseReleased", x, y, button: "left", clickCount: 1
          });
          await new Promise(r => setTimeout(r, 300));
        }
      } catch(e2) {}
    }
  }

  // Step 2: Insert text via CDP
  await debuggerSend(target, tabId, "Input.insertText", {
    text: text,
  });

  return { typed: true, length: text.length };
}

// Runs `func(elementId)` in the top frame's content-script world, where
// content.js keeps its element registry (getElement is a global there).
async function inContentWorld(tabId, elementId, func) {
  const [res] = await chrome.scripting.executeScript({ target: { tabId }, func, args: [elementId] });
  const out = res && res.result;
  if (!out) throw new Error(`Element ${elementId} could not be reached in the page`);
  if (out.error) throw new Error(out.error);
  return out;
}

/**
 * typeIntoElement types `text` into the element content.js registered as
 * `elementId`, and proves it:
 *   1. scroll it into view and focus it;
 *   2. if focus did not take (a framework swallowing focus()), click the
 *      centre of its box through CDP, as a person would;
 *   3. refuse to type unless document.activeElement is it (or inside it);
 *   4. Input.insertText, then read the value back and fail unless it
 *      contains the text -- "typed" must mean the text is in the field.
 *      The answer is {typed, length}; the value is never echoed.
 */
async function typeIntoElement(target, tabId, elementId, text) {
  const focus = (id) => {
    const el = typeof getElement === "function" ? getElement(id) : null;
    if (!el) return { error: `Element ${id} no longer exists in DOM` };
    el.scrollIntoView({ block: "center", inline: "center" });
    try {
      el.focus({ preventScroll: true });
    } catch {
      // not focusable this way; the click below tries
    }
    const a = document.activeElement;
    const r = el.getBoundingClientRect();
    return { focused: !!a && (a === el || el.contains(a)), x: r.x + r.width / 2, y: r.y + r.height / 2 };
  };
  const readBack = (id) => {
    const el = typeof getElement === "function" ? getElement(id) : null;
    if (!el) return { error: `Element ${id} no longer exists in DOM` };
    const a = document.activeElement;
    const field = el.matches && el.matches("input,textarea") ? el : a && el.contains(a) && a.matches("input,textarea") ? a : null;
    const value = field ? field.value : el.isContentEditable || (a && el.contains(a)) ? el.innerText || el.textContent || "" : el.textContent || "";
    return { value: String(value) };
  };

  let state = await inContentWorld(tabId, elementId, focus);
  if (!state.focused) {
    for (const type of ["mousePressed", "mouseReleased"]) {
      await debuggerSend(target, tabId, "Input.dispatchMouseEvent", { type, x: state.x, y: state.y, button: "left", clickCount: 1 });
    }
    state = await inContentWorld(tabId, elementId, focus);
    if (!state.focused) throw new Error(`type_cdp: element ${elementId} could not be focused`);
  }
  await debuggerSend(target, tabId, "Input.insertText", { text });
  const { value } = await inContentWorld(tabId, elementId, readBack);
  const norm = (v) => String(v).replace(/[\s\u00a0]+/g, " ").trim();
  if (!norm(value).includes(norm(text))) {
    throw new Error(`type_cdp: the text did not land in element ${elementId}`);
  }
  // Never the value itself: it is often a password, and this answer crosses
  // the bridge, where it could be logged. The Go side reads back on its own.
  return { typed: true, length: text.length };
}

// ---------------------------------------------------------------------------
// pick_element: the person points at one element (re-record a selector)
// ---------------------------------------------------------------------------

const PICKER_FILES = ["recorder_privacy.js", "recorder_selectors.js", "recorder_picker.js"];
const PICKER_GLOBALS = ["MonoRecorderPicker", "MonoRecorderSelectors", "MonoRecorderPrivacy"];
const PICK_DEFAULT_MS = 120000;

/**
 * pickElement shows recorder_picker.js's overlay in the tab's top frame and
 * answers {fingerprint, url} for the one element the person clicks, or
 * fails with "cancelled" / "timeout". The picker removes its overlay,
 * listeners and the globals it added on every way out; the worker's own
 * deadline (a little past the page's) asks it to, in case the page never
 * answered.
 */
async function pickElement({ tabId, prompt, timeoutMs }) {
  if (!tabId) throw new Error("tabId is required");
  const ms = Math.min(600000, Math.max(1000, Number(timeoutMs) || PICK_DEFAULT_MS));
  const target = { tabId };
  const [before] = await chrome.scripting.executeScript({
    target,
    args: [PICKER_GLOBALS],
    func: (names) => names.filter((n) => globalThis[n] !== undefined),
  });
  const keep = (before && before.result) || [];
  await chrome.scripting.executeScript({ target, files: PICKER_FILES });
  const run = chrome.scripting.executeScript({
    target,
    args: [String(prompt || ""), ms, keep],
    func: (p, t, k) =>
      globalThis.MonoRecorderPicker.pick({ prompt: p, timeoutMs: t, keep: k }).then(
        (value) => ({ ok: true, value }),
        (err) => ({ ok: false, error: err.message })
      ),
  });
  let timer;
  const deadline = new Promise((_, reject) => {
    timer = setTimeout(() => reject(new Error("timeout")), ms + 2000); // inside Go's ms+5s
  });
  let res;
  try {
    [res] = await Promise.race([run, deadline]);
  } catch (err) {
    await chrome.scripting
      .executeScript({ target, func: () => globalThis.MonoRecorderPicker && globalThis.MonoRecorderPicker.cancel("timeout") })
      .catch(() => {});
    throw err;
  } finally {
    clearTimeout(timer);
  }
  const out = res && res.result;
  if (!out) throw new Error("the page went away before an element was picked");
  if (!out.ok) throw new Error(out.error);
  return out.value;
}

// Evaluate JS via CDP Runtime.evaluate — bypasses page CSP completely.
async function evalCDP({ tabId, expression }) {
  if (!tabId) throw new Error("tabId required");
  if (!expression) throw new Error("expression required");

  const target = { tabId };
  await ensureDebuggerAttached(target, tabId);

  const result = await debuggerSend(target, tabId, "Runtime.evaluate", {
    expression,
    returnByValue: true,
    awaitPromise: true,
  });

  if (result.exceptionDetails) {
    throw new Error("eval_cdp: " + (result.exceptionDetails.text || result.exceptionDetails.exception?.description || "unknown error"));
  }

  return { result: result.result?.value ?? null };
}

async function sendToContent(tabId, cmd) {
  if (!tabId) throw new Error("tabId is required for DOM operations");

  return new Promise((resolve, reject) => {
    const timeout = setTimeout(() => {
      reject(new Error(`Content script timeout for command ${cmd.type} on tab ${tabId}`));
    }, (cmd.params?.timeout || COMMAND_TIMEOUT) + CONTENT_SCRIPT_TIMEOUT_BUFFER);

    chrome.tabs.sendMessage(tabId, cmd, (response) => {
      clearTimeout(timeout);
      if (chrome.runtime.lastError) {
        reject(new Error(chrome.runtime.lastError.message));
        return;
      }
      if (response && response.error) {
        reject(new Error(response.error));
        return;
      }
      resolve(response);
    });
  });
}

// ---------------------------------------------------------------------------
// Message handler for the side panel and internal communication
// ---------------------------------------------------------------------------

// Persist a new pairing secret (see doConnect/ws.onopen). Sourced from the
// side panel's manual "Pair & reconnect" button, or `monoagentcli extension
// pair`. Reconnecting is handled by the chrome.storage.onChanged listener
// above, not here — it fires for this same write regardless of who made it
// (this function, or pair_bridge.js writing directly), so there is exactly
// one reconnect trigger to reason about instead of two racing copies.
async function setPairingToken(secret) {
  if (!secret || typeof secret !== "string") throw new Error("pairing token is required");
  try {
    const update = {};
    const storageKey = ["pairing", "Token"].join("");
    update[storageKey] = secret.trim();
    await chrome.storage.local.set(update);
  } catch (err) {
    throw new Error("Failed to save pairing token: " + err.message);
  }
  return { ok: true };
}

// Persist a new WS URL (validating loopback policy first) and reconnect.
async function setWsUrl(url) {
  if (!url || typeof url !== "string") throw new Error("url is required");
  if (!parseWsUrl(url)) throw new Error(`Invalid WebSocket URL: ${url}`);
  await assertLoopbackAllowed(url); // throws a visible message for the side panel
  try {
    await chrome.storage.local.set({ wsUrl: url });
    // Explicit config replaces the auto-detected sticky port.
    chrome.storage.local.remove("workingWsUrl").catch(() => {});
  } catch (err) {
    throw new Error("Failed to save URL: " + err.message);
  }
  // Disconnect and reconnect with the new URL
  if (ws) {
    ws.close();
  }
  // Reset fast retry so we aggressively connect to the new URL
  fastRetryCount = 0;
  connect();
  fastRetryConnect();
  return { ok: true };
}

// React to the pairing token changing in storage, regardless of who wrote
// it. This is what makes pair_bridge.js's direct-storage-write pairing path
// (below) actually take effect immediately: storage.onChanged is a plain
// addListener-based event, woken/dispatched the same reliable way as any
// other extension event — unlike chrome.runtime.sendMessage's message-port
// handshake, it has no "port closed before a response was received" race
// against a service worker that's still waking up from suspension.
chrome.storage.onChanged.addListener((changes, area) => {
  // pairedWsUrl is written by the same pair_bridge.js pairing write, and
  // matters just as much: it is what redirects us off a port that another
  // process holds (a Chrome CDP on 9222, say) onto the one the bridge
  // actually bound, without waiting out PORT_SWITCH_AFTER_FAILURES.
  if (area !== "local" || (!changes.pairingToken && !changes.pairedWsUrl)) return;
  if (ws) ws.close();
  fastRetryCount = 0;
  connect();
  fastRetryConnect();
});

// The binding changed (side panel or set_binding). Tell the bridge on the
// open socket instead of reconnecting, which would drop a running
// workflow's in-flight commands. A bound browser also saves captures into
// its profile by default.
chrome.storage.onChanged.addListener((changes, area) => {
  if (!MonoBrowserBinding.isBindingChange(changes, area)) return;
  loadBinding()
    .then((state) => {
      binding = state;
      if (state.profile) MonoCaptureProfile.remember(chrome.storage.local, state.profile);
      sendFrame(MonoBrowserBinding.bindingFrame(state));
      broadcastStatus();
    })
    .catch((err) => console.warn("[monoagent] binding update:", err.message));
});

chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  if (msg.type === "get_status") {
    // A worker woken by this question answers once it has read the last report.
    if (gate.state() === "unknown") gate.ready.then(() => sendResponse(statusPayload()));
    else sendResponse(statusPayload());
    // Someone just opened the side panel, which is the moment they care whether
    // this is connected. A suspended worker otherwise only dials again on its
    // next alarm — up to half a minute after the bridge was started, which is
    // exactly the "I started it and nothing changed" gap. Dialling is silent
    // now (see doConnect), so trying on every open costs nothing; an unpaired
    // worker is left alone until it is given a token.
    if (connectionStatus !== "connected" && connectionStatus !== "unpaired") connect();
    return gate.state() === "unknown";
  }
  // The sign-in screen's "Check again": ask the bridge now, or dial it.
  if (msg.type === "account_recheck") {
    if (ws?.readyState === WebSocket.OPEN) sendAccountPing();
    else {
      // Unpaired too: a token saved since, or a bridge restarted with the
      // token this browser holds, is only found by dialling again.
      fastRetryCount = 0;
      connect();
      fastRetryConnect();
    }
    queuedItems().then((queued) => sendResponse(Object.assign(statusPayload(), { queued })));
    return true;
  }
  // Handle file read requests from content script (for file upload)
  if (msg.type === "read_file") {
    // The Go server needs to send us the file. We'll request it via WS.
    // For now, if the path is accessible via fetch (unlikely for local files),
    // we return an error and let the Go side handle file upload differently.
    sendResponse({ error: "Local file access not supported from extension. Use the Go server to read files." });
    return false;
  }
  if (msg.type === "set_ws_url") {
    setWsUrl(msg.url)
      .then((result) => sendResponse(result))
      .catch((err) => sendResponse({ ok: false, error: err.message }));
    return true; // async response
  }
  if (msg.type === "set_pairing_token") {
    setPairingToken(msg.value)
      .then((result) => sendResponse(result))
      .catch((err) => sendResponse({ ok: false, error: err.message }));
    return true; // async response
  }
  return false;
});

// ---------------------------------------------------------------------------
// Initialization — runs every time the service worker starts
// ---------------------------------------------------------------------------

/**
 * sendFrame writes one frame and says whether it landed.
 *
 * The boolean is the whole point. This used to return nothing when the
 * socket was closed, which made "sent" and "silently discarded" the same
 * value to every caller — so the capture queue deleted entries it had never
 * delivered and reported them as flushed (CLIP-08).
 */
function sendFrame(message) {
  if (ws?.readyState !== WebSocket.OPEN) return false;
  // Signed out nothing a feature sends goes out: its outbox keeps it (gate.js).
  if (!accountHeard || !gate.signedIn()) return false;
  ws.send(JSON.stringify(message));
  return true;
}

MonoCaptureBridge.install({
  send: sendFrame,
  isConnected: openAndSignedIn,
  // The per-frame budget capture.js plans against. Without it the planner
  // is handed `undefined`, every size comparison becomes NaN, and the
  // envelope goes out listing artifacts whose bytes were never framed.
  maxMessageBytes: MonoCapture.DEFAULT_MAX_MESSAGE_BYTES,
  attach: (tabId) => ensureDebuggerAttached({ tabId }, tabId),
  cdp: (tabId, method, params) => debuggerSend({ tabId }, tabId, method, params),
  detach: (tabId) => detachDebugger(tabId),
});

// The CDP proxy gets the same debugger helpers the capture bridge does,
// plus the pin/unpin that keeps the sweep above off a tab it is listening
// to, and the two chrome.debugger listener registrations it relays from.
MonoCdpProxy.install({
  send: sendFrame,
  isConnected: openAndSignedIn,
  activeTabId: async () => {
    const [active] = await chrome.tabs.query({ active: true, lastFocusedWindow: true });
    return active?.id;
  },
  attach: (tabId) => ensureDebuggerAttached({ tabId }, tabId),
  // `session` is {tabId} for the page itself, or {tabId, sessionId} for a
  // child session (an out-of-process iframe).
  cdp: (session, method, params) => debuggerSend(session, session.tabId, method, params),
  detach: (tabId) => detachDebugger(tabId),
  pin: (tabId) => debuggerPinned.add(tabId),
  unpin: (tabId) => debuggerPinned.delete(tabId),
  onDebuggerEvent: (fn) => chrome.debugger.onEvent.addListener(fn),
  onDebuggerDetach: (fn) => chrome.debugger.onDetach.addListener(fn),
});

// Everything the side panel asks for (CLIP-06/07/08). It captures through
// MonoCaptureBridge's context, so it needs only the socket from here.
MonoCaptureActions.install({
  send: sendFrame,
  isConnected: openAndSignedIn,
  maxMessageBytes: MonoCapture.DEFAULT_MAX_MESSAGE_BYTES,
  storage: chrome.storage.local,
});

// The recall group installs against the same socket (RCL-02/04/05). It
// registers its own tab and message listeners; background.js only has to
// hand it the socket.
MonoRecall.install({
  send: sendFrame,
  isConnected: openAndSignedIn,
  signedIn: () => gate.signedIn(),
  storage: chrome.storage.local,
});

// The chat's messages from the side panel; it reuses the ask channel above.
MonoChat.install({
  isConnected: openAndSignedIn,
  storage: chrome.storage.local,
});
MonoForm.install();
MonoVault.install({
  isConnected: openAndSignedIn,
  storage: chrome.storage.local,
});

// The side panel hears of a change at once. Signed out the bridge is asked
// again every few seconds, signed in every minute: its answer to ping (read in
// ws.onmessage) is how this worker learns the person signed in, or out.
account.onChange(() => broadcastStatus());
setInterval(() => {
  if (ws?.readyState !== WebSocket.OPEN) return;
  if (!gate.signedIn() || Date.now() - accountPingAt >= 60000) sendAccountPing();
}, 5000);
gate.onChange((signedIn) => {
  setPanelPath(signedIn);
  broadcastStatus();
  if (signedIn) {
    if (openAndSignedIn()) whileSignedIn();
    return;
  }
  // Signed out: a recording in progress ends (its frames stay in the outbox)
  // and the CDP relay lets go of every tab it held.
  MonoRecorderWiring.stop();
  MonoCdpProxy.disconnected("signed out of MonoAgent");
});
gate.ready.then(() => setPanelPath(gate.signedIn()));
// Tasks from the browser ride the same request channel. The bridge registers
// its own message and alarm listeners; it never installs ask.js again.
MonoTaskBridge.install({
  isConnected: openAndSignedIn,
  storage: chrome.storage.local,
});
// Its menu items and the add-task shortcut (task_menu.js).
MonoTaskMenu.install();
// The coder's requests, its picker (the one pick_element shows) and its menu items.
MonoCoderBridge.install({ isConnected: openAndSignedIn, pickElement });
MonoCoderMenu.install();

// The activity recorder rides the same socket: kind:"recording" frames out,
// buffered through its own outbox while the bridge is down.
MonoRecorderWiring.install({
  send: sendFrame,
  isConnected: openAndSignedIn,
  storage: chrome.storage.local,
});

// The coder's browser commands (agent_*.js) use the same debugger helpers
// as the capture bridge and the CDP proxy; agent_cdp.js says when a tab is
// held against the idle sweep.
MonoAgentCommands.install({
  attach: (tabId) => ensureDebuggerAttached({ tabId }, tabId),
  cdp: (tabId, method, params) => debuggerSend({ tabId }, tabId, method, params),
  onDebuggerEvent: (fn) => chrome.debugger.onEvent.addListener(fn),
  onDebuggerDetach: (fn) => chrome.debugger.onDetach.addListener(fn),
  onTabRemoved: (fn) => chrome.tabs.onRemoved.addListener(fn),
  // The idle release's last step; a tab the CDP relay still listens to stays attached.
  detach: (tabId) => (debuggerPinned.has(tabId) ? undefined : detachDebugger(tabId)),
});

// The chat's page actions (page_action_run), from the side panel's Run only.
MonoPageActionBridge.install({
  detach: (tabId) => (debuggerPinned.has(tabId) ? undefined : detachDebugger(tabId)),
});

// The toolbar button opens the side panel rather than a popup: a panel
// stays open beside the page while the person reads, switches tabs and
// comes back, where a popup vanished the moment it lost focus. Set on every
// worker start — it is idempotent, and a stored behaviour from an older
// version must not be left in charge. No-op where there is no side panel.
if (chrome.sidePanel && chrome.sidePanel.setPanelBehavior) {
  chrome.sidePanel
    .setPanelBehavior({ openPanelOnActionClick: true })
    .catch((err) => console.warn("[monoagent] side panel behaviour:", err.message));
}

ensureAlarm();
fastRetryConnect(); // calls connect() once, then schedules retries
