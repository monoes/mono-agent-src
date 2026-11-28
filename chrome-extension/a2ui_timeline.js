/**
 * MonoAgent — the chat's A2UI surfaces in order (after a2ui_model.js and
 * a2ui_render.js): where each one sits, what is saved, and the short note
 * under it. Adds `timeline` to MonoA2UIRender.
 */

(function (root) {
  "use strict";

  const R = root.MonoA2UIRender;
  const { mount, textOf, clip } = R;
  const isObj = (v) => v !== null && typeof v === "object" && !Array.isArray(v);

  /**
   * timeline: the chat's surfaces. A surface sits right after the message that was
   * last when its first event arrived, and is rebuilt from the saved events when
   * the panel reopens. `ingest` is fed every chat event; anything else is ignored.
   */
  const MAX_SAVED = 300;
  const MAX_JOURNAL = 200; // as many as chat.ui_journal takes

  function timeline(doc, opts) {
    const o = opts || {};
    const M = root.MonoA2UIModel;
    let store = M.newStore();
    let anchors = new Map(); // surfaceId -> message id it follows ("" = top)
    let saved = []; // events kept for the rebuild
    let journal = []; // journal turns already read
    const mounted = new Map(); // surfaceId -> { slot, ctl, note }
    const notes = new Map(); // surfaceId -> text of a refused/stale action

    function reset() {
      for (const m of mounted.values()) {
        m.ctl.destroy();
        m.slot.remove();
      }
      mounted.clear();
      notes.clear();
      store = M.newStore();
      anchors = new Map();
      saved = [];
      journal = [];
    }

    /** ingest folds one chat event ({seq, turn?, type, payload}); true when something to draw changed. */
    function ingest(ev, anchorId) {
      if (!ev) return false;
      if (ev.type === "ui.action") {
        const p = ev.payload || {};
        const id = typeof p.surfaceId === "string" ? p.surfaceId : "";
        if (!id || !store.surfaces.has(id)) return false;
        const text = p.outcome && p.outcome !== "accepted" ? clip(`${textOf(p.name)}: ${textOf(p.outcome)}${p.reason ? ` (${textOf(p.reason)})` : ""}`, 200) : "";
        return setNote(id, text);
      }
      if (ev.type !== "ui.surface") return false;
      const s = M.apply(store, ev);
      if (!s) return false;
      if (!anchors.has(s.id)) anchors.set(s.id, anchorId || "");
      saved.push({ turn: ev.turn || "", seq: ev.seq, at: ev.at, payload: ev.payload });
      // The oldest agent event goes first: agents cannot push a host card out.
      if (saved.length > MAX_SAVED) saved.splice(Math.max(0, saved.findIndex((e) => !(e.payload && e.payload.provenance === "system"))), 1);
      return true;
    }

    /** setNote shows a short line under a surface (a refused press); "" clears it. */
    function setNote(id, text) {
      const t = clip(textOf(text), 200);
      if ((notes.get(id) || "") === t) return false;
      if (t) notes.set(id, t);
      else notes.delete(id);
      return true;
    }

    /** place draws every surface into `log`, each after its anchor (rootOf(id) -> node), before later messages. */
    function place(log, empty, rootOf) {
      const lastAt = new Map();
      for (const [id, m] of mounted) {
        if (store.surfaces.has(id)) continue; // evicted to make room
        m.ctl.destroy();
        m.slot.remove();
        mounted.delete(id);
      }
      for (const [id, s] of store.surfaces) {
        let m = mounted.get(id);
        if (s.deleted) {
          if (m) {
            m.ctl.destroy();
            m.slot.remove();
            mounted.delete(id);
          }
          continue;
        }
        if (!m) {
          const slot = doc.createElement("div");
          slot.className = "chat-msg a2ui-slot";
          slot.dataset.role = "surface";
          slot.dataset.a2ui = "1";
          const ctl = mount(doc, s, o);
          // A live region from the start, so a note that arrives later is announced (alone, not the card).
          const note = doc.createElement("div");
          note.className = "a2ui-note";
          note.setAttribute("role", "status");
          slot.appendChild(ctl.el);
          slot.appendChild(note);
          m = { slot, ctl, note };
          mounted.set(id, m);
        } else m.ctl.sync(s);
        // Only on change: writing the same text again would announce it again.
        if (m.note.textContent !== (notes.get(id) || "")) m.note.textContent = notes.get(id) || "";
        const a = anchors.get(id) || "";
        const ref = lastAt.get(a) || (a && rootOf(a)) || empty;
        if (ref.nextSibling !== m.slot) log.insertBefore(m.slot, ref.nextSibling);
        lastAt.set(a, m.slot);
      }
    }

    const dump = (conversation) => ({ conversation, anchors: Object.fromEntries(anchors), events: saved, notes: Object.fromEntries(notes), journal });

    /** restore refolds saved events; only for the conversation they belong to. */
    function restore(data, conversation) {
      reset();
      if (!data || typeof data !== "object" || data.conversation !== conversation || !Array.isArray(data.events)) return;
      for (const ev of data.events) ingest({ type: "ui.surface", seq: ev && ev.seq, turn: ev && ev.turn, at: ev && ev.at, payload: ev && ev.payload }, "");
      if (Array.isArray(data.journal)) journal = data.journal.filter((t) => typeof t === "string").slice(-MAX_JOURNAL);
      const a = isObj(data.anchors) ? data.anchors : {};
      for (const id of store.surfaces.keys()) anchors.set(id, typeof a[id] === "string" ? a[id] : "");
      const n = isObj(data.notes) ? data.notes : {};
      for (const id of Object.keys(n)) if (store.surfaces.has(id) && typeof n[id] === "string") setNote(id, n[id]);
    }

    /** journalSeen / markJournal: which journal turns have been read, so they are not asked for again. */
    const journalSeen = () => journal.slice();
    function markJournal(turn) {
      if (journal.includes(turn)) return;
      journal.push(turn);
      if (journal.length > MAX_JOURNAL) journal.shift();
    }

    return { ingest, setNote, place, dump, restore, reset, journalSeen, markJournal, has: () => store.surfaces.size > 0 };
  }

  R.timeline = timeline;
})(typeof globalThis !== "undefined" ? globalThis : this);
