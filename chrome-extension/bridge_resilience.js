/**
 * MonoAgent Bridge — what the worker does when a tab or the bridge went away
 * underneath it.
 *
 * Plain script, loaded by background.js with importScripts, so the same code
 * runs under `node --test`.
 */

(function (root) {
  "use strict";

  const NO_RECEIVER = /Receiving end does not exist|Could not establish connection/i;
  const RELOAD_HINT =
    "this tab was open before the extension was reloaded or updated and could not be reached: reload the tab and try again";

  /**
   * withReinject runs `attempt` (one message to a tab's content script). A tab
   * opened before the extension reloaded has no content script listening, which
   * Chrome reports as "Receiving end does not exist": on exactly that error
   * `inject` puts the script into the tab and `attempt` runs once more. A tab
   * that still cannot be reached gets an error that says what to do.
   */
  async function withReinject(attempt, inject) {
    try {
      return await attempt();
    } catch (err) {
      if (!NO_RECEIVER.test((err && err.message) || "")) throw err;
    }
    try {
      await inject();
    } catch {
      throw new Error(RELOAD_HINT);
    }
    try {
      return await attempt();
    } catch (err) {
      if (NO_RECEIVER.test((err && err.message) || "")) throw new Error(RELOAD_HINT);
      throw err;
    }
  }

  /**
   * fastRetryAfterClose is the fast-retry count to use once a socket closed
   * without a pairing rejection. A socket the bridge had answered on (`heard`)
   * was a working connection that went away, a bridge restart for one: start
   * the fast retry over. One that never got an answer keeps its count, so a
   * bridge that accepts and drops is not redialled every half second forever.
   */
  function fastRetryAfterClose({ heard, count }) {
    return heard ? 0 : count;
  }

  root.MonoResilience = { withReinject, fastRetryAfterClose, NO_RECEIVER };
})(globalThis);
