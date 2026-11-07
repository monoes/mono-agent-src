// The pairing-without-a-step state machine (autopair.js): what the worker
// does for each thing the bridge can say.
//
// `node --test chrome-extension/autopair.test.mjs`

import test from "node:test";
import assert from "node:assert/strict";
import { loadExtensionScripts } from "./test_helpers.mjs";

const { MonoAutoPair: A } = loadExtensionScripts(["autopair.js"]);

const json = (body, status = 200) => ({ ok: status >= 200 && status < 300, status, json: async () => body });

/** bridge answers /status and /pair/claim on the given ports; any other port is refused. */
function fakeBridge({ ports = [9222], doc = {}, claim = json({ token: "t".repeat(64), id: "abc123" }), poll = json({ status: "pending" }) } = {}) {
  const calls = [];
  const impl = async (url, init = {}) => {
    calls.push({ url, method: init.method || "GET", body: init.body, headers: init.headers });
    const m = /^http:\/\/127\.0\.0\.1:(\d+)(\/.*)$/.exec(url);
    if (!m || !ports.includes(Number(m[1]))) throw new TypeError("refused");
    if (m[2] === "/monoagent/status") return json(Object.assign({ service: "monoagent-bridge", signedIn: false, paired: false, pairMode: "auto" }, doc));
    if (m[2] === "/monoagent/pair/claim") return claim;
    if (m[2].startsWith("/monoagent/pair/claim/")) return poll;
    throw new Error("unexpected " + url);
  };
  return { impl, calls };
}

test("bridge up and signed in: claims, and sends who it is", async () => {
  const b = fakeBridge({ doc: { signedIn: true } });
  const r = await A.step({ fetch: b.impl, instance: "0123456789abcdef", label: "Edge 0123" });
  assert.equal(r.kind, "claimed");
  assert.equal(r.token.length, 64);
  assert.equal(r.wsUrl, "ws://127.0.0.1:9222/monoagent");
  const claim = b.calls.find((c) => c.method === "POST");
  assert.deepEqual(JSON.parse(claim.body), { instance: "0123456789abcdef", label: "Edge 0123", token: "" });
});

test("bridge up and signed out: no claim, says signed_out", async () => {
  const b = fakeBridge({ doc: { signedIn: false } });
  const r = await A.step({ fetch: b.impl });
  assert.equal(r.kind, "signed_out");
  assert.equal(b.calls.some((c) => c.method === "POST"), false);
});

test("no bridge on any port: down", async () => {
  const b = fakeBridge({ ports: [] });
  assert.equal((await A.step({ fetch: b.impl })).kind, "down");
});

test("a bridge with /health but no /status is outdated, not down", async () => {
  const impl = async (url) => {
    if (url === "http://127.0.0.1:9222/monoagent/health") return json({ service: "monoagent-bridge", status: "connected" });
    if (url.startsWith("http://127.0.0.1:9222/")) return json("404 page not found", 404);
    throw new TypeError("refused");
  };
  assert.equal((await A.step({ fetch: impl })).kind, "outdated");
});

test("something else answering /health is still down", async () => {
  const impl = async (url) => (url.endsWith("/monoagent/health") ? json({ Browser: "Chrome/140" }) : json("nope", 404));
  assert.equal((await A.step({ fetch: impl })).kind, "down");
});

test("the fallback port is found", async () => {
  const b = fakeBridge({ ports: [9323], doc: { signedIn: true } });
  const r = await A.step({ fetch: b.impl });
  assert.equal(r.kind, "claimed");
  assert.equal(r.wsUrl, "ws://127.0.0.1:9323/monoagent");
});

test("something else on the port is not the bridge", async () => {
  const impl = async () => json({ Browser: "Chrome/140", signedIn: true });
  assert.equal((await A.step({ fetch: impl })).kind, "down");
});

test("pairing off: manual path, no claim", async () => {
  const b = fakeBridge({ doc: { signedIn: true, pairMode: "off" } });
  const r = await A.step({ fetch: b.impl });
  assert.equal(r.kind, "disabled");
  assert.equal(b.calls.some((c) => c.method === "POST"), false);
});

test("a refused claim carries the bridge's reason; a mute one is refused too", async () => {
  const b = fakeBridge({ doc: { signedIn: true }, claim: json({ error: "too many pairing attempts" }, 429) });
  const r = await A.step({ fetch: b.impl });
  assert.equal(r.kind, "refused");
  assert.match(r.error, /too many/);
  const odd = fakeBridge({ doc: { signedIn: true }, claim: json({}, 200) });
  assert.equal((await A.step({ fetch: odd.impl })).kind, "refused");
});

test("a bridge that accepts and never answers does not hang the step", async () => {
  const impl = (url, init = {}) =>
    new Promise((_, reject) => {
      init.signal && init.signal.addEventListener("abort", () => reject(new Error("aborted")));
    });
  const r = await A.step({ fetch: impl, timeoutMs: 30 });
  assert.equal(r.kind, "down");
});

test("the configured address is asked first", () => {
  assert.deepEqual(A.bases({ wsUrl: "ws://127.0.0.1:9500/monoagent" }), ["http://127.0.0.1:9500", "http://127.0.0.1:9222", "http://127.0.0.1:9323"]);
});

const pendingReply = json({ pending: true, claimId: "c1c1c1c1c1c1c1c1", pollSecret: "s".repeat(64), code: "K7M-2QX", expiresAt: "2099-01-01T00:00:00Z" }, 202);

test("ask mode: the claim comes back pending, with what is needed to poll; no token is assumed", async () => {
  const b = fakeBridge({ doc: { signedIn: true, pairMode: "ask" }, claim: pendingReply });
  const r = await A.step({ fetch: b.impl, instance: "0123456789abcdef", label: "Edge" });
  assert.equal(r.kind, "pending");
  assert.equal(r.claimId, "c1c1c1c1c1c1c1c1");
  assert.equal(r.pollSecret, "s".repeat(64));
  assert.equal(r.code, "K7M-2QX", "the code the owner compares comes with the claim");
  assert.equal(r.token, undefined);
});

test("a poll keeps showing the code stored with the request", async () => {
  const b = fakeBridge({ doc: { signedIn: true, pairMode: "ask" }, poll: json({ status: "pending" }) });
  const r = await A.step({ fetch: b.impl, pending: { claimId: "c1c1c1c1c1c1c1c1", pollSecret: "sec", code: "K7M-2QX" } });
  assert.equal(r.kind, "pending");
  assert.equal(r.code, "K7M-2QX");
  const quiet = await A.step({ fetch: async () => null, timeoutMs: 20, pending: { claimId: "c1", pollSecret: "sec", code: "K7M-2QX" }, known: {} });
  assert.notEqual(quiet.kind, "claimed");
});

test("a 202 without the poll handles is refused, not read as paired", async () => {
  const b = fakeBridge({ doc: { signedIn: true, pairMode: "ask" }, claim: json({ pending: true }, 202) });
  assert.equal((await A.step({ fetch: b.impl })).kind, "refused");
});

test("polling sends the secret as a bearer, never in the URL, and does not claim again", async () => {
  const b = fakeBridge({ doc: { signedIn: true, pairMode: "ask" }, poll: json({ status: "pending" }) });
  const r = await A.step({ fetch: b.impl, pending: { claimId: "c1c1c1c1c1c1c1c1", pollSecret: "sec" } });
  assert.equal(r.kind, "pending");
  assert.equal(b.calls.some((c) => c.method === "POST"), false, "a request is already waiting: no second card");
  const p = b.calls.find((c) => c.url.includes("/pair/claim/"));
  assert.equal(p.url, "http://127.0.0.1:9222/monoagent/pair/claim/c1c1c1c1c1c1c1c1");
  assert.equal(p.headers.Authorization, "Bearer sec");
});

test("approved: the poll yields the token", async () => {
  const b = fakeBridge({ doc: { signedIn: true, pairMode: "ask" }, poll: json({ status: "approved", token: "k".repeat(64), id: "id1" }) });
  const r = await A.step({ fetch: b.impl, pending: { claimId: "c1", pollSecret: "sec" } });
  assert.equal(r.kind, "claimed");
  assert.equal(r.token, "k".repeat(64));
});

test("denied, and expired (410 or forgotten 404), are told apart from waiting", async () => {
  const mk = (poll) => fakeBridge({ doc: { signedIn: true, pairMode: "ask" }, poll });
  assert.equal((await A.step({ fetch: mk(json({ status: "denied" })).impl, pending: { claimId: "c", pollSecret: "s" } })).kind, "denied");
  assert.equal((await A.step({ fetch: mk(json({ status: "expired" }, 410)).impl, pending: { claimId: "c", pollSecret: "s" } })).kind, "expired");
  assert.equal((await A.step({ fetch: mk(json({ error: "no such" }, 404)).impl, pending: { claimId: "c", pollSecret: "s" } })).kind, "expired");
});

test("an unanswered poll keeps waiting rather than dropping the request", async () => {
  const b = fakeBridge({ doc: { signedIn: true, pairMode: "ask" } });
  const impl = async (url, init = {}) => {
    if (url.includes("/pair/claim/")) throw new TypeError("reset");
    return b.impl(url, init);
  };
  const r = await A.step({ fetch: impl, pending: { claimId: "c", pollSecret: "s" } });
  assert.equal(r.kind, "pending");
});

test("a signed-out or pairing-off bridge is not polled", async () => {
  const off = fakeBridge({ doc: { signedIn: true, pairMode: "off" } });
  assert.equal((await A.step({ fetch: off.impl, pending: { claimId: "c", pollSecret: "s" } })).kind, "disabled");
  const out = fakeBridge({ doc: { signedIn: false } });
  assert.equal((await A.step({ fetch: out.impl, pending: { claimId: "c", pollSecret: "s" } })).kind, "signed_out");
});

test("a token the bridge turned away is offered back, so the same browser may rotate it", async () => {
  const b = fakeBridge({ doc: { signedIn: true } });
  await A.step({ fetch: b.impl, instance: "0123456789abcdef", token: "old" });
  assert.equal(JSON.parse(b.calls.find((c) => c.method === "POST").body).token, "old");
});
