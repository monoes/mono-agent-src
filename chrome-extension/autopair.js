/**
 * MonoAgent Bridge — pairing without a step (the worker's half)
 *
 * A browser with no pairing token cannot learn that the person signed in to
 * MonoAgent: that arrives over a paired socket. So instead of asking the
 * person to run `extension pair`, the worker asks the bridge directly:
 *
 *   GET  /monoagent/status             {service, signedIn, paired, pairMode}
 *   POST /monoagent/pair/claim         {instance, label, token?}
 *        -> {token, id}                                  (pairMode auto)
 *        -> 202 {pending, claimId, pollSecret, code, expiresAt} (pairMode ask)
 *   GET  /monoagent/pair/claim/<id>    Authorization: Bearer <pollSecret>
 *        -> {status: pending|approved|denied|expired, token?, id?}
 *
 * In ask mode (the default) the bridge hands out nothing until the OWNER
 * approves the request in the MonoAgent app or with `monoagentcli extension
 * approve`: the Origin header this request carries is not proof of anything.
 * The reply's `code` (ABC-123) is what the panel shows while waiting; the owner
 * types it to approve, so only the request THEIR browser shows can be approved.
 * `step` decides what to do from what the bridge says; the worker acts on the
 * answer:
 *
 *   claimed     the token to store and dial with;
 *   pending     waiting for the owner: keep {claimId, pollSecret} and poll;
 *   denied      the owner said no;
 *   expired     nobody answered in time (or the bridge forgot the request);
 *   down        no bridge on any port: "MonoAgent isn't running";
 *   outdated    a bridge answers /monoagent/health but has no /monoagent/status:
 *               it is running, and older than this extension;
 *   signed_out  the bridge is up and nobody is signed in: poll, claim the
 *               moment that changes;
 *   disabled    pairing is off on this machine: the manual path;
 *   refused     the bridge turned the claim down (reason in `error`).
 *
 * Pure apart from the injected fetch, so it runs in node (autopair.test.mjs).
 */

(function (root) {
  "use strict";

  const SERVICE = "monoagent-bridge";
  const PORTS = [9222, 9323];
  const HOST = "127.0.0.1";
  const TIMEOUT_MS = 1500;
  // After a refused claim the worker does not ask again for this long: the
  // bridge rate-limits claims, and a refusal does not change by asking.
  const REFUSED_BACKOFF_MS = 60 * 1000;
  // After a denial or an expiry it does not ask again for this long, unless
  // the person presses "Check again": each request puts a card in front of them.
  const DECLINED_BACKOFF_MS = 10 * 60 * 1000;

  /** bases lists the http origins to ask, best first: the configured ones, then the defaults. */
  function bases(known) {
    const out = [];
    const add = (raw) => {
      const m = /^(?:wss?|https?):\/\/([^/?#]+)/i.exec(String(raw || "").trim());
      if (m && out.indexOf(`http://${m[1]}`) === -1) out.push(`http://${m[1]}`);
    };
    const k = known || {};
    add(k.wsUrl);
    add(k.pairedWsUrl);
    add(k.workingWsUrl);
    for (const p of PORTS) add(`ws://${HOST}:${p}/monoagent`);
    return out;
  }

  function withDeadline(fetchImpl, url, init, ms) {
    const controller = typeof AbortController === "function" ? new AbortController() : null;
    let timer;
    const deadline = new Promise((resolve) => {
      timer = setTimeout(() => {
        if (controller) controller.abort();
        resolve(null);
      }, ms);
    });
    const attempt = fetchImpl(url, Object.assign({ cache: "no-store", signal: controller ? controller.signal : undefined }, init)).catch(() => null);
    return Promise.race([attempt, deadline]).finally(() => clearTimeout(timer));
  }

  /**
   * outdated asks whether one base is a bridge too old to have /status: its
   * /status is a 404 and its /health is the bridge's. A timeout or a 5xx on
   * /status is a busy bridge, not an old one.
   */
  async function outdated(fetchImpl, base, ms) {
    const gone = await withDeadline(fetchImpl, `${base}/monoagent/status`, { method: "GET" }, ms);
    if (!gone || gone.status !== 404) return false;
    const res = await withDeadline(fetchImpl, `${base}/monoagent/health`, { method: "GET" }, ms);
    if (!res || !res.ok) return false;
    try {
      const doc = await res.json();
      return !!doc && doc.service === SERVICE;
    } catch {
      return false;
    }
  }

  /** status asks one base; the document only counts when it says it is the bridge. */
  async function status(fetchImpl, base, ms) {
    const res = await withDeadline(fetchImpl, `${base}/monoagent/status`, { method: "GET" }, ms);
    if (!res || !res.ok) return null;
    try {
      const doc = await res.json();
      if (!doc || doc.service !== SERVICE) return null;
      return { signedIn: doc.signedIn === true, mode: typeof doc.pairMode === "string" ? doc.pairMode : doc.autoPair === false ? "off" : "ask", paired: doc.paired === true };
    } catch {
      return null;
    }
  }

  /**
   * step is one pass of the state machine. deps: fetch, known (stored wsUrl,
   * pairedWsUrl, workingWsUrl), instance, label, token (the one the bridge
   * turned away, if any), pending ({claimId, pollSecret} of a request already
   * made), timeoutMs.
   */
  async function step(deps) {
    const d = deps || {};
    const fetchImpl = d.fetch || (typeof fetch === "function" ? fetch : null);
    if (!fetchImpl) return { kind: "down" };
    const ms = Number(d.timeoutMs) > 0 ? Number(d.timeoutMs) : TIMEOUT_MS;
    const list = bases(d.known);
    const found = await Promise.all(list.map((b) => status(fetchImpl, b, ms)));
    const at = found.findIndex((s) => s);
    if (at === -1) {
      // Nothing answered /status. A bridge that predates the route 404s it
      // and still answers /health: that one is running, not missing.
      const up = await Promise.all(list.map((b) => outdated(fetchImpl, b, ms)));
      return { kind: up.some(Boolean) ? "outdated" : "down" };
    }
    const base = list[at];
    const s = found[at];
    const wsUrl = `ws://${base.slice("http://".length)}/monoagent`;
    if (!s.signedIn) return { kind: "signed_out", wsUrl };
    if (s.mode === "off") return { kind: "disabled", wsUrl };

    const pending = d.pending && d.pending.claimId && d.pending.pollSecret ? d.pending : null;
    if (pending) {
      const r = await withDeadline(
        fetchImpl,
        `${base}/monoagent/pair/claim/${encodeURIComponent(pending.claimId)}`,
        { method: "GET", headers: { Authorization: `Bearer ${pending.pollSecret}` } },
        ms
      );
      // No answer: the bridge is busy, the request still stands.
      if (!r) return { kind: "pending", claimId: pending.claimId, pollSecret: pending.pollSecret, code: pending.code || "", expiresAt: pending.expiresAt, wsUrl };
      let doc = null;
      try {
        doc = await r.json();
      } catch {
        // handled by the status below
      }
      if (r.ok && doc && doc.status === "approved" && typeof doc.token === "string" && doc.token) {
        return { kind: "claimed", token: doc.token, id: String(doc.id || ""), wsUrl };
      }
      if (r.ok && doc && doc.status === "pending") {
        return { kind: "pending", claimId: pending.claimId, pollSecret: pending.pollSecret, code: pending.code || "", expiresAt: pending.expiresAt, wsUrl };
      }
      if (r.ok && doc && doc.status === "denied") return { kind: "denied", wsUrl };
      if (r.status === 404 || r.status === 410) return { kind: "expired", wsUrl };
      return { kind: "refused", error: (doc && doc.error) || `the bridge refused the pairing check (${r.status})`, wsUrl };
    }

    const res = await withDeadline(
      fetchImpl,
      `${base}/monoagent/pair/claim`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ instance: d.instance || "", label: d.label || "", token: d.token || "" }),
      },
      ms
    );
    if (!res) return { kind: "refused", error: "the bridge did not answer the pairing request", wsUrl };
    let body = null;
    try {
      body = await res.json();
    } catch {
      // not JSON: handled below
    }
    if (body && body.pending === true && body.claimId && body.pollSecret) {
      return { kind: "pending", claimId: String(body.claimId), pollSecret: String(body.pollSecret), code: String(body.code || ""), expiresAt: String(body.expiresAt || ""), wsUrl };
    }
    if (res.ok && body && typeof body.token === "string" && body.token) {
      return { kind: "claimed", token: body.token, id: String(body.id || ""), wsUrl };
    }
    return { kind: "refused", error: (body && body.error) || `the bridge refused to pair (${res.status})`, wsUrl };
  }

  root.MonoAutoPair = { step, bases, REFUSED_BACKOFF_MS, DECLINED_BACKOFF_MS, PORTS };
})(globalThis);
