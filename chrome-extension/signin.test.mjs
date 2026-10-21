// The sign-in screen's words (signin.js `view`): what the person reads while
// nothing works, and that it leaves the moment a bridge reports a sign-in.
//
// `node --test chrome-extension/signin.test.mjs`

import test from "node:test";
import assert from "node:assert/strict";
import { loadExtensionScripts } from "./test_helpers.mjs";

const { MonoSignIn: S } = loadExtensionScripts(["signin.js"]);

test("signed in leaves the screen; anything else stays on it", () => {
  assert.equal(S.view({ signedIn: true, status: "connected" }).signedIn, true);
  assert.equal(S.view({ signedIn: false, status: "connected" }).signedIn, false);
  assert.equal(S.view({ signedIn: "yes" }).signedIn, false);
  assert.equal(S.view(undefined).signedIn, false);
});

test("it says where MonoAgent stands", () => {
  assert.match(S.view({ status: "connected" }).state, /nobody is signed in/);
  assert.match(S.view({ status: "disconnected" }).state, /not running/);
  assert.match(S.view({ status: "connecting" }).state, /Looking for MonoAgent/);
});

test("an unpaired browser is told to pair after signing in", () => {
  assert.match(S.view({ status: "unpaired" }).pair, /After you sign in, run monoagentcli extension pair/);
  assert.equal(S.view({ status: "connected" }).pair, "");
});

test("an unpaired browser gets the token field; a paired one does not", () => {
  assert.equal(S.view({ status: "unpaired" }).unpaired, true);
  assert.match(S.view({ status: "unpaired" }).pair, /paste the token/);
  assert.equal(S.view({ status: "connected" }).unpaired, false);
});

test("what is kept is named, and said not to be sent until sign-in", () => {
  const v = S.view({ status: "disconnected", queued: { captures: 2, tasks: 1, recording: true } });
  assert.equal(v.queued, "Waiting in this browser: 2 saved pages, 1 task, a recording. Nothing is sent until you sign in; then it is sent as usual.");
  assert.equal(S.view({ queued: { captures: 0, tasks: 0, recording: false } }).queued, "");
});
