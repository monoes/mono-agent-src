// pair_bridge.js adopts only a bridge on 9222, 9323 or the configured port, and
// asks before it replaces an existing pairing with a different bridge.
// `node --test chrome-extension/pair_bridge_guard.test.mjs`

import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createContext, runInContext } from "node:vm";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const SRC = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "pair_bridge.js"), "utf8");

async function run({ origin, existing = {}, confirmAnswer = true }) {
  const stored = { ...existing };
  const messages = [];
  const asked = [];
  const fetched = [];
  let settle;
  const finished = new Promise((r) => (settle = r));
  const sandbox = {
    location: { origin, search: "?n=abc" },
    URLSearchParams,
    URL,
    fetch: async (u) => {
      fetched.push(u);
      return { ok: true, json: async () => ({ token: "tok" }) };
    },
    window: {
      postMessage: (m) => {
        messages.push(m);
        settle();
      },
      confirm: (text) => {
        asked.push(text);
        return confirmAnswer;
      },
    },
    chrome: {
      storage: {
        local: {
          get: async (keys) => Object.fromEntries(keys.filter((k) => k in stored).map((k) => [k, stored[k]])),
          set: async (u) => Object.assign(stored, u),
        },
      },
    },
  };
  runInContext(SRC, createContext(sandbox));
  await finished;
  return { stored, report: messages[0], asked, fetched };
}

test("a pairing page on an unrelated loopback port is not adopted", async () => {
  const r = await run({ origin: "http://127.0.0.1:8080" });
  assert.equal(r.report.ok, false);
  assert.match(r.report.error, /not a MonoAgent bridge port/);
  assert.deepEqual(r.fetched, [], "the nonce is not even sent to it");
  assert.equal(r.stored.pairingToken, undefined);
});

test("the configured port is accepted", async () => {
  const r = await run({ origin: "http://127.0.0.1:9777", existing: { wsUrl: "ws://127.0.0.1:9777/monoagent" } });
  assert.equal(r.report.ok, true);
  assert.equal(r.stored.pairedWsUrl, "ws://127.0.0.1:9777/monoagent");
});

test("replacing a pairing with a different bridge asks first, and a refusal keeps the old one", async () => {
  const existing = { pairingToken: "old", pairedWsUrl: "ws://127.0.0.1:9222/monoagent" };
  const refused = await run({ origin: "http://127.0.0.1:9323", existing, confirmAnswer: false });
  assert.equal(refused.asked.length, 1);
  assert.equal(refused.report.ok, false);
  assert.equal(refused.stored.pairingToken, "old");
  assert.deepEqual(refused.fetched, []);

  const accepted = await run({ origin: "http://127.0.0.1:9323", existing, confirmAnswer: true });
  assert.equal(accepted.report.ok, true);
  assert.equal(accepted.stored.pairedWsUrl, "ws://127.0.0.1:9323/monoagent");
});

test("re-pairing the same bridge does not ask", async () => {
  const r = await run({ origin: "http://127.0.0.1:9222", existing: { pairingToken: "old" } });
  assert.deepEqual(r.asked, []);
  assert.equal(r.report.ok, true);
  assert.equal(r.stored.pairingToken, "tok");
});
