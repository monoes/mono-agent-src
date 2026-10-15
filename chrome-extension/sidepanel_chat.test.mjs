// The chat section of the side panel (sidepanel_chat.js) against a fake
// document, a fake worker (`ask`) and real chat_core.js / chat_render.js:
// the keys, what is sent and with which page, streaming, Stop, the notices
// for an old or absent daemon, and reopening mid-turn.
// `CHROME_PATH=/nonexistent node --test chrome-extension/sidepanel_chat.test.mjs`

import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import { loadExtensionScripts } from "./test_helpers.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const settle = (ms = 15) => new Promise((r) => setTimeout(r, ms));

class Node_ {
  constructor(tag) {
    this.tag = tag; this.children = []; this.parentNode = null; this.dataset = {}; this.attrs = {};
    this.listeners = {}; this._text = ""; this.hidden = false; this.disabled = false; this.value = "";
    this.className = ""; this.scrollTop = 0; this.scrollHeight = 0; this.clientHeight = 0; this.title = "";
  }
  get nextSibling() { const i = this.parentNode ? this.parentNode.children.indexOf(this) : -1; return i >= 0 ? this.parentNode.children[i + 1] || null : null; }
  appendChild(c) { if (c.tag === "#fragment") { for (const k of [...c.children]) this.appendChild(k); return c; } c.remove?.(); c.parentNode = this; this.children.push(c); return c; }
  insertBefore(c, ref) { c.remove?.(); c.parentNode = this; const i = ref ? this.children.indexOf(ref) : -1; if (i < 0) this.children.push(c); else this.children.splice(i, 0, c); return c; }
  remove() { if (this.parentNode) { this.parentNode.children.splice(this.parentNode.children.indexOf(this), 1); this.parentNode = null; } }
  replaceWith(n) { const p = this.parentNode; const i = p.children.indexOf(this); n.remove?.(); n.parentNode = p; p.children[i] = n; this.parentNode = null; }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return this.attrs[k]; }
  addEventListener(t, fn) { (this.listeners[t] = this.listeners[t] || []).push(fn); }
  fire(t, e = {}) {
    const ev = Object.assign({ preventDefault() { ev.prevented = true; } }, e);
    for (const fn of this.listeners[t] || []) fn(ev);
    return ev;
  }
  focus() {}
  set textContent(v) { this._text = String(v); this.children = []; }
  get textContent() { return this._text + this.children.map((c) => c.textContent).join(""); }
}

const IDS = ["chat-log", "chat-empty", "chat-input", "chat-send", "chat-stop", "chat-new", "chat-fill", "chat-summarize", "chat-page", "chat-notice", "chat-state"];

function setup({ replies = {}, stored = {}, pageNow = { tabId: 5, url: "https://a.example/p", title: "A page" }, profile = "p-work" } = {}) {
  const els = {};
  for (const id of IDS) els[id] = new Node_(id);
  els["chat-log"].appendChild(els["chat-empty"]);
  const docListeners = {};
  const doc = {
    getElementById: (id) => els[id],
    createElement: (t) => new Node_(t),
    createTextNode: (t) => { const n = new Node_("#text"); n._text = t; return n; },
    createDocumentFragment: () => new Node_("#fragment"),
    addEventListener: (t, fn) => (docListeners[t] = docListeners[t] || []).push(fn),
    fire: (t) => (docListeners[t] || []).forEach((fn) => fn({})),
  };
  const sent = [];
  const ask = async (m) => {
    sent.push(m);
    const r = replies[m.type];
    return typeof r === "function" ? r(m) : r || { ok: true };
  };
  const store = { ...stored };
  let onMessage = null;
  const chrome = {
    runtime: { onMessage: { addListener: (fn) => (onMessage = fn) } },
    storage: { local: { get: async (k) => Object.fromEntries([].concat(k).filter((x) => x in store).map((x) => [x, store[x]])), set: async (o) => Object.assign(store, o) } },
  };
  const env = { document: doc, chrome, ask, profiles: { current: { id: profile } }, page: pageNow, here: { windowId: 1 }, CustomEvent: class { constructor(t) { this.type = t; } } };
  doc.dispatchEvent = (e) => sent.push({ event: e.type });
  const sandbox = loadExtensionScripts(["chat_core.js", "chat_render.js", "sidepanel_chat.js"], env);
  return {
    els, sent, store, doc, sandbox,
    progress: (tag, seq, stage, payload) => onMessage({ type: "chat_progress", tag, progress: { stage, detail: JSON.stringify({ seq, payload }) } }),
    log: () => els["chat-log"].children.filter((c) => c !== els["chat-empty"]),
    type: (text) => (els["chat-input"].value = text),
    enter: (e = {}) => els["chat-input"].fire("keydown", { key: "Enter", shiftKey: false, ...e }),
  };
}

const READY = { chat_ready: { ok: true, state: "ready" } };
const lastOf = (sent, type) => sent.filter((m) => m.type === type).at(-1);

test("a ready panel enables the composer; Stop is hidden until a turn runs", async () => {
  const t = setup({ replies: READY });
  t.doc.fire("panel:profiles");
  await settle();
  assert.equal(t.els["chat-input"].disabled, false);
  assert.equal(t.els["chat-stop"].hidden, true);
  assert.equal(t.els["chat-page"].textContent, "Using this page: A page");
  assert.equal(t.els["chat-page"].attrs["aria-pressed"], "true");
  assert.equal(t.els["chat-empty"].hidden, false);
});

test("Enter sends, Shift+Enter does not, an empty box does nothing", async () => {
  const t = setup({ replies: { ...READY, chat_send: { ok: true, data: { conversation: "c-1", text: "ok" } } } });
  await settle();
  t.enter();
  await settle();
  assert.equal(t.sent.filter((m) => m.type === "chat_send").length, 0);
  t.type("hello");
  const shift = t.enter({ shiftKey: true });
  assert.equal(shift.prevented, undefined, "Shift+Enter is left to insert a newline");
  await settle();
  assert.equal(t.sent.filter((m) => m.type === "chat_send").length, 0);
  t.type("hello");
  const plain = t.enter();
  assert.equal(plain.prevented, true);
  await settle(30);
  assert.equal(t.sent.filter((m) => m.type === "chat_send").length, 1);
  assert.equal(t.els["chat-input"].value, "", "the box is cleared on send");
});

test("a send carries the profile, the typed message and the page's context", async () => {
  const ctx = { url: "https://a.example/p", title: "A page", text: "body", selection: "sel" };
  const t = setup({ replies: { ...READY, chat_page_context: { ok: true, context: ctx }, chat_send: { ok: true, data: { conversation: "c-1", text: "answer" } } } });
  await settle();
  t.type("what is this?");
  t.enter();
  await settle(30);
  const s = lastOf(t.sent, "chat_send");
  assert.deepEqual([s.profile, s.message, s.context, s.conversation], ["p-work", "what is this?", ctx, ""]);
  assert.match(s.tag, /^c/);
  const [u, a] = t.log();
  assert.equal(u.dataset.role, "user");
  assert.match(u.textContent, /what is this\?/);
  assert.match(u.textContent, /Using this page: A page/);
  assert.equal(a.dataset.role, "assistant");
  assert.equal(a.textContent, "answer");
  assert.equal(t.store["chatConv:p-work"], "c-1", "the conversation is remembered");
});

test("switching the page chip off leaves the page out of that one message only", async () => {
  const t = setup({ replies: { ...READY, chat_page_context: { ok: true, context: { url: "https://a.example/p", title: "A page", text: "b", selection: "" } }, chat_send: { ok: true, data: { conversation: "c", text: "r" } } } });
  await settle();
  t.els["chat-page"].fire("click");
  assert.equal(t.els["chat-page"].textContent, "Not sharing this page");
  assert.equal(t.els["chat-page"].attrs["aria-pressed"], "false");
  t.type("private question");
  t.enter();
  await settle(30);
  assert.equal(lastOf(t.sent, "chat_send").context, null);
  assert.equal(t.sent.some((m) => m.type === "chat_page_context"), false, "the page is not even read");
  assert.equal(t.els["chat-page"].attrs["aria-pressed"], "false", "and the page is not shared again on its own");
});

// The rule that stops "switched tabs, sent a different page": the page is
// shared with the first message of a conversation; after that it is off until
// the person turns the chip on again, and the chip says so.
test("sharing the page is on for the first message only, and the chip says what is shared", async () => {
  const ctx = { url: "https://a.example/p", title: "A page", text: "b", selection: "" };
  const t = setup({ replies: { ...READY, chat_page_context: { ok: true, context: ctx }, chat_send: { ok: true, data: { conversation: "c", text: "r" } } } });
  await settle();
  assert.equal(t.els["chat-page"].textContent, "Using this page: A page");
  t.type("first");
  t.enter();
  await settle(30);
  assert.equal(lastOf(t.sent, "chat_send").context.title, "A page");
  assert.equal(t.els["chat-page"].attrs["aria-pressed"], "false");
  assert.equal(t.els["chat-page"].textContent, "Not sharing this page");

  t.type("second");
  t.enter();
  await settle(30);
  assert.equal(lastOf(t.sent, "chat_send").context, null, "a follow-up carries no page");

  t.els["chat-page"].fire("click");
  assert.equal(t.els["chat-page"].textContent, "Using this page: A page");
  t.type("third, about this page");
  t.enter();
  await settle(30);
  assert.equal(lastOf(t.sent, "chat_send").context.title, "A page");
  assert.equal(t.els["chat-page"].attrs["aria-pressed"], "false", "off again afterwards");

  t.els["chat-new"].fire("click");
  await settle();
  assert.equal(t.els["chat-page"].attrs["aria-pressed"], "true", "a new conversation shares its first page");
});

test("a reopened conversation does not share the page until asked", async () => {
  const { MonoChatCore: Core } = loadExtensionScripts(["chat_core.js"]);
  const earlier = Core.newState(null);
  Core.startTurn(earlier, "earlier", "");
  earlier.conversation = "c-9";
  const stored = { "chatConv:p-work": "c-9", "chatLog:p-work": Core.serialize(earlier) };
  const t = setup({ stored, replies: { ...READY, chat_events: { ok: true, turn: "", events: [], turn_active: false } } });
  t.doc.fire("panel:profiles");
  await settle(40);
  assert.equal(t.log().length > 0, true, "the earlier message was restored");
  assert.equal(t.els["chat-page"].attrs["aria-pressed"], "false");
});

test("the send is refused, with a message, when the page is no longer the one shown", async () => {
  const t = setup({
    replies: { ...READY, chat_page_context: { ok: false, code: "page_changed", error: "The page changed after you wrote this. Nothing was sent." } },
  });
  await settle();
  t.type("about the page I was looking at");
  t.enter();
  await settle(30);
  const ask = lastOf(t.sent, "chat_page_context");
  assert.deepEqual(ask.expect, { tabId: 5, url: "https://a.example/p" }, "the worker is told which tab and address the chip showed");
  assert.equal(t.sent.some((m) => m.type === "chat_send"), false, "nothing is sent");
  assert.equal(t.els["chat-input"].value, "about the page I was looking at", "what was typed is given back");
  assert.equal(t.els["chat-notice"].hidden, false);
  assert.match(t.els["chat-notice"].textContent, /page changed/);
  assert.equal(t.log().length, 0, "no turn was started");
  assert.equal(t.els["chat-send"].disabled, false, "the panel is usable again");
});

test("a page that cannot be shared is not offered, and nothing of it is sent", async () => {
  const t = setup({ pageNow: { tabId: 5, url: "chrome://extensions", title: "Extensions" }, replies: { ...READY, chat_send: { ok: true, data: { text: "x" } } } });
  await settle();
  assert.equal(t.els["chat-page"].disabled, true);
  assert.equal(t.els["chat-summarize"].disabled, true);
  t.type("hi");
  t.enter();
  await settle(30);
  assert.equal(lastOf(t.sent, "chat_send").context, null);
});

test("Summarize this page sends the canned prompt with the page, even after the chip was turned off", async () => {
  const t = setup({ replies: { ...READY, chat_page_context: { ok: true, context: { url: "https://a.example/p", title: "A page", text: "b", selection: "" } }, chat_send: { ok: true, data: { conversation: "c", text: "r" } } } });
  await settle();
  t.els["chat-page"].fire("click");
  t.els["chat-summarize"].fire("click");
  await settle(30);
  const s = lastOf(t.sent, "chat_send");
  assert.equal(s.message, "Summarize this page.");
  assert.equal(s.context.title, "A page");
});

test("streamed deltas and tool chips draw while the turn runs, and Stop appears", async () => {
  let finish;
  const t = setup({ replies: { ...READY, chat_send: () => new Promise((r) => (finish = r)) } });
  await settle();
  t.type("go");
  t.els["chat-page"].fire("click");
  t.enter();
  await settle();
  const tag = lastOf(t.sent, "chat_send").tag;
  assert.equal(t.els["chat-stop"].hidden, false);
  assert.equal(t.els["chat-send"].hidden, true);
  t.progress(tag, 1, "tool.started", { id: "t1", name: "read_page" });
  t.progress(tag, 2, "assistant.delta", { text: "Hello **wor" });
  t.progress(tag, 3, "assistant.delta", { text: "ld**" });
  t.progress(tag, 4, "tool.completed", { id: "t1" });
  await settle();
  const reply = t.log().at(-1);
  const flat = JSON.stringify(reply, (k, v) => (k === "parentNode" || k === "listeners" ? undefined : v));
  assert.match(flat, /read_page/);
  assert.match(flat, /"status":"done"/);
  assert.match(reply.textContent, /Hello world/);
  t.progress("someone-elses-tag", 9, "assistant.delta", { text: "NOPE" });
  assert.doesNotMatch(t.log().at(-1).textContent, /NOPE/);
  finish({ ok: true, data: { conversation: "c-1", text: "Hello **world**" } });
  await settle(30);
  assert.equal(t.els["chat-stop"].hidden, true);
  assert.equal(t.log().filter((m) => m.dataset.role === "assistant").length, 1);
});

test("Stop asks the worker to stop that conversation", async () => {
  let finish;
  const t = setup({ replies: { ...READY, chat_send: () => new Promise((r) => (finish = r)) }, stored: { "chatConv:p-work": "c-5" } });
  await settle();
  t.type("go");
  t.els["chat-page"].fire("click");
  t.enter();
  await settle();
  t.els["chat-stop"].fire("click");
  await settle();
  assert.deepEqual(lastOf(t.sent, "chat_stop"), { type: "chat_stop", profile: "p-work", conversation: "c-5" });
  finish({ ok: true, data: { conversation: "c-5", text: "" } });
});

test("a failed turn is said in the transcript, not thrown away", async () => {
  const t = setup({ replies: { ...READY, chat_send: { ok: false, error: "x", code: "offline" } } });
  await settle();
  t.type("hi");
  t.els["chat-page"].fire("click");
  t.enter();
  await settle(30);
  const last = t.log().at(-1);
  assert.equal(last.dataset.role, "notice");
  assert.equal(last.dataset.error, "true");
  assert.match(last.textContent, /offline/);
  assert.equal(t.els["chat-send"].hidden, false, "the composer is usable again");
});

test("an older MonoAgent gets one clear notice and a disabled composer", async () => {
  const t = setup({ replies: { chat_ready: { ok: true, state: "update" } } });
  await settle();
  assert.equal(t.els["chat-notice"].hidden, false);
  assert.match(t.els["chat-notice"].textContent, /Update MonoAgent/);
  assert.equal(t.els["chat-input"].disabled, true);
  assert.equal(t.els["chat-send"].disabled, true);
  assert.equal(t.sent.some((m) => m.type === "chat_events"), false);
});

test("an offline bridge says so, and comes back with it", async () => {
  let state = "offline";
  const t = setup({ replies: { chat_ready: () => ({ ok: true, state }) } });
  await settle();
  assert.match(t.els["chat-notice"].textContent, /isn't connected/);
  state = "ready";
  t.doc.fire("panel:bridge-up");
  await settle();
  assert.equal(t.els["chat-notice"].hidden, true);
  assert.equal(t.els["chat-input"].disabled, false);
});

test("a worker older than the panel is reported as stale rather than breaking", async () => {
  const t = setup({ replies: { chat_ready: { ok: false, error: "Could not establish connection. Receiving end does not exist." } } });
  await settle();
  assert.ok(t.sent.some((m) => m.event === "panel:stale-worker"));
});

test("reopening replays what happened after the last seen event, and keeps polling while a turn runs", async () => {
  const stored = {
    "chatConv:p-work": "c-1",
    "chatLog:p-work": { conversation: "c-1", turn: "t-1", lastSeq: 2, nextId: 3, messages: [{ id: "m1", role: "user", text: "q" }, { id: "m2", role: "assistant", text: "Par", tools: [], open: true }] },
  };
  let calls = 0;
  const t = setup({
    stored,
    replies: {
      ...READY,
      chat_events: () => {
        calls++;
        return calls === 1
          ? { ok: true, events: [{ seq: 2, type: "assistant.delta", payload: { text: "dup" } }, { seq: 3, type: "assistant.delta", payload: { text: "tial" } }], turn_active: true }
          : { ok: true, events: [{ seq: 4, type: "assistant.delta", payload: { text: " done" } }], turn_active: false };
      },
    },
  });
  await settle(40);
  const ev = t.sent.find((m) => m.type === "chat_events");
  assert.deepEqual([ev.conversation, ev.after_seq, ev.turn, ev.profile], ["c-1", 2, "t-1", "p-work"]);
  assert.equal(t.els["chat-stop"].hidden, false, "a turn is still running there");
  await settle(1700);
  assert.equal(t.log().at(-1).textContent, "Partial done", "the replay filled in the rest once, without doubling seq 2");
  assert.equal(t.els["chat-stop"].hidden, true);
  assert.equal(t.store["chatLog:p-work"].lastSeq, 4);
});

test("New chat forgets the conversation so the next send starts a fresh one", async () => {
  const t = setup({ replies: { ...READY, chat_events: { ok: true, events: [], turn_active: false } }, stored: { "chatConv:p-work": "c-1", "chatLog:p-work": { conversation: "c-1", lastSeq: 1, nextId: 2, messages: [{ id: "m1", role: "user", text: "q" }] } } });
  await settle();
  assert.equal(t.log().length, 1);
  t.els["chat-new"].fire("click");
  await settle();
  assert.equal(t.log().length, 0);
  assert.equal(t.store["chatConv:p-work"], "");
});

test("each profile has its own chat", async () => {
  const stored = { "chatLog:p-work": { conversation: "c-1", lastSeq: 0, nextId: 2, messages: [{ id: "m1", role: "user", text: "work question" }] }, "chatConv:p-work": "c-1" };
  const t = setup({ stored, replies: { ...READY, chat_events: { ok: true, events: [], turn_active: false } } });
  await settle();
  assert.match(t.log()[0].textContent, /work question/);
  t.sandbox.profiles.current = { id: "p-home" };
  t.doc.fire("panel:profiles");
  await settle();
  assert.equal(t.log().length, 0);
});

test("the section is in the panel, first and open by default", () => {
  const html = readFileSync(join(HERE, "sidepanel.html"), "utf8");
  assert.match(html, /<details class="panel chat" id="chat" open>/);
  assert.ok(html.indexOf('id="chat"') < html.indexOf('class="capture"'), "above the capture card");
  for (const f of ["chat_core.js", "chat_render.js", "sidepanel_chat.js", "sidepanel_chat.css"]) assert.ok(html.includes(f), f);
  assert.ok(html.indexOf("sidepanel.js") < html.indexOf("sidepanel_chat.js"), "after sidepanel.js, which it leans on");
  assert.match(html, /<label for="chat-input" class="sr-only">/);
});

test("Fill form, or typing 'fill the form', fills from the profile instead of chatting", async () => {
  const t = setup({ replies: { ...READY, form_fill: { ok: true, filled: 7, found: 9, sources: ["passport.pdf"] } } });
  await settle();
  t.els["chat-fill"].fire("click");
  await settle(30);
  const f = lastOf(t.sent, "form_fill");
  assert.equal(f.profile, "p-work");
  assert.equal(f.windowId, 1);
  assert.match(t.els["chat-notice"].textContent, /Filled 7 of 9 fields from passport\.pdf/);

  t.type("Please fill the form with my information");
  t.enter();
  await settle(30);
  assert.equal(t.sent.filter((m) => m.type === "form_fill").length, 2);
  assert.equal(t.sent.some((m) => m.type === "chat_send"), false, "the chat model is never asked");
  assert.equal(t.els["chat-input"].value, "");
});
