/**
 * MonoAgent Bridge — the sign-in screen (signin.html)
 *
 * What the side panel shows while the worker's gate says nobody is signed in
 * (gate.js). It asks the worker for its status, says what to do, and goes
 * back to the full panel the moment the worker reports a sign-in. `view` is
 * pure, so the wording is tested in node (signin.test.mjs).
 */

(function (root) {
  "use strict";

  const LOGIN_COMMAND = "monoagentcli account login";
  const PAIR_COMMAND = "monoagentcli extension pair";

  function plural(n, one, many) {
    return `${n} ${n === 1 ? one : many}`;
  }

  /**
   * view turns the worker's status (get_status / account_recheck) into what
   * the screen says: {signedIn, pair, queued, state}. Empty strings are hidden.
   */
  function view(status) {
    const s = status || {};
    const out = { signedIn: s.signedIn === true, unpaired: s.status === "unpaired", pair: "", queued: "", state: "" };
    if (out.unpaired) {
      // Signing in on the computer cannot clear this screen by itself: this
      // browser only learns of it over a paired connection.
      out.pair = `This browser is not connected to MonoAgent yet, so it cannot learn that you signed in. After you sign in, run ${PAIR_COMMAND}, or paste the token it prints below.`;
    }
    const q = s.queued || {};
    const parts = [];
    if (q.captures > 0) parts.push(plural(q.captures, "saved page", "saved pages"));
    if (q.tasks > 0) parts.push(plural(q.tasks, "task", "tasks"));
    if (q.recording) parts.push("a recording");
    if (parts.length) {
      out.queued = `Waiting in this browser: ${parts.join(", ")}. Nothing is sent until you sign in; then it is sent as usual.`;
    }
    if (s.status === "connected") out.state = "MonoAgent is running here, and says nobody is signed in.";
    else if (s.status === "connecting") out.state = "Looking for MonoAgent on this computer…";
    else if (s.status === "disconnected") out.state = "MonoAgent is not running on this computer. Start the app, sign in, then check again.";
    return out;
  }

  root.MonoSignIn = { view, LOGIN_COMMAND, PAIR_COMMAND };

  if (typeof document === "undefined" || typeof chrome === "undefined" || !chrome.runtime) return;

  const $ = (id) => document.getElementById(id);

  // A status broadcast does not carry the outboxes; keep the last count.
  let queued = null;

  function show(status) {
    if (status && status.queued) queued = status.queued;
    const v = view(Object.assign({}, status, { queued }));
    if (v.signedIn) {
      location.replace("sidepanel.html");
      return;
    }
    $("signin-pairing").hidden = !v.unpaired;
    for (const [id, text] of [["signin-pair", v.pair], ["signin-queued", v.queued], ["signin-state", v.state]]) {
      $(id).textContent = text;
      $(id).hidden = !text;
    }
  }

  function recheck() {
    chrome.runtime.sendMessage({ type: "account_recheck" }, (reply) => {
      void chrome.runtime.lastError; // a worker still waking answers the next time
      if (reply) show(reply);
    });
  }

  $("signin-pair-save").addEventListener("click", () => {
    const input = $("signin-pair-token");
    const value = input.value.trim();
    if (!value) {
      input.focus();
      return;
    }
    chrome.runtime.sendMessage({ type: "set_pairing_token", value }, (reply) => {
      void chrome.runtime.lastError;
      if (reply && reply.ok) {
        input.value = "";
        $("signin-state").textContent = "Saved. Connecting…";
        $("signin-state").hidden = false;
      } else {
        $("signin-state").textContent = (reply && reply.error) || "Could not save the pairing token.";
        $("signin-state").hidden = false;
      }
    });
  });

  chrome.runtime.onMessage.addListener((msg) => {
    if (msg && msg.type === "status") show(msg);
  });
  $("signin-recheck").addEventListener("click", recheck);
  $("signin-copy").addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(LOGIN_COMMAND);
      $("signin-copy").textContent = "Copied";
    } catch {
      // no clipboard: the command is on screen to select
    }
  });
  recheck();
})(globalThis);
