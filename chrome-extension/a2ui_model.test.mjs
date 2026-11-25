// The A2UI surface fold (a2ui_model.js): spec semantics, replay, hostile input.
// `CHROME_PATH=/nonexistent node --test chrome-extension/a2ui_model.test.mjs`

import test from "node:test";
import assert from "node:assert/strict";
import { loadExtensionScripts } from "./test_helpers.mjs";

const { MonoA2UIModel: M } = loadExtensionScripts(["a2ui_model.js"]);
const ev = (seq, message, extra = {}) => ({ seq, turn: "t1", type: "ui.surface", payload: { surfaceId: "s", provenance: "agent", message, ...extra } });
const create = (body = {}) => ({ version: "1.0", createSurface: { surfaceId: "s", ...body } });
const comps = (list) => ({ version: "1.0", updateComponents: { surfaceId: "s", components: list } });
const data = (path, value) => ({ version: "1.0", updateDataModel: { surfaceId: "s", path, value } });
const plain = (v) => JSON.parse(JSON.stringify(v));

test("createSurface, updateComponents merge by id, updateDataModel, deleteSurface", () => {
  const st = M.newStore();
  M.apply(st, ev(1, create({ components: [{ id: "root", component: "Text", text: "a" }], dataModel: { n: 1 } })));
  const s = st.surfaces.get("s");
  assert.equal(s.components.root.text, "a");
  M.apply(st, ev(2, comps([{ id: "root", component: "Text", text: "b" }, { id: "x", component: "Divider" }])));
  assert.equal(s.components.root.text, "b");
  assert.ok(s.components.x);
  M.apply(st, ev(3, data("/draft/text", "hi")));
  assert.deepEqual(plain(s.dataModel), { n: 1, draft: { text: "hi" } });
  M.apply(st, ev(4, data("/n", null)));
  assert.deepEqual(plain(s.dataModel), { draft: { text: "hi" } });
  M.apply(st, ev(5, data("/", { z: 1 })));
  assert.deepEqual(plain(s.dataModel), { z: 1 });
  M.apply(st, ev(6, { version: "1.0", deleteSurface: { surfaceId: "s" } }));
  assert.equal(s.deleted, true);
  assert.equal(M.apply(st, ev(7, comps([{ id: "root", component: "Text", text: "late" }]))), null);
});

test("a replayed (turn, seq) changes nothing; the same seq in another turn applies", () => {
  const st = M.newStore();
  M.apply(st, ev(1, create({ dataModel: { n: 1 } })));
  M.apply(st, ev(2, data("/n", 5)));
  const s = st.surfaces.get("s");
  assert.equal(M.apply(st, ev(1, create({ dataModel: { n: 1 } }))), null, "replayed create does not reset");
  assert.equal(M.apply(st, ev(2, data("/n", 5))), null);
  assert.equal(s.dataModel.n, 5);
  assert.ok(M.apply(st, { ...ev(2, data("/n", 9)), turn: "t2" }));
  assert.equal(s.dataModel.n, 9);
});

test("messages for an unknown surface, junk kinds and bad payloads never throw", () => {
  const st = M.newStore();
  for (const bad of [null, {}, { seq: 1 }, ev(1, null), ev(2, "not json"), ev(3, { version: "1.0", weird: {} }), ev(4, comps([1, null, { id: 5 }])), ev(5, data("/a", 1))]) {
    assert.doesNotThrow(() => M.apply(st, bad));
  }
  assert.equal(st.surfaces.size, 0);
  assert.ok(M.apply(st, ev(6, JSON.stringify(create()))), "the message may arrive as a JSON string");
});

test("unknown component types are kept for the renderer to show as placeholders", () => {
  const st = M.newStore();
  M.apply(st, ev(1, create({ components: [{ id: "root", component: "Iframe", src: "x" }] })));
  assert.equal(st.surfaces.get("s").components.root.component, "Iframe");
});

test("prototype pollution: __proto__ keys in the model and pointer paths are inert", () => {
  const st = M.newStore();
  const hostile = JSON.parse('{"__proto__":{"polluted":1},"a":{"constructor":{"prototype":{"polluted":2}},"ok":1}}');
  M.apply(st, ev(1, create({ dataModel: hostile })));
  const s = st.surfaces.get("s");
  M.apply(st, ev(2, data("/__proto__/x", 1)));
  M.apply(st, ev(3, data("/a/constructor/prototype/y", 1)));
  M.apply(st, ev(4, data("/a/__proto__/polluted", 1)));
  assert.equal({}.polluted, undefined);
  assert.equal({}.x, undefined);
  assert.equal({}.y, undefined);
  assert.equal(Object.getPrototypeOf(s.dataModel), null);
  assert.deepEqual(Object.keys(s.dataModel), ["a"]);
  assert.deepEqual(Object.keys(s.dataModel.a), ["ok"]);
  assert.equal(M.getAt(s.dataModel, "/__proto__"), undefined);
  assert.equal(M.getAt(s.dataModel, "/a/constructor"), undefined);
  assert.equal(M.pointer("/a/__proto__"), null);
  assert.equal(M.getAt(s.dataModel, "/toString"), undefined, "inherited names are not own data");
  // A component id of __proto__ is refused too.
  M.apply(st, ev(5, comps([{ id: "__proto__", component: "Text", text: "x" }])));
  assert.equal(Object.keys(s.components).length, 0);
});

test("JSON Pointer escapes and arrays", () => {
  let m = M.setAt(null, "/a~1b/c~0d", 1);
  assert.equal(m["a/b"]["c~d"], 1);
  m = M.setAt(m, "/list", ["x", "y"]);
  m = M.setAt(m, "/list/1", "z");
  assert.deepEqual(plain(M.getAt(m, "/list")), ["x", "z"]);
  m = M.setAt(m, "/list/1", null);
  assert.deepEqual(plain(M.getAt(m, "/list")), ["x"]);
  assert.equal(M.pointer("no-slash"), null);
});

test("size bounds: depth, width and surface count", () => {
  let deep = "x";
  for (let i = 0; i < 100; i++) deep = { d: deep };
  const wide = Object.fromEntries(Array.from({ length: 2000 }, (_, i) => [`k${i}`, i]));
  const st = M.newStore();
  M.apply(st, ev(1, create({ dataModel: { deep, wide } })));
  assert.ok(Object.keys(st.surfaces.get("s").dataModel.wide).length <= 500);
  for (let i = 0; i < 40; i++) M.apply(st, { seq: 10 + i, turn: "t", type: "ui.surface", payload: { message: { version: "1.0", createSurface: { surfaceId: `n${i}` } } } });
  assert.ok(st.surfaces.size <= 20);
});

test("the same seq twice is applied once, even when one copy lacks its turn (a live frame against its replay)", () => {
  const st = M.newStore();
  const live = { seq: 4, type: "ui.surface", payload: { surfaceId: "s", message: create({ dataModel: { n: 1 } }) } };
  assert.ok(M.apply(st, live));
  M.apply(st, { seq: 5, turn: "t1", type: "ui.surface", payload: { message: data("/n", 7) } });
  assert.equal(M.apply(st, { ...live, turn: "t1" }), null, "the replay with a turn is the same event");
  assert.equal(st.surfaces.get("s").dataModel.n, 7, "the replay did not reset what came after");
  assert.equal(M.apply(st, live), null);
  assert.equal(M.apply(st, { seq: 4, turn: "t2", type: "ui.surface", payload: { surfaceId: "s", message: create({ dataModel: { n: 2 } }) } }) !== null, true, "another turn's seq 4 is a different event");
});

test("an event journaled before what the surface already shows does not take it back", () => {
  const st = M.newStore();
  const at = (turn, s, message) => ({ seq: 1, turn, at: `2026-11-25T20:00:0${s}.000Z`, type: "ui.surface", payload: { surfaceId: "s", message } });
  M.apply(st, at("live", 5, create({ dataModel: { n: 5 } })));
  assert.equal(M.apply(st, at("system-uisurface-old", 1, create({ dataModel: { n: 1 } }))), null);
  assert.equal(st.surfaces.get("s").dataModel.n, 5);
  assert.ok(M.apply(st, at("system-uisurface-new", 7, data("/n", 7))));
});

test("a full store makes room from deleted or settled surfaces, never from one with a button", () => {
  const st = M.newStore();
  let seq = 0;
  const put = (id, components) => M.apply(st, { seq: ++seq, turn: "t", type: "ui.surface", payload: { message: { version: "1.0", createSurface: { surfaceId: id, components } } } });
  const button = [{ id: "root", component: "Button" }];
  put("keep", button);
  for (let i = 0; i < 19; i++) put(`n${i}`, [{ id: "root", component: "Text", text: "settled" }]);
  assert.ok(put("new", button));
  assert.equal(st.surfaces.has("keep"), true);
  assert.equal(st.surfaces.has("n0"), false);
  assert.equal(st.surfaces.size, 20);
});
