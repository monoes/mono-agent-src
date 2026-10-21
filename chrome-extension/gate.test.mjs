// The sign-in gate (gate.js): nothing works before the person signs in.
//
// Three layers: the gate's own decision and its storage; the four chrome
// events it takes over, against listeners that stand for any feature (so a
// feature added later is covered without being named here); and the real task
// and highlight modules, installed behind the gate the way background.js does,
// refusing signed out and keeping what they hold.
//
// `node --test chrome-extension/gate.test.mjs`

import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import { loadExtensionScripts } from "./test_helpers.mjs";
import { fakeStorage, fakeAsk, send, PAGE, PANEL, until } from "./task_harness.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const DAY = 24 * 60 * 60 * 1000;
const T0 = Date.UTC(2026, 9, 14, 12, 0, 0);

const pingReply = (account) => ({ kind: "reply", id: "account-1", ok: true, data: { pong: true, methods: ["ping"], account } });
const LOCKED = pingReply({ state: "locked", reason: "not_logged_in", enforced: true });
const OK = pingReply({ state: "ok", reason: "", enforced: true });

/** An event the way chrome has it: addListener, plus a way to fire it. */
function event() {
  const list = [];
  return {
    list,
    addListener: (fn) => list.push(fn),
    fire: (...args) => list.map((fn) => fn(...args)),
  };
}

function fakeChrome() {
  return {
    runtime: { id: "ext-id", onMessage: event(), onConnect: event() },
    contextMenus: { onClicked: event() },
    commands: { onCommand: event() },
  };
}

/** setup loads the gate over a fake clock and storage; `seen` is the stored report. */
function setup({ seen, now = T0 } = {}) {
  const clock = { now };
  const storage = fakeStorage(seen ? { accountSeen: seen } : {});
  const opened = [];
  const env = loadExtensionScripts(["account_state.js", "gate.js"]);
  const gate = env.MonoGate.create({ storage, now: () => clock.now, openPanel: (tab) => opened.push(tab) });
  const chrome = fakeChrome();
  gate.install(chrome);
  return { env, gate, chrome, storage, clock, opened };
}

/** deliver sends one message through the (gated) onMessage the way Chrome does. */
function deliver(chrome, msg, sender = PANEL) {
  return new Promise((resolve) => {
    const async = chrome.runtime.onMessage.fire(msg, sender, resolve).some((r) => r === true);
    if (!async) setTimeout(() => resolve(undefined), 0);
  });
}

// ── The decision ─────────────────────────────────────────────────────────

test("a fresh install has no report and is signed out", async () => {
  const { gate } = setup();
  assert.equal(gate.state(), "unknown", "undecided until storage is read");
  await gate.ready;
  assert.equal(gate.state(), "out");
  assert.equal(gate.signedIn(), false);
});

test("a bridge report signs in, is stored, and signs out again", async () => {
  const { gate, storage } = setup();
  await gate.ready;
  const changes = [];
  gate.onChange((v) => changes.push(v));
  assert.equal(gate.observe(OK), true);
  assert.equal(gate.signedIn(), true);
  assert.deepEqual(storage.data.accountSeen, { state: "ok", at: T0 });
  assert.equal(gate.observe({ kind: "reply", id: "r", error: "Log in", code: "account_locked" }), true);
  assert.equal(gate.signedIn(), false);
  assert.deepEqual(storage.data.accountSeen, { state: "locked", at: T0 });
  assert.deepEqual(changes, [true, false]);
});

test("the stored report keeps a worker signed in offline, until the window runs out", async () => {
  const { gate, clock } = setup({ seen: { state: "ok", at: T0 - 60 * 60 * 1000 } });
  const changes = [];
  gate.onChange((v) => changes.push(v));
  await gate.ready;
  assert.equal(gate.signedIn(), true);
  clock.now = T0 + DAY;
  gate.check(); // the keep-alive alarm
  assert.equal(gate.signedIn(), false);
  assert.deepEqual(changes, [true, false]);
});

test("a report read off the socket beats the stored one being read", async () => {
  const { gate } = setup({ seen: { state: "ok", at: T0 } });
  gate.observe(LOCKED); // before storage answered
  await gate.ready;
  assert.equal(gate.signedIn(), false);
});

// ── The events every feature listens on ──────────────────────────────────

test("signed out, no feature hears a message; the sender is told to sign in", async () => {
  const { gate, chrome } = setup();
  await gate.ready;
  const heard = [];
  // Any feature: these stand for capture, tasks, highlights, chat, recall, the recorder...
  chrome.runtime.onMessage.addListener((msg) => void heard.push(msg.type));
  for (const type of ["capture_active_tab", "task_add", "highlight_add", "highlight_list", "chat_send", "ask_brain", "record_start", "recorder_event", "queue_retry", "anything_added_later"]) {
    const reply = await deliver(chrome, { type }, type.startsWith("record") || type.startsWith("highlight") ? PAGE : PANEL);
    assert.deepEqual(reply, { ok: false, error: "Sign in to MonoAgent first: open the MonoAgent app, or run monoagentcli account login", code: "login_required" }, type);
  }
  assert.deepEqual(heard, []);
});

test("signed out, the sign-in screen and pairing still answer", async () => {
  const { gate, chrome, env } = setup();
  await gate.ready;
  const heard = [];
  chrome.runtime.onMessage.addListener((msg, sender, respond) => {
    heard.push(msg.type);
    respond({ ok: true });
  });
  for (const type of env.MonoGate.OPEN_MESSAGES) assert.deepEqual(await deliver(chrome, { type }), { ok: true }, type);
  assert.deepEqual(heard, ["get_status", "set_ws_url", "set_pairing_token", "account_recheck"]);
});

test("signed in, messages reach every listener as Chrome would deliver them", async () => {
  const { gate, chrome } = setup();
  await gate.ready;
  gate.observe(OK);
  chrome.runtime.onMessage.addListener(() => false); // not its message
  chrome.runtime.onMessage.addListener((msg, sender, respond) => {
    setTimeout(() => respond({ ok: true, type: msg.type }), 1);
    return true;
  });
  assert.deepEqual(await deliver(chrome, { type: "task_add" }), { ok: true, type: "task_add" });
});

test("a worker woken by a message decides once the stored report is read", async () => {
  const signedOut = setup();
  signedOut.chrome.runtime.onMessage.addListener((m, s, respond) => respond({ ok: true }));
  assert.deepEqual(await deliver(signedOut.chrome, { type: "task_add" }), { ok: false, error: signedOut.env.MonoGate.LOGIN_MESSAGE, code: "login_required" });

  const signedIn = setup({ seen: { state: "ok", at: T0 } });
  signedIn.chrome.runtime.onMessage.addListener((m, s, respond) => respond({ ok: true }));
  assert.deepEqual(await deliver(signedIn.chrome, { type: "task_add" }), { ok: true });

  const nobody = setup({ seen: { state: "ok", at: T0 } });
  nobody.chrome.runtime.onMessage.addListener(() => false);
  assert.equal(await deliver(nobody.chrome, { type: "nobody_handles_this" }), undefined, "the port is closed, not left open");
});

test("signed out, menu clicks and shortcuts only open the side panel", async () => {
  const { gate, chrome, opened } = setup();
  await gate.ready;
  const ran = [];
  chrome.contextMenus.onClicked.addListener((info) => ran.push(info.menuItemId));
  chrome.commands.onCommand.addListener((command) => ran.push(command));
  chrome.contextMenus.onClicked.fire({ menuItemId: "monoagent-capture-full" }, PAGE.tab);
  chrome.commands.onCommand.fire("capture-page", PAGE.tab);
  chrome.commands.onCommand.fire("add-task", PAGE.tab);
  assert.deepEqual(ran, []);
  assert.deepEqual(opened, [PAGE.tab, PAGE.tab, PAGE.tab]);

  gate.observe(OK);
  chrome.commands.onCommand.fire("capture-page", PAGE.tab);
  assert.deepEqual(ran, ["capture-page"]);
  assert.equal(opened.length, 3, "signed in a shortcut does what it does");
});

test("a gesture that wakes the worker opens the panel at once, and runs only if signed in", async () => {
  for (const [seen, expected] of [[undefined, []], [{ state: "ok", at: T0 }, ["capture-page"]]]) {
    const { gate, chrome, opened } = setup({ seen });
    const ran = [];
    chrome.commands.onCommand.addListener((c) => ran.push(c));
    chrome.commands.onCommand.fire("capture-page", PAGE.tab);
    assert.equal(opened.length, 1, "inside the gesture, before anything is awaited");
    await gate.ready;
    await new Promise((r) => setTimeout(r, 0));
    assert.deepEqual(ran, expected);
  }
});

test("signed out, ports (the recorder's panel port) are not handed on", async () => {
  const { gate, chrome } = setup();
  const got = [];
  chrome.runtime.onConnect.addListener((port) => got.push(port.name));
  chrome.runtime.onConnect.fire({ name: "monoagent-record" });
  await gate.ready;
  await new Promise((r) => setTimeout(r, 0));
  chrome.runtime.onConnect.fire({ name: "monoagent-record" });
  assert.deepEqual(got, []);
  gate.observe(OK);
  chrome.runtime.onConnect.fire({ name: "monoagent-record" });
  assert.deepEqual(got, ["monoagent-record"]);
});

test("removeListener and hasListener keep working behind the gate", async () => {
  const { chrome } = setup();
  const fn = () => {};
  chrome.runtime.onMessage.addListener(fn);
  assert.equal(chrome.runtime.onMessage.hasListener(fn), true);
  chrome.runtime.onMessage.removeListener(fn);
  assert.equal(chrome.runtime.onMessage.hasListener(fn), false);
});

// ── The real features behind the gate ────────────────────────────────────

/**
 * features installs the task bridge, the task menu and the recall group's
 * highlights behind the gate, as background.js does: the gate takes the
 * events first, and every module is "connected" only open AND signed in.
 */
function features({ seen, bridgeUp = true } = {}) {
  const local = fakeStorage(Object.assign(
    { captureProfile: "p-work", captureProfilesCache: [{ id: "p-work", name: "Work" }] },
    seen ? { accountSeen: seen } : {}
  ));
  const opened = [];
  const sent = [];
  const chrome = {
    runtime: {
      id: "ext-id",
      lastError: null,
      getURL: (p) => `chrome-extension://ext-id/${p}`,
      onMessage: event(),
      onConnect: event(),
      sendMessage: () => Promise.resolve(),
    },
    contextMenus: { onClicked: event() },
    commands: { onCommand: event() },
    alarms: { onAlarm: event(), create: async () => {}, get: async () => undefined, clear: async () => true },
    action: { setBadgeText: async () => {}, setBadgeBackgroundColor: async () => {}, setTitle: async () => {} },
    scripting: { executeScript: async () => [{ result: "selected words" }] },
    sidePanel: { open: (o) => (opened.push(o), Promise.resolve()) },
    tabs: { query: async () => [PAGE.tab], get: async () => PAGE.tab, sendMessage: async (id, m) => sent.push(m), onUpdated: event(), onActivated: event(), onRemoved: event() },
    storage: { local, session: fakeStorage(), onChanged: event() },
  };
  // One global for all of them, as in the worker; loading only defines, the
  // installs below register, and the gate's comes first.
  const env = loadExtensionScripts(
    ["account_state.js", "gate.js", "capture_profile.js", "capture_modes.js", "capture_queue.js", "recorder_privacy.js",
      "task_outbox.js", "task_bridge.js", "task_menu.js", "saved.js", "highlights.js", "recall_bridge.js"],
    { chrome }
  );
  const gate = env.MonoGate.create({ storage: local, now: () => T0, openPanel: (tab) => chrome.sidePanel.open({ windowId: tab.windowId }) });
  gate.install(chrome);
  const ask = Object.assign(fakeAsk(), { install() {}, disconnected() {} });
  env.MonoAsk = ask;
  env.MonoCaptureBridge = { toast: () => {} };
  const net = { up: bridgeUp };
  const isConnected = () => net.up && gate.signedIn();
  env.MonoTaskBridge.install({ isConnected, storage: local });
  env.MonoTaskMenu.install();
  env.MonoRecall.install({ send: () => isConnected(), isConnected, storage: local, signedIn: () => gate.signedIn() });
  const listeners = { message: chrome.runtime.onMessage.list };
  return { env, gate, chrome, local, ask, net, opened, sent, listeners };
}

test("signed out, adding a task from the page, the panel, the menu or the shortcut does nothing but open the panel", async () => {
  const f = features();
  await f.gate.ready;
  const before = JSON.stringify(f.local.data);
  const page = await send(f.listeners, { type: "task_add", text: "from the page" }, PAGE);
  const panel = await send(f.listeners, { type: "task_add", text: "from the panel" }, PANEL);
  assert.equal(page.code, "login_required");
  assert.equal(panel.code, "login_required");
  f.chrome.contextMenus.onClicked.fire({ menuItemId: "monoagent-tasks-selection", selectionText: "menu", frameId: 0 }, PAGE.tab);
  f.chrome.commands.onCommand.fire("add-task", PAGE.tab);
  await new Promise((r) => setTimeout(r, 20));
  assert.equal(JSON.stringify(f.local.data), before, "nothing was queued");
  assert.deepEqual(f.ask.calls, [], "nothing was sent");
  assert.equal(f.opened.length, 2, "the menu and the shortcut opened the panel");
});

test("signed out, highlights are neither stored, listed nor painted", async () => {
  const f = features();
  await f.gate.ready;
  // One highlight stored before the person signed out: it stays, unpainted.
  await f.env.MonoHighlights.add(f.local, PAGE.tab.url, { text: "words read earlier" });
  assert.equal((await f.env.MonoHighlights.load(f.local, PAGE.tab.url)).length, 1);
  const before = JSON.stringify(f.local.data);
  const add = await send(f.listeners, { type: "highlight_add", highlight: { quote: "words", color: "yellow" } }, PAGE);
  const list = await send(f.listeners, { type: "highlight_list" }, PAGE);
  assert.equal(add.code, "login_required");
  assert.equal(list.code, "login_required");
  assert.equal(JSON.stringify(f.local.data), before);
  f.chrome.tabs.onUpdated.fire(PAGE.tab.id, { status: "complete" }, PAGE.tab);
  await new Promise((r) => setTimeout(r, 10));
  assert.deepEqual(f.sent.filter((m) => m.type === "highlight_restore"), [], "no page was told to paint");
});

test("what waited is kept signed out and sent once a bridge says the person signed in", async () => {
  const f = features({ seen: { state: "ok", at: T0 } });
  await f.gate.ready;
  // Signed in, bridge down: the task waits, as it always has.
  f.net.up = false;
  const queued = await send(f.listeners, { type: "task_add", text: "written offline" }, PAGE);
  assert.equal(queued.status, "queued");
  assert.equal(f.local.data.taskOutbox.length, 1);
  // The bridge comes back and says nobody is signed in: nothing goes, nothing is lost.
  f.gate.observe(LOCKED);
  f.net.up = true;
  await f.env.MonoTaskBridge.connected();
  assert.deepEqual(f.ask.calls.filter((c) => c.method === "task.add"), []);
  assert.equal(f.local.data.taskOutbox.length, 1, "kept");
  // The person signs in: it goes.
  f.gate.observe(OK);
  await f.env.MonoTaskBridge.connected();
  await until(() => f.ask.calls.some((c) => c.method === "task.add"), "the task to be sent");
  assert.equal(f.ask.calls.find((c) => c.method === "task.add").params.text, "written offline");
});

// ── The worker's wiring (background.js), read from the source ────────────

const background = readFileSync(join(HERE, "background.js"), "utf8");

test("the worker takes the events before any module can add a listener", () => {
  const gateAt = background.indexOf("gate.install(chrome);");
  assert.ok(gateAt !== -1, "gate.install(chrome) not found");
  const firstImport = background.indexOf("importScripts(");
  assert.equal(background.slice(firstImport, background.indexOf("\n", firstImport)), 'importScripts("account_state.js", "gate.js");');
  const secondImport = background.indexOf("importScripts(", firstImport + 1);
  assert.ok(gateAt < secondImport, "a module is imported before the gate is installed");
  assert.ok(gateAt < background.indexOf("addListener("), "a listener is added before the gate is installed");
});

test("every feature sees signed out as offline, and nothing it sends goes out", () => {
  assert.equal(background.includes("isConnected: () => ws?.readyState === WebSocket.OPEN"), false, "a module that is connected while signed out");
  assert.ok((background.match(/isConnected: openAndSignedIn/g) || []).length >= 7);
  const send = background.slice(background.indexOf("function sendFrame(message) {"));
  assert.match(send.slice(0, send.indexOf("\n}\n")), /if \(!accountHeard \|\| !gate\.signedIn\(\)\) return false;/);
  assert.match(background, /function openAndSignedIn\(\) \{\n  return ws\?\.readyState === WebSocket\.OPEN && accountHeard && gate\.signedIn\(\);/);
});

test("a new socket sends nothing until its bridge has reported the account", () => {
  const onopen = background.slice(background.indexOf("ws.onopen = () => {"), background.indexOf("ws.onmessage = "));
  assert.equal(onopen.includes("whileSignedIn()"), false, "a stored report must not flush onto a bridge that has not answered");
  assert.match(onopen, /accountHeard = false;\n    sendAccountPing\(\);/);
  const onmessage = background.slice(background.indexOf("ws.onmessage = "), background.indexOf("ws.onerror = "));
  assert.match(onmessage, /if \(gate\.observe\(cmd\) && !accountHeard\) \{\n      accountHeard = true;\n      if \(gate\.signedIn\(\)\) whileSignedIn\(\);/);
});

test("signed out, the bridge's commands are refused before any handler runs", () => {
  const onmessage = background.slice(background.indexOf("ws.onmessage = "), background.indexOf("ws.onerror = "));
  const refuse = onmessage.indexOf("if (!accountHeard || !gate.signedIn()) {");
  assert.ok(refuse !== -1 && refuse < onmessage.indexOf("handleCommand(cmd);"));
  assert.ok(onmessage.indexOf("gate.observe(cmd)") < refuse);
});

test("the side panel loads the gate first, and signin.html carries nothing else", () => {
  const panel = readFileSync(join(HERE, "sidepanel.html"), "utf8");
  const scripts = [...panel.matchAll(/<script src="([^"]+)"/g)].map((m) => m[1]);
  assert.equal(scripts[0], "sidepanel_gate.js");
  assert.ok(panel.indexOf('<script src="sidepanel_gate.js">') < panel.indexOf("<body>"), "in the head, before the panel renders");
  const signin = readFileSync(join(HERE, "signin.html"), "utf8");
  assert.deepEqual([...signin.matchAll(/<script src="([^"]+)"/g)].map((m) => m[1]), ["signin.js"]);
  assert.deepEqual([...signin.matchAll(/<button[^>]*id="([^"]+)"/g)].map((m) => m[1]), ["signin-copy", "signin-pair-save", "signin-recheck"]);
  assert.deepEqual([...signin.matchAll(/<(input|textarea|select|form|nav)\b[^>]*?(?:id="([^"]+)")?/g)].map((m) => m[1]), ["input"], "no control but the pairing token");
  assert.ok(signin.includes('id="signin-pair-token"'));
});
