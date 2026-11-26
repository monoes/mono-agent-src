/**
 * MonoAgent — A2UI surface state (A2UI v1.0, https://a2ui.org/specification/v1.0-a2ui/)
 *
 * A pure fold: `ui.surface` chat events, applied in seq order per surface,
 * become one state object per surfaceId. No DOM here; a2ui_render.js draws it.
 * The desktop app has a React-side twin of this fold; the semantics match the spec:
 *
 *   createSurface     {surfaceId, components?, dataModel?}  (re)starts the surface
 *   updateComponents  {surfaceId, components}               merge by component id
 *   updateDataModel   {surfaceId, path?, value?}            JSON Pointer set;
 *                                                           absent/null value deletes;
 *                                                           "/" (or no path) replaces the model
 *   deleteSurface     {surfaceId}                           removes it
 *
 * Replaying an event already applied (same turn and seq) changes nothing, so the
 * live stream and a chat.events replay may overlap. Unknown message kinds and
 * components never throw: the model keeps them, the renderer shows a placeholder.
 *
 * The data model is untrusted JSON. Every object in it is a null-prototype copy
 * with the keys __proto__, constructor and prototype dropped, and pointer paths
 * naming those segments are refused, so nothing here can pollute a prototype.
 */

(function (root) {
  "use strict";

  const BANNED = new Set(["__proto__", "constructor", "prototype"]);
  const MAX_DEPTH = 24; // data model nesting kept
  const MAX_KEYS = 500; // keys per object kept
  const MAX_SEEN = 2000; // applied (turn, seq) keys remembered
  const MAX_COMPONENTS = 500; // components kept per surface (the renderer draws far fewer)
  const MAX_SURFACES = 20; // of the host's, and as many of the agents'
  const SYSTEM_ID = /^(hil|send|ask|mission)-/; // the host's card ids (internal/a2ui SystemSurfacePrefixes)

  const hasOwn = (o, k) => Object.prototype.hasOwnProperty.call(o, k);
  const isObj = (v) => v !== null && typeof v === "object" && !Array.isArray(v);
  const str = (v) => (typeof v === "string" ? v : "");

  /** clean copies untrusted JSON into null-prototype objects, dropping banned keys and over-deep or over-wide parts. */
  function clean(v, depth) {
    if (v === null || typeof v === "string" || typeof v === "boolean") return v;
    if (typeof v === "number") return Number.isFinite(v) ? v : null;
    if (depth >= MAX_DEPTH) return null;
    if (Array.isArray(v)) return v.slice(0, MAX_KEYS).map((x) => clean(x, depth + 1));
    if (typeof v === "object") {
      const out = Object.create(null);
      let n = 0;
      for (const k of Object.keys(v)) {
        if (BANNED.has(k)) continue;
        if (++n > MAX_KEYS) break;
        out[k] = clean(v[k], depth + 1);
      }
      return out;
    }
    return null;
  }

  /** pointer splits a JSON Pointer; null when it is malformed or names a banned segment. */
  function pointer(path) {
    if (path === undefined || path === null || path === "" || path === "/") return [];
    if (typeof path !== "string" || path[0] !== "/") return null;
    const parts = path
      .slice(1)
      .split("/")
      .map((s) => s.replace(/~1/g, "/").replace(/~0/g, "~"));
    for (const p of parts) if (BANNED.has(p)) return null;
    return parts.length > MAX_DEPTH ? null : parts;
  }

  /** getAt reads a path out of a data model; undefined when it is not there. Own properties only. */
  function getAt(model, path) {
    const parts = pointer(path);
    if (!parts) return undefined;
    let cur = model;
    for (const p of parts) {
      if (Array.isArray(cur)) {
        if (!/^\d+$/.test(p)) return undefined;
        cur = cur[Number(p)];
      } else if (isObj(cur) && hasOwn(cur, p)) cur = cur[p];
      else return undefined;
    }
    return cur;
  }

  /**
   * setAt writes `value` at `path` and returns the new model (the root changes
   * for "/"). value undefined or null deletes. A refused path changes nothing.
   */
  function setAt(model, path, value) {
    const parts = pointer(path);
    if (!parts) return model;
    if (!parts.length) return value === undefined || value === null ? Object.create(null) : clean(value, 0);
    let cur = isObj(model) ? model : Object.create(null);
    const rootObj = cur;
    for (let i = 0; i < parts.length - 1; i++) {
      const p = parts[i];
      let next = Array.isArray(cur) ? cur[Number(p)] : hasOwn(cur, p) ? cur[p] : undefined;
      if (!isObj(next) && !Array.isArray(next)) {
        next = Object.create(null);
        if (Array.isArray(cur)) {
          if (!/^\d+$/.test(p) || Number(p) > cur.length) return rootObj;
          cur[Number(p)] = next;
        } else cur[p] = next;
      }
      cur = next;
    }
    const last = parts[parts.length - 1];
    const del = value === undefined || value === null;
    if (Array.isArray(cur)) {
      if (!/^\d+$/.test(last) || Number(last) > cur.length) return rootObj;
      if (del) cur.splice(Number(last), 1);
      else cur[Number(last)] = clean(value, parts.length);
    } else if (del) delete cur[last];
    else cur[last] = clean(value, parts.length);
    return rootObj;
  }

  /** newStore: surfaces by id, in order of first appearance. */
  function newStore() {
    return { surfaces: new Map(), seen: new Set(), loose: new Map(), seenOrder: [] };
  }

  function newSurface(id, meta) {
    return {
      id,
      provenance: meta.provenance === "system" ? "system" : "agent",
      agentId: str(meta.agentId),
      ref: meta.ref && typeof meta.ref === "object" ? { kind: str(meta.ref.kind), id: str(meta.ref.id) } : null,
      components: Object.create(null),
      dataModel: Object.create(null),
      structVersion: 0, // changes when components change: the renderer rebuilds
      dataVersion: 0, // changes when the data model changes: the renderer refreshes bindings
      deleted: false,
      at: 0, // when the newest event applied to it was journaled
    };
  }

  function mergeComponents(s, list) {
    if (!Array.isArray(list)) return;
    let held = Object.keys(s.components).length;
    // One message is read up to the cap, however long its list is.
    for (const c of list.slice(0, MAX_COMPONENTS)) {
      if (!isObj(c) || typeof c.id !== "string" || !c.id || BANNED.has(c.id)) continue;
      if (!hasOwn(s.components, c.id)) {
        if (held >= MAX_COMPONENTS) continue;
        held++;
      }
      s.components[c.id] = clean(c, 0);
    }
    s.structVersion++;
  }

  /** digest is a short fingerprint of a message (length and FNV-1a), so the replay memory stays small. */
  function digest(text) {
    let h = 0x811c9dc5;
    for (let i = 0; i < text.length; i++) h = Math.imul(h ^ text.charCodeAt(i), 0x01000193);
    return `${text.length}:${h >>> 0}`;
  }

  function messageOf(payload) {
    let m = payload && payload.message;
    if (typeof m === "string") {
      try {
        m = JSON.parse(m);
      } catch {
        return null;
      }
    }
    return isObj(m) ? m : null;
  }

  /**
   * makeRoom keeps the surfaces of one provenance under their cap: past it the
   * oldest deleted one of that provenance goes, else the oldest without a button
   * (a settled notice). Agent surfaces never take a host card's place.
   */
  function makeRoom(store, provenance) {
    const mine = [...store.surfaces].filter(([, s]) => s.provenance === provenance);
    if (mine.length < MAX_SURFACES) return true;
    const settled = (s) => !Object.keys(s.components).some((k) => s.components[k].component === "Button");
    for (const test of [(s) => s.deleted, settled]) {
      for (const [id, s] of mine) if (test(s)) return store.surfaces.delete(id);
    }
    return false;
  }

  /**
   * allowed says whether an event may touch surface s (undefined: none). The
   * host may replace anything. An agent never names a host card, and touches
   * only its own live surfaces: same provenance, same author. An event that does
   * not say it is the host's is an agent's.
   */
  function allowed(s, id, payload) {
    if (payload.provenance === "system") return true;
    if (SYSTEM_ID.test(id)) return false;
    if (!s || s.deleted) return true;
    return s.provenance === "agent" && s.agentId === str(payload.agentId);
  }

  /**
   * apply folds one event ({seq, turn?, payload}) into the store. Returns the
   * surface it changed, or null when nothing changed (a replay, a message for
   * a surface we do not hold, a malformed one). Never throws.
   */
  function apply(store, ev) {
    try {
      if (!ev || !Number.isFinite(ev.seq)) return null;
      const p = ev.payload || {};
      // Seen before? Exact (turn, seq); or the same seq and payload where one side did not
      // know its turn (a live frame without it against its replay): applied once either way.
      const key = `${str(ev.turn)}:${ev.seq}`;
      const loose = `${ev.seq}|${digest(JSON.stringify(p.message) || "")}`;
      const was = store.loose.get(loose);
      if (store.seen.has(key) || (was !== undefined && (was === "" || !ev.turn))) return null;
      store.seen.add(key);
      store.loose.set(loose, str(ev.turn));
      store.seenOrder.push([key, loose]);
      if (store.seenOrder.length > MAX_SEEN) {
        const [k, l] = store.seenOrder.shift();
        store.seen.delete(k);
        store.loose.delete(l);
      }
      const msg = messageOf(p);
      if (!msg) return null;
      const kind = ["createSurface", "updateComponents", "updateDataModel", "deleteSurface"].find((k) => isObj(msg[k]));
      if (!kind) return null;
      const body = msg[kind];
      const id = str(body.surfaceId) || str(p.surfaceId);
      if (!id || BANNED.has(id)) return null;
      let s = store.surfaces.get(id);
      if (!allowed(s, id, p)) return null;
      // Journal turns are read after the live stream: one older than what the surface
      // already shows must not take it back.
      const at = Date.parse(ev.at) || 0;
      if (s && at && s.at && at < s.at) return null;
      if (kind === "createSurface") {
        if (!s && !makeRoom(store, p.provenance === "system" ? "system" : "agent")) return null;
        s = newSurface(id, p);
        s.at = at;
        store.surfaces.set(id, s);
        mergeComponents(s, body.components);
        if (body.dataModel !== undefined) s.dataModel = setAt(null, "/", body.dataModel);
        s.dataVersion++;
        return s;
      }
      if (!s || s.deleted) return null;
      if (at) s.at = at;
      if (kind === "updateComponents") mergeComponents(s, body.components);
      else if (kind === "updateDataModel") {
        s.dataModel = setAt(s.dataModel, typeof body.path === "string" ? body.path : "/", body.value);
        s.dataVersion++;
      } else {
        s.deleted = true; // kept, with its time, until room is needed
        s.components = Object.create(null);
        s.structVersion++;
      }
      return s;
    } catch {
      return null;
    }
  }

  /** writeLocal is a person's edit of a field: the local data model only; nothing is sent. */
  function writeLocal(surface, path, value) {
    surface.dataModel = setAt(surface.dataModel, path, value);
    surface.dataVersion++;
  }

  root.MonoA2UIModel = { apply, newStore, getAt, setAt, pointer, writeLocal, clean, BANNED };
})(typeof globalThis !== "undefined" ? globalThis : this);
