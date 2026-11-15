// The two decisions the worker makes when a tab or the bridge went away
// underneath it: re-injecting the content script into a tab that was open
// before the extension reloaded, and restarting the fast reconnect after a
// bridge restart. `node --test 'chrome-extension/**/*.test.mjs'`.

import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import { loadExtensionScripts } from "./test_helpers.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const { MonoResilience } = loadExtensionScripts(["bridge_resilience.js"]);

const NO_RECEIVER = new Error("Could not establish connection. Receiving end does not exist.");

test("a tab with no content script is injected once and the command retried", async () => {
  let calls = 0;
  let injected = 0;
  const out = await MonoResilience.withReinject(
    async () => {
      calls++;
      if (calls === 1) throw NO_RECEIVER;
      return { ok: true };
    },
    async () => injected++,
  );
  assert.deepEqual(out, { ok: true });
  assert.equal(calls, 2);
  assert.equal(injected, 1);
});

test("any other error is passed on without injecting", async () => {
  let injected = 0;
  await assert.rejects(
    MonoResilience.withReinject(
      async () => {
        throw new Error("element not found");
      },
      async () => injected++,
    ),
    /element not found/,
  );
  assert.equal(injected, 0);
});

test("a tab that still has no receiver after the injection says to reload it", async () => {
  let calls = 0;
  await assert.rejects(
    MonoResilience.withReinject(
      async () => {
        calls++;
        throw NO_RECEIVER;
      },
      async () => {},
    ),
    /reload the tab/i,
  );
  assert.equal(calls, 2, "retried once, not in a loop");
});

test("a tab that cannot be injected says to reload it", async () => {
  await assert.rejects(
    MonoResilience.withReinject(
      async () => {
        throw NO_RECEIVER;
      },
      async () => {
        throw new Error("Cannot access contents of the page.");
      },
    ),
    /reload the tab/i,
  );
});

test("fast reconnect restarts only after a connection that was established", () => {
  assert.equal(MonoResilience.fastRetryAfterClose({ heard: true, count: 60, max: 60 }), 0);
  assert.equal(MonoResilience.fastRetryAfterClose({ heard: false, count: 60, max: 60 }), 60, "a bridge that never answered is not hammered");
  assert.equal(MonoResilience.fastRetryAfterClose({ heard: false, count: 7, max: 60 }), 7);
});

const background = readFileSync(join(HERE, "background.js"), "utf8");

test("the worker marks connected only once the bridge has answered", () => {
  const onopen = background.slice(background.indexOf("ws.onopen = () => {"), background.indexOf("ws.onmessage = "));
  assert.doesNotMatch(onopen, /setStatus\("connected"\)/);
  assert.doesNotMatch(onopen, /fastRetryCount = FAST_RETRY_MAX/);
  const heard = background.slice(background.indexOf("if (gate.observe(cmd) && !accountHeard) {"));
  assert.match(heard.slice(0, heard.indexOf("\n    }\n")), /setStatus\("connected"\)/);
});

test("an unexpected close of an established socket restarts the fast reconnect", () => {
  const onclose = background.slice(background.indexOf("ws.onclose = (event) => {"));
  assert.match(onclose.slice(0, onclose.indexOf("\n}\n")), /MonoResilience\.fastRetryAfterClose/);
});

test("DOM commands go through the re-injecting send", () => {
  const send = background.slice(background.indexOf("async function sendToContent("));
  assert.match(send.slice(0, send.indexOf("\n}\n")), /MonoResilience\.withReinject/);
});
