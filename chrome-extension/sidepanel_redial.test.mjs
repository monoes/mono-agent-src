// sidepanel.js: dialling again ("Try again", and a bridge appearing while the
// panel is open) must not save a bridge address. A saved address is the only
// one the worker ever dials, so saving the default one cut off a bridge on
// 9323 or on a paired port. The two blocks are cut out of the real source.
// `node --test chrome-extension/sidepanel_redial.test.mjs`

import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const src = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "sidepanel.js"), "utf8");
const DEFAULT = "ws://127.0.0.1:9222/monoagent";

function cut(from, to) {
  const start = src.indexOf(from);
  assert.ok(start >= 0, `${from} exists`);
  const end = src.indexOf(to, start);
  assert.ok(end > start, `${from} ends`);
  return src.slice(start, end + to.length);
}

test("Try again dials without saving an address", async () => {
  const sent = [];
  let listener;
  const block = cut('noticeAction.addEventListener("click"', "\n});\n");
  new Function("noticeAction", "Status", "ask", "noteStatus", "wsUrlInput", "settingsPanel", "pairingTokenInput", block)(
    { dataset: { action: "retry" }, addEventListener: (_, fn) => (listener = fn) },
    { ACTIONS: { PAIR: "pair", SETTINGS: "settings", RETRY: "retry" }, DEFAULT_WS_URL: DEFAULT },
    async (m) => sent.push(m),
    () => {},
    { value: DEFAULT },
  );
  await listener();
  assert.deepEqual(sent, [{ type: "account_recheck" }]);
});

test("a bridge that appears is dialled without saving an address", async () => {
  const sent = [];
  const block = cut("async function checkHealth()", "\n}\n");
  const run = new Function(
    "sent", "DEFAULT",
    `let probing = false, healthState = { reason: "no_bridge" };
     const MonoBridgeHealth = { probe: async () => ({}), toStatus: () => ({ reason: "" }) };
     const chrome = { storage: { local: { get: async () => ({}) } } };
     const connection = { status: "disconnected" }, wsUrlInput = { value: DEFAULT }, Status = { DEFAULT_WS_URL: DEFAULT };
     const probeHints = {}, settle = () => {}, ask = async (m) => sent.push(m);
     ${block}
     return checkHealth;`,
  )(sent, DEFAULT);
  await run();
  assert.deepEqual(sent, [{ type: "account_recheck" }]);
});
