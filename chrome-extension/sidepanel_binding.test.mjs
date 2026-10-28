// Tests for the side panel's "Automations in this browser" checklist.
// `node --test 'chrome-extension/*.test.mjs'`.

import test from "node:test";
import assert from "node:assert/strict";
import { loadExtensionScripts } from "./test_helpers.mjs";

const { MonoBrowserBinding: B, MonoPanelBinding: P } = loadExtensionScripts(["browser_binding.js", "sidepanel_binding.js"]);

// Just enough DOM for the checklist: elements with children, a checkbox
// query, click listeners and a textContent that clears.
function el(tag = "div") {
  const node = {
    tag, children: [], dataset: {}, listeners: {}, value: "", checked: false, className: "", type: "",
    appendChild(c) { this.children.push(c); return c; },
    addEventListener(name, fn) { this.listeners[name] = fn; },
    querySelectorAll() {
      const out = [];
      const walk = (n) => { for (const c of n.children) { if (c.tag === "input" && c.type === "checkbox") out.push(c); walk(c); } };
      walk(this);
      return out;
    },
    click() { return this.listeners.click(); },
  };
  let text = "";
  Object.defineProperty(node, "textContent", {
    get: () => text,
    set: (v) => { text = v; node.children.length = 0; },
  });
  return node;
}

function fakeDoc() {
  const ids = {};
  for (const id of ["binding-profile", "binding-label", "binding-save", "binding-msg"]) ids[id] = el(id === "binding-label" ? "input" : "div");
  return {
    ids,
    getElementById: (id) => ids[id],
    createElement: (tag) => el(tag),
  };
}

function fakeStorage(seed = {}) {
  const data = Object.assign({}, seed);
  return {
    data,
    get: async (keys) => Object.fromEntries((Array.isArray(keys) ? keys : [keys]).filter((k) => k in data).map((k) => [k, data[k]])),
    set: async (v) => Object.assign(data, v),
  };
}

const PROFILES = [{ id: "p-work", name: "Work" }, { id: "p-home", name: "Personal" }, { id: "p-solo", name: "Solo" }];

function boxes(doc) {
  return doc.ids["binding-profile"].querySelectorAll();
}

async function mount(seed) {
  const doc = fakeDoc();
  const storage = fakeStorage(seed);
  const panel = P.install({ doc, storage, userAgent: "", profiles: () => PROFILES });
  await panel.refresh();
  return { doc, storage, panel };
}

test("ticks every profile this browser runs", async () => {
  const { doc } = await mount({ [B.PROFILE_IDS_KEY]: ["p-work", "p-solo"], [B.PROFILE_KEY]: "p-work" });
  assert.deepEqual(boxes(doc).map((b) => [b.value, b.checked]), [["p-work", true], ["p-home", false], ["p-solo", true]]);
});

test("a legacy single binding shows as one ticked profile", async () => {
  const { doc } = await mount({ [B.PROFILE_KEY]: "p-home" });
  assert.deepEqual(boxes(doc).filter((b) => b.checked).map((b) => b.value), ["p-home"]);
});

test("Save stores every ticked profile, first one in the legacy field", async () => {
  const { doc, storage } = await mount({});
  const [work, home] = boxes(doc);
  work.checked = true;
  home.checked = true;
  await doc.ids["binding-save"].click();
  assert.deepEqual(storage.data[B.PROFILE_IDS_KEY], ["p-work", "p-home"]);
  assert.equal(storage.data[B.PROFILE_KEY], "p-work");
  assert.equal(doc.ids["binding-msg"].dataset.tone, "ok");
});

test("Save with nothing ticked makes a default browser", async () => {
  const { doc, storage } = await mount({ [B.PROFILE_IDS_KEY]: ["p-work"], [B.PROFILE_KEY]: "p-work" });
  boxes(doc)[0].checked = false;
  await doc.ids["binding-save"].click();
  assert.deepEqual(storage.data[B.PROFILE_IDS_KEY], []);
  assert.equal(storage.data[B.PROFILE_KEY], "");
  assert.match(doc.ids["binding-msg"].textContent, /default browser/);
});

test("redrawing for a new profile list keeps unsaved ticks", async () => {
  const { doc, panel } = await mount({});
  boxes(doc)[1].checked = true;
  panel.redraw();
  assert.deepEqual(boxes(doc).filter((b) => b.checked).map((b) => b.value), ["p-home"]);
});

test("a refresh drops unsaved ticks: storage is the truth", async () => {
  const { doc, panel } = await mount({ [B.PROFILE_IDS_KEY]: ["p-work"] });
  boxes(doc)[2].checked = true;
  await panel.refresh();
  assert.deepEqual(boxes(doc).filter((b) => b.checked).map((b) => b.value), ["p-work"]);
});
