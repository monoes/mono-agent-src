// The chat's pure core (chat_core.js): what page context may be sent, how a
// progress frame or a replayed event becomes a message, and what is kept
// across a closed panel.
// `CHROME_PATH=/nonexistent node --test chrome-extension/chat_core.test.mjs`

import test from "node:test";
import assert from "node:assert/strict";
import { loadExtensionScripts } from "./test_helpers.mjs";

const { MonoChatCore: C } = loadExtensionScripts(["chat_context.js", "chat_core.js"]);

const ev = (seq, type, payload = {}) => ({ seq, type, payload });
const frame = (seq, stage, payload) => ({ stage, detail: JSON.stringify({ seq, payload }) });

test("a message is trimmed and an empty one is nothing", () => {
  assert.equal(C.checkMessage("  hi \n"), "hi");
  assert.equal(C.checkMessage("   "), "");
  assert.equal(C.checkMessage(undefined), "");
  assert.equal(C.checkMessage("y".repeat(C.MESSAGE_CAP + 10)).length, C.MESSAGE_CAP);
});

test("progress frames parse as stage + JSON detail, and bad ones are dropped", () => {
  assert.deepEqual(C.parseProgress(frame(3, "assistant.delta", { text: "hi" })), ev(3, "assistant.delta", { text: "hi" }));
  assert.equal(C.parseProgress({ stage: "notice", detail: "not json" }), null);
  assert.equal(C.parseProgress({ stage: "notice", detail: "{\"payload\":{}}" }), null, "no seq");
  assert.equal(C.parseProgress({ detail: "{}" }), null);
  assert.equal(C.parseProgress(null), null);
  assert.deepEqual(C.parseProgress({ stage: "usage.updated", detail: "{\"seq\":1}" }).payload, {});
});

test("replayed events are filtered and ordered", () => {
  const out = C.normalizeEvents([ev(5, "notice"), { type: "x" }, null, ev(2, "assistant.delta"), { seq: 1 }]);
  assert.deepEqual(out.map((e) => e.seq), [2, 5]);
  assert.deepEqual(C.normalizeEvents("nope"), []);
});

test("deltas stream into one assistant message after the user's", () => {
  const s = C.newState(null);
  C.startTurn(s, "hello", "A page");
  C.applyEvent(s, ev(1, "assistant.delta", { text: "Hel" }));
  C.applyEvent(s, ev(2, "assistant.delta", { delta: "lo " }));
  C.applyEvent(s, ev(3, "assistant.delta", { content: "there" }));
  assert.deepEqual(s.messages.map((m) => [m.role, m.text]), [["user", "hello"], ["assistant", "Hello there"]]);
  assert.equal(s.messages[0].page, "A page");
  assert.equal(s.messages[1].open, true);
  C.finishTurn(s, "Hello there");
  assert.equal(s.messages[1].open, false);
  assert.equal(s.messages.length, 2, "the final text does not double the streamed one");
});

test("the final text fills in a reply that streamed nothing", () => {
  const s = C.newState(null);
  C.startTurn(s, "q");
  C.finishTurn(s, "whole answer");
  assert.equal(s.messages.at(-1).text, "whole answer");
  assert.equal(s.messages.at(-1).role, "assistant");
});

test("an event already seen changes nothing: the stream and a replay may overlap", () => {
  const s = C.newState(null);
  C.startTurn(s, "q");
  assert.equal(C.applyEvent(s, ev(1, "assistant.delta", { text: "a" })), true);
  assert.equal(C.applyEvent(s, ev(1, "assistant.delta", { text: "a" })), false);
  assert.equal(C.applyEvent(s, ev(0, "assistant.delta", { text: "z" })), false);
  assert.equal(s.messages.at(-1).text, "a");
  assert.equal(s.lastSeq, 1);
});

test("tools show as running, then done or failed, matched by id then by name", () => {
  const s = C.newState(null);
  C.startTurn(s, "q");
  C.applyEvent(s, ev(1, "tool.started", { id: "t1", name: "read_page" }));
  C.applyEvent(s, ev(2, "tool.started", { id: "t2", name: "search" }));
  C.applyEvent(s, ev(3, "tool.completed", { id: "t2" }));
  C.applyEvent(s, ev(4, "tool.completed", { name: "read_page", error: "boom" }));
  assert.deepEqual(s.messages.at(-1).tools.map((t) => [t.name, t.status]), [["read_page", "failed"], ["search", "done"]]);
});

test("a tool left running when the turn closes is not shown running forever", () => {
  const s = C.newState(null);
  C.startTurn(s, "q");
  C.applyEvent(s, ev(1, "tool.started", { name: "x" }));
  C.finishTurn(s, "done");
  assert.equal(s.messages.at(-1).tools[0].status, "done");
});

test("notices become transcript lines; session.bound names the conversation; usage is ignored", () => {
  const s = C.newState(null);
  C.applyEvent(s, ev(1, "notice", { message: "Rate limited, retrying" }));
  C.applyEvent(s, ev(2, "session.bound", { conversation: "c-9" }));
  C.applyEvent(s, ev(3, "usage.updated", { tokens: 10 }));
  assert.deepEqual(s.messages.map((m) => [m.role, m.text]), [["notice", "Rate limited, retrying"]]);
  assert.equal(s.conversation, "c-9");
  C.applyEvent(s, ev(4, "session.bound", { conversation: "other" }));
  assert.equal(s.conversation, "c-9", "a bound conversation is not swapped mid-chat");
  assert.equal(s.lastSeq, 4);
});

test("a replayed user message is added once, and is not doubled by the one typed locally", () => {
  const s = C.newState(null);
  C.startTurn(s, "hi");
  C.applyEvent(s, ev(1, "user.message", { text: "hi" }));
  assert.equal(s.messages.filter((m) => m.role === "user").length, 1);
  C.applyEvent(s, ev(2, "assistant.delta", { text: "yo" }));
  C.applyEvent(s, ev(3, "user.message", { text: "again" }));
  C.applyEvent(s, ev(4, "assistant.delta", { text: "yo2" }));
  assert.deepEqual(s.messages.map((m) => m.text), ["hi", "yo", "again", "yo2"]);
});

test("a failed turn says so in the transcript and closes the reply", () => {
  const s = C.newState(null);
  C.startTurn(s, "q");
  C.applyEvent(s, ev(1, "assistant.delta", { text: "part" }));
  C.failTurn(s, "went offline");
  assert.equal(s.messages.at(-1).role, "notice");
  assert.equal(s.messages.at(-1).error, true);
  assert.equal(s.messages.find((m) => m.role === "assistant").open, false);
});

// Only the explicit shared flag makes a retry share the page: a page title
// alone (an old-format log, or a record from another shape) never does.
test("unreadAfterFailure needs the explicit shared flag, not just a page title", () => {
  const failed = (user) => ({ messages: [user, { role: "notice", text: "x", error: true }] });
  assert.equal(C.unreadAfterFailure(failed({ role: "user", text: "q", page: "old page title" })), false, "legacy record with only a page");
  assert.equal(C.unreadAfterFailure(failed({ role: "user", text: "q", page: "t", shared: false })), false);
  assert.equal(C.unreadAfterFailure(failed({ role: "user", text: "q", shared: true })), true);
  assert.equal(C.unreadAfterFailure(failed({ role: "user", text: "q", page: "t", shared: true })), true);
  const s = C.newState(null);
  C.startTurn(s, "q", "A page", true);
  C.failTurn(s, "offline");
  assert.equal(C.unreadAfterFailure(s), true, "a live turn that shared the page");
  const legacy = C.newState(null);
  legacy.messages.push({ role: "user", text: "q", page: "old page title" }, { role: "notice", text: "x", error: true });
  assert.equal(C.unreadAfterFailure(legacy), false);
});

function fakeStorage(seed = {}) {
  const data = { ...seed };
  return {
    data,
    get: async (keys) => Object.fromEntries([].concat(keys).filter((k) => k in data).map((k) => [k, data[k]])),
    set: async (o) => Object.assign(data, o),
  };
}

test("state survives a closed panel, per profile", async () => {
  const st = fakeStorage();
  const s = C.newState(null);
  C.startTurn(s, "q");
  C.applyEvent(s, ev(4, "assistant.delta", { text: "a" }));
  s.conversation = "c-1";
  await C.saveState(st, "p-work", s);

  const back = await C.loadState(st, "p-work");
  assert.equal(back.conversation, "c-1");
  assert.equal(back.lastSeq, 4);
  assert.deepEqual(back.messages.map((m) => m.text), ["q", "a"]);
  assert.equal(back.messages[1].open, true, "a reply cut off by a closed panel carries on from the replay");

  const other = await C.loadState(st, "p-home");
  assert.deepEqual([other.conversation, other.messages.length], ["", 0]);
  assert.ok(C.convKey("p-work") !== C.convKey("p-home"));
});

test("a conversation the worker learned while the panel was closed is picked up and replayed from the start", async () => {
  const st = fakeStorage();
  const s = C.newState(null);
  C.startTurn(s, "first question"); // saved before any reply: no conversation yet
  await C.saveState(st, "", s);
  await C.rememberConversation(st, "", "c-7");
  const back = await C.loadState(st, "");
  assert.equal(back.conversation, "c-7");
  assert.equal(back.lastSeq, 0);
  assert.equal(back.messages[0].text, "first question", "the question is kept");
});

test("New chat elsewhere makes a stored transcript someone else's", async () => {
  const st = fakeStorage();
  const s = C.newState(null);
  s.conversation = "old";
  C.startTurn(s, "q");
  await C.saveState(st, "p", s);
  await C.rememberConversation(st, "p", "new");
  const back = await C.loadState(st, "p");
  assert.deepEqual([back.conversation, back.messages.length, back.lastSeq], ["new", 0, 0]);
});

test("the stored transcript is bounded", async () => {
  const st = fakeStorage();
  const s = C.newState(null);
  for (let i = 0; i < 200; i++) C.startTurn(s, "m" + i);
  s.messages.at(-1).text = "z".repeat(100000);
  await C.saveState(st, "p", s);
  const stored = st.data[C.logKey("p")];
  assert.equal(stored.messages.length, 60);
  assert.ok(stored.messages.at(-1).text.length <= 40000);
});

test("storage that throws leaves a working, empty chat", async () => {
  const broken = { get: async () => { throw new Error("gone"); }, set: async () => { throw new Error("full"); } };
  assert.deepEqual((await C.loadState(broken, "p")).messages, []);
  await C.saveState(broken, "p", C.newState(null));
  await C.rememberConversation(broken, "p", "c");
});

test("a profile id with a path in it is not a key of its own", () => {
  assert.equal(C.convKey("../x"), C.convKey(""));
  assert.equal(C.validProfile("p-work"), true);
  assert.equal(C.validProfile("a/b"), false);
});

// ── the real chatevents records (internal/ai/chatevents/event.go) ──

test("real tool records: callId pairs started with completed; ok:false, denied and cancelled fail", () => {
  const s = C.newState(null);
  C.startTurn(s, "q");
  C.applyEvent(s, ev(1, "tool.started", { callId: "a", name: "read_page", arguments: { x: 1 } }));
  C.applyEvent(s, ev(2, "tool.started", { callId: "b", name: "write", arguments: {} }));
  C.applyEvent(s, ev(3, "tool.started", { callId: "c", name: "rm", arguments: {} }));
  C.applyEvent(s, ev(4, "tool.started", { callId: "d", name: "x", arguments: {}, agentId: "w1" }));
  C.applyEvent(s, ev(5, "tool.completed", { callId: "a", ok: true, result: "fine" }));
  C.applyEvent(s, ev(6, "tool.completed", { callId: "b", ok: false, result: "err" }));
  C.applyEvent(s, ev(7, "tool.completed", { callId: "c", denied: true }));
  C.applyEvent(s, ev(8, "tool.completed", { callId: "d", cancelled: true }));
  assert.deepEqual(s.messages.at(-1).tools.map((t) => [t.id, t.status]), [["a", "done"], ["b", "failed"], ["c", "failed"], ["d", "failed"]]);
});

test("a completed tool with no ok field is not a failure", () => {
  const s = C.newState(null);
  C.startTurn(s, "q");
  C.applyEvent(s, ev(1, "tool.started", { callId: "a", name: "n" }));
  C.applyEvent(s, ev(2, "tool.completed", { callId: "a", result: "r" }));
  assert.equal(s.messages.at(-1).tools[0].status, "done");
});

test("real deltas: partId text streams; a worker's (agentId) is ignored", () => {
  const s = C.newState(null);
  C.startTurn(s, "q");
  C.applyEvent(s, ev(1, "assistant.delta", { partId: "p1", text: "Hi " }));
  C.applyEvent(s, ev(2, "assistant.delta", { partId: "p2", text: "WORKER", agentId: "w1" }));
  C.applyEvent(s, ev(3, "assistant.delta", { partId: "p1", text: "there" }));
  assert.equal(s.messages.at(-1).text, "Hi there");
});

test("real notice {code,message,severity} and session.bound {runtime,sessionId}", () => {
  const s = C.newState(null);
  C.applyEvent(s, ev(1, "session.bound", { runtime: "claude", sessionId: "sess-1" }));
  C.applyEvent(s, ev(2, "notice", { code: "rate_limited", message: "Slow down", severity: "warn" }));
  C.applyEvent(s, ev(3, "notice", { code: "boom", message: "Broke", severity: "error" }));
  assert.equal(s.conversation, "", "session.bound names no conversation");
  assert.deepEqual(s.messages.map((m) => [m.text, !!m.error]), [["Slow down", false], ["Broke", true]]);
});

test("turn.finished closes the reply; a failure says why; a cancel says Stopped", () => {
  const s = C.newState(null);
  C.startTurn(s, "q");
  C.applyEvent(s, ev(1, "assistant.delta", { text: "part" }));
  C.applyEvent(s, ev(2, "turn.finished", { status: "completed" }));
  assert.equal(s.messages.at(-1).open, false);
  assert.equal(s.messages.length, 2, "a normal finish adds no line");

  const f = C.newState(null);
  C.startTurn(f, "q");
  C.applyEvent(f, ev(1, "turn.finished", { status: "failed", code: "runtime_exit" }));
  assert.deepEqual([f.messages.at(-1).role, f.messages.at(-1).error], ["notice", true]);
  assert.match(f.messages.at(-1).text, /runtime_exit/);

  const c = C.newState(null);
  C.startTurn(c, "q");
  C.applyEvent(c, ev(1, "turn.finished", { status: "cancelled" }));
  assert.equal(c.messages.at(-1).text, "Stopped.");
});

test("turn.started carries the user's text: a replay rebuilds the bubble, a live turn does not double it", () => {
  const replay = C.newState(null);
  C.applyEvent(replay, { ...ev(1, "turn.started", { text: "what is this?" }), turn: "t-9" });
  C.applyEvent(replay, { ...ev(2, "assistant.delta", { text: "a page" }), turn: "t-9" });
  assert.deepEqual(replay.messages.map((m) => [m.role, m.text]), [["user", "what is this?"], ["assistant", "a page"]]);

  const live = C.newState(null);
  C.startTurn(live, "what is this?");
  C.applyEvent(live, ev(1, "turn.started", { text: "what is this?" }));
  assert.equal(live.messages.filter((m) => m.role === "user").length, 1);
});

test("turn.started text wrapped with the page context shows only what the person typed", () => {
  const wrapped = "The person is looking at a web page.\n[untrusted user data]\nurl: https://x.test/\n[/untrusted]\n\nThe person's message:\nwhat is it about";
  const live = C.newState(null);
  C.startTurn(live, "what is it about");
  C.applyEvent(live, ev(1, "turn.started", { text: wrapped }));
  assert.deepEqual(live.messages.filter((m) => m.role === "user").map((m) => m.text), ["what is it about"]);
  const replay = C.newState(null);
  C.applyEvent(replay, { ...ev(1, "turn.started", { text: wrapped }), turn: "t-1" });
  assert.deepEqual(replay.messages.map((m) => m.text), ["what is it about"]);
});

test("seq restarts every turn: a new turn's events are not mistaken for seen ones", () => {
  const s = C.newState(null);
  C.startTurn(s, "one");
  C.applyEvent(s, { ...ev(1, "assistant.delta", { text: "a" }), turn: "t-1" });
  C.applyEvent(s, { ...ev(2, "assistant.delta", { text: "b" }), turn: "t-1" });
  C.finishTurn(s, "ab", "t-1");
  assert.deepEqual([s.turn, s.lastSeq], ["t-1", 2]);

  C.startTurn(s, "two"); // no turn id known yet: live frames carry no wrapper
  assert.deepEqual([s.turn, s.lastSeq], ["", 0]);
  assert.equal(C.applyEvent(s, ev(1, "assistant.delta", { text: "c" })), true);
  assert.equal(C.applyEvent(s, ev(1, "assistant.delta", { text: "c" })), false);
  C.finishTurn(s, "c", "t-2");
  assert.equal(s.turn, "t-2");

  // a replay of the same turn skips what was seen; a different turn starts over
  assert.equal(C.applyEvent(s, { ...ev(1, "assistant.delta", { text: "c" }), turn: "t-2" }), false);
  assert.equal(C.applyEvent(s, { ...ev(1, "assistant.delta", { text: "d" }), turn: "t-3" }), true);
  assert.equal(s.turn, "t-3");
});

test("the wrapper's conversation and turn are read from a progress frame, as strings or numbers", () => {
  const e = C.parseProgress({ stage: "assistant.delta", detail: JSON.stringify({ seq: 1, conversation: "c-5", turn: 7, payload: { text: "x" } }) });
  assert.deepEqual([e.conversation, e.turn], ["c-5", "7"]);
  const s = C.newState(null);
  C.startTurn(s, "q");
  C.applyEvent(s, e);
  assert.deepEqual([s.conversation, s.turn], ["c-5", "7"], "Stop can name the conversation before the turn ends");
});

test("the turn is stored with the transcript", async () => {
  const st = fakeStorage();
  const s = C.newState(null);
  s.conversation = "c-1";
  C.startTurn(s, "q");
  C.applyEvent(s, { ...ev(3, "assistant.delta", { text: "a" }), turn: "t-2" });
  await C.saveState(st, "p", s);
  const back = await C.loadState(st, "p");
  assert.deepEqual([back.turn, back.lastSeq], ["t-2", 3]);
});

test("events from chat.events are stamped with the reply's turn", () => {
  const out = C.normalizeEvents([{ seq: 2, type: "assistant.delta", payload: {} }], 4);
  assert.equal(out[0].turn, "4");
});

// #451: a proposal tool's answer becomes a card under the reply. Only a call
// that finished cleanly makes one, and a conversation that read a page marks
// its cards as made after reading it.
test("a finished propose_task call puts a card under the reply, marked when a page was read", () => {
  const { MonoChatCore: C2 } = loadExtensionScripts(["chat_proposals.js", "chat_context.js", "chat_core.js"]);
  const s = C2.newState(null);
  C2.startTurn(s, "add a task for this", "A page");
  C2.applyEvent(s, ev(1, "tool.started", { callId: "toolu_1", name: "propose_task", arguments: { title: "Book flights" } }));
  C2.applyEvent(s, ev(2, "tool.completed", { callId: "toolu_1", ok: true, result: JSON.stringify({ proposal: { kind: "task", title: "Book flights", notes: "Before May" }, added: false }) }));
  C2.applyEvent(s, ev(3, "tool.completed", { callId: "toolu_1", ok: true, result: "{}" })); // a replay of the same call adds nothing
  const a = s.messages.at(-1);
  assert.equal(a.role, "assistant");
  assert.deepEqual(a.proposals.map((p) => [p.id, p.title, p.untrustedPage, p.state]), [["toolu_1", "Book flights", true, "pending"]]);

  // Kept across a closed panel, with whether the conversation read a page.
  const back = C2.newState(JSON.parse(JSON.stringify(C2.serialize(s))));
  assert.equal(back.readPage, true);
  assert.equal(back.messages.at(-1).proposals[0].title, "Book flights");

  // A refused call makes no card.
  const t = C2.newState(null);
  C2.startTurn(t, "add a task", "");
  C2.applyEvent(t, ev(1, "tool.started", { callId: "x", name: "propose_task" }));
  C2.applyEvent(t, ev(2, "tool.completed", { callId: "x", ok: false, result: JSON.stringify({ proposal: { kind: "task", title: "T" } }) }));
  assert.equal((t.messages.at(-1).proposals || []).length, 0);
});

test("a conversation that never read a page makes unmarked cards; a replayed wrapped turn counts as reading one", () => {
  const { MonoChatCore: C2 } = loadExtensionScripts(["chat_proposals.js", "chat_context.js", "chat_core.js"]);
  const s = C2.newState(null);
  C2.startTurn(s, "remind me to call Sam", "");
  C2.applyEvent(s, ev(1, "tool.started", { callId: "c1", name: "propose_task" }));
  C2.applyEvent(s, ev(2, "tool.completed", { callId: "c1", ok: true, result: JSON.stringify({ proposal: { kind: "task", title: "Call Sam" } }) }));
  assert.equal(s.messages.at(-1).proposals[0].untrustedPage, false);

  const r = C2.newState(null);
  C2.applyEvent(r, ev(1, "turn.started", { text: C2.WRAPPER_INTRO + "\n…\n" + C2.MESSAGE_BOUNDARY + "\nadd a task" }));
  C2.applyEvent(r, ev(2, "tool.started", { callId: "c2", name: "propose_task" }));
  C2.applyEvent(r, ev(3, "tool.completed", { callId: "c2", ok: true, result: JSON.stringify({ proposal: { kind: "task", title: "T" } }) }));
  assert.equal(r.readPage, true);
  assert.equal(r.messages.at(-1).proposals[0].untrustedPage, true);
});

// #868: any page context marks the conversation as having read a page, also
// one with text but no title or address, so its cards carry the warning.
test("a turn that carried an untitled page counts as reading one", () => {
  const { MonoChatCore: C2 } = loadExtensionScripts(["chat_proposals.js", "chat_context.js", "chat_core.js"]);
  const s = C2.newState(null);
  C2.startTurn(s, "add a task", "", true);
  assert.equal(s.readPage, true);
  assert.equal(s.messages[0].page, undefined, "no label to show");
  C2.applyEvent(s, ev(1, "tool.started", { callId: "c1", name: "propose_task" }));
  C2.applyEvent(s, ev(2, "tool.completed", { callId: "c1", ok: true, result: JSON.stringify({ proposal: { kind: "task", title: "T" } }) }));
  assert.equal(s.messages.at(-1).proposals[0].untrustedPage, true);
  const none = C2.newState(null);
  C2.startTurn(none, "hello", "", false);
  assert.equal(none.readPage, false);
});

// #876 review: a runtime that reuses call ids may answer two proposals in one
// reply under the same id. Each call is a card; a replayed answer is not.
test("two proposals under one repeated call id in one reply are two cards, and a replay adds none", () => {
  const { MonoChatCore: C2 } = loadExtensionScripts(["chat_proposals.js", "chat_context.js", "chat_core.js"]);
  const s = C2.newState(null);
  C2.startTurn(s, "add two tasks", "");
  const done = (seq, title) => ev(seq, "tool.completed", { callId: "functions.propose_task:0", ok: true, result: JSON.stringify({ proposal: { kind: "task", title } }) });
  C2.applyEvent(s, ev(1, "tool.started", { callId: "functions.propose_task:0", name: "propose_task" }));
  C2.applyEvent(s, done(2, "Book flights"));
  C2.applyEvent(s, ev(3, "tool.started", { callId: "functions.propose_task:0", name: "propose_task" }));
  C2.applyEvent(s, done(4, "Pack"));
  C2.applyEvent(s, done(5, "Book flights")); // the first answer again, replayed under a new number
  const a = s.messages.at(-1);
  assert.deepEqual(a.proposals.map((p) => p.title), ["Book flights", "Pack"]);
  assert.deepEqual(a.tools.map((t) => t.status), ["done", "done"], "each call's chip is done");
  assert.notEqual(a.proposals[0].nonce, a.proposals[1].nonce);
});

// #876 review: a card is bound to the page the panel showed when its turn was
// sent, so Start recording can record that page and no other.
test("a card is bound to the page its turn was sent from, also after the panel reopens", () => {
  const { MonoChatCore: C2 } = loadExtensionScripts(["chat_proposals.js", "chat_context.js", "chat_core.js"]);
  const s = C2.newState(null);
  const page = { tabId: 5, url: "https://a.example/p" };
  C2.startTurn(s, "record it", "A page", true, page);
  C2.applyEvent(s, ev(1, "tool.started", { callId: "c1", name: "propose_recording" }));
  C2.applyEvent(s, ev(2, "tool.completed", { callId: "c1", ok: true, result: JSON.stringify({ proposal: { kind: "recording", goal: "Export" } }) }));
  assert.deepEqual(s.messages.at(-1).proposals[0].page, page);
  const back = C2.newState(JSON.parse(JSON.stringify(C2.serialize(s))));
  assert.deepEqual(back.messages.at(-1).proposals[0].page, page);
  assert.deepEqual(back.turnPage, page, "a reply replayed after reopening binds to it too");
  C2.startTurn(back, "and now?", "", false, null);
  assert.equal(back.turnPage, null);
});

// A stored record is not trusted: only the boolean true survives a reload, so a truthy string, number
// or object in `shared` never reopens a failed turn with the page chip on.
test("a stored non-boolean shared value never turns page sharing on", () => {
  for (const bad of ["true", 1, {}, [], "yes"]) {
    const s = C.newState({ messages: [{ role: "user", text: "q", page: "t", shared: bad }, { role: "notice", text: "x", error: true }] });
    assert.equal(s.messages[0].shared, undefined, `shared ${JSON.stringify(bad)} is dropped`);
    assert.equal(C.unreadAfterFailure(s), false, `shared ${JSON.stringify(bad)}`);
  }
  const ok = C.newState({ messages: [{ role: "user", text: "q", shared: true }, { role: "notice", text: "x", error: true }] });
  assert.equal(C.unreadAfterFailure(ok), true);
});
