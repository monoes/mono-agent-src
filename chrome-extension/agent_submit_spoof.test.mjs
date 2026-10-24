// installGuard (agent_submit_page.js) against a page that tries to make an
// unguarded frame look guarded, or to find a handle to switch the guard off.
// The guard used to hang a `control` object on a well-known window property
// and trust one that was already there; a page that defined its own made a
// frame with no guard answer "armed". Now nothing of the guard is on window:
// it is controlled over a per-tab random event name.
// `CHROME_PATH=/nonexistent node --test chrome-extension/agent_submit_spoof.test.mjs`

import test from "node:test";
import assert from "node:assert/strict";
import { loadExtensionScripts } from "./test_helpers.mjs";

function frame() {
  class HTMLFormElement {
    submit() {
      this.sent = true;
    }
    requestSubmit() {
      this.sent = true;
    }
  }
  const win = new EventTarget();
  const doc = new EventTarget();
  const env = { window: win, document: doc, HTMLFormElement, location: { href: "https://shop.test/cart" } };
  const g = loadExtensionScripts(["agent_submit_page.js"], env);
  return { page: g.MonoAgentSubmitPage, win, doc, HTMLFormElement };
}

const control = (doc, ctl, action) => {
  const box = { action, result: false };
  doc.dispatchEvent(new CustomEvent(ctl, { detail: box }));
  return box.result;
};

test("an object the page defines under the old well-known name makes nothing look guarded", () => {
  const f = frame();
  let asked = 0;
  f.win.__monoagentSubmitGuard = Object.freeze({ control: () => (asked++, true) });
  const original = f.HTMLFormElement.prototype.submit;
  assert.equal(f.page.installGuard("ctl-a", "evt-a"), true);
  assert.equal(asked, 0, "the page's object was consulted");
  assert.notEqual(f.HTMLFormElement.prototype.submit, original, "no guard was installed: the form's submit is untouched");
  // A guard really is there: it blocks a submit and answers on its own name.
  const form = new f.HTMLFormElement();
  form.submit();
  assert.equal(form.sent, undefined, "an armed guard lets form.submit() through");
  assert.equal(control(f.doc, "ctl-a", "status"), true);
});

test("the guard is controlled only over its own name: disarm, allow_once and a second arm", () => {
  const f = frame();
  assert.equal(f.page.installGuard("ctl-a", "evt-a"), true);
  const wrapped = f.HTMLFormElement.prototype.submit;
  // A name the page guesses (the old key, the event name, a lookalike) does nothing.
  for (const guess of ["__monoagentSubmitGuard", "evt-a", "ctl-b", "ctl-A"]) {
    assert.equal(control(f.doc, guess, "disarm"), false, guess);
  }
  const form = new f.HTMLFormElement();
  form.submit();
  assert.equal(form.sent, undefined, "still armed after the guesses");
  // Arming again is the same guard, not a second wrapper.
  assert.equal(f.page.installGuard("ctl-a", "evt-a"), true);
  assert.equal(f.HTMLFormElement.prototype.submit, wrapped, "a second guard was stacked on the first");
  assert.equal(control(f.doc, "ctl-a", "disarm"), true);
  form.submit();
  assert.equal(form.sent, true, "disarmed: the submit goes through");
  assert.equal(control(f.doc, "ctl-a", "arm"), true);
  form.sent = undefined;
  form.submit();
  assert.equal(form.sent, undefined, "armed again");
  assert.equal(control(f.doc, "ctl-a", "allow_once"), true);
  form.submit();
  assert.equal(form.sent, true, "allow_once let exactly one through");
  form.sent = undefined;
  form.submit();
  assert.equal(form.sent, undefined);
});

test("nothing of the guard is on window", () => {
  const f = frame();
  f.page.installGuard("ctl-a", "evt-a");
  assert.deepEqual(Object.getOwnPropertyNames(f.win).filter((k) => /monoagent|guard|ctl/i.test(k)), []);
});
