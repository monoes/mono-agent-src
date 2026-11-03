/**
 * End to end: a freshly installed extension in a real (headless) Chromium
 * against a real bridge. Not a *.test.mjs: it needs a bridge that
 * internal/accountsmoke/autopair_browser_test.go starts (the real binary, a
 * scratch HOME, a private port) and signs in or out as this script asks.
 *
 *   node autopair_e2e.mjs signed-in  <bridge-port>
 *   node autopair_e2e.mjs ask        <bridge-port>
 *   node autopair_e2e.mjs signed-out <bridge-port>
 *
 * signed-in:  the bridge is in auto mode (`extension autopair auto`): the side
 *             panel shows the normal panel, never the sign-in screen, and the
 *             browser holds a pairing token it was handed.
 * ask:        the default mode, signed in: the panel shows "Waiting for
 *             approval", the browser holds no token; after "WAITING_OK" is
 *             printed and the driver prints "go" on stdin (it ran `extension
 *             approve --code <code>`, with the code printed after WAITING_OK)
 *             the panel flips to the normal one.
 * signed-out: the side panel shows the sign-in screen and, after "SIGNED_OUT_OK"
 *             is printed and the driver prints "go" on stdin (it signs in), the
 *             browser asks to pair and waits for approval, as in ask.
 *
 * Prints "OK" and exits 0 on success; a failure exits 1 with the reason.
 * SHOTS=<dir> saves screenshots.
 */

import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { createInterface } from "node:readline";
import { findChrome } from "./browser_harness.mjs";
import { attach, sleep, start } from "./extension_harness.mjs";

const [mode, portArg] = process.argv.slice(2);
const port = Number(portArg);
if (!["signed-in", "ask", "signed-out"].includes(mode) || !port) {
  console.error("usage: autopair_e2e.mjs signed-in|ask|signed-out <bridge-port>");
  process.exit(2);
}
const CHROME = findChrome();
if (!CHROME) {
  console.log("SKIP no Chrome");
  process.exit(0);
}

const fail = (msg) => {
  console.error("FAIL " + msg);
  throw new Error(msg);
};
const browser = await start(CHROME, { bridgePort: port });
let code = 0;
try {
  if (!browser.worker) fail(browser.workerError);
  const { cdp } = browser;
  const sw = await attach(cdp, { targetId: browser.worker.targetId });
  const swEval = (expr) => cdp.evaluate(sw, expr);
  const { targetId } = await cdp.send("Target.createTarget", { url: `chrome-extension://${browser.worker.id}/sidepanel.html` });
  const panel = (await cdp.send("Target.attachToTarget", { targetId, flatten: true })).sessionId;
  await cdp.send("Runtime.enable", {}, panel);
  await cdp.send("Page.enable", {}, panel);
  await cdp.send("Emulation.setDeviceMetricsOverride", { width: 400, height: 760, deviceScaleFactor: 1, mobile: false }, panel);
  const ev = (expr) => cdp.evaluate(panel, expr);
  const until = async (expr, what, ms = 30000) => {
    for (let t = 0; t < ms; t += 100) {
      try {
        if (await ev(expr)) return;
      } catch {
        // the document is being replaced
      }
      await sleep(100);
    }
    let seen = "";
    try {
      seen = await ev(`location.pathname + " | " + (document.getElementById("signin-state")?.textContent || document.body.innerText.slice(0, 200))`);
    } catch {
      // nothing to add
    }
    fail(`timed out waiting for ${what} (panel: ${seen}; worker: ${await swEval("connectionStatus + '/' + connectionReason").catch(() => "?")})`);
  };
  const shot = async (name) => {
    if (!process.env.SHOTS) return;
    const { data } = await cdp.send("Page.captureScreenshot", { format: "png" }, panel);
    await writeFile(join(process.env.SHOTS, name), Buffer.from(data, "base64"));
  };
  const stored = async () => JSON.parse(await swEval(`chrome.storage.local.get(["pairingToken","pairedWsUrl"]).then((o) => JSON.stringify(o))`));
  const paired = async () => {
    const o = await stored();
    if (!/^[0-9a-f]{64}$/.test(o.pairingToken || "")) fail(`no pairing token was stored: ${JSON.stringify(Object.keys(o))}`);
    if (o.pairedWsUrl !== `ws://127.0.0.1:${port}/monoagent`) fail(`paired to ${o.pairedWsUrl}`);
  };
  // One reader for the whole run: a line that arrives before it is awaited is kept.
  const stdinLines = [];
  const stdinWaiters = [];
  createInterface({ input: process.stdin }).on("line", (l) => (stdinWaiters.length ? stdinWaiters.shift()(l) : stdinLines.push(l)));
  const lineOnStdin = () => new Promise((res) => (stdinLines.length ? res(stdinLines.shift()) : stdinWaiters.push(res)));
  // The request is with the owner: the panel says where to approve it, no
  // token is stored, then the driver approves and the panel flips by itself.
  const waitThenApprove = async () => {
    await until(`/Waiting for approval in the MonoAgent app \\(or, in your own terminal, not one started from a coder session, run monoagentcli extension approve <id> --code <code>\\)/.test(document.getElementById("signin-state")?.textContent || "")`, "the waiting-for-approval line", 45000);
    // The panel shows the confirmation code the owner types to approve.
    const code = JSON.parse(await ev(`JSON.stringify((/Code: ([A-Z2-9]{3}-[A-Z2-9]{3})/.exec(document.getElementById("signin-code")?.textContent || "") || [])[1] || "")`));
    if (!/^[A-Z2-9]{3}-[A-Z2-9]{3}$/.test(code)) fail(`the waiting panel shows no confirmation code: ${JSON.stringify(code)}`);
    const screen = JSON.parse(await ev(`JSON.stringify({ pathname: location.pathname, tokenFieldHidden: document.getElementById("signin-pairing").hidden })`));
    if (screen.pathname !== "/signin.html" || !screen.tokenFieldHidden) fail("waiting for approval must be the sign-in screen without a token box");
    // Several polls pass with nothing handed out.
    await sleep(7000);
    if ((await stored()).pairingToken) fail("a token was stored before the owner approved");
    await shot("autopair-waiting.png");
    console.log(`WAITING_OK ${code}`);
    await lineOnStdin();
    await until(`${normalPanel} && document.documentElement.style.visibility === ""`, "the panel to flip to the normal one after approval", 45000);
    await paired();
    await shot("autopair-approved.png");
  };
  const normalPanel = `location.pathname === "/sidepanel.html" && document.readyState === "complete"`;

  if (mode === "ask") {
    await waitThenApprove();
  } else if (mode === "signed-in") {
    // Never on the sign-in screen, from the first moment to a settled normal panel.
    let sawSignIn = false;
    let settled = 0;
    for (let t = 0; t < 30000 && settled < 15; t += 200) {
      try {
        const path = await ev(`location.pathname`);
        if (path === "/signin.html") sawSignIn = true;
        const ok = await ev(`${normalPanel} && document.documentElement.style.visibility === ""`);
        settled = ok ? settled + 1 : 0;
      } catch {
        settled = 0;
      }
      await sleep(200);
    }
    if (settled < 15) fail("the normal panel never settled");
    await paired();
    if (!(await swEval(`connectionStatus === "connected" && gate.signedIn()`))) fail("the worker is not connected and signed in");
    await shot("autopair-signed-in.png");
    if (sawSignIn) console.error("NOTE the sign-in screen showed briefly while the browser paired");
  } else {
    await until(`location.pathname === "/signin.html" && document.readyState === "complete"`, "the sign-in screen");
    await until(`/updates automatically/.test(document.getElementById("signin-state").textContent)`, "the line saying the window updates automatically");
    const screen = JSON.parse(await ev(`JSON.stringify({
      state: document.getElementById("signin-state").textContent,
      tokenFieldHidden: document.getElementById("signin-pairing").hidden,
      pairLineHidden: document.getElementById("signin-pair").hidden,
    })`));
    if (!/Sign in in the MonoAgent app or run monoagentcli account login\. This window updates automatically\./.test(screen.state)) fail("wrong copy: " + screen.state);
    if (!screen.tokenFieldHidden || !screen.pairLineHidden) fail("signed out with the bridge up must not ask for a token");
    const o = await stored();
    if (o.pairingToken) fail("a token was handed out while signed out");
    await shot("autopair-signed-out.png");
    console.log("SIGNED_OUT_OK");
    await lineOnStdin();
    await waitThenApprove();
  }
  console.log("OK");
} catch (e) {
  console.error("FAIL " + e.message);
  code = 1;
} finally {
  await browser.close();
}
process.exit(code);
