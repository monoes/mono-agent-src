// A2UI rendering (a2ui_render.js) against a hand-written fake document: the
// catalog, bindings, actions, caps, and that no markup path exists.
// `CHROME_PATH=/nonexistent node --test chrome-extension/a2ui_render.test.mjs`

import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import { loadExtensionScripts } from "./test_helpers.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const { MonoA2UIModel: M, MonoA2UIRender: R } = loadExtensionScripts(["a2ui_model.js", "a2ui_render.js", "a2ui_timeline.js"]);

function fakeDoc() {
  const made = [];
  const node = (tag) => {
    const n = {
      tag, children: [], attrs: {}, className: "", dataset: {}, style: {}, listeners: {}, _text: "", parent: null, hidden: false, disabled: false, value: "", checked: false,
      setAttribute(k, v) { this.attrs[k] = String(v); },
      getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; },
      removeAttribute(k) { delete this.attrs[k]; },
      contains(x) { return x === this || this.children.some((c) => c.contains && c.contains(x)); },
      appendChild(c) { if (c.tag === "#fragment") { for (const k of c.children) this.appendChild(k); c.children = []; return c; } c.parent = this; this.children.push(c); return c; },
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
    // As the real DOM: a label's `control` is read-only, and assigning it throws in strict code.
    if (tag === "label") Object.defineProperty(n, "control", { get: () => null });
    return n;
  };
  return { made, createElement: (t) => { const n = node(t); made.push(n); return n; }, createDocumentFragment: () => node("#fragment"), createTextNode: (t) => ({ tag: "#text", textContent: t }) };
}
const all = (n, out = []) => { out.push(n); for (const c of n.children || []) all(c, out); return out; };
const find = (n, tag) => all(n).filter((x) => x.tag === tag);

function surfaceOf(components, dataModel = {}, extra = {}) {
  const st = M.newStore();
  M.apply(st, { seq: 1, turn: "t", type: "ui.surface", payload: { surfaceId: "s", provenance: "agent", ...extra, message: { version: "1.0", createSurface: { surfaceId: "s", components, dataModel } } } });
  return { st, s: st.surfaces.get("s") };
}
const approve = [
  { id: "root", component: "Column", children: ["t", "f", "go"] },
  { id: "t", component: "Text", text: { "@path": "/title" } },
  { id: "f", component: "TextField", label: "Text", value: { "@path": "/draft" } },
  { id: "gl", component: "Text", text: "Approve" },
  { id: "go", component: "Button", child: "gl", variant: "primary", action: { event: { name: "hil.approve", context: { id: "482", text: { "@path": "/draft" } } } } },
];

test("the whole catalog renders; unknown types become a muted placeholder", () => {
  const doc = fakeDoc();
  const { s } = surfaceOf([
    { id: "root", component: "Column", children: ["a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "u"] },
    { id: "a", component: "Text", text: "hello", variant: "caption" },
    { id: "b", component: "Row", children: ["a"], justify: "spaceBetween" },
    { id: "c", component: "Card", child: "a" },
    { id: "d", component: "Divider" },
    { id: "e", component: "CheckBox", label: "ok?", value: { "@path": "/ok" } },
    { id: "f", component: "ChoicePicker", options: [{ label: "One", value: "1" }, { label: "Two", value: "2" }], value: { "@path": "/pick" }, variant: "mutuallyExclusive" },
    { id: "g", component: "Progress", value: 3, max: 10, label: "step" },
    { id: "h", component: "Badge", text: "new", tone: "success" },
    { id: "i", component: "TextField", label: "n", value: "", variant: "longText" },
    { id: "j", component: "Text", text: { "@path": "/missing" } },
    { id: "u", component: "Iframe", src: "http://evil" },
  ], { ok: true, pick: ["2"] });
  const m = R.mount(doc, s, {});
  const text = m.el.textContent;
  assert.match(text, /from agent/);
  assert.match(text, /hello/);
  assert.match(text, /Unsupported: Iframe/);
  assert.equal(find(m.el, "textarea").length, 1);
  assert.equal(find(m.el, "input").filter((i) => i.type === "radio").length, 2);
  assert.equal(find(m.el, "input").find((i) => i.type === "radio" && i.value === "2").checked, true);
  assert.equal(find(m.el, "input").find((i) => i.type === "checkbox").checked, true);
  const fill = all(m.el).find((n) => n.className === "a2ui-fill");
  assert.equal(fill.style.width, "30%");
  for (const n of doc.made) assert.ok(["div", "p", "hr", "button", "input", "textarea", "label", "span", "fieldset", "legend"].includes(n.tag), `unexpected tag ${n.tag}`);
});

test("provenance mark: system surfaces say 'from monoagent'", () => {
  const { s } = surfaceOf([{ id: "root", component: "Text", text: "x" }], {}, { provenance: "system" });
  assert.match(R.mount(fakeDoc(), s, {}).el.textContent, /from monoagent/);
});

test("a TextField edits the local model only and bound Text follows; nothing is sent", () => {
  const { s } = surfaceOf(approve, { title: "Send DM?", draft: "Hi" });
  const sent = [];
  const m = R.mount(fakeDoc(), s, { onAction: (e) => sent.push(e) });
  const input = find(m.el, "textarea")[0];
  assert.equal(input.value, "Hi");
  input.value = "Hello there";
  input.fire("input");
  assert.equal(M.getAt(s.dataModel, "/draft"), "Hello there");
  assert.equal(sent.length, 0);
});

test("a Button click sends the A2UI action envelope with the context resolved, then disables for 3s", () => {
  const { s } = surfaceOf(approve, { title: "Send DM?", draft: "Hi" });
  const sent = [];
  const timers = [];
  const m = R.mount(fakeDoc(), s, { onAction: (e) => sent.push(e), now: () => "2026-01-01T00:00:00Z", setTimeout: (fn, ms) => (timers.push({ fn, ms }), timers.length), clearTimeout: () => {} });
  find(m.el, "textarea")[0].value = "Edited";
  find(m.el, "textarea")[0].fire("input");
  const btn = find(m.el, "button")[0];
  btn.fire("click");
  assert.deepEqual(JSON.parse(JSON.stringify(sent)), [{ version: "1.0", action: { name: "hil.approve", surfaceId: "s", sourceComponentId: "go", timestamp: "2026-01-01T00:00:00Z", context: { id: "482", text: "Edited" } } }]);
  assert.equal(btn.getAttribute("aria-disabled"), "true");
  btn.fire("click");
  assert.equal(sent.length, 1, "a second click while disabled does nothing");
  assert.equal(timers[0].ms, 3000);
  timers[0].fn();
  assert.equal(btn.getAttribute("aria-disabled"), null);
});

test("the surface changing re-enables the button; the person's own typing does not", () => {
  const { st, s } = surfaceOf(approve, { title: "T", draft: "" });
  const m = R.mount(fakeDoc(), s, { onAction() {}, setTimeout: () => 1, clearTimeout() {} });
  const btn = find(m.el, "button")[0];
  btn.fire("click");
  find(m.el, "textarea")[0].value = "x";
  find(m.el, "textarea")[0].fire("input");
  assert.equal(btn.getAttribute("aria-disabled"), "true");
  M.apply(st, { seq: 2, turn: "t", type: "ui.surface", payload: { message: { version: "1.0", updateDataModel: { surfaceId: "s", path: "/title", value: "Changed" } } } });
  m.sync(s);
  assert.equal(btn.getAttribute("aria-disabled"), null);
  assert.match(m.el.textContent, /Changed/);
});

test("updateComponents rebuilds; updateDataModel refreshes bindings without rebuilding", () => {
  const { st, s } = surfaceOf(approve, { title: "A", draft: "" });
  const doc = fakeDoc();
  const m = R.mount(doc, s, {});
  const input = find(m.el, "textarea")[0];
  M.apply(st, { seq: 2, turn: "t", type: "ui.surface", payload: { message: { version: "1.0", updateDataModel: { surfaceId: "s", path: "/draft", value: "from agent" } } } });
  m.sync(s);
  assert.equal(find(m.el, "textarea")[0], input, "same element: focus and caret survive");
  assert.equal(input.value, "from agent");
  M.apply(st, { seq: 3, turn: "t", type: "ui.surface", payload: { message: { version: "1.0", updateComponents: { surfaceId: "s", components: [{ id: "gl", component: "Text", text: "Go" }] } } } });
  m.sync(s);
  assert.match(m.el.textContent, /Go/);
});

test("deleteSurface hides it", () => {
  const { st, s } = surfaceOf(approve);
  const m = R.mount(fakeDoc(), s, {});
  M.apply(st, { seq: 2, turn: "t", type: "ui.surface", payload: { message: { version: "1.0", deleteSurface: { surfaceId: "s" } } } });
  m.sync(s);
  assert.equal(m.el.hidden, true);
});

test("hostile fixtures are inert: script text, 100k chars, 5000 nodes, cycles, deep nesting", () => {
  const doc = fakeDoc();
  const comps = [{ id: "root", component: "Column", children: Array.from({ length: 5000 }, (_, i) => `n${i}`) }];
  for (let i = 0; i < 5000; i++) comps.push({ id: `n${i}`, component: "Text", text: i === 0 ? "<script>alert(1)</script><img src=x onerror=alert(1)>" : i === 1 ? "x".repeat(100000) : "javascript:alert(1)" });
  const m = R.mount(doc, surfaceOf(comps).s, {});
  assert.ok(doc.made.length <= R.MAX_NODES + 5, `${doc.made.length} nodes`);
  assert.ok(m.el.textContent.includes("<script>alert(1)</script>"), "shown as text");
  assert.ok(m.el.textContent.length < 100000, "long text is clipped");
  assert.ok(!doc.made.some((n) => ["script", "img", "a", "iframe"].includes(n.tag)));
  // A cycle and a very deep chain stop at the depth cap.
  const chain = [{ id: "root", component: "Card", child: "root" }];
  const d2 = fakeDoc();
  assert.doesNotThrow(() => R.mount(d2, surfaceOf(chain).s, {}));
  assert.ok(d2.made.length < 40);
});

test("a data model with __proto__ keys and context with __proto__ never reaches the action", () => {
  const model = JSON.parse('{"__proto__":{"evil":1},"draft":"ok"}');
  const comps = approve.map((c) => (c.id === "go" ? { ...c, action: { event: { name: "a.b", context: JSON.parse('{"__proto__":"x","k":{"@path":"/draft"},"p":{"@path":"/__proto__/evil"}}') } } } : c));
  const sent = [];
  const m = R.mount(fakeDoc(), surfaceOf(comps, model).s, { onAction: (e) => sent.push(e) });
  find(m.el, "button")[0].fire("click");
  const ctx = sent[0].action.context;
  assert.deepEqual(Object.keys(ctx), ["k", "p"]);
  assert.equal(ctx.k, "ok");
  assert.equal(ctx.p, null);
  assert.equal({}.evil, undefined);
});

test("a rendering failure becomes an error card, not an exception", () => {
  const { s } = surfaceOf([{ id: "root", component: "Text", text: "x" }]);
  const doc = fakeDoc();
  const real = doc.createElement;
  doc.createElement = (t) => { if (t === "p") throw new Error("boom"); return real(t); };
  const m = R.mount(doc, s, {});
  assert.match(m.el.textContent, /could not be shown/);
});

test("timeline: a surface sits after its anchor message, replays from saved events, ignores non-ui events", () => {
  const doc = fakeDoc();
  const log = doc.createElement("div");
  const empty = doc.createElement("p");
  const a = doc.createElement("div");
  const b = doc.createElement("div");
  for (const n of [empty, a, b]) log.appendChild(n);
  const rootOf = (id) => ({ m1: a, m2: b })[id] || null;
  const tl = R.timeline(doc, {});
  assert.equal(tl.ingest({ seq: 1, type: "assistant.delta", payload: {} }, "m1"), false);
  const create = (seq) => ({ seq, turn: "t", type: "ui.surface", payload: { surfaceId: "s", provenance: "system", message: { version: "1.0", createSurface: { surfaceId: "s", components: [{ id: "root", component: "Text", text: "card" }] } } } });
  assert.equal(tl.ingest(create(2), "m1"), true);
  assert.equal(tl.ingest(create(2), "m1"), false, "replayed");
  tl.place(log, empty, rootOf);
  assert.deepEqual(log.children.map((c) => c.dataset.a2ui || c.tag), ["p", "div", "1", "div"].map((x, i) => (i === 2 ? "1" : x)));
  assert.equal(log.children[1], a);
  assert.equal(log.children[3], b);
  // refused action note
  assert.equal(tl.ingest({ seq: 3, type: "ui.action", payload: { surfaceId: "s", name: "hil.approve", outcome: "stale" } }, "m2"), true);
  tl.place(log, empty, rootOf);
  assert.match(log.children[2].textContent, /hil\.approve: stale/);
  // reopen: a fresh timeline restored from what was saved
  const saved = JSON.parse(JSON.stringify(tl.dump("c1")));
  const tl2 = R.timeline(doc, {});
  tl2.restore(saved, "c1");
  const log2 = doc.createElement("div");
  const e2 = doc.createElement("p");
  const a2 = doc.createElement("div");
  [e2, a2].forEach((n) => log2.appendChild(n));
  tl2.place(log2, e2, () => a2);
  assert.equal(log2.children[2].dataset.a2ui, "1");
  assert.match(log2.textContent, /card/);
  const tl3 = R.timeline(doc, {});
  tl3.restore(saved, "other-conversation");
  assert.equal(tl3.has(), false, "another conversation's surfaces are not restored");
});

test("no markup path: none of the A2UI files uses innerHTML, insertAdjacentHTML, outerHTML, document.write or eval", () => {
  for (const f of ["a2ui_model.js", "a2ui_render.js", "a2ui_timeline.js"]) {
    const src = readFileSync(join(HERE, f), "utf8");
    assert.doesNotMatch(src, /innerHTML|insertAdjacentHTML|outerHTML|document\.write|\beval\(|new Function|createContextualFragment|\.href\s*=|setAttribute\(\s*["']href/, f);
  }
});

test("panel wiring: sidepanel.html loads the model and renderer before the chat, and the stylesheet", () => {
  const html = readFileSync(join(HERE, "sidepanel.html"), "utf8");
  const at = (s) => html.indexOf(s);
  assert.ok(at("a2ui_model.js") > 0 && at("a2ui_model.js") < at("a2ui_render.js") && at("a2ui_render.js") < at("a2ui_timeline.js") && at("a2ui_timeline.js") < at('"sidepanel_chat.js"'));
  assert.ok(at("a2ui.css") > 0);
  const chat = readFileSync(join(HERE, "sidepanel_chat.js"), "utf8");
  assert.match(chat, /ui\.surface/);
});

const byClass = (n, c) => all(n).filter((x) => String(x.className).split(" ").includes(c));

test("a11y: each surface is a named region that is not itself live", () => {
  const { st, s } = surfaceOf(approve, { title: "Send DM?" }, { provenance: "system" });
  const m = R.mount(fakeDoc(), s, {});
  assert.equal(m.el.getAttribute("role"), "region");
  assert.equal(m.el.getAttribute("aria-live"), "off");
  assert.equal(m.el.getAttribute("aria-label"), "Card from monoagent: Send DM?");
  M.apply(st, { seq: 2, turn: "t", type: "ui.surface", payload: { message: { version: "1.0", updateDataModel: { surfaceId: "s", path: "/title", value: "Sent" } } } });
  m.sync(s);
  assert.equal(m.el.getAttribute("aria-label"), "Card from monoagent: Sent");
});

test("a11y: one press marks every button of the surface, with the reason in a status line", () => {
  const comps = [
    { id: "root", component: "Row", children: ["a", "b"] },
    { id: "al", component: "Text", text: "Approve" }, { id: "bl", component: "Text", text: "Reject" },
    { id: "a", component: "Button", child: "al", action: { event: { name: "x.a" } } },
    { id: "b", component: "Button", child: "bl", action: { event: { name: "x.b" } } },
  ];
  const timers = [];
  const sent = [];
  const m = R.mount(fakeDoc(), surfaceOf(comps).s, { onAction: (e) => sent.push(e), setTimeout: (fn) => timers.push(fn), clearTimeout() {} });
  const [a, b] = find(m.el, "button");
  const status = byClass(m.el, "a2ui-status")[0];
  assert.equal(status.getAttribute("role"), "status");
  assert.equal(status.textContent, "");
  a.fire("click");
  b.fire("click");
  assert.equal(sent.length, 1, "reject after approve does nothing");
  assert.equal(a.disabled, false, "aria-disabled only: the button keeps focus");
  assert.equal(b.getAttribute("aria-disabled"), "true");
  assert.match(status.textContent, /Sent\./);
  assert.equal(b.getAttribute("aria-describedby"), status.id);
  timers.at(-1)();
  assert.equal(status.textContent, "");
  assert.equal(b.getAttribute("aria-describedby"), null);
});

test("a11y: choices are a fieldset with a legend, described by the question above; labels have a fallback", () => {
  const { s } = surfaceOf([
    { id: "root", component: "Column", children: ["q", "p", "f"] },
    { id: "q", component: "Text", text: "Which channel?" },
    { id: "p", component: "ChoicePicker", options: [{ label: "A", value: "a" }], value: [], variant: "mutuallyExclusive" },
    { id: "f", component: "TextField", label: "", value: "", placeholder: "Note" },
  ]);
  const m = R.mount(fakeDoc(), s, {});
  const set = find(m.el, "fieldset")[0];
  assert.equal(find(set, "legend")[0].textContent, "Choose one");
  const q = byClass(m.el, "a2ui-text")[0];
  assert.equal(set.getAttribute("aria-describedby"), q.id);
  assert.equal(find(m.el, "textarea").at(-1).getAttribute("aria-label"), "Note");
});

test("a11y: progress has a name and values; badge tones carry a word and a mark", () => {
  const { s } = surfaceOf([
    { id: "root", component: "Column", children: ["g", "b", "n"] },
    { id: "g", component: "Progress", value: 2, max: 8 },
    { id: "b", component: "Badge", text: "late", tone: "danger" },
    { id: "n", component: "Badge", text: "idle" },
  ]);
  const m = R.mount(fakeDoc(), s, {});
  const bar = all(m.el).find((n) => n.attrs && n.attrs.role === "progressbar");
  assert.deepEqual([bar.attrs["aria-label"], bar.attrs["aria-valuenow"], bar.attrs["aria-valuemax"], bar.attrs["aria-valuetext"]], ["Progress", "2", "8", "2 of 8"]);
  const [danger, plainBadge] = byClass(m.el, "a2ui-badge");
  assert.equal(byClass(danger, "sr-only")[0].textContent, "Problem: ");
  assert.equal(byClass(danger, "a2ui-mark")[0].attrs["aria-hidden"], "true");
  assert.equal(plainBadge.textContent, "idle");
});

test("filterable choices hide what does not match and say when nothing does", () => {
  const { s } = surfaceOf([{ id: "root", component: "ChoicePicker", filterable: true, value: [], options: [{ label: "Email", value: "e" }, { label: "Phone", value: "p" }] }]);
  const m = R.mount(fakeDoc(), s, {});
  const filter = find(m.el, "input")[0];
  assert.equal(filter.getAttribute("aria-label"), "Filter options");
  const rows = byClass(m.el, "a2ui-choice");
  filter.value = "mai";
  filter.fire("input");
  assert.deepEqual(rows.map((r) => r.hidden), [false, true]);
  filter.value = "zzz";
  filter.fire("input");
  assert.match(byClass(m.el, "a2ui-nomatch")[0].textContent, /No options match/);
});

test("a rebuild keeps the keyboard on the same control", () => {
  const { st, s } = surfaceOf(approve);
  const doc = fakeDoc();
  const real = doc.createElement;
  doc.createElement = (t) => { const n = real(t); n.focus = () => (doc.activeElement = n); return n; };
  const m = R.mount(doc, s, {});
  const btn = find(m.el, "button")[0];
  btn.focus();
  M.apply(st, { seq: 2, turn: "t", type: "ui.surface", payload: { message: { version: "1.0", updateComponents: { surfaceId: "s", components: [{ id: "gl", component: "Text", text: "Go" }] } } } });
  m.sync(s);
  assert.notEqual(doc.activeElement, btn);
  assert.equal(doc.activeElement.dataset.cid, "go");
});

test("a11y: the note under a surface is a status region that exists before it has text", () => {
  const doc = fakeDoc();
  const log = doc.createElement("div");
  const empty = doc.createElement("p");
  log.appendChild(empty);
  const tl = R.timeline(doc, {});
  tl.ingest({ seq: 1, turn: "t", type: "ui.surface", payload: { message: { version: "1.0", createSurface: { surfaceId: "s" } } } }, "");
  tl.place(log, empty, () => null);
  const note = byClass(log, "a2ui-note")[0];
  assert.deepEqual([note.attrs.role, note.hidden, note.textContent], ["status", false, ""]);
  tl.setNote("s", "x.a: stale");
  tl.place(log, empty, () => null);
  assert.equal(note.textContent, "x.a: stale");
});

test("a TextField card renders on a document whose label.control is read-only", () => {
  const m = R.mount(fakeDoc(), surfaceOf(approve, { title: "T", draft: "d" }).s, {});
  assert.doesNotMatch(m.el.textContent, /could not be shown/);
  assert.equal(find(m.el, "textarea").length, 1);
});

// Repeated child references make a naive tree walk exponential (9 components, each listing the
// next 40 times). The walk is synchronous, so no in-process timer could stop it: it runs in a
// child process with a deadline, and an unbounded walk fails by being killed.
const REPEATS = `
  import { loadExtensionScripts } from ${JSON.stringify(new URL("./test_helpers.mjs", import.meta.url).href)};
  const { MonoA2UIModel: M, MonoA2UIRender: R } = loadExtensionScripts(["a2ui_model.js", "a2ui_render.js", "a2ui_timeline.js"]);
  let made = 0;
  const node = () => ({ children: [], dataset: {}, style: {}, attrs: {}, textContent: "", appendChild(c) { this.children.push(c); return c; },
    replaceChildren(...c) { this.children = c; }, setAttribute(k, v) { this.attrs[k] = v; }, removeAttribute() {}, getAttribute() { return null; }, addEventListener() {} });
  const doc = { createElement: () => (made++, node()), createDocumentFragment: node };
  const chain = (leaf, width = 40) => {
    const list = [];
    for (let i = 0; i < 9; i++) list.push({ id: i ? "c" + i : "root", component: "Column", children: Array(width).fill("c" + (i + 1)) });
    return list.concat(leaf);
  };
  const fixtures = {
    emptyText: chain([{ id: "c9", component: "Text", text: "" }]),
    missingLeaf: chain([]),
    unknownLeaf: chain([{ id: "c9", component: "Nope" }]),
    cycle: chain([{ id: "c9", component: "Column", children: Array(40).fill("root") }]),
    manyMissing: [{ id: "root", component: "Column", children: Array(100000).fill("gone") }],
    twoWide: [{ id: "root", component: "Column", children: Array(100).fill("t") }, { id: "t", component: "Text", text: "once" }],
  };
  const out = {};
  for (const [name, components] of Object.entries(fixtures)) {
    const st = M.newStore();
    M.apply(st, { seq: 1, turn: "t", type: "ui.surface", payload: { message: { version: "1.0", createSurface: { surfaceId: "s", components } } } });
    const s = st.surfaces.get("s");
    let lookups = 0;
    s.components = new Proxy(s.components, { getOwnPropertyDescriptor: (t, k) => (lookups++, Reflect.getOwnPropertyDescriptor(t, k)) });
    made = 0;
    const m = R.mount(doc, s, {});
    s.dataVersion++;
    m.sync(s);
    const texts = [];
    const walk = (n) => { if (n.textContent) texts.push(n.textContent); (n.children || []).forEach(walk); };
    walk(m.el);
    out[name] = { made, lookups, once: texts.filter((t) => t === "once").length };
  }
  process.stdout.write(JSON.stringify(out));
`;

test("repeated, missing and cyclic child references render in bounded time and work", () => {
  const run = spawnSync(process.execPath, ["--input-type=module", "-e", REPEATS], { encoding: "utf8", timeout: 10000, killSignal: "SIGKILL" });
  assert.equal(run.signal, null, "the walk did not finish by the deadline");
  assert.equal(run.status, 0, run.stderr);
  const out = JSON.parse(run.stdout);
  for (const [name, r] of Object.entries(out)) {
    assert.ok(r.made <= 20, `${name}: ${r.made} elements for at most 10 distinct components`);
    // One bounded draw and two bounded region-name walks.
    assert.ok(r.lookups <= 3 * R.MAX_NODES, `${name}: ${r.lookups} component lookups`);
  }
  assert.equal(out.twoWide.once, 1, "a component referenced 100 times is drawn once");
});

test("the fold reads one message up to the component cap", () => {
  const components = Array.from({ length: 100000 }, (_, i) => ({ id: `n${i}`, component: "Divider" }));
  let read = 0;
  const counted = components.map((c) => ({ component: c.component, get id() { read++; return c.id; } }));
  const st = M.newStore();
  M.apply(st, { seq: 1, turn: "t", type: "ui.surface", payload: { message: { version: "1.0", createSurface: { surfaceId: "s" } } } });
  const msg = { version: "1.0", updateComponents: { surfaceId: "s", components: counted } };
  msg.toJSON = () => "fixed"; // the replay fingerprint is not what is counted here
  M.apply(st, { seq: 2, turn: "t", type: "ui.surface", payload: { message: msg } });
  assert.equal(Object.keys(st.surfaces.get("s").components).length, 500);
  assert.ok(read < 5000, `${read} id reads`);
});

// The fake document lays nothing out: a text box here is `size.width`
// characters wide, 20px a line.
function layOut(doc, size) {
  const make = doc.createElement;
  doc.createElement = (tag) => {
    const n = make(tag);
    if (tag === "textarea") {
      const lines = () => String(n.value).split("\n").reduce((c, l) => c + Math.max(1, Math.ceil(l.length / size.width)), 0);
      Object.defineProperties(n, {
        scrollHeight: { get: () => (size.width ? Math.max(lines(), n.rows || 1) * 20 : 0) },
        offsetHeight: { get: () => 22 },
        clientHeight: { get: () => 20 },
        clientWidth: { get: () => size.width },
      });
    }
    return n;
  };
  return doc;
}
function observed() {
  const seen = { all: [], frames: [] };
  seen.opts = {
    requestAnimationFrame: (fn) => seen.frames.push(fn),
    ResizeObserver: class {
      constructor(callback) { this.callback = callback; this.targets = []; seen.all.push(this); }
      observe(t) { this.targets.push(t); }
      disconnect() { this.targets = []; }
    },
  };
  /** report delivers what a browser would: the observed boxes changed size. */
  seen.report = () => {
    for (const ob of seen.all) if (ob.targets.length) ob.callback(ob.targets.map((target) => ({ target })));
    seen.frames.splice(0).forEach((f) => f());
  };
  return seen;
}
const tall = (lines) => `${lines * 20 + 3}px`; // the lines, the 2px border and 1px for rounding
const textCard = (variant) => [{ id: "root", component: "TextField", label: "Message", value: { "@path": "/v" }, ...(variant ? { variant } : {}) }];

test("a text field holds its whole value: nothing a button would send is clipped", () => {
  const value = `${"word ".repeat(4000)}THE END: $499`;
  for (const variant of ["longText", "shortText", undefined]) {
    const { s } = surfaceOf(textCard(variant), { v: value });
    const m = R.mount(fakeDoc(), s, {});
    assert.equal(find(m.el, "textarea")[0].value, value, String(variant));
    assert.equal(find(m.el, "input").length, 0);
  }
});

test("a text box grows on first layout, edit, data update, width change and font load", () => {
  const size = { width: 0 }; // drawn before it is in the panel
  const doc = layOut(fakeDoc(), size);
  const fontListeners = new Set();
  doc.fonts = { addEventListener: (_, fn) => fontListeners.add(fn), removeEventListener: (_, fn) => fontListeners.delete(fn) };
  const seen = observed();
  const { st, s } = surfaceOf(textCard("longText"), { v: "x".repeat(200) });
  const m = R.mount(doc, s, seen.opts);
  const box = find(m.el, "textarea")[0];
  seen.report();
  assert.equal(box.style.height, "auto", "not shown yet");
  size.width = 50;
  seen.report();
  assert.equal(box.style.height, tall(4), "first layout");
  box.value = "y".repeat(500);
  box.fire("input");
  assert.equal(box.style.height, tall(10), "edit");
  M.apply(st, { seq: 2, turn: "t", type: "ui.surface", payload: { surfaceId: "s", message: { version: "1.0", updateDataModel: { surfaceId: "s", path: "/v", value: "z".repeat(1000) } } } });
  m.sync(st.surfaces.get("s"));
  assert.equal(box.style.height, tall(20), "data update");
  size.width = 20;
  seen.report();
  assert.equal(box.style.height, tall(50), "width change");
  box.style.height = "1px";
  seen.report();
  assert.equal(box.style.height, "1px", "the same width again (our own resize reports it) measures nothing");
  for (const fn of fontListeners) fn();
  assert.equal(box.style.height, tall(50), "font load");
  m.destroy();
  assert.equal(fontListeners.size, 0);
  assert.equal(seen.all.every((ob) => !ob.targets.length), true);
});

test("a rebuilt card watches its new boxes only", () => {
  const seen = observed();
  const { st, s } = surfaceOf(textCard("longText"), { v: "a" });
  const m = R.mount(layOut(fakeDoc(), { width: 50 }), s, seen.opts);
  M.apply(st, { seq: 2, turn: "t", type: "ui.surface", payload: { surfaceId: "s", message: { version: "1.0", updateComponents: { surfaceId: "s", components: textCard("shortText") } } } });
  m.sync(st.surfaces.get("s"));
  assert.deepEqual(seen.all.flatMap((ob) => ob.targets), find(m.el, "textarea"));
});

test("a one-line field takes no line break from the keyboard; number fields stay inputs", () => {
  const enter = (variant) => {
    const { s } = surfaceOf(textCard(variant), { v: "a" });
    const box = find(R.mount(fakeDoc(), s, {}).el, "textarea")[0];
    let stopped = false;
    (box.listeners.keydown || []).forEach((fn) => fn({ key: "Enter", preventDefault: () => (stopped = true) }));
    return [box.rows, stopped];
  };
  assert.deepEqual(enter("shortText"), [1, true]);
  assert.deepEqual(enter("longText"), [3, false]);
  const { s } = surfaceOf(textCard("number"), { v: 5 });
  const m = R.mount(fakeDoc(), s, {});
  assert.equal(find(m.el, "textarea").length, 0);
  assert.equal(find(m.el, "input")[0].type, "number");
});

test("no style rule hides or cuts a card's text", () => {
  const css = readFileSync(join(HERE, "a2ui.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const rules = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].map((r) => [r[1].trim(), r[2]]);
  assert.ok(rules.length > 30);
  // The progress bar holds no text.
  for (const [selector, body] of rules) {
    if (selector === ".a2ui-bar") continue;
    assert.doesNotMatch(body, /line-clamp|text-overflow|nowrap|max-height|overflow(-[xy])?:\s*(hidden|clip)|resize:\s*(vertical|both)/, selector);
  }
});
