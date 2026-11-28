/**
 * MonoAgent — the chat's A2UI surfaces in order (after a2ui_model.js and
 * a2ui_render.js): where each one sits, what is saved, and the short note
 * under it. Adds `timeline` to MonoA2UIRender.
 *
 * Two or more cards of one turn that wait for the person are a set (#1803): none
 * sends alone, and one bar under them sends them all with `onSet`, once each is
 * answered (or, an agent's card, skipped). What was typed or picked is saved
 * with the cards and never sent by itself; a card's version ("turn:seq" of its
 * last event) goes with each press, and a pick made on an earlier one is dropped.
 */

(function (root) {
  "use strict";

  const R = root.MonoA2UIRender;
  const M = () => root.MonoA2UIModel;
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
    let store = M().newStore();
    let anchors = new Map(); // surfaceId -> message id it follows ("" = top)
    let saved = []; // events kept for the rebuild
    let journal = []; // journal turns already read
    const mounted = new Map(); // surfaceId -> { slot, ctl, note }
    const notes = new Map(); // surfaceId -> text of a refused/stale action
    let turns = new Map(); // surfaceId -> the turn that made it: the set it belongs to
    let settled = new Set(); // agent cards the host accepted a press of
    let drafts = new Map(); // surfaceId -> {turn, values: {path: {value, meaning, was}}, picked}
    let revs = new Map(); // surfaceId -> its version
    const bars = new Map(); // turn -> {el, count, text, button}
    const sending = new Set(); // turns whose set is going out
    const edited = () => typeof o.onDirty === "function" && o.onDirty();

    function reset() {
      for (const m of mounted.values()) {
        m.ctl.destroy();
        m.slot.remove();
      }
      mounted.clear();
      for (const b of bars.values()) b.el.remove();
      bars.clear();
      sending.clear();
      turns = new Map();
      settled = new Set();
      drafts = new Map();
      revs = new Map();
      notes.clear();
      store = M().newStore();
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
        if (p.outcome === "accepted" && store.surfaces.get(id).provenance === "agent") {
          settled.add(id);
          drafts.delete(id);
        }
        const text = p.outcome && p.outcome !== "accepted" ? clip(`${textOf(p.name)}: ${textOf(p.outcome)}${p.reason ? ` (${textOf(p.reason)})` : ""}`, 200) : "";
        return setNote(id, text);
      }
      if (ev.type !== "ui.surface") return false;
      const s = M().apply(store, ev);
      if (!s) return false;
      revs.set(s.id, `${ev.turn || ""}:${ev.seq}`);
      const msg = ev.payload && ev.payload.message;
      if (isObj(msg) && msg.createSurface) {
        const turn = String(ev.turn || "");
        // The host's own journal turns redraw a card of an earlier turn: it stays in that set.
        if (!turns.has(s.id) || !turn.startsWith("system-")) turns.set(s.id, turn);
        settled.delete(s.id);
        if (drafts.has(s.id) && drafts.get(s.id).turn !== turns.get(s.id)) drafts.delete(s.id);
      }
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

    const draftOf = (id) => {
      let d = drafts.get(id);
      if (!d) drafts.set(id, (d = { turn: turns.get(id) || "", values: {}, picked: null }));
      return d;
    };
    /** mountCard draws a surface with what the person had typed and picked on it. */
    function mountCard(id, s) {
      const d = drafts.get(id);
      return mount(doc, s, Object.assign({}, o, {
        picked: d && d.picked,
        typed: d && Object.assign({}, d.values),
        revision: () => revs.get(id) || "",
        onEdit: (path, entry) => {
          if (entry) draftOf(id).values[path] = entry;
          else delete draftOf(id).values[path];
          edited();
        },
        onPick: (picked) => {
          draftOf(id).picked = picked;
          edited();
        },
        onChange: () => sets(),
      }));
    }

    /** waitingSets: the mounted cards that wait for the person, by the turn that made them. */
    function waitingSets() {
      const groups = new Map();
      for (const [id, m] of mounted) {
        const w = settled.has(id) ? null : m.ctl.waiting();
        if (!w) {
          m.ctl.setGrouped(false, false);
          continue;
        }
        const turn = turns.get(id) || "";
        if (!groups.has(turn)) groups.set(turn, []);
        groups.get(turn).push({ id, m, w });
      }
      return groups;
    }

    /** send hands a whole set to onSet; anything missing sends nothing. */
    function send(turn) {
      const cards = waitingSets().get(turn) || [];
      if (sending.has(turn) || cards.length < 2) return;
      const parts = cards.map((c) => c.m.ctl.collect());
      if (parts.some((p) => !p) || typeof o.onSet !== "function") return;
      const actions = parts.filter((p) => p.action).map((p) => p.action);
      const skipped = parts.filter((p) => p.skip).map((p) => p.skip);
      if (!actions.length) return;
      sending.add(turn);
      sets();
      const over = () => {
        sending.delete(turn);
        sets();
      };
      Promise.resolve(o.onSet(Object.assign({ version: "1.0", actions }, skipped.length ? { skipped } : {}))).then(over, over);
    }

    function newBar(turn) {
      const el = doc.createElement("div");
      el.className = "chat-msg a2ui-setbar";
      el.dataset.a2ui = "1";
      const count = doc.createElement("div");
      count.className = "a2ui-setcount";
      const text = doc.createElement("div");
      text.className = "a2ui-status";
      text.setAttribute("role", "status");
      const button = doc.createElement("button");
      button.type = "button";
      button.className = "a2ui-btn a2ui-btn-primary";
      button.textContent = "Send all";
      button.addEventListener("click", () => {
        if (button.getAttribute("aria-disabled") !== "true") send(turn);
      });
      el.appendChild(count);
      el.appendChild(text);
      el.appendChild(button);
      return { el, count, text, button };
    }

    const write = (el, text) => {
      if (el.textContent !== text) el.textContent = text;
    };

    /** sets marks the cards that must go out together and keeps one bar under each set. */
    function sets() {
      const groups = waitingSets();
      for (const [turn, bar] of bars) {
        if ((groups.get(turn) || []).length > 1) continue;
        bar.el.remove();
        bars.delete(turn);
      }
      for (const [turn, cards] of groups) {
        const many = cards.length > 1;
        const busy = sending.has(turn);
        for (const c of cards) c.m.ctl.setGrouped(many, many && busy);
        if (!many) continue;
        let bar = bars.get(turn);
        if (!bar) bars.set(turn, (bar = newBar(turn)));
        const ready = cards.filter((c) => c.w.ready).length;
        const nothing = cards.every((c) => c.w.skipped);
        write(bar.count, `${cards.length} cards are waiting. Nothing is sent until you answer them all and send them together.`);
        write(bar.text, busy ? "Sending…" : nothing ? "Everything is skipped: nothing to send" : `${ready} of ${cards.length} ready`);
        bar.button.setAttribute("aria-disabled", String(busy || nothing || ready < cards.length));
        const last = cards[cards.length - 1].m.slot;
        const holder = last.parentNode || last.parent; // parent: the tests' document
        if (holder && last.nextSibling !== bar.el) holder.insertBefore(bar.el, last.nextSibling);
      }
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
          const ctl = mountCard(id, s);
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
        // A set's bar is not a card: the cards keep their order around it.
        let next = ref.nextSibling;
        while (next && next !== m.slot && String(next.className).includes("a2ui-setbar")) next = next.nextSibling;
        if (next !== m.slot) log.insertBefore(m.slot, next);
        lastAt.set(a, m.slot);
      }
      sets();
    }

    const dump = (conversation) => ({
      conversation, anchors: Object.fromEntries(anchors), events: saved, notes: Object.fromEntries(notes), journal,
      settled: [...settled], drafts: Object.fromEntries(drafts),
    });

    /** restore refolds saved events; only for the conversation they belong to. */
    function restore(data, conversation) {
      reset();
      if (!data || typeof data !== "object" || data.conversation !== conversation || !Array.isArray(data.events)) return;
      for (const ev of data.events) ingest({ type: "ui.surface", seq: ev && ev.seq, turn: ev && ev.turn, at: ev && ev.at, payload: ev && ev.payload }, "");
      if (Array.isArray(data.journal)) journal = data.journal.filter((t) => typeof t === "string").slice(-MAX_JOURNAL);
      const a = isObj(data.anchors) ? data.anchors : {};
      for (const id of store.surfaces.keys()) anchors.set(id, typeof a[id] === "string" ? a[id] : "");
      if (Array.isArray(data.settled)) for (const id of data.settled) if (store.surfaces.has(id)) settled.add(id);
      const d = isObj(data.drafts) ? data.drafts : {};
      for (const id of Object.keys(d)) {
        const x = d[id];
        if (store.surfaces.has(id) && isObj(x) && x.turn === (turns.get(id) || "") && isObj(x.values)) drafts.set(id, { turn: x.turn, values: Object.assign({}, x.values), picked: isObj(x.picked) ? x.picked : null });
      }
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
