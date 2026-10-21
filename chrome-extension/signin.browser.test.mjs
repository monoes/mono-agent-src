/**
 * The sign-in gate as production runs it: the unpacked extension in a
 * headless Chromium, fresh (no bridge has ever said anyone signed in).
 *
 *   - the side panel is the sign-in screen and nothing else;
 *   - every feature the panel or a page can ask for is refused, through
 *     Chrome's own message routing (gate.js took the events first);
 *   - a bridge report of a sign-in brings the full panel back.
 *
 * Isolation: its own profile dir under TMPDIR and a random DevTools port; no
 * pairing token, so it never dials a bridge. WITHOUT CHROME the suite skips.
 * SHOTS=<dir> also saves screenshots of the panel.
 */

import { after, describe, it } from "node:test";
import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { findChrome } from "./browser_harness.mjs";
import { attach, sleep, start } from "./extension_harness.mjs";

const CHROME = findChrome();
let browser = null;
let why = "no Chrome found — set CHROME_PATH to run these";
if (CHROME && typeof WebSocket === "function") {
  try {
    browser = await start(CHROME);
  } catch (e) {
    why = `${CHROME} would not start: ${e.message.split("\n")[0]}`;
  }
}

after(async () => {
  if (browser) await browser.close();
});

const OK_REPORT = `{ kind: "reply", id: "account-test", ok: true, data: { pong: true, account: { state: "ok", reason: "", enforced: true } } }`;

describe("signed out, the extension is a sign-in screen", { skip: browser ? false : why, concurrency: 1 }, () => {
  let panel;
  let sw;
  const ev = (s, expr) => browser.cdp.evaluate(s, expr);
  const until = async (expr, what, ms = 8000) => {
    for (let t = 0; t < ms; t += 50) {
      try {
        if (await ev(panel, expr)) return;
      } catch {
        // the document is being replaced
      }
      await sleep(50);
    }
    throw new Error(`timed out waiting for ${what}`);
  };
  async function shot(name) {
    if (!process.env.SHOTS) return;
    const { data } = await browser.cdp.send("Page.captureScreenshot", { format: "png" }, panel);
    await writeFile(join(process.env.SHOTS, name), Buffer.from(data, "base64"));
  }

  it("opens the side panel on the sign-in screen, with nothing else on it", async () => {
    assert.ok(browser.worker, browser.workerError);
    sw = await attach(browser.cdp, { targetId: browser.worker.targetId });
    assert.equal(await ev(sw, "gate.ready.then(() => gate.state())"), "out", "a fresh install is signed out");
    const { cdp } = browser;
    const { targetId } = await cdp.send("Target.createTarget", { url: `chrome-extension://${browser.worker.id}/sidepanel.html` });
    panel = (await cdp.send("Target.attachToTarget", { targetId, flatten: true })).sessionId;
    await cdp.send("Runtime.enable", {}, panel);
    await cdp.send("Page.enable", {}, panel);
    await cdp.send("Emulation.setDeviceMetricsOverride", { width: 400, height: 760, deviceScaleFactor: 1, mobile: false }, panel);
    await until(`location.pathname === "/signin.html" && document.readyState === "complete"`, "the sign-in screen");
    await until(`!document.getElementById("signin-pair").hidden`, "the pairing line (this browser is unpaired)");
    const screen = JSON.parse(await ev(panel, `JSON.stringify({
      title: document.getElementById("signin-title").textContent,
      command: document.getElementById("signin-command").textContent,
      buttons: [...document.querySelectorAll("button")].map((b) => b.id),
      fields: document.querySelectorAll("input, textarea, select, details, nav, [role=tab]").length,
      pair: document.getElementById("signin-pair").textContent,
    })`));
    assert.equal(screen.title, "Sign in to MonoAgent");
    assert.equal(screen.command, "monoagentcli account login");
    assert.deepEqual(screen.buttons, ["signin-copy", "signin-pair-save", "signin-recheck"]);
    assert.equal(screen.fields, 1, "the pairing token field, shown because this browser is unpaired");
    assert.match(screen.pair, /monoagentcli extension pair/, "unpaired: told to pair after signing in");
    await shot("signin-unpaired.png");
  });

  it("refuses every feature the panel or a page could ask for", async () => {
    const types = [
      "capture_active_tab", "capture_form_state", "queue_state", "queue_retry", "task_add", "task_state",
      "highlight_add", "highlight_list", "highlight_pages", "saved_get", "ask_brain", "chat_send",
      "record_start", "record_status", "summary_ai_state", "capture_profile_set",
    ];
    for (const type of types) {
      const reply = JSON.parse(await ev(panel, `new Promise((r) => chrome.runtime.sendMessage({ type: ${JSON.stringify(type)}, text: "x" }, (res) => { void chrome.runtime.lastError; r(JSON.stringify(res || null)); }))`));
      assert.equal(reply && reply.code, "login_required", `${type} answered ${JSON.stringify(reply)}`);
    }
    const stored = JSON.parse(await ev(sw, `chrome.storage.local.get(null).then((o) => JSON.stringify(Object.keys(o)))`));
    for (const key of ["taskOutbox", "captureQueue", "recordingOutboxMeta"]) assert.equal(stored.includes(key), false, `${key} was written`);
  });

  it("goes back to the full panel once a bridge reports a sign-in", async () => {
    assert.equal(await ev(sw, `(gate.observe(${OK_REPORT}), gate.signedIn())`), true);
    await until(`location.pathname === "/sidepanel.html" && document.readyState === "complete" && document.documentElement.style.visibility === ""`, "the full panel");
    await sleep(300);
    await shot("signed-in-panel.png");
    const reply = JSON.parse(await ev(panel, `new Promise((r) => chrome.runtime.sendMessage({ type: "task_state" }, (res) => r(JSON.stringify(res || null))))`));
    assert.equal(reply.ok, true, "signed in, the features answer");
  });

  it("returns to the sign-in screen when the bridge says the person signed out", async () => {
    await ev(sw, `(gate.observe({ kind: "reply", id: "r", error: "Log in to monoes.me first", code: "account_locked" }), gate.signedIn())`);
    await until(`location.pathname === "/signin.html"`, "the sign-in screen again");
    await shot("signed-out-again.png");
  });
});
