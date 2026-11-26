/**
 * MonoAgent — draws an A2UI surface (a2ui_model.js state) in the side panel.
 *
 * Built with createElement / textContent only: no HTML strings, no links, no
 * images. Every value, URLs included, is plain text. Catalog (internal/a2ui/catalog.go):
 * Text, Row, Column, Card, Divider, Button, TextField, CheckBox, ChoicePicker,
 * Progress, Badge. Anything else is a muted placeholder.
 *
 * Fields (TextField, CheckBox, ChoicePicker) change the LOCAL data model only.
 * A Button builds the A2UI action envelope and hands it to `onAction`:
 *
 *   {"version":"1.0","action":{name, surfaceId, sourceComponentId, timestamp, context}}
 *
 * Accessibility: each surface is a named region that is not itself live; its
 * status line (after a press) and the note under it announce on their own.
 * A pressed surface's buttons are aria-disabled (they keep focus) with the
 * reason in text. Badge tones carry a word and a mark, not only a colour.
 * Strings are English: the extension has no i18n.
 *
 * Caps: 200 drawn nodes and 200 component visits (repeats and missing ids count),
 * each component drawn at most once, depth 12, text clipped; a surface that cannot be drawn
 * becomes one error card instead of throwing.
 *
 * Use:  const m = MonoA2UIRender.mount(document, surface, { onAction });
 *       parent.appendChild(m.el);  ...  m.sync(surface) after each model change.
 */

(function (root) {
  "use strict";

  const Model = () => root.MonoA2UIModel;
  const MAX_NODES = 200;
  const MAX_DEPTH = 12;
  const MAX_TEXT = 4000;
  const MAX_OPTIONS = 20;
  const REENABLE_MS = 3000;

  const isObj = (v) => v !== null && typeof v === "object" && !Array.isArray(v);
  const oneOf = (v, list, dflt) => (list.includes(v) ? v : dflt);
  const clip = (s, n) => (s.length > n ? s.slice(0, n) + "…" : s);
  const SENT = "Sent. The buttons work again in a few seconds.";
  const TONE = { info: ["i", "Note"], success: ["✓", "Success"], warning: ["!", "Warning"], danger: ["✕", "Problem"] };
  const FLEX = { start: "flex-start", center: "center", end: "flex-end", stretch: "stretch", spaceBetween: "space-between", spaceAround: "space-around", spaceEvenly: "space-evenly" };
  let uid = 0;

  /** text turns a bound or literal value into display text; objects show nothing. */
  function textOf(v) {
    if (typeof v === "string") return clip(v, MAX_TEXT);
    if (typeof v === "number" || typeof v === "boolean") return String(v);
    return "";
  }

  /**
   * fieldText is a text field's value. Unlike textOf it is never clipped: the
   * whole of it is what a button sends, so the whole of it is shown.
   */
  const fieldText = (v) => (typeof v === "string" ? v : typeof v === "number" || typeof v === "boolean" ? String(v) : "");

  /**
   * grow makes a text box as tall as its text. The field keeps its height while
   * the box is measured, so the log does not jump on each key.
   */
  function grow(el) {
    const holder = el.parentElement && el.parentElement.style ? el.parentElement : null;
    const held = holder ? holder.style.minHeight : "";
    if (holder && holder.offsetHeight) holder.style.minHeight = `${holder.offsetHeight}px`;
    el.style.height = "auto";
    // 0 while the card is not shown: the width observer measures it once it is.
    if (el.scrollHeight) el.style.height = `${el.scrollHeight + el.offsetHeight - el.clientHeight + 1}px`;
    if (holder) holder.style.minHeight = held;
  }

  function mount(doc, surface, opts) {
    const o = opts || {};
    const setT = o.setTimeout || ((fn, ms) => setTimeout(fn, ms));
    const clearT = o.clearTimeout || ((t) => clearTimeout(t));
    const now = o.now || (() => new Date().toISOString());
    const wrap = doc.createElement("div");
    wrap.className = "a2ui";
    wrap.setAttribute("role", "region");
    // The chat log is a live region; the card inside it must not be read out again on every change.
    wrap.setAttribute("aria-live", "off");
    let status = null; // says why the buttons are unavailable
    let s = surface;
    let binds = []; // closures that refresh a bound value after the model changed
    let buttons = []; // the surface's button elements
    let timer = null; // re-enables them after a press
    let seen = { struct: -1, data: -1 };
    let count = 0;
    let visits = 0; // component references followed, whatever they led to
    let drawn = new Set(); // ids already followed: a repeated reference draws nothing
    let localData = -1; // dataVersion after our own last edit: not a reason to re-enable buttons

    // The card's text boxes show their whole text: measured on every change of
    // value, when their width changes (the first layout too) and when a font arrives.
    const Observer = o.ResizeObserver || root.ResizeObserver;
    const frame = o.requestAnimationFrame || (root.requestAnimationFrame ? (fn) => root.requestAnimationFrame(fn) : (fn) => setT(fn, 0));
    let boxes = [];
    let observer = null;
    const widths = new Map();
    const growAll = () => boxes.forEach(grow);
    if (doc.fonts && doc.fonts.addEventListener) doc.fonts.addEventListener("loadingdone", growAll);
    function watch(el) {
      boxes.push(el);
      if (!Observer) return;
      if (!observer) {
        observer = new Observer((entries) => {
          for (const { target } of entries) {
            if (widths.get(target) === target.clientWidth) continue;
            widths.set(target, target.clientWidth);
            // A frame later: resizing inside the observer's callback is a loop error.
            frame(() => grow(target));
          }
        });
      }
      observer.observe(el);
    }
    function unwatch() {
      if (observer) observer.disconnect();
      widths.clear();
      boxes = [];
    }

    const mk = (tag, cls) => {
      const e = doc.createElement(tag);
      if (cls) e.className = cls;
      count++;
      return e;
    };
    /** named gives an element an id, for aria-describedby. */
    const named = (e) => {
      if (!e.id) e.id = `a2ui-${++uid}`;
      return e.id;
    };
    /** press marks the whole surface as sent (or not): one press, one answer. */
    function setSent(sent) {
      for (const b of buttons) {
        if (sent) {
          b.setAttribute("aria-disabled", "true");
          b.setAttribute("aria-describedby", named(status));
        } else {
          b.removeAttribute("aria-disabled");
          b.removeAttribute("aria-describedby");
        }
      }
      if (status) status.textContent = sent ? SENT : "";
      if (!sent) clearT(timer);
    }
    const muted = (text) => {
      const e = mk("span", "a2ui-muted");
      e.textContent = text;
      return e;
    };

    /** dyn reads a literal or {"@path"} value from the model. */
    function dyn(v) {
      if (isObj(v) && typeof v["@path"] === "string") return Model().getAt(s.dataModel, v["@path"]);
      return v;
    }
    const pathOf = (v) => (isObj(v) && typeof v["@path"] === "string" ? v["@path"] : "");

    /** local keeps a field's value: in the model when it is bound, in a closure when it is a literal. */
    function field(v) {
      const path = pathOf(v);
      let lit = path ? undefined : v;
      return {
        get: () => (path ? dyn(v) : lit),
        set: (x) => {
          if (path) {
            Model().writeLocal(s, path, x);
            localData = s.dataVersion;
            seen.data = s.dataVersion;
            for (const b of binds) b();
          } else lit = x;
        },
      };
    }

    function bindText(el, v) {
      const paint = () => {
        el.textContent = textOf(dyn(v));
      };
      paint();
      if (pathOf(v)) binds.push(paint);
    }

    function build(id, depth) {
      if (count >= MAX_NODES || visits >= MAX_NODES) return null;
      visits++;
      if (drawn.has(id)) return null;
      drawn.add(id);
      const c = Object.prototype.hasOwnProperty.call(s.components, id) ? s.components[id] : null;
      if (!c) return null;
      if (depth > MAX_DEPTH) return muted("…");
      const draw = DRAW[c.component];
      if (!draw) return muted(`Unsupported: ${clip(textOf(c.component) || "?", 40)}`);
      const e = draw(c, depth, id);
      if (e) e.dataset.cid = id;
      return e;
    }

    function kids(parent, ids, depth) {
      if (!Array.isArray(ids)) return;
      let before = null; // the text just above a field says what the field is about
      for (const id of ids.slice(0, 100)) {
        if (typeof id !== "string") continue;
        const k = build(id, depth + 1);
        if (!k) continue;
        parent.appendChild(k);
        const type = Object.prototype.hasOwnProperty.call(s.components, id) ? s.components[id].component : "";
        if (before && (type === "ChoicePicker" || type === "TextField")) (k.field || k).setAttribute("aria-describedby", named(before));
        before = type === "Text" ? k : null;
      }
    }

    function box(cls) {
      return (c, depth) => {
        const e = mk("div", cls);
        e.style.justifyContent = FLEX[oneOf(c.justify, Object.keys(FLEX), "start")];
        e.style.alignItems = FLEX[oneOf(c.align, ["start", "center", "end", "stretch"], "stretch")];
        kids(e, c.children, depth);
        return e;
      };
    }

    function labelled(labelProp, input, cls) {
      const l = mk("label", cls);
      const t = mk("span", "a2ui-label");
      bindText(t, labelProp);
      l.appendChild(t);
      l.appendChild(input);
      if (!t.textContent) input.setAttribute("aria-label", input.placeholder || "Field");
      return l;
    }

    const DRAW = {
      Text(c) {
        const e = mk("p", `a2ui-text a2ui-${oneOf(c.variant, ["caption", "body"], "body")}`);
        bindText(e, c.text);
        return e;
      },
      Row: box("a2ui-row"),
      Column: box("a2ui-col"),
      Card(c, depth) {
        const e = mk("div", "a2ui-card");
        if (typeof c.child === "string") {
          const k = build(c.child, depth + 1);
          if (k) e.appendChild(k);
        }
        return e;
      },
      Divider(c) {
        const e = mk("hr", `a2ui-divider a2ui-${oneOf(c.axis, ["horizontal", "vertical"], "horizontal")}`);
        e.setAttribute("role", "separator");
        return e;
      },
      Button(c, depth, id) {
        const e = mk("button", `a2ui-btn a2ui-btn-${oneOf(c.variant, ["default", "primary", "borderless", "danger"], "default")}`);
        e.type = "button";
        if (typeof c.child === "string") {
          const k = build(c.child, depth + 1);
          if (k) e.appendChild(k);
        }
        buttons.push(e);
        e.addEventListener("click", () => {
          if (e.getAttribute("aria-disabled") === "true") return;
          const env = envelope(c.action, id);
          if (!env) return;
          setSent(true);
          timer = setT(() => setSent(false), REENABLE_MS);
          if (typeof o.onAction === "function") o.onAction(env);
        });
        return e;
      },
      TextField(c) {
        const f = field(c.value);
        const num = c.variant === "number";
        const long = c.variant === "longText";
        const input = mk(num ? "input" : "textarea", "a2ui-input");
        if (num) input.type = "number";
        else {
          input.rows = long ? 3 : 1;
          watch(input);
          // A one-line field wraps like any other but takes no line break from the keyboard.
          if (!long) {
            input.addEventListener("keydown", (ev) => {
              if (ev.key === "Enter" && !ev.isComposing) ev.preventDefault();
            });
          }
        }
        if (c.placeholder !== undefined) input.placeholder = textOf(dyn(c.placeholder));
        input.value = fieldText(f.get());
        const fit = () => {
          if (!num) grow(input);
        };
        input.addEventListener("input", () => {
          f.set(input.value);
          fit();
        });
        binds.push(() => {
          const v = fieldText(f.get());
          if (input.value !== v) input.value = v;
          fit();
        });
        input.dataset.cid = `${c.id}:input`;
        const l = labelled(c.label, input, "a2ui-field");
        l.field = input; // not `control`: a real <label> has a read-only property of that name
        return l;
      },
      CheckBox(c) {
        const f = field(c.value);
        const input = mk("input", "a2ui-check");
        input.type = "checkbox";
        input.checked = f.get() === true;
        input.addEventListener("change", () => f.set(input.checked));
        binds.push(() => {
          input.checked = f.get() === true;
        });
        input.dataset.cid = `${c.id}:input`;
        return labelled(c.label, input, "a2ui-field a2ui-checkrow");
      },
      ChoicePicker(c) {
        const f = field(c.value);
        const single = c.variant === "mutuallyExclusive";
        const chips = c.displayStyle === "chips";
        const group = mk("fieldset", `a2ui-choices${chips ? " a2ui-chips" : ""}`);
        const legend = mk("legend", "a2ui-legend");
        legend.textContent = single ? "Choose one" : "Choose any";
        group.appendChild(legend);
        const name = `a2ui-${++uid}`;
        const picked = () => (Array.isArray(f.get()) ? f.get().filter((x) => typeof x === "string") : []);
        const rows = [];
        let filter = null;
        if (c.filterable === true) {
          filter = mk("input", "a2ui-input a2ui-filter");
          filter.type = "text";
          filter.placeholder = "Filter";
          filter.setAttribute("aria-label", "Filter options");
          filter.dataset.cid = `${c.id}:filter`;
          group.appendChild(filter);
        }
        for (const op of (Array.isArray(c.options) ? c.options : []).slice(0, MAX_OPTIONS)) {
          if (count >= MAX_NODES) break;
          if (!isObj(op) || typeof op.value !== "string") continue;
          const row = mk("label", "a2ui-choice");
          const input = mk("input", "a2ui-check");
          input.type = single ? "radio" : "checkbox";
          if (single) input.name = name;
          input.value = op.value;
          input.dataset.cid = `${c.id}:${op.value}`;
          input.checked = picked().includes(op.value);
          input.addEventListener("change", () => {
            const cur = picked();
            f.set(single ? [op.value] : input.checked ? cur.concat(op.value) : cur.filter((x) => x !== op.value));
          });
          const t = mk("span", "a2ui-label");
          t.textContent = textOf(dyn(op.label)) || op.value;
          row.appendChild(input);
          row.appendChild(t);
          group.appendChild(row);
          rows.push({ row, input, text: t.textContent.toLowerCase() });
        }
        if (filter) {
          const none = mk("span", "a2ui-muted a2ui-nomatch");
          none.setAttribute("role", "status");
          group.appendChild(none);
          filter.addEventListener("input", () => {
            const q = filter.value.trim().toLowerCase();
            for (const r of rows) r.row.hidden = !!q && !r.text.includes(q);
            none.textContent = rows.every((r) => r.row.hidden) ? "No options match." : "";
          });
        }
        binds.push(() => {
          const cur = picked();
          for (const r of rows) r.input.checked = cur.includes(r.input.value);
        });
        return group;
      },
      Progress(c) {
        const e = mk("div", "a2ui-progress");
        const bar = mk("div", "a2ui-bar");
        const fill = mk("div", "a2ui-fill");
        const cap = mk("span", "a2ui-caption");
        bar.appendChild(fill);
        e.appendChild(bar);
        e.setAttribute("role", "progressbar");
        const paint = () => {
          const max = Number(dyn(c.max)) > 0 ? Number(dyn(c.max)) : 100;
          const val = Math.min(max, Math.max(0, Number(dyn(c.value)) || 0));
          fill.style.width = `${Math.round((val / max) * 100)}%`;
          e.setAttribute("aria-valuemin", "0");
          e.setAttribute("aria-valuemax", String(max));
          e.setAttribute("aria-valuenow", String(val));
          const label = c.label !== undefined ? textOf(dyn(c.label)) : "";
          cap.textContent = label;
          e.setAttribute("aria-label", label || "Progress");
          e.setAttribute("aria-valuetext", `${val} of ${max}`);
        };
        paint();
        binds.push(paint);
        if (c.label !== undefined) e.appendChild(cap);
        return e;
      },
      Badge(c) {
        const e = mk("span", `a2ui-badge a2ui-tone-${oneOf(c.tone, ["neutral", "info", "success", "warning", "danger"], "neutral")}`);
        const tone = TONE[c.tone];
        if (tone) {
          const mark = mk("span", "a2ui-mark");
          mark.setAttribute("aria-hidden", "true");
          mark.textContent = tone[0];
          const word = mk("span", "sr-only");
          word.textContent = `${tone[1]}: `;
          e.appendChild(mark);
          e.appendChild(word);
        }
        const t = mk("span", "a2ui-badge-text");
        bindText(t, c.text);
        e.appendChild(t);
        return e;
      },
    };

    /** envelope resolves a button's action into the A2UI action message; null when it names no event. */
    function envelope(action, sourceId) {
      const ev = isObj(action) && isObj(action.event) ? action.event : null;
      if (!ev || typeof ev.name !== "string" || !ev.name) return null;
      const ctx = Object.create(null);
      if (isObj(ev.context)) {
        for (const k of Object.keys(ev.context).slice(0, MAX_OPTIONS)) {
          if (Model().BANNED.has(k)) continue;
          const v = dyn(ev.context[k]);
          ctx[k] = v === undefined ? null : Model().clean(v, 0);
        }
      }
      return { version: "1.0", action: { name: ev.name, surfaceId: s.id, sourceComponentId: sourceId, timestamp: now(), context: ctx } };
    }

    function provenance() {
      const p = mk("div", "a2ui-from");
      p.textContent = from();
      return p;
    }

    /** title is the first text the surface shows, for the region's name. Same bounds as drawing. */
    function title() {
      const seenIds = new Set();
      let left = MAX_NODES;
      const walk = (id, depth) => {
        if (depth > MAX_DEPTH || left-- <= 0 || seenIds.has(id)) return "";
        seenIds.add(id);
        const c = Object.prototype.hasOwnProperty.call(s.components, id) ? s.components[id] : null;
        if (!c) return "";
        if (c.component === "Text") return clip(textOf(dyn(c.text)), 80);
        for (const k of Array.isArray(c.children) ? c.children.slice(0, 100) : [c.child]) {
          if (left <= 0) break;
          const t = typeof k === "string" ? walk(k, depth + 1) : "";
          if (t) return t;
        }
        return "";
      };
      return walk("root", 0);
    }
    const from = () => (s.provenance === "system" ? "from monoagent" : "from agent");
    const rename = () => {
      const t = title();
      wrap.setAttribute("aria-label", `Card ${from()}${t ? `: ${t}` : ""}`);
    };

    /** find returns the drawn element with this data-cid. */
    function find(node, cid) {
      if (node.dataset && node.dataset.cid === cid) return node;
      for (const k of node.children || []) {
        const hit = find(k, cid);
        if (hit) return hit;
      }
      return null;
    }

    function rebuild() {
      clearT(timer);
      unwatch();
      // A rebuild replaces the elements: the keyboard stays where it was.
      const focused = doc.activeElement;
      const cid = focused && focused.dataset && wrap.contains && wrap.contains(focused) ? focused.dataset.cid : "";
      binds = [];
      buttons = [];
      count = 0;
      visits = 0;
      drawn = new Set();
      const frag = doc.createDocumentFragment();
      try {
        frag.appendChild(provenance());
        const body = build("root", 0);
        if (body) frag.appendChild(body);
        else frag.appendChild(muted("Nothing to show."));
      } catch {
        // A surface that cannot be drawn is one error card, never an exception in the panel.
        while (frag.children && frag.children.length) frag.removeChild(frag.children[0]);
        binds = [];
        buttons = [];
        const err = mk("div", "a2ui-card a2ui-error");
        err.textContent = "This card could not be shown.";
        frag.appendChild(err);
      }
      status = mk("div", "a2ui-status");
      status.setAttribute("role", "status");
      frag.appendChild(status);
      wrap.replaceChildren(frag);
      wrap.dataset.provenance = s.provenance;
      const again = cid ? find(wrap, cid) : null;
      if (again && again.focus) again.focus();
    }

    function sync(next) {
      // A surface deleted and created again under the same id is a new object, whatever its counters say.
      if (next && next !== s) {
        s = next;
        seen = { struct: -1, data: -1 };
      }
      if (s.deleted) {
        unwatch();
        wrap.hidden = true;
        wrap.replaceChildren();
        return;
      }
      wrap.hidden = false;
      if (s.structVersion !== seen.struct) rebuild();
      else if (s.dataVersion !== seen.data) {
        // The agent changed the data: refresh what is bound, and let buttons act again.
        if (s.dataVersion !== localData) setSent(false);
        for (const b of binds) b();
      }
      seen = { struct: s.structVersion, data: s.dataVersion };
      rename();
    }

    sync(surface);
    function destroy() {
      clearT(timer);
      unwatch();
      if (doc.fonts && doc.fonts.removeEventListener) doc.fonts.removeEventListener("loadingdone", growAll);
    }
    return { el: wrap, sync, destroy };
  }

  root.MonoA2UIRender = { mount, textOf, clip, MAX_NODES, MAX_DEPTH };
})(typeof globalThis !== "undefined" ? globalThis : this);
