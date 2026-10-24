// The side panel's Coder section (sidepanel_coder.js, coder_mirror.js)
// against a fake document, a fake worker (`ask`), fake timers and the real
// chat_core.js / chat_render.js: its states, what Queue sends (never the
// page), the picker, the folder list, the filled-in form a menu click
// leaves, and the live mirror: drawn without innerHTML, polled only while
// visible, the follow-up box only where MonoAgent allows it.
// `CHROME_PATH=/nonexistent node --test chrome-extension/sidepanel_coder.test.mjs`

import test from "node:test";
import assert from "node:assert/strict";
import { loadExtensionScripts } from "./test_helpers.mjs";

const settle = (ms = 10) => new Promise((r) => setTimeout(r, ms));
const html = [];

class Node_ {
  constructor(tag) {
    this.tag = tag; this.children = []; this.parentNode = null; this.dataset = {}; this.attrs = {};
    this.listeners = {}; this._text = ""; this.hidden = false; this.disabled = false; this.value = "";
    this.className = ""; this.scrollTop = 0; this.scrollHeight = 0; this.clientHeight = 0; this.title = "";
    this.open = false;
  }
  appendChild(c) { if (c.tag === "#fragment") { for (const k of [...c.children]) this.appendChild(k); return c; } c.remove?.(); c.parentNode = this; this.children.push(c); return c; }
  append(...cs) { for (const c of cs) this.appendChild(c); }
  remove() { if (this.parentNode) { this.parentNode.children.splice(this.parentNode.children.indexOf(this), 1); this.parentNode = null; } }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return this.attrs[k]; }
  addEventListener(t, fn) { (this.listeners[t] = this.listeners[t] || []).push(fn); }
  fire(t, e = {}) { const ev = Object.assign({ preventDefault() { ev.prevented = true; } }, e); for (const fn of this.listeners[t] || []) fn(ev); return ev; }
  focus() {}
  scrollIntoView() {}
  set textContent(v) { this._text = String(v); this.children = []; }
  get textContent() { return this._text + this.children.map((c) => c.textContent).join(""); }
  // The mirror must never parse markup: any of these is a failure.
  set innerHTML(v) { html.push(v); throw new Error("innerHTML was used"); }
  set outerHTML(v) { html.push(v); throw new Error("outerHTML was used"); }
  insertAdjacentHTML(_w, v) { html.push(v); throw new Error("insertAdjacentHTML was used"); }
  all() { return [this, ...this.children.flatMap((c) => c.all())]; }
}

const IDS = [
  "coder-panel", "coder-count", "coder-notice", "coder-kind", "coder-reload-row", "coder-reload", "coder-selector", "coder-pick", "coder-clear", "coder-note",
  "coder-cwd", "coder-queue", "coder-why", "coder-msg", "coder-lists", "coder-queued", "coder-queued-empty", "coder-convs",
  "coder-convs-empty", "coder-mirror", "coder-mirror-title", "coder-mirror-close", "coder-log", "coder-follow",
  "coder-follow-text", "coder-send", "coder-stop", "coder-mirror-msg",
];

/** fakeTimers: nothing fires until the test says so. */
function fakeTimers() {
  let id = 0;
  const pending = new Map();
  return {
    pending,
    setTimeout: (fn, ms) => (pending.set(++id, { fn, ms }), id),
    clearTimeout: (i) => pending.delete(i),
    setInterval: (fn, ms) => (pending.set(++id, { fn, ms, every: true }), id),
    clearInterval: (i) => pending.delete(i),
    /** fire runs every pending timer of that delay once. */
    fire(ms) {
      for (const [i, t] of [...pending]) {
        if (t.ms !== ms) continue;
        if (!t.every) pending.delete(i);
        t.fn();
      }
    },
  };
}

const PAGE = () => ({ key: "5|https://a.example/p", tabId: 5, url: "https://a.example/p", title: "SECRET TITLE of the page" });
const READY = {
  coder_ready: { ok: true, state: "ready" },
  coder_projects: {
    ok: true,
    projects: [{ origin: "https://b.example", dir: "/w/b", exists: true }, { origin: "https://a.example", dir: "/w/a", exists: true }, { origin: "https://c.example", dir: "/w/gone", exists: false }],
    recent: [{ path: "/w/old", exists: true }, { path: "/w/a", exists: true }, { path: "/w/vanished", exists: false }],
  },
  coder_queue_list: { ok: true, items: [] },
  coder_list: { ok: true, conversations: [] },
};

function setup({ replies = {}, session = {}, pageNow = PAGE(), windowId = 1, tabWindow = 1, current = { id: "p-work" } } = {}) {
  html.length = 0;
  const els = {};
  for (const id of IDS) els[id] = new Node_(id);
  els["coder-kind"].value = "copy-style";
  els["coder-reload"].checked = true; // as the markup has it
  const docListeners = {};
  const doc = {
    visibilityState: "visible",
    getElementById: (id) => els[id],
    createElement: (t) => new Node_(t),
    createTextNode: (t) => { const n = new Node_("#text"); n._text = t; return n; },
    createDocumentFragment: () => new Node_("#fragment"),
    addEventListener: (t, fn) => (docListeners[t] = docListeners[t] || []).push(fn),
    fire: (t) => (docListeners[t] || []).forEach((fn) => fn({})),
  };
  const sent = [];
  const all = { ...READY, ...replies };
  const ask = async (m) => {
    sent.push(m);
    const r = all[m.type];
    return typeof r === "function" ? r(m) : r || { ok: true };
  };
  const store = { ...session };
  const changed = [];
  const chrome = {
    storage: {
      session: {
        get: async (k) => (k in store ? { [k]: store[k] } : {}),
        remove: async (k) => void delete store[k],
      },
      onChanged: { addListener: (fn) => changed.push(fn) },
    },
    tabs: { get: async (id) => ({ id, windowId: tabWindow }) },
  };
  const timers = fakeTimers();
  const env = {
    document: doc, chrome, ask, profiles: { current }, page: pageNow, here: { windowId },
    setTimeout: timers.setTimeout, clearTimeout: timers.clearTimeout, setInterval: timers.setInterval, clearInterval: timers.clearInterval,
  };
  const sandbox = loadExtensionScripts(["chat_context.js", "chat_core.js", "chat_render.js", "coder_mirror.js", "sidepanel_coder.js"], env);
  return {
    els, doc, sent, timers, store, sandbox, env,
    of: (type) => sent.filter((m) => m.type === type),
    setSession: (key, value) => {
      store[key] = value;
      for (const fn of changed) fn({ [key]: { newValue: value } }, "session");
    },
    openSection: () => { els["coder-panel"].open = true; els["coder-panel"].fire("toggle"); },
  };
}

test("a ready bridge enables the form; Queue says what it is waiting for", async () => {
  const t = setup();
  await settle();
  assert.equal(t.els["coder-notice"].hidden, true);
  assert.equal(t.els["coder-kind"].disabled, false);
  assert.equal(t.els["coder-pick"].disabled, false);
  assert.equal(t.els["coder-selector"].textContent, "The whole page");
  assert.equal(t.els["coder-queue"].disabled, true, "copy-style needs an element");
  assert.equal(t.els["coder-why"].textContent, "Pick the element first.");
  t.els["coder-kind"].value = "ask";
  t.els["coder-kind"].fire("change");
  assert.equal(t.els["coder-why"].textContent, "Say what you want in the note.");
  t.els["coder-note"].value = "why is this slow?";
  t.els["coder-note"].fire("input");
  assert.equal(t.els["coder-queue"].disabled, false);
  assert.equal(t.els["coder-why"].hidden, true);
  assert.equal(t.els["coder-lists"].hidden, false);
  assert.equal(t.els["coder-queued-empty"].hidden, false, "nothing queued says so");
});

test("bridge down, an old MonoAgent, and coder mode off each say so and disable the form", async () => {
  const off = setup({ replies: { coder_ready: { ok: true, state: "offline" } } });
  await settle();
  assert.match(off.els["coder-notice"].textContent, /isn't connected/);
  assert.equal(off.els["coder-notice"].hidden, false);
  assert.equal(off.els["coder-kind"].disabled, true);
  assert.equal(off.els["coder-queue"].disabled, true);
  assert.equal(off.of("coder_projects").length, 0);
  assert.equal(off.els["coder-lists"].hidden, true, "lists MonoAgent was not asked for");

  const old = setup({ replies: { coder_ready: { ok: true, state: "update" } } });
  await settle();
  assert.match(old.els["coder-notice"].textContent, /Update MonoAgent/);

  const words = "coder mode is off. Turn it on with `monoagentcli settings set coder_mode on`.";
  const refused = setup({ replies: { coder_projects: { ok: false, error: words, code: "coder_disabled" } } });
  await settle();
  assert.equal(refused.els["coder-notice"].textContent, words, "MonoAgent's own words, as they are");
  assert.equal(refused.els["coder-queue"].disabled, true);
  assert.equal(refused.els["coder-pick"].disabled, true);
});

test("Queue sends the kind, tab, selector, note, folder and profile, and none of the page", async () => {
  const t = setup({ replies: { coder_queue: { ok: true, item: { id: "q1" } }, coder_pick: { ok: true, selector: "#hero" } } });
  await settle();
  t.els["coder-pick"].fire("click");
  await settle();
  assert.equal(t.els["coder-selector"].textContent, "#hero");
  assert.equal(t.els["coder-clear"].hidden, false);
  t.els["coder-note"].value = "match the spacing";
  t.els["coder-cwd"].value = "/w/old";
  t.els["coder-cwd"].fire("change");
  t.els["coder-queue"].fire("click");
  await settle();
  const [q] = t.of("coder_queue");
  assert.deepEqual(q, { type: "coder_queue", kind: "copy-style", tabId: 5, selector: "#hero", note: "match the spacing", cwd: "/w/old", profile: "p-work" });
  assert.ok(!JSON.stringify(t.sent).includes("SECRET"), "the page's title went out");
  assert.equal(t.els["coder-msg"].textContent, "Queued. Start it in the MonoAgent app.");
  assert.equal(t.els["coder-note"].value, "");
  assert.equal(t.els["coder-selector"].textContent, "The whole page", "the element was for that job");
});

test("the picker asks for the panel's tab; moving to another page drops the element", async () => {
  const t = setup({ replies: { coder_pick: { ok: true, selector: "nav > a" } } });
  await settle();
  t.els["coder-pick"].fire("click");
  assert.equal(t.els["coder-pick"].disabled, true, "one pick at a time");
  await settle();
  assert.deepEqual(t.of("coder_pick")[0], { type: "coder_pick", tabId: 5 });
  assert.equal(t.els["coder-selector"].textContent, "nav > a");
  t.env.page.key = "6|https://other.example/"; // the same tab, another page
  t.doc.fire("panel:page");
  assert.equal(t.els["coder-selector"].textContent, "The whole page");
});

test("this site's project folder comes first and is chosen; folders that are gone are not offered; the default always is", async () => {
  const t = setup();
  await settle();
  const opts = t.els["coder-cwd"].children.map((o) => o.value);
  assert.deepEqual(opts, ["/w/a", "/w/b", "/w/old", ""]);
  assert.equal(t.els["coder-cwd"].value, "/w/a");
  assert.match(t.els["coder-cwd"].children[0].textContent, /this site's project/);
});

test("queued jobs say to start them in the app; conversations offer a mirror", async () => {
  const t = setup({
    replies: {
      coder_queue_list: { ok: true, items: [{ id: "q1", kind: "bug-report", status: "queued", title: "Checkout", cwd: "/w/a" }, { id: "q0", status: "started" }] },
      coder_list: { ok: true, conversations: [{ id: "c-1", title: "Fix the hero", cwd: "/w/a", running: true }] },
    },
  });
  await settle();
  assert.equal(t.els["coder-queued"].children.length, 1);
  assert.match(t.els["coder-queued"].textContent, /Bug report: Checkout/);
  assert.match(t.els["coder-queued"].textContent, /Start it in the MonoAgent app · a/);
  assert.equal(t.els["coder-count"].textContent, "1 queued");
  assert.match(t.els["coder-convs"].textContent, /Fix the hero/);
  assert.match(t.els["coder-convs"].textContent, /Working/);
});

const HOSTILE = "Done. <img src=x onerror=alert(1)> **bold** [x](javascript:alert(1))";
const SHOT = "data:image/png;base64,iVBORw0KGgo=";

function mirrorSetup(conv, events) {
  return setup({
    replies: {
      coder_list: { ok: true, conversations: [conv] },
      coder_events: Array.isArray(events) ? { ok: true, turn: "t1", events, turn_active: false, images: [] } : events,
    },
  });
}

async function watchFirst(t) {
  t.openSection();
  await settle();
  const btn = t.els["coder-convs"].children[0].all().find((n) => n.tag === "button");
  btn.fire("click");
  await settle();
}

test("the mirror draws replies as nodes, never markup, with tool cards and screenshots", async () => {
  const t = mirrorSetup({ id: "c-1", title: "Fix the hero" }, {
    ok: true,
    turn: "t1",
    turn_active: false,
    events: [
      { seq: 1, type: "turn.started", payload: { text: "Recreate the hero" } },
      { seq: 2, type: "tool.started", payload: { callId: "t1", name: "Bash" } },
      { seq: 3, type: "tool.completed", payload: { callId: "t1", ok: true } },
      { seq: 4, type: "assistant.delta", payload: { text: HOSTILE } },
    ],
    images: [
      { seq: 3, path: "/w/a/.monoagent/screenshots/1.png", dataUrl: SHOT },
      { seq: 3, path: "/w/a/evil.png", dataUrl: "javascript:alert(1)" },
      { seq: 3, path: "/w/a/remote.png", dataUrl: "https://evil.example/x.png" },
      { seq: 3, path: "/w/a/svg.svg", dataUrl: "data:image/svg+xml;base64,PHN2Zz4=" },
    ],
  });
  await watchFirst(t);
  assert.deepEqual(t.of("coder_events")[0], { type: "coder_events", conversationId: "c-1", since: 0, profile: "p-work" });
  assert.equal(t.els["coder-mirror"].hidden, false);
  assert.equal(t.els["coder-mirror-title"].textContent, "Fix the hero");
  const nodes = t.els["coder-log"].all();
  assert.deepEqual(html, [], "markup was parsed");
  assert.ok(!nodes.some((n) => n.tag === "script" || (n.tag === "img" && n.src !== SHOT)), "an element came from the reply's markup");
  const imgs = nodes.filter((n) => n.tag === "img");
  assert.equal(imgs.length, 1, "only the raster data URL is shown");
  assert.equal(imgs[0].alt, "Screenshot 1.png");
  assert.match(t.els["coder-log"].textContent, /<img src=x onerror=alert\(1\)>/, "raw HTML is shown as text");
  assert.ok(!nodes.some((n) => n.tag === "a"), "a javascript: link became a link");
  assert.ok(nodes.some((n) => n.tag === "strong" && n.textContent === "bold"));
  assert.ok(nodes.some((n) => n.className === "chat-tool" && n.textContent === "Bash"));
  assert.equal(t.of("coder_events").at(-1).since, 0);
  t.timers.fire(6000);
  await settle();
  assert.equal(t.of("coder_events").at(-1).since, 4, "the next poll starts where the last ended");
  assert.equal(t.els["coder-log"].all().filter((n) => n.tag === "img").length, 1, "a screenshot seen twice is drawn once");
});

test("the follow-up box shows only where MonoAgent allows a message from here", async () => {
  const cases = [
    [{ id: "c-1", startedInDesktop: false }, false],
    [{ id: "c-1" }, false],
    [{ id: "c-1", startedInDesktop: "yes" }, false],
    [{ id: "c-1", startedInDesktop: true }, true],
    [{ id: "c-1", startedInDesktop: true, running: true }, false],
  ];
  for (const [conv, shown] of cases) {
    const t = mirrorSetup(conv, []);
    await watchFirst(t);
    assert.equal(t.els["coder-follow"].hidden, !shown, JSON.stringify(conv));
  }
  const busy = mirrorSetup({ id: "c-1", startedInDesktop: true }, { ok: true, turn: "t2", events: [], turn_active: true, images: [] });
  await watchFirst(busy);
  assert.equal(busy.els["coder-follow"].hidden, true, "not while a turn runs");
  assert.equal(busy.els["coder-stop"].hidden, false, "Stop shows while a turn runs");
  busy.els["coder-stop"].fire("click");
  await settle();
  assert.deepEqual(busy.of("coder_stop")[0], { type: "coder_stop", conversationId: "c-1", profile: "p-work" });
});

test("a follow-up is sent only when allowed, and a refusal is shown in the mirror", async () => {
  const t = mirrorSetup({ id: "c-1", startedInDesktop: true }, []);
  await watchFirst(t);
  t.els["coder-follow-text"].value = "now the footer";
  t.els["coder-follow-text"].fire("input");
  assert.equal(t.els["coder-send"].disabled, false);
  t.els["coder-send"].fire("click");
  await settle();
  assert.deepEqual(t.of("coder_send")[0], { type: "coder_send", conversationId: "c-1", text: "now the footer", profile: "p-work" });

  const no = mirrorSetup({ id: "c-1", startedInDesktop: false }, []);
  await watchFirst(no);
  no.els["coder-follow-text"].value = "sneaky";
  no.els["coder-send"].fire("click");
  await settle();
  assert.equal(no.of("coder_send").length, 0, "a hidden box still sent");
});

test("the mirror polls only while the section is open and the panel visible", async () => {
  const t = mirrorSetup({ id: "c-1" }, []);
  await watchFirst(t);
  const before = t.of("coder_events").length;
  t.timers.fire(6000);
  await settle();
  assert.equal(t.of("coder_events").length, before + 1);
  t.doc.visibilityState = "hidden";
  t.doc.fire("visibilitychange");
  t.timers.fire(6000);
  await settle();
  assert.equal(t.of("coder_events").length, before + 1, "polled while hidden");
  t.doc.visibilityState = "visible";
  t.doc.fire("visibilitychange");
  await settle();
  assert.equal(t.of("coder_events").length, before + 2, "visible again: polls at once");
  t.els["coder-panel"].open = false;
  t.els["coder-panel"].fire("toggle");
  t.timers.fire(6000);
  await settle();
  assert.equal(t.of("coder_events").length, before + 2, "polled while folded");
});

test("the mirror polls every 1.5 s while a turn runs, every 6 s when idle, and not at all hidden or unwatched", async () => {
  let active = true;
  const t = mirrorSetup({ id: "c-1", startedInDesktop: true }, () => ({ ok: true, turn: "t1", events: [], turn_active: active, images: [] }));
  const polls = () => [...t.timers.pending.values()].filter((x) => !x.every).map((x) => x.ms);
  t.openSection();
  await settle();
  assert.deepEqual(polls(), [], "nothing watched: no poll");
  await watchFirst(t);
  assert.deepEqual(polls(), [1500], "a turn runs");
  active = false;
  t.timers.fire(1500);
  await settle();
  assert.deepEqual(polls(), [6000], "idle");
  t.doc.visibilityState = "hidden";
  t.doc.fire("visibilitychange");
  assert.deepEqual(polls(), [], "hidden: no poll left waiting");
  t.doc.visibilityState = "visible";
  t.doc.fire("visibilitychange");
  await settle();
  assert.deepEqual(polls(), [6000]);
  t.els["coder-mirror-close"].fire("click");
  assert.deepEqual(polls(), [], "the mirror closed: no poll");
});

test("a menu click's prefill opens the section filled in; stale or another window's is ignored", async () => {
  const t = setup({ session: { coderPrefill: { at: Date.now(), kind: "fix-element", tabId: 5, selector: "#cta" } } });
  await settle();
  assert.equal(t.els["coder-panel"].open, true);
  assert.equal(t.els["coder-kind"].value, "fix-element");
  assert.equal(t.els["coder-selector"].textContent, "#cta");
  assert.equal(t.store.coderPrefill, undefined, "the prefill is used once");
  t.setSession("coderPrefill", { at: Date.now(), kind: "bug-report", tabId: 5, selector: "" });
  await settle();
  assert.equal(t.els["coder-kind"].value, "bug-report");
  assert.equal(t.els["coder-selector"].textContent, "The whole page");

  const stale = setup({ session: { coderPrefill: { at: Date.now() - 120000, kind: "tokens", tabId: 5, selector: "" } } });
  await settle();
  assert.equal(stale.els["coder-panel"].open, false);
  const other = setup({ tabWindow: 2, session: { coderPrefill: { at: Date.now(), kind: "tokens", tabId: 5, selector: "" } } });
  await settle();
  assert.equal(other.els["coder-panel"].open, false);
});

test("a new turn is read from its start, after the end of the one on screen", async () => {
  let latest = "t1";
  const turns = {
    t1: [
      { seq: 1, type: "turn.started", payload: { text: "first job" } },
      { seq: 2, type: "assistant.delta", payload: { text: "one" } },
      { seq: 3, type: "turn.finished", payload: { status: "completed" } },
    ],
    t2: [
      { seq: 1, type: "turn.started", payload: { text: "a follow-up" } },
      { seq: 2, type: "assistant.delta", payload: { text: "two" } },
    ],
  };
  const t = mirrorSetup({ id: "c-1", startedInDesktop: true }, (m) => {
    const turn = m.turn || latest;
    return { ok: true, turn, turn_active: turn === "t2", events: turns[turn].filter((e) => e.seq > (m.since || 0)), images: [] };
  });
  await watchFirst(t);
  assert.match(t.els["coder-log"].textContent, /one/);
  latest = "t2";
  const before = t.of("coder_events").length;
  t.timers.fire(6000);
  await settle(20);
  const asked = t.of("coder_events").slice(before).map(({ turn, since }) => ({ turn, since }));
  assert.deepEqual(asked, [{ turn: undefined, since: 3 }, { turn: "t1", since: 3 }, { turn: "t2", since: undefined }]);
  const text = t.els["coder-log"].textContent;
  assert.match(text, /a follow-up/);
  assert.match(text, /two/);
  assert.equal(t.els["coder-stop"].hidden, false, "the new turn runs");
});

test("coder mode off, said by coder.queue, becomes the section's notice", async () => {
  const words = "coder mode is disabled — enable it in Settings or with `monoagentcli coder enable --yes-i-understand`";
  const t = setup({ replies: { coder_queue: { ok: false, error: words, code: "coder_disabled" } } });
  await settle();
  t.els["coder-kind"].value = "bug-report";
  t.els["coder-kind"].fire("change");
  t.els["coder-queue"].fire("click");
  await settle();
  assert.equal(t.els["coder-notice"].textContent, words);
  assert.equal(t.els["coder-notice"].hidden, false);
  assert.equal(t.els["coder-msg"].textContent, "", "said once");
  assert.equal(t.els["coder-queue"].disabled, true);
});

test("the reload checkbox shows for a bug report only, and its answer goes with that kind alone", async () => {
  const t = setup({ replies: { coder_queue: { ok: true, item: { id: "q" } } } });
  await settle();
  const kind = (k) => {
    t.els["coder-kind"].value = k;
    t.els["coder-kind"].fire("change");
  };
  assert.equal(t.els["coder-reload-row"].hidden, true, "shown for copy-style");
  kind("bug-report");
  assert.equal(t.els["coder-reload-row"].hidden, false);
  assert.equal(t.els["coder-reload"].checked, true, "checked by default");
  t.els["coder-queue"].fire("click");
  await settle();
  t.els["coder-reload"].checked = false;
  t.els["coder-queue"].fire("click");
  await settle();
  kind("tokens");
  assert.equal(t.els["coder-reload-row"].hidden, true, "shown for tokens");
  t.els["coder-queue"].fire("click");
  await settle();
  const sent = t.of("coder_queue").map(({ kind: k, reload }) => [k, reload]);
  assert.deepEqual(sent, [["bug-report", true], ["bug-report", false], ["tokens", undefined]]);
  assert.ok(!("reload" in t.of("coder_queue")[2]), "reload went with another kind");
  assert.ok(!JSON.stringify(t.sent).includes("SECRET"), "the page's title went out");
});

test("a queued job shows its whole address under the page's title, as text", async () => {
  const path = `/${"a".repeat(600)}<img src=x onerror=alert(1)>%0A?next=https://github.com/login`;
  const t = setup({
    replies: { coder_queue_list: { ok: true, items: [{ id: "q1", kind: "bug-report", status: "queued", title: "GitHub", origin: "https://evil.example", path }] } },
  });
  await settle();
  const item = t.els["coder-queued"].children[0];
  const address = item.children.find((n) => n.className === "coder-address");
  assert.ok(address, "no address line");
  assert.equal(address.textContent, `https://evil.example${path}`, "the address was cut or changed");
  assert.equal(item.children.indexOf(address), 1, "the address sits right under the title");
  assert.equal(item.children[0].textContent, "Bug report: GitHub");
  assert.deepEqual(html, [], "markup was parsed");
  assert.ok(!item.all().some((n) => n.tag === "img"), "the path became an element");
});

test("nothing about a profile is asked before the header has one; then everything is, with it (#822)", async () => {
  const t = setup({ current: null, replies: { coder_list: { ok: true, conversations: [{ id: "c-1", startedInDesktop: true }] }, coder_events: { ok: true, turn: "t1", events: [], turn_active: false, images: [] } } });
  t.openSection();
  await settle();
  const profiled = ["coder_projects", "coder_queue_list", "coder_list", "coder_events", "coder_queue"];
  assert.deepEqual(t.sent.filter((m) => profiled.includes(m.type)), [], "asked with no profile");
  assert.equal(t.els["coder-queue"].disabled, true);
  t.env.profiles.current = { id: "p-work" };
  t.doc.fire("panel:profiles");
  await settle();
  for (const type of ["coder_projects", "coder_queue_list", "coder_list"]) {
    assert.deepEqual(t.of(type).map((m) => m.profile), ["p-work"], `${type} once, with the profile`);
  }
  t.env.profiles.current = { id: "p-home" };
  t.doc.fire("panel:profiles");
  await settle();
  assert.deepEqual(t.of("coder_projects").map((m) => m.profile), ["p-work", "p-home"], "another profile's folders");
  assert.ok(t.sent.every((m) => !profiled.includes(m.type) || m.profile), "a request went out with no profile");
});

test("Fill with coder and Ask coder queue a task: coder_task with the tab and the note, none of the page, no folder or element", async () => {
  const t = setup({ replies: { coder_task: { ok: true, task: { taskId: "at-1", status: "queued" } } } });
  await settle();
  t.els["coder-kind"].value = "extension-fill";
  t.els["coder-kind"].fire("change");
  assert.equal(t.els["coder-queue"].disabled, false, "a fill needs neither an element nor a note");
  t.els["coder-note"].value = "use my work email";
  t.els["coder-queue"].fire("click");
  await settle();
  assert.deepEqual(t.of("coder_task"), [{ type: "coder_task", kind: "extension-fill", tabId: 5, note: "use my work email", profile: "p-work" }]);
  assert.equal(t.of("coder_queue").length, 0, "a task went to the coder queue");
  assert.ok(!JSON.stringify(t.sent).includes("SECRET"), "the page's title went out");
  assert.match(t.els["coder-msg"].textContent, /Queued as a task\. Start it in the MonoAgent app/);
  assert.equal(t.els["coder-note"].value, "");

  t.els["coder-kind"].value = "extension-ask";
  t.els["coder-kind"].fire("change");
  assert.equal(t.els["coder-queue"].disabled, true, "an ask needs its question");
  assert.equal(t.els["coder-why"].textContent, "Say what you want in the note.");
  t.els["coder-note"].value = "why is Pay disabled?";
  t.els["coder-note"].fire("input");
  t.els["coder-queue"].fire("click");
  await settle();
  assert.equal(t.of("coder_task")[1].kind, "extension-ask");
  assert.equal(t.of("coder_task")[1].note, "why is Pay disabled?");
});

test("with coder mode off a task says it was not queued and that the quick tools remain", async () => {
  const t = setup({ replies: { coder_task: { ok: false, error: "coder mode is disabled", code: "coder_disabled" } } });
  await settle();
  t.els["coder-kind"].value = "extension-fill";
  t.els["coder-kind"].fire("change");
  t.els["coder-queue"].fire("click");
  await settle();
  assert.match(t.els["coder-notice"].textContent, /Coder mode is off.*not queued.*quick one-shots/);
  assert.equal(t.els["coder-queue"].disabled, true);
});
