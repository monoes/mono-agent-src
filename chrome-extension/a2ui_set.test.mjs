// Several cards waiting at once are answered together in the side panel (#1803).
// `CHROME_PATH=/nonexistent node --test chrome-extension/a2ui_set.test.mjs`

import test from "node:test";
import assert from "node:assert/strict";
import { loadExtensionScripts } from "./test_helpers.mjs";

const { MonoA2UIRender: R } = loadExtensionScripts(["a2ui_model.js", "a2ui_render.js", "a2ui_timeline.js"]);

function fakeDoc() {
  const node = (tag) => {
    const n = {
      tag, children: [], attrs: {}, className: "", dataset: {}, style: {}, listeners: {}, _text: "", parent: null, hidden: false, value: "", checked: false,
      setAttribute(k, v) { this.attrs[k] = String(v); },
      getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; },
      removeAttribute(k) { delete this.attrs[k]; },
      contains(x) { return x === this || this.children.some((c) => c.contains && c.contains(x)); },
      appendChild(c) { if (c.tag === "#fragment") { for (const k of c.children) this.appendChild(k); c.children = []; return c; } c.remove?.(); c.parent = this; this.children.push(c); return c; },
      replaceChildren(...cs) { this.children = []; for (const c of cs) this.appendChild(c); },
      removeChild(c) { this.children = this.children.filter((x) => x !== c); },
      remove() { if (this.parent) this.parent.children = this.parent.children.filter((x) => x !== this); this.parent = null; },
      insertBefore(c, ref) { c.remove(); const i = ref ? this.children.indexOf(ref) : -1; c.parent = this; if (i < 0) this.children.push(c); else this.children.splice(i, 0, c); return c; },
      get nextSibling() { const p = this.parent; return p ? p.children[p.children.indexOf(this) + 1] || null : null; },
      addEventListener(t, fn) { (this.listeners[t] = this.listeners[t] || []).push(fn); },
      fire(t) { (this.listeners[t] || []).forEach((fn) => fn({})); },
      set textContent(v) { this._text = String(v); this.children = []; },
      get textContent() { return this._text + this.children.map((c) => c.textContent).join(""); },
    };
    if (tag === "label") Object.defineProperty(n, "control", { get: () => null });
    return n;
  };
  return { createElement: node, createDocumentFragment: () => node("#fragment"), createTextNode: (t) => ({ tag: "#text", textContent: t }) };
}
const all = (n, out = []) => { out.push(n); for (const c of n.children || []) all(c, out); return out; };
const shown = (n) => all(n).filter((x) => x.tag === "button" && !x.hidden);
const button = (n, label) => shown(n).find((b) => b.textContent === label);
const inputs = (n) => all(n).filter((x) => x.tag === "textarea");
const on = (b) => b && b.getAttribute("aria-disabled") !== "true";

const askCard = (agent, seq) => ({ seq, turn: "t1", type: "ui.surface", payload: { surfaceId: `ask-${agent}.q1`, provenance: "system", message: { version: "1.0", createSurface: {
  surfaceId: `ask-${agent}.q1`, dataModel: { answers: { q0: "" } }, components: [
    { id: "root", component: "Column", children: ["q", "in", "go"] }, { id: "q", component: "Text", text: `Question of ${agent}` },
    { id: "in", component: "TextField", label: "Your answer", value: { "@path": "/answers/q0" } },
    { id: "gl", component: "Text", text: "Continue" },
    { id: "go", component: "Button", child: "gl", action: { event: { name: "system.answer", context: { questionId: `${agent}.q1`, q0: { "@path": "/answers/q0" } } } } },
  ] } } } });
const agentCard = (id, seq) => ({ seq, turn: "t1", type: "ui.surface", payload: { surfaceId: id, provenance: "agent", message: { version: "1.0", createSurface: {
  surfaceId: id, components: [{ id: "root", component: "Column", children: ["go"] }, { id: "gl", component: "Text", text: `Yes ${id}` },
    { id: "go", component: "Button", child: "gl", action: { event: { name: `yes_${id}`, context: {} } } }] } } } });

function panel(opts = {}) {
  const doc = fakeDoc();
  const log = doc.createElement("div");
  const empty = doc.createElement("div");
  log.appendChild(empty);
  const one = [];
  const sets = [];
  const tl = R.timeline(doc, { onAction: (e) => one.push(e), onSet: (e) => sets.push(JSON.parse(JSON.stringify(e))), setTimeout: () => 1, clearTimeout() {}, ...opts });
  const feed = (...evs) => { for (const e of evs) tl.ingest(e, ""); tl.place(log, empty, () => null); };
  return { doc, log, tl, feed, one, sets };
}
const type = (log, i, value) => { const el = inputs(log)[i]; el.value = value; el.fire("input"); };

test("two questions: no card sends alone, and Send all waits for every answer", async () => {
  const p = panel();
  p.feed(askCard("w1", 1));
  assert.ok(button(p.log, "Continue"), "a single question keeps its Continue");
  assert.equal(on(button(p.log, "Continue")), false, "nothing typed yet");
  type(p.log, 0, "eu");
  assert.equal(on(button(p.log, "Continue")), true);
  const first = inputs(p.log)[0];
  p.feed(askCard("w2", 2)); // a question arrives while the person types
  assert.equal(inputs(p.log)[0], first);
  assert.equal(first.value, "eu");
  assert.equal(button(p.log, "Continue"), undefined);
  assert.match(p.log.textContent, /2 cards are waiting/);
  const send = button(p.log, "Send all");
  assert.equal(on(send), false);
  send.fire("click");
  assert.equal(p.sets.length + p.one.length, 0);
  type(p.log, 1, "postgres");
  assert.equal(on(send), true);
  assert.equal(p.sets.length, 0, "typing the last answer sends nothing");
  send.fire("click");
  send.fire("click");
  assert.equal(p.sets.length, 1);
  assert.deepEqual(p.sets[0].actions.map((a) => [a.surfaceId, a.context.q0]), [["ask-w1.q1", "eu"], ["ask-w2.q1", "postgres"]]);
  assert.equal(p.one.length, 0);
});

test("agent cards: a button only picks, a skipped card is named, and one card alone sends at once", () => {
  const p = panel();
  p.feed(agentCard("region", 1), agentCard("db", 2));
  button(p.log, "Yes region").fire("click");
  assert.equal(button(p.log, "Yes region").getAttribute("aria-pressed"), "true");
  assert.equal(p.one.length + p.sets.length, 0);
  assert.equal(on(button(p.log, "Send all")), false);
  shown(p.log).filter((b) => b.textContent === "Skip this one")[1].fire("click");
  button(p.log, "Send all").fire("click");
  assert.deepEqual([p.sets[0].actions.map((a) => a.name), p.sets[0].skipped], [["yes_region"], ["db"]]);

  const q = panel();
  q.feed(agentCard("solo", 1));
  assert.equal(button(q.log, "Send all"), undefined);
  button(q.log, "Yes solo").fire("click");
  assert.equal(q.one.length, 1);
});

test("an answered agent card leaves the set, and so does a question that closed", () => {
  const p = panel();
  p.feed(agentCard("region", 1), askCard("w1", 2));
  type(p.log, 0, "eu");
  p.feed({ seq: 1, turn: "system-uiaction-1", type: "ui.action", payload: { surfaceId: "region", name: "yes_region", outcome: "accepted" } });
  assert.equal(button(p.log, "Send all"), undefined);
  assert.equal(inputs(p.log)[0].value, "eu");
  assert.equal(on(button(p.log, "Continue")), true);
  assert.equal(p.one.length + p.sets.length, 0);
});

test("what was typed and picked comes back after the panel reopens, and is not sent", () => {
  let dirty = 0;
  const p = panel({ onDirty: () => dirty++ });
  p.feed(askCard("w1", 1), agentCard("db", 2));
  type(p.log, 0, "eu");
  button(p.log, "Yes db").fire("click");
  assert.ok(dirty >= 2);
  const saved = JSON.parse(JSON.stringify(p.tl.dump("c1")));
  const again = panel();
  again.tl.restore(saved, "c1");
  again.feed();
  assert.equal(inputs(again.log)[0].value, "eu");
  assert.equal(button(again.log, "Yes db").getAttribute("aria-pressed"), "true");
  assert.equal(on(button(again.log, "Send all")), true);
  assert.equal(again.one.length + again.sets.length, 0);
  // The bar stays under its set however often the log is drawn.
  again.feed();
  assert.equal(all(again.log).filter((n) => String(n.className).includes("a2ui-setbar")).length, 1);
  assert.match(again.log.children.at(-1).className, /a2ui-setbar/);
});
