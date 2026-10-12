// Tests for which monoagent profile this browser runs automations for.
// `node --test 'chrome-extension/*.test.mjs'`.

import test from "node:test";
import assert from "node:assert/strict";
import { loadExtensionScripts } from "./test_helpers.mjs";

const { MonoBrowserBinding: B } = loadExtensionScripts(["browser_binding.js"]);

function fakeStorage(seed = {}) {
  const data = Object.assign({}, seed);
  return {
    data,
    get: async (keys) => {
      const out = {};
      for (const k of Array.isArray(keys) ? keys : [keys]) if (k in data) out[k] = data[k];
      return out;
    },
    set: async (values) => Object.assign(data, values),
  };
}

let n = 0;
const fakeCrypto = { randomUUID: () => `0000000${++n}-0000-4000-8000-000000000000` };
const EDGE_UA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/140.0 Safari/537.36 Edg/140.0";

test("the worker creates an instance id once and keeps it", async () => {
  const storage = fakeStorage();
  const first = await B.load(storage, { crypto: fakeCrypto, userAgent: EDGE_UA, create: true });
  const again = await B.load(storage, { crypto: fakeCrypto, userAgent: EDGE_UA, create: true });
  assert.match(first.instance, /^[A-Za-z0-9-]{8,64}$/);
  assert.equal(again.instance, first.instance);
  assert.equal(storage.data[B.INSTANCE_KEY], first.instance);
});

test("the side panel never mints an id (only the worker does)", async () => {
  const storage = fakeStorage();
  const state = await B.load(storage, { crypto: fakeCrypto, userAgent: EDGE_UA });
  assert.equal(state.instance, "");
  assert.equal(B.INSTANCE_KEY in storage.data, false);
});

test("a corrupt stored id is replaced", async () => {
  const storage = fakeStorage({ [B.INSTANCE_KEY]: "../x" });
  const state = await B.load(storage, { crypto: fakeCrypto, userAgent: EDGE_UA, create: true });
  assert.notEqual(state.instance, "../x");
});

test("default label names the browser", async () => {
  const state = await B.load(fakeStorage(), { crypto: fakeCrypto, userAgent: EDGE_UA, create: true });
  assert.match(state.label, /^Edge /);
});

test("auth fields leave out an empty set", () => {
  const f = B.authFields({ instance: "abcdefgh-1", profile: "", profiles: [], label: "Edge" }, "1.12.0");
  assert.deepEqual(f, { instance: "abcdefgh-1", label: "Edge", version: "1.12.0" });
});

test("auth fields send the set and, for old bridges, the first id as profile", () => {
  const g = B.authFields({ instance: "abcdefgh-1", profiles: ["p-work", "p-home"], label: "Edge" }, "1.12.0");
  assert.deepEqual(g.profiles, ["p-work", "p-home"]);
  assert.equal(g.profile, "p-work");
  const legacy = B.authFields({ instance: "abcdefgh-1", profile: "p-work", label: "Edge" }, "1.12.0");
  assert.deepEqual(legacy.profiles, ["p-work"]);
});

test("a legacy single binding reads as a one-element set", async () => {
  const state = await B.load(fakeStorage({ [B.PROFILE_KEY]: "p-old" }), { userAgent: EDGE_UA });
  assert.deepEqual(state.profiles, ["p-old"]);
  assert.equal(state.profile, "p-old");
});

test("the array wins over a stale legacy field; invalid ids and duplicates are dropped", async () => {
  const state = await B.load(
    fakeStorage({ [B.PROFILE_KEY]: "p-old", [B.PROFILE_IDS_KEY]: ["p-a", "../x", "p-b", " p-a "] }),
    { userAgent: EDGE_UA },
  );
  assert.deepEqual(state.profiles, ["p-a", "p-b"]);
  assert.equal(state.profile, "p-a");
});

test("an empty array means unbound even if the legacy field is stale", async () => {
  const state = await B.load(fakeStorage({ [B.PROFILE_KEY]: "p-old", [B.PROFILE_IDS_KEY]: [] }), { userAgent: EDGE_UA });
  assert.deepEqual(state.profiles, []);
  assert.equal(state.profile, "");
});

test("setBinding stores the set, the first id in the legacy field, and a trimmed label", async () => {
  const storage = fakeStorage();
  const out = await B.setBinding(storage, { profiles: [" p-work ", "p-home", "p-work"], label: "  Edge   Work " });
  assert.deepEqual(out, { profile: "p-work", profiles: ["p-work", "p-home"], label: "Edge Work" });
  assert.deepEqual(storage.data[B.PROFILE_IDS_KEY], ["p-work", "p-home"]);
  assert.equal(storage.data[B.PROFILE_KEY], "p-work");
});

test("setBinding with the legacy profile replaces the set with that one id", async () => {
  const storage = fakeStorage({ [B.PROFILE_IDS_KEY]: ["p-a", "p-b"], [B.PROFILE_KEY]: "p-a" });
  await B.setBinding(storage, { profile: "p-work" });
  assert.deepEqual(storage.data[B.PROFILE_IDS_KEY], ["p-work"]);
  assert.equal(storage.data[B.PROFILE_KEY], "p-work");
});

test("setBinding with only a label leaves the set alone", async () => {
  const storage = fakeStorage({ [B.PROFILE_IDS_KEY]: ["p-a", "p-b"], [B.PROFILE_KEY]: "p-a" });
  await B.setBinding(storage, { label: "Mine" });
  assert.deepEqual(storage.data[B.PROFILE_IDS_KEY], ["p-a", "p-b"]);
  assert.equal(storage.data[B.LABEL_KEY], "Mine");
});

test("setBinding refuses a path-like profile anywhere in the set and keeps the old set", async () => {
  const storage = fakeStorage({ [B.PROFILE_KEY]: "p-home", [B.PROFILE_IDS_KEY]: ["p-home"] });
  await assert.rejects(() => B.setBinding(storage, { profile: "../etc" }), /invalid profile/);
  await assert.rejects(() => B.setBinding(storage, { profiles: ["p-ok", "a/b"] }), /invalid profile/);
  await assert.rejects(() => B.setBinding(storage, { profiles: [42] }), /invalid profile/);
  assert.equal(storage.data[B.PROFILE_KEY], "p-home");
  assert.deepEqual(storage.data[B.PROFILE_IDS_KEY], ["p-home"]);
});

test("unbinding stores an empty set and leaves the label alone", async () => {
  const storage = fakeStorage({ [B.PROFILE_KEY]: "p-home", [B.PROFILE_IDS_KEY]: ["p-home"], [B.LABEL_KEY]: "Mine" });
  await B.setBinding(storage, { profiles: [] });
  assert.deepEqual(storage.data[B.PROFILE_IDS_KEY], []);
  assert.equal(storage.data[B.PROFILE_KEY], "");
  assert.equal(storage.data[B.LABEL_KEY], "Mine");
  await B.setBinding(storage, { profile: "" });
  assert.equal(storage.data[B.PROFILE_KEY], "");
});

test("choices: every profile listed, bound ones checked, an unknown bound id kept", () => {
  const profiles = [{ id: "p-home", name: "Personal" }, { id: "p-work", name: "Work" }];
  const c = B.choices(profiles, ["p-work", "p-home"]);
  assert.deepEqual(c.map((x) => x.checked), [true, true]);
  assert.deepEqual(B.choices(profiles, []).map((x) => x.checked), [false, false]);
  const stale = B.choices(profiles, ["p-gone"]);
  assert.equal(stale.find((o) => o.checked).id, "p-gone");
  assert.match(stale.find((o) => o.id === "p-gone").name, /no longer exists/);
});

test("binding frame carries the set and the first id; change detection sees the new key", () => {
  assert.deepEqual(
    B.bindingFrame({ profiles: ["p-work", "p-home"], label: "Edge" }),
    { kind: "binding", profile: "p-work", profiles: ["p-work", "p-home"], label: "Edge" },
  );
  assert.deepEqual(B.bindingFrame({ profile: "", label: "" }), { kind: "binding", profile: "", profiles: [], label: "" });
  assert.equal(B.isBindingChange({ [B.PROFILE_KEY]: {} }, "local"), true);
  assert.equal(B.isBindingChange({ [B.PROFILE_IDS_KEY]: {} }, "local"), true);
  assert.equal(B.isBindingChange({ [B.LABEL_KEY]: {} }, "local"), true);
  assert.equal(B.isBindingChange({ captureProfile: {} }, "local"), false);
  assert.equal(B.isBindingChange({ [B.PROFILE_IDS_KEY]: {} }, "session"), false);
});

test("the set is capped: reading drops the excess, setBinding refuses it and keeps the old set", async () => {
  const many = Array.from({ length: B.MAX_PROFILES + 5 }, (_, i) => `p-${i}`);
  const st = fakeStorage({ bridgeInstanceId: "abcdefgh-1", boundProfiles: many });
  assert.equal((await B.load(st, {})).profiles.length, B.MAX_PROFILES);
  const ok = fakeStorage({ boundProfiles: ["p-a"] });
  await assert.rejects(B.setBinding(ok, { profiles: many }), /too many/);
  assert.deepEqual(ok.data.boundProfiles, ["p-a"]);
});
