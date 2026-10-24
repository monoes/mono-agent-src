/**
 * MonoAgent Bridge — the submit guard's page half (plan C1a)
 *
 * Two functions chrome.scripting injects into a tab's frames; neither runs
 * in this worker. Each is serialised on its own, so each is self-contained.
 *
 * installGuard runs in the page's MAIN world, because only there can it
 * wrap the page's own HTMLFormElement.prototype.submit / requestSubmit. A
 * capture-phase `submit` listener on window, the first thing a submit event
 * meets, cancels every submit and stops it reaching the page's handlers (a
 * single-page app's onSubmit would otherwise send it by fetch while the guard
 * claimed to have blocked it). form.submit() fires no event, so it is
 * wrapped; requestSubmit() is too, after the browser's own validation.
 * Each stopped attempt is summarised (password, hidden and card-like values
 * masked as "•••"; a card number is 13 to 19 digits that pass the Luhn
 * check) and handed to installRelay's listener as a CustomEvent named by a
 * nonce the page never sees. allow_once lets exactly the next submit
 * through, then the guard blocks again.
 *
 * installRelay runs in the extension's isolated world of the same frame and
 * forwards those events to the worker (agent_submit_guard.js).
 *
 * Not caught, by design and documented: a "form" that is only a fetch/XHR
 * from a click handler, with no <form> and no submit. The guard is a
 * courtesy layer against mistakes and page-borne prompt injection, not a
 * sandbox: page script that runs before it (it could see the event names the
 * guard registers) can
 * get past it.
 *
 * Browser-tested in agent_submit.browser.test.mjs.
 */

(function (root) {
  "use strict";

  /**
   * installGuard(ctl, eventName) → true once the frame is guarded (and armed).
   * ctl is a per-tab random event name the page never sees (like eventName): the
   * guard is controlled over it, and nothing about the guard is on window, so a
   * page that defines an object under some well-known name cannot make an
   * unguarded frame look armed, and cannot find a handle to disarm it.
   */
  function installGuard(ctl, eventName) {
    // Taken now, before page script can swap them for its own.
    const dispatchNative = EventTarget.prototype.dispatchEvent;
    const Custom0 = CustomEvent;
    // This document is already guarded under this name: arm that guard again.
    // Only a guard listening on the name answers, so an object the page defines
    // proves nothing.
    const probe = { action: "arm", result: false };
    try {
      dispatchNative.call(document, new Custom0(ctl, { detail: probe }));
    } catch {
      // no answer: not guarded yet
    }
    if (probe.result === true) return true;

    // Taken now, before page script can swap them for its own.
    const proto = HTMLFormElement.prototype;
    const nativeSubmit = proto.submit;
    const nativeRequestSubmit = proto.requestSubmit;
    const dispatch = EventTarget.prototype.dispatchEvent;
    const Custom = CustomEvent;
    const toJSON = JSON.stringify;
    const MASK = "•••";
    let armed = true;
    let allow = 0;

    function control(action) {
      if (action === "arm") {
        armed = true;
        allow = 0;
        return true;
      }
      if (action === "disarm") {
        armed = false;
        allow = 0;
        return true;
      }
      if (action === "allow_once") {
        if (!armed) return false;
        allow = 1;
        return true;
      }
      return armed;
    }
    document.addEventListener(ctl, (event) => {
      const d = event && event.detail;
      if (d && typeof d === "object" && typeof d.action === "string") d.result = control(d.action) === true;
    });

    const cut = (s, n) => String(s == null ? "" : s).slice(0, n);
    const clean = (s) => String(s || "").replace(/\s+/g, " ").trim();

    function luhn(digits) {
      let sum = 0;
      for (let i = 0; i < digits.length; i++) {
        let d = Number(digits[digits.length - 1 - i]);
        if (i % 2 === 1) {
          d *= 2;
          if (d > 9) d -= 9;
        }
        sum += d;
      }
      return sum % 10 === 0;
    }

    function sensitive(el, type, value) {
      if (type === "password" || type === "hidden") return true;
      const ac = String(el.getAttribute("autocomplete") || "").toLowerCase();
      if (/(^|\s)(cc-|current-password|new-password|one-time-code)/.test(ac)) return true;
      const named = `${el.name || ""} ${el.id || ""}`.toLowerCase();
      if (/card|cc[-_]?(num|number|exp|csc|cvv)|cvv|cvc|csc|iban|ssn|secret|token|passw|otp|(^|[^a-z])pin([^a-z]|$)/.test(named)) return true;
      // A card number anywhere in the value, spaced or dashed: 13 to 19
      // digits that pass the Luhn check (an order number mostly does not).
      const runs = value.match(/\d(?:[ -]?\d){12,18}/g) || [];
      return runs.some((run) => luhn(run.replace(/[ -]/g, "")));
    }

    function labelOf(el) {
      if (el.labels && el.labels.length) return clean(el.labels[0].textContent);
      const aria = el.getAttribute("aria-label");
      if (aria) return clean(aria);
      const by = el.getAttribute("aria-labelledby");
      if (by) {
        const t = by
          .split(/\s+/)
          .map((id) => document.getElementById(id))
          .filter(Boolean)
          .map((n) => n.textContent)
          .join(" ");
        if (clean(t)) return clean(t);
      }
      return clean(el.placeholder || el.title || "");
    }

    /** selectorOf is a CSS selector for el: its id when unique, else a path of nth-of-type steps. */
    function selectorOf(el) {
      const esc = (s) => (window.CSS && CSS.escape ? CSS.escape(s) : String(s).replace(/[^\w-]/g, "\\$&"));
      const parts = [];
      let node = el;
      while (node && node.nodeType === 1 && parts.length < 32) {
        if (node.id && document.querySelectorAll("#" + esc(node.id)).length === 1) {
          parts.unshift("#" + esc(node.id));
          break;
        }
        const tag = node.tagName.toLowerCase();
        const parent = node.parentElement;
        let part = tag;
        if (parent) {
          const same = Array.from(parent.children).filter((c) => c.tagName === node.tagName);
          if (same.length > 1) part += `:nth-of-type(${same.indexOf(node) + 1})`;
        }
        parts.unshift(part);
        node = parent;
      }
      return parts.join(" > ");
    }

    function defaultButton(form) {
      return Array.from(form.elements || []).find((el) => {
        const tag = String(el.tagName || "").toLowerCase();
        const type = String(el.type || "").toLowerCase();
        return (tag === "button" && type === "submit") || (tag === "input" && (type === "submit" || type === "image"));
      });
    }

    function summarize(form, submitter) {
      const sub = submitter || defaultButton(form) || null;
      const has = (el, name) => !!(el && el.hasAttribute && el.hasAttribute(name));
      const action = has(sub, "formaction") ? sub.formAction : form.action;
      const method = (has(sub, "formmethod") ? sub.formMethod : form.method) || "get";
      const fields = [];
      for (const el of Array.from(form.elements || [])) {
        if (fields.length >= 100) break;
        const tag = String(el.tagName || "").toLowerCase();
        if (!["input", "select", "textarea"].includes(tag) || el.disabled) continue;
        const type = tag === "input" ? String(el.type || "text").toLowerCase() : tag === "select" ? (el.multiple ? "select-multiple" : "select-one") : "textarea";
        if (["submit", "button", "reset", "image"].includes(type)) continue;
        if ((type === "checkbox" || type === "radio") && !el.checked) continue;
        let value;
        if (type === "file") value = Array.from(el.files || []).map((f) => f.name).join(", ");
        else if (tag === "select") value = Array.from(el.selectedOptions || []).map((o) => o.value).join(", ");
        else value = String(el.value == null ? "" : el.value);
        if (value && sensitive(el, type, value)) value = MASK;
        fields.push({ name: cut(el.name || el.id, 200), type, label: cut(labelOf(el), 200), value: cut(value, 500) });
      }
      return {
        action: cut(action || location.href, 2048),
        method: String(method).toUpperCase(),
        fields,
        submitter: sub ? selectorOf(sub) : "",
        selector: selectorOf(form),
      };
    }

    function report(form, submitter) {
      let summary;
      try {
        summary = summarize(form, submitter);
      } catch {
        summary = { action: "", method: "", fields: [], submitter: "", selector: "" };
      }
      try {
        dispatch.call(document, new Custom(eventName, { detail: toJSON({ url: String(location.href), form: summary }) }));
      } catch {
        // the relay is gone with its world; the submit stays blocked
      }
    }

    // A page put into the back/forward cache stops guarding: restored, it is
    // armed again only if its tab still is (agent_submit_guard.js re-injects
    // on the commit), so a guard that ended cannot linger in a cached page.
    window.addEventListener(
      "pagehide",
      (event) => {
        if (event.isTrusted !== true) return; // the page cannot disarm it by pretending
        armed = false;
        allow = 0;
      },
      true
    );

    window.addEventListener(
      "submit",
      (event) => {
        if (!armed) return;
        if (allow > 0) {
          allow -= 1;
          return;
        }
        event.preventDefault();
        event.stopImmediatePropagation();
        report(event.target, event.submitter || null);
      },
      true
    );

    proto.submit = function submit() {
      if (armed && allow === 0) {
        report(this, null);
        return undefined;
      }
      if (armed) allow -= 1;
      return nativeSubmit.call(this);
    };

    if (nativeRequestSubmit) {
      proto.requestSubmit = function requestSubmit(submitter) {
        // Allowed: the submit event it fires is the one that spends the allowance.
        if (!armed || allow > 0) return nativeRequestSubmit.apply(this, arguments);
        const skip = this.noValidate || (submitter && submitter.formNoValidate);
        if (!skip && !this.reportValidity()) return undefined; // invalid: nothing would have been sent
        report(this, submitter || null);
        return undefined;
      };
    }
    return true;
  }

  /** installRelay(eventName) forwards the guard's reports from this frame to the worker. */
  function installRelay(eventName) {
    const seen = globalThis.MonoAgentSubmitRelay || (globalThis.MonoAgentSubmitRelay = new Set());
    if (seen.has(eventName)) return true;
    seen.add(eventName);
    document.addEventListener(eventName, (event) => {
      const detail = event.detail;
      if (typeof detail !== "string" || detail.length > 200000) return;
      try {
        chrome.runtime.sendMessage({ type: "agent_submit_blocked", detail }).catch(() => {});
      } catch {
        // the extension reloaded; this frame's relay is orphaned
      }
    });
    return true;
  }

  root.MonoAgentSubmitPage = { installGuard, installRelay };
})(globalThis);
