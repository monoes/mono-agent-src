// Panel-only runtime messages are answered only to this extension's own pages.
// A content script runs on every site, so a compromised page's message must
// not open a file:// tab (saved_open), probe the backend (ask_ready), list the
// highlighted pages, or start/commit a capture.
// `CHROME_PATH=/nonexistent node --test chrome-extension/panel_only_guard.test.mjs`

import test from "node:test";
import assert from "node:assert/strict";
import { loadExtensionScripts } from "./test_helpers.mjs";

const PANEL = { id: "ext", url: "chrome-extension://ext/sidepanel.html" };
const PAGE = { id: "ext", url: "https://evil.example/", tab: { id: 5 } };
const OTHER = { id: "other", url: "chrome-extension://other/x.html" };

function fakeStorage() {
  const data = {};
  return {
    data,
    get: async (key) => (key in data ? { [key]: data[key] } : {}),
    set: async (values) => Object.assign(data, values),
    remove: async (key) => delete data[key],
  };
}

function chromeStub(reached) {
  const listeners = [];
  return {
    listeners,
    runtime: {
      id: "ext",
      getURL: (p) => `chrome-extension://ext/${p}`,
      onMessage: { addListener: (fn) => listeners.push(fn) },
      sendMessage: () => Promise.resolve(),
    },
    tabs: {
      onUpdated: { addListener() {} },
      onActivated: { addListener() {} },
      onRemoved: { addListener() {} },
      query: async () => [],
      get: async () => ({ id: 7, url: "https://paper.test/" }),
      sendMessage: async () => ({ ok: true }),
      create: async (spec) => {
        reached.push(["tabs.create", spec.url]);
        return { id: 8 };
      },
    },
    action: { setBadgeText: async () => {}, setBadgeBackgroundColor: async () => {}, setTitle: async () => {} },
    commands: { onCommand: { addListener() {} } },
    storage: { local: fakeStorage() },
  };
}

test("recall: saved_open, ask_ready and highlight_pages refuse a web page and other extensions", async () => {
  const reached = [];
  const chrome = chromeStub(reached);
  const sandbox = loadExtensionScripts(["ask.js", "saved.js", "highlights.js", "recall_bridge.js"], { chrome });
  sandbox.MonoRecall.install({ send() {}, isConnected: () => true, storage: chrome.storage.local });
  for (const type of ["saved_open", "ask_ready", "highlight_pages"]) {
    for (const sender of [PAGE, OTHER, {}, undefined]) {
      const handled = chrome.listeners.map((fn) => fn({ type, path: "/etc/passwd" }, sender, () => assert.fail("must not respond")));
      assert.ok(handled.every((h) => h === false), `${type} from ${JSON.stringify(sender)}`);
    }
  }
  await new Promise((r) => setTimeout(r, 5));
  assert.deepEqual(reached, []);
});

test("recall: saved_open from the side panel still opens the archived copy", async () => {
  const reached = [];
  const chrome = chromeStub(reached);
  const sandbox = loadExtensionScripts(["ask.js", "saved.js", "highlights.js", "recall_bridge.js"], { chrome });
  sandbox.MonoRecall.install({ send() {}, isConnected: () => true, storage: chrome.storage.local });
  const reply = await new Promise((resolve) => {
    for (const fn of chrome.listeners) if (fn({ type: "saved_open", path: "/tmp/a.html" }, PANEL, resolve) === true) return;
  });
  assert.equal(reply.ok, true);
  assert.deepEqual(reached, [["tabs.create", "file:///tmp/a.html"]]);
});

test("recall: a page's highlight messages are still answered", () => {
  const chrome = chromeStub([]);
  const sandbox = loadExtensionScripts(["ask.js", "saved.js", "highlights.js", "recall_bridge.js"], { chrome });
  sandbox.MonoRecall.install({ send() {}, isConnected: () => true, storage: chrome.storage.local });
  const handled = chrome.listeners.map((fn) => fn({ type: "highlight_list", url: "https://x.test/" }, PAGE, () => {}));
  assert.ok(handled.includes(true));
});

test("capture_actions: capture messages refuse a web page and other extensions", async () => {
  const chrome = chromeStub([]);
  const sandbox = loadExtensionScripts(
    ["capture_meta.js", "capture.js", "capture_outbox.js", "capture_form.js", "capture_profile.js", "capture_batch.js", "capture_queue.js", "capture_actions.js"],
    { chrome },
  );
  sandbox.MonoCaptureActions.install({ storage: chrome.storage.local, isConnected: () => true, send: () => true, capture: async () => ({}) });
  for (const type of ["capture_begin", "capture_commit", "capture_discard", "capture_profile_set"]) {
    for (const sender of [PAGE, OTHER, {}, undefined]) {
      const handled = chrome.listeners.map((fn) => fn({ type }, sender, () => assert.fail("must not respond")));
      assert.ok(handled.every((h) => h === false), `${type} from ${JSON.stringify(sender)}`);
    }
  }
  const fromPanel = chrome.listeners.map((fn) => fn({ type: "capture_discard" }, PANEL, () => {}));
  assert.ok(fromPanel.includes(true), "the side panel is still answered");
});
