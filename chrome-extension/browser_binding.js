/**
 * MonoAgent Bridge — which monoagent profile this browser runs automations for
 *
 * Every browser profile with this extension runs its own copy of it, and the
 * bridge keeps one socket per copy. This file is what lets the bridge tell
 * them apart and route by profile:
 *
 *   INSTANCE ID. Minted once, by the service worker only (two contexts
 *   minting at once would race to two ids), kept in chrome.storage.local,
 *   which is per browser profile. It is how the bridge recognises the same
 *   browser across worker restarts.
 *
 *   BINDING. The SET of monoagent profiles whose browser actions run here
 *   (any number of profiles may share one browser), or empty for a default
 *   browser (runs profiles that have no browser of their own). Set in the
 *   side panel, or remotely with `monoagentcli extension bind/unbind/assign`
 *   (a set_binding command). Either way it lands in storage, and the worker
 *   tells the bridge with a kind:"binding" frame. The socket is never
 *   reconnected for it, because that would drop a run's in-flight commands.
 *
 *   Storage keeps two fields: boundProfiles (the array) and the legacy
 *   boundProfile (the first id, for bridges that only know one). An install
 *   that only has the legacy field is read as a one-element set.
 *
 * isValidProfileId is kept identical to profiledir.ValidProfileID (Go) and
 * MonoCaptureProfile.isValidProfileId: the id becomes a directory name on
 * the other side.
 */

(function (root) {
  "use strict";

  const INSTANCE_KEY = "bridgeInstanceId";
  const PROFILE_KEY = "boundProfile"; // legacy: the first id of the set
  const PROFILE_IDS_KEY = "boundProfiles";
  const LABEL_KEY = "browserLabel";
  const KEYS = [INSTANCE_KEY, PROFILE_KEY, PROFILE_IDS_KEY, LABEL_KEY];
  const MAX_LABEL = 60;
  const MAX_PROFILES = 64; // keep identical to maxProfilesPerBrowser (Go)
  const MAX_PROFILE_ID = 128; // keep identical to maxProfileIDLen (Go)
  const INSTANCE_RE = /^[A-Za-z0-9-]{8,64}$/;

  function isValidProfileId(id) {
    const value = typeof id === "string" ? id.trim() : "";
    if (!value) return false;
    if (value.includes("/") || value.includes("\\")) return false;
    if (value.includes("..")) return false;
    return true;
  }

  /** bindable is isValidProfileId plus the length cap a binding enforces. */
  function bindable(id) {
    return isValidProfileId(id) && id.trim().length <= MAX_PROFILE_ID;
  }

  /** cleanIds trims, drops invalid ids and duplicates, keeps order. */
  function cleanIds(list) {
    const out = [];
    for (const v of Array.isArray(list) ? list : []) {
      if (!bindable(v)) continue;
      const id = v.trim();
      if (!out.includes(id)) out.push(id);
      if (out.length >= MAX_PROFILES) break;
    }
    return out;
  }

  /**
   * storedIds reads the set: the array if present, else the legacy field.
   * This version writes both in lock-step (legacy = the first id). A legacy
   * field that disagrees with the array's first id was written by a pre-sets
   * extension after a downgrade, so it is the newer of the two and the stale
   * array is discarded; otherwise removed profiles would come back.
   */
  function storedIds(stored) {
    const legacy = stored[PROFILE_KEY];
    if (Array.isArray(stored[PROFILE_IDS_KEY])) {
      const ids = cleanIds(stored[PROFILE_IDS_KEY]);
      const newer = typeof legacy === "string" && legacy.trim() !== (ids[0] || "");
      if (!newer) return ids;
    }
    return cleanIds([legacy]);
  }

  function browserName(ua) {
    const s = String(ua || "");
    if (/Edg\//.test(s)) return "Edge";
    if (/OPR\//.test(s)) return "Opera";
    if (/Chrome\//.test(s)) return "Chrome";
    return "Browser";
  }

  function cleanLabel(s) {
    const v = String(s || "").replace(/\s+/g, " ").trim();
    return v.length > MAX_LABEL ? v.slice(0, MAX_LABEL) : v;
  }

  function defaultLabel(ua, instance) {
    return `${browserName(ua)} ${String(instance || "").slice(0, 4)}`.trim();
  }

  /**
   * load reads this browser's binding. opts: { crypto, userAgent, create }.
   * Only the worker passes create:true; the side panel reads what the worker
   * made (and shows no id until it has).
   */
  async function load(storage, opts) {
    const o = opts || {};
    let stored = {};
    try {
      stored = (await storage.get(KEYS)) || {};
    } catch {
      // No storage: an unbound browser with a fresh id is still routable.
    }
    let instance = typeof stored[INSTANCE_KEY] === "string" ? stored[INSTANCE_KEY] : "";
    if (!INSTANCE_RE.test(instance)) {
      instance = "";
      if (o.create && o.crypto && typeof o.crypto.randomUUID === "function") {
        instance = o.crypto.randomUUID();
        try {
          await storage.set({ [INSTANCE_KEY]: instance });
        } catch {
          // Kept for this worker's life; the next start mints another.
        }
      }
    }
    const profiles = storedIds(stored);
    const label = cleanLabel(stored[LABEL_KEY]) || defaultLabel(o.userAgent, instance);
    return { instance, profile: profiles[0] || "", profiles, label };
  }

  function profileList(state) {
    if (Array.isArray(state.profiles)) return cleanIds(state.profiles);
    return cleanIds([state.profile]);
  }

  /** authFields is what the worker adds to its auth frame. */
  function authFields(state, version) {
    const out = { instance: state.instance, label: state.label, version: String(version || "") };
    const profiles = profileList(state);
    // profiles for this bridge; profile (the first) for bridges that only
    // know one binding per browser.
    if (profiles.length) {
      out.profiles = profiles;
      out.profile = profiles[0];
    }
    return out;
  }

  /**
   * setBinding stores {profiles?, profile?, label?}. profiles (an array)
   * replaces the whole set; the legacy profile (a string) replaces it with
   * that one id. An empty set unbinds. With neither, the set is untouched.
   */
  async function setBinding(storage, params) {
    const p = params || {};
    let ids = null;
    if (Array.isArray(p.profiles)) {
      if (p.profiles.length > MAX_PROFILES) throw new Error("too many profiles");
      for (const v of p.profiles) {
        if (typeof v !== "string" || !bindable(v)) throw new Error("invalid profile id");
      }
      ids = cleanIds(p.profiles);
    } else if (typeof p.profile === "string") {
      if (p.profile.trim() && !bindable(p.profile)) throw new Error("invalid profile id");
      ids = cleanIds([p.profile]);
    }
    const update = {};
    if (ids) {
      update[PROFILE_IDS_KEY] = ids;
      update[PROFILE_KEY] = ids[0] || "";
    }
    if (typeof p.label === "string") update[LABEL_KEY] = cleanLabel(p.label);
    await storage.set(update);
    return { profile: update[PROFILE_KEY], profiles: update[PROFILE_IDS_KEY], label: update[LABEL_KEY] };
  }

  function bindingFrame(state) {
    const profiles = profileList(state);
    return { kind: "binding", profile: profiles[0] || "", profiles, label: state.label || "" };
  }

  function isBindingChange(changes, area) {
    return area === "local" && !!changes && (PROFILE_KEY in changes || PROFILE_IDS_KEY in changes || LABEL_KEY in changes);
  }

  /** choices is what the side panel's checklist offers: every profile, ticked
   *  when bound here. A bound id the list no longer has stays, so it can be
   *  unticked. */
  function choices(profiles, bound) {
    const ids = cleanIds(bound);
    const list = [];
    const seen = new Set();
    for (const p of Array.isArray(profiles) ? profiles : []) {
      if (!p || !isValidProfileId(p.id) || seen.has(p.id)) continue;
      seen.add(p.id);
      list.push({ id: p.id, name: p.name || p.id, checked: ids.includes(p.id) });
    }
    for (const id of ids) {
      if (!seen.has(id)) list.push({ id, name: `${id} (no longer exists)`, checked: true });
    }
    return list;
  }

  root.MonoBrowserBinding = {
    INSTANCE_KEY, PROFILE_KEY, PROFILE_IDS_KEY, LABEL_KEY, MAX_LABEL, MAX_PROFILES, MAX_PROFILE_ID,
    isValidProfileId, browserName, cleanLabel, defaultLabel,
    cleanIds, load, authFields, setBinding, bindingFrame, isBindingChange, choices,
  };
})(globalThis);
