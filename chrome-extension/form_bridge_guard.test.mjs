// form_bridge.js answers form_fill, card_fill and files_fill only to this
// extension's own pages (the side panel). A content script runs on every
// site, so a compromised page's message must not fill a form or the saved card.
// `CHROME_PATH=/nonexistent node --test chrome-extension/form_bridge_guard.test.mjs`

import test from "node:test";
import assert from "node:assert/strict";
import { loadExtensionScripts } from "./test_helpers.mjs";

const PANEL = { id: "ext", url: "chrome-extension://ext/sidepanel.html" };
const PAGE = { id: "ext", url: "https://evil.example/", tab: { id: 5 } };
const OTHER = { id: "other", url: "chrome-extension://other/x.html" };

function setup() {
  const listeners = [];
  const reached = [];
  const chrome = {
    runtime: { id: "ext", getURL: (p) => `chrome-extension://ext/${p}`, onMessage: { addListener: (fn) => listeners.push(fn) } },
    tabs: {
      query: async () => {
        reached.push("tabs.query");
        return [];
      },
    },
  };
  const sandbox = loadExtensionScripts(["chat_context.js", "chat_core.js", "form_bridge.js"], { chrome });
  sandbox.MonoForm.install();
  return { listeners, reached };
}

for (const type of ["form_fill", "card_fill", "files_fill"]) {
  test(`${type} from a web page or another extension is not handled`, async () => {
    const { listeners, reached } = setup();
    for (const sender of [PAGE, OTHER, {}, undefined]) {
      const handled = listeners[0]({ type }, sender, () => assert.fail("must not respond"));
      assert.equal(handled, false);
    }
    await new Promise((r) => setTimeout(r, 5));
    assert.deepEqual(reached, []);
  });

  test(`${type} from the side panel is handled`, async () => {
    const { listeners } = setup();
    let answered;
    const handled = listeners[0]({ type }, PANEL, (r) => (answered = r));
    assert.equal(handled, true);
    await new Promise((r) => setTimeout(r, 20));
    assert.ok(answered, "the panel gets an answer");
  });
}
