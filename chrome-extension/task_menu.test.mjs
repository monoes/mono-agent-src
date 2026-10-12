// The ways into the task bridge besides a message: the MonoAgent menu's two
// task items and the add-task shortcut, and the manifest that declares them.
// `CHROME_PATH=/nonexistent node --test chrome-extension/task_menu.test.mjs`

import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import { fakeAsk, setupTasks, until } from "./task_harness.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const TAB = { id: 42, windowId: 7, url: "https://paper.example/a", title: "A paper" };
const click = (listeners, info, tab = TAB) => {
  for (const fn of listeners.menu) fn(info, tab);
};

test("Add selection as task reads the selection as the reader sees it, in the frame clicked", async () => {
  const ask = fakeAsk();
  const { listeners, record } = setupTasks({ ask, selection: "First line\nsecond line" });
  click(listeners, { menuItemId: "monoagent-tasks-selection", frameId: 3, selectionText: "First line second line" });
  await until(() => ask.calls.length === 1, "the menu's task");
  const p = ask.calls[0].params;
  assert.deepEqual([p.kind, p.text, p.url, p.title], ["selection", "First line\nsecond line", "https://paper.example/a", "A paper"]);
  assert.deepEqual(record.scripts[0], { name: "selectedText", target: { tabId: 42, frameIds: [3] } });
  await until(() => record.toasts.length === 1, "the toast");
  assert.equal(record.toasts[0].tabId, 42);
});

test("a page that refuses scripts falls back to the menu's own selection text", async () => {
  const ask = fakeAsk();
  const { listeners } = setupTasks({ ask, scriptFails: true });
  click(listeners, { menuItemId: "monoagent-tasks-selection", frameId: 0, selectionText: "flattened text" });
  await until(() => ask.calls.length === 1, "the menu's task");
  assert.equal(ask.calls[0].params.text, "flattened text");
});

test("Add page as task sends the page, reads no selection, and keeps no user-info or token", async () => {
  const ask = fakeAsk();
  const { listeners, record } = setupTasks({ ask, selection: "not to be read" });
  click(listeners, { menuItemId: "monoagent-tasks-page" }, Object.assign({}, TAB, { url: "https://u:secret@paper.example/a?session=xyz#top" }));
  await until(() => ask.calls.length === 1, "the page task");
  const p = ask.calls[0].params;
  assert.deepEqual([p.kind, p.text, p.url, p.title], ["page", "", "https://paper.example/a?session=REDACTED", "A paper"]);
  assert.equal(record.scripts.length, 0);
});

test("Add page as task on a page with no title of its own sends no title, only the address", async () => {
  const ask = fakeAsk();
  const { listeners } = setupTasks({ ask });
  click(listeners, { menuItemId: "monoagent-tasks-page" }, Object.assign({}, TAB, { url: "https://paper.example/a?session=xyz", title: "paper.example/a?session=xyz" }));
  await until(() => ask.calls.length === 1, "the page task");
  const p = ask.calls[0].params;
  assert.deepEqual([p.kind, p.url, p.title], ["page", "https://paper.example/a?session=REDACTED", ""]);
  assert.ok(!JSON.stringify(p).includes("xyz"), "the token was sent by way of the title");
});

test("the capture items, and a click with no tab, are not the task menu's", async () => {
  const { M } = setupTasks();
  assert.equal(await M.handleMenuClick({ menuItemId: "monoagent-capture-full" }, TAB), null);
  assert.equal(await M.handleMenuClick({ menuItemId: "monoagent-tasks-page" }, undefined), null);
});

test("the add-task shortcut opens the side panel before anything else, then adds the selection", async () => {
  const ask = fakeAsk();
  const { listeners, record, session, B } = setupTasks({ ask, selection: "Book the flights" });
  for (const fn of listeners.command) fn("add-task", TAB);
  assert.deepEqual(record.opened, [{ windowId: 7 }], "opened inside the shortcut's own handler, before any await");
  await until(() => ask.calls.length === 1, "the shortcut's task");
  assert.equal(ask.calls[0].params.text, "Book the flights");
  assert.equal(typeof session.data[B.FOCUS_KEY], "number", "the panel is asked to focus its task box");
});

test("with nothing selected the shortcut only opens the side panel", async () => {
  const ask = fakeAsk();
  const { listeners, record } = setupTasks({ ask, selection: "" });
  for (const fn of listeners.command) fn("add-task", TAB);
  assert.equal(record.opened.length, 1);
  await new Promise((r) => setTimeout(r, 30));
  assert.equal(ask.calls.length, 0);
});

test("a browser that will not open the panel gets a toast instead", async () => {
  const { listeners, record, chrome } = setupTasks({ selection: "" });
  chrome.sidePanel.open = () => Promise.reject(new Error("sidePanel.open() may only be called in response to a user gesture."));
  for (const fn of listeners.command) fn("add-task", TAB);
  await until(() => record.toasts.length === 1, "the fallback toast");
  assert.equal(record.toasts[0].text, "To type a task, open MonoAgent's side panel (its toolbar button)");
});

test("other shortcuts are not the task menu's", () => {
  const { listeners, record } = setupTasks();
  for (const fn of listeners.command) fn("capture-page", TAB);
  assert.equal(record.opened.length, 0);
});

test("the manifest declares the shortcut, a minor version up, and no new permission", () => {
  const manifest = JSON.parse(readFileSync(join(HERE, "manifest.json"), "utf8"));
  assert.equal(manifest.version, "1.8.0");
  assert.deepEqual(manifest.permissions, [
    "tabs", "scripting", "activeTab", "storage", "alarms", "debugger",
    "cookies", "contextMenus", "tabGroups", "sidePanel", "webNavigation",
  ]);
  assert.deepEqual(manifest.host_permissions, ["<all_urls>"]);
  const command = manifest.commands["add-task"];
  assert.ok(command, "no add-task command");
  assert.deepEqual(command.suggested_key, { default: "Ctrl+Shift+K", mac: "Command+Shift+K" });
  assert.ok(Object.values(manifest.commands).filter((c) => c.suggested_key).length <= 4, "Chrome takes at most four suggested keys");
});
