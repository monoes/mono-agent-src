// background.js: set_ws_url and set_pairing_token re-point the bridge, so only
// the extension's own page (the side panel) may send them, not a content
// script on a web page. The two branches are cut out of the real source and
// run here.
// `node --test chrome-extension/background_set_guard.test.mjs`

import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const HERE = dirname(fileURLToPath(import.meta.url));
const src = readFileSync(join(HERE, "background.js"), "utf8");

const PANEL = { id: "ext", url: "chrome-extension://ext/sidepanel.html" };
const PAGE = { id: "ext", url: "https://evil.example/", tab: { id: 5 } };

function branch(type) {
  const start = src.indexOf(`  if (msg.type === "${type}") {`);
  assert.ok(start > 0, `${type} branch exists`);
  const end = src.indexOf("\n  }\n", start) + 4;
  return src.slice(start, end);
}

function run(type, sender) {
  const calls = [];
  const chrome = { runtime: { id: "ext", getURL: (p) => `chrome-extension://ext/${p}` } };
  const body = branch(type);
  // The helper background.js defines above its listener, when it has one.
  const helper = /function fromOwnPage[\s\S]*?\n}\n/.exec(src);
  const fn = new Function(
    "msg", "sender", "sendResponse", "setWsUrl", "setPairingToken", "chrome",
    `${helper ? helper[0] : ""}\n${body}\nreturn false;`,
  );
  const set = async (v) => {
    calls.push(v);
    return { ok: true };
  };
  const handled = fn({ type, url: "ws://x", value: "tok" }, sender, () => {}, set, set, chrome);
  return { handled, calls };
}

for (const type of ["set_ws_url", "set_pairing_token"]) {
  test(`${type} from a web page is refused`, () => {
    const r = run(type, PAGE);
    assert.equal(r.handled, false);
    assert.deepEqual(r.calls, []);
  });
  test(`${type} from the side panel is applied`, () => {
    const r = run(type, PANEL);
    assert.equal(r.handled, true);
    assert.equal(r.calls.length, 1);
  });
}
