/**
 * MonoAgent Bridge — the offline capture queue (CLIP-08)
 *
 * Nothing captured is ever silently lost: a capture taken while the bridge
 * is down is queued in chrome.storage.local and flushed when it is back.
 * Loaded after capture.js, whose MonoCapture it adds queueCapture and
 * flushQueue to.
 */

(function (root) {
  "use strict";

  const MB = 1024 * 1024;
  const mb = (bytes) => `${(bytes / MB).toFixed(1)}MB`;
  const utf8Bytes = (text) => root.MonoCapture.utf8Bytes(text);

  const QUEUE_KEY = "captureQueue";
  const QUEUE_MAX_ENTRIES = 20;
  // chrome.storage.local is a ~10MB bucket; a big MHTML does not belong in it.
  const QUEUE_MAX_ENTRY_BYTES = 4 * MB;

  // Every read-then-write of the queue runs behind the one before it, per
  // storage area: two captures taken together each read the queue before
  // either wrote, and the second write dropped the first.
  const writes = new WeakMap();
  function locked(storage, fn) {
    const run = (writes.get(storage) || Promise.resolve()).then(fn);
    const done = run.catch(() => {});
    writes.set(storage, done);
    done.then(() => {
      if (writes.get(storage) === done) writes.delete(storage);
    });
    return run;
  }

  async function queueCapture(storage, envelope) {
    const size = utf8Bytes(JSON.stringify(envelope));
    if (size > QUEUE_MAX_ENTRY_BYTES) {
      return { queued: false, reason: `capture too large to queue offline (${mb(size)})` };
    }
    return locked(storage, async () => {
      const current = (await storage.get(QUEUE_KEY))[QUEUE_KEY] || [];
      // Never drop what is already waiting to make room: a capture the person
      // was told is queued has to still be there when the bridge is back.
      if (current.length >= QUEUE_MAX_ENTRIES) {
        return { queued: false, reason: `${QUEUE_MAX_ENTRIES} captures are already waiting to sync` };
      }
      const next = current.concat([{ queuedAt: new Date().toISOString(), envelope }]);
      await storage.set({ [QUEUE_KEY]: next });
      return { queued: true, depth: next.length };
    });
  }

  /**
   * flushQueue sends what was queued, oldest first. `prepare(envelope)`, when
   * given, files an envelope that names no profile (D2: there is no
   * Unassigned pile) and answers null for one that has no profile to go to:
   * that one stays queued, as it was, until a later flush can file it.
   *
   * One flush runs at a time (#862): ws.onopen and a reconnect can both
   * start one, and two that each read the queue before sending would each
   * send all of it. A call made while one runs waits for it, then flushes
   * what is left.
   */
  function flushQueue(storage, send, prepare) {
    const ahead = flushing.get(storage) || Promise.resolve();
    const run = ahead.then(() => flushOnce(storage, send, prepare));
    const done = run.catch(() => {});
    flushing.set(storage, done);
    done.then(() => {
      if (flushing.get(storage) === done) flushing.delete(storage);
    });
    return run;
  }

  // The flush running for each storage area, which a later one waits on.
  const flushing = new WeakMap();

  async function flushOnce(storage, send, prepare) {
    const queued = (await storage.get(QUEUE_KEY))[QUEUE_KEY] || [];
    if (!queued.length) return { flushed: 0 };

    // The queue is emptied AFTER the sending, never before. Clearing it up
    // front and relying on the catch to put the rest back cannot work:
    // ws.onopen calls this and an MV3 socket is routinely torn down seconds
    // later, so the usual shape of a failed flush is the first envelope
    // going out and the rest being dropped into a queue key that was already
    // emptied — captures gone, and reported as flushed.
    //
    // The cost of this order is that a worker suspended between the last
    // send and the write-back re-sends what it already sent. A duplicate
    // capture is a duplicate row on the Go side; a lost one is a page the
    // person never gets back. CLIP-08 picks the duplicate every time.
    let flushed = 0;
    let held = 0;
    let reason = null;
    const sent = new Set();
    for (const entry of queued) {
      try {
        const envelope = prepare ? await prepare(entry.envelope) : entry.envelope;
        if (!envelope) {
          held++;
          continue;
        }
        send(envelope);
        flushed++;
        sent.add(entry);
      } catch (err) {
        // A frame that did not reach the wire, or a prepare that failed: what
        // went is still taken off the queue below, so it is not sent again.
        reason = (err && err.message) || String(err);
        break;
      }
    }
    // Written back from what is there now, less what went: prepare may have
    // waited on the bridge, and a capture queued meanwhile must stay.
    if (sent.size) {
      const gone = new Set([...sent].map(entryKey));
      await locked(storage, async () => {
        const now = (await storage.get(QUEUE_KEY))[QUEUE_KEY] || [];
        await storage.set({ [QUEUE_KEY]: now.filter((e) => !gone.has(entryKey(e))) });
      });
    }
    const out = { flushed };
    if (held) out.held = held;
    if (reason) out.reason = reason;
    return out;
  }

  const entryKey = (e) => (e && e.envelope && e.envelope.id) || (e && e.queuedAt) || "";

  root.MonoCapture.queueCapture = queueCapture;
  root.MonoCapture.flushQueue = flushQueue;
})(globalThis);
