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
  const RECHECK_MS = 3000;

  function plural(n, one, many) {
    return `${n} ${n === 1 ? one : many}`;
  }

  /**
   * view turns the worker's status (get_status / account_recheck) into what
   * the screen says: {signedIn, pair, queued, state}. Empty strings are hidden.
   */
  function view(status) {
    const s = status || {};
    const out = { signedIn: s.signedIn === true, unpaired: false, pair: "", queued: "", state: "", code: "", lead: "", command: LOGIN_COMMAND, more: [], debug: "" };
    const unpaired = s.status === "unpaired";
    if (unpaired && s.reason === "bridge_signed_out") {
      // The bridge is up, so signing in is all it takes: this browser pairs
      // itself the moment the account is signed in, and this screen leaves.
      out.state = "Sign in in the MonoAgent app or run monoagentcli account login. This window updates automatically.";
    } else if (unpaired && s.reason === "pair_waiting") {
      // The bridge holds this browser's request until the owner decides.
      out.state = "Waiting for approval in the MonoAgent app (or, in your own terminal, not one started from a coder session, run monoagentcli extension approve <id> --code <code>). This window updates automatically.";
      // The owner types this code to approve; a request they did not start shows it to nobody they can ask.
      // Only plain tokens go into a command the owner pastes into a shell.
      if (/^[A-Za-z0-9-]+$/.test(String(s.code || "")) && /^[A-Za-z0-9-]+$/.test(String(s.claimId || ""))) {
        // Spelled out, so there is nothing to look up: this browser's own request, with its own code.
        const id = String(s.claimId).slice(0, 8);
        out.lead = "This browser asked to pair. Approve it from your own terminal (not one started from a coder session) with:";
        out.command = `monoagentcli extension approve ${id} --code ${s.code}`;
        out.more = [{ note: "Not you? Deny it:", cmd: `monoagentcli extension deny ${id}` }];
      }
      if (s.code) out.code = `Code: ${s.code} \u2014 approve only the request that shows this code.`;
    } else if (unpaired && (s.reason === "pair_denied" || s.reason === "pair_expired")) {
      out.unpaired = true;
      out.state = s.reason === "pair_denied" ? "The request to pair this browser was denied." : "The request to pair this browser expired before it was approved.";
      out.pair = `Press Check again to ask once more, or run ${PAIR_COMMAND} and paste the token it prints below.`;
    } else if (unpaired) {
      // Automatic pairing is off or was refused: the manual path.
      out.unpaired = true;
      out.pair = `This browser could not connect to MonoAgent by itself. After you sign in, run ${PAIR_COMMAND}, or paste the token it prints below.`;
    }
    if (unpaired && (s.reason === "pair_denied" || s.reason === "pair_expired")) {
      out.lead = "Ask again with Check again, or pair this browser by hand:";
      out.command = `${PAIR_COMMAND} --open`;
      out.more = [{ note: "See how new browsers pair:", cmd: "monoagentcli extension autopair" }];
    } else if (unpaired && !s.reason) {
      out.lead = "After you sign in, pair this browser from a terminal:";
      out.command = `${PAIR_COMMAND} --open`;
      out.more = [{ note: "Check the bridge:", cmd: "monoagentcli extension status" }, { note: "Sign in:", cmd: LOGIN_COMMAND }];
    } else if (s.status === "disconnected") {
      out.lead = "MonoAgent isn\u2019t running. Start it with:";
      out.command = "monoagentcli extension serve";
      out.more = [{ note: "Or check what is wrong:", cmd: "monoagentcli doctor" }];
    }
    const a = s.account || {};
    out.debug = [
      `status: ${s.status || "?"}${s.reason ? " / " + s.reason : ""}`,
      s.since ? `for: ${Math.max(0, Math.round((Date.now() - s.since) / 1000))}s` : "",
      s.binding && (s.binding.label || s.binding.instance) ? `browser: ${s.binding.label || ""} ${s.binding.instance || ""}`.trim() : "",
      a.state || a.reason ? `account: ${a.state || ""}${a.reason ? " / " + a.reason : ""}` : "",
      `signed in: ${s.signedIn === true}`,
      "On this computer: monoagentcli extension status, monoagentcli extension browsers",
    ].filter(Boolean).join("\n");
    const q = s.queued || {};
    const parts = [];
    if (q.captures > 0) parts.push(plural(q.captures, "saved page", "saved pages"));
    if (q.tasks > 0) parts.push(plural(q.tasks, "task", "tasks"));
    if (q.recording) parts.push("a recording");
    if (parts.length) {
      out.queued = `Waiting in this browser: ${parts.join(", ")}. Nothing is sent until you sign in; then it is sent as usual.`;
    }
    if (unpaired) {
      // worded above
    } else if (s.status === "connected") out.state = "MonoAgent is running here, and says nobody is signed in.";
    else if (s.status === "connecting") out.state = "Looking for MonoAgent on this computer…";
    else if (s.status === "disconnected") out.state = "MonoAgent isn\u2019t running. Start the app or run monoagentcli extension serve. This window updates automatically.";
    return out;
  }

  root.MonoSignIn = { view, LOGIN_COMMAND, PAIR_COMMAND };

  if (typeof document === "undefined" || typeof chrome === "undefined" || !chrome.runtime) return;

  const $ = (id) => document.getElementById(id);
  const DEFAULT_LEAD = $("signin-lead").textContent;

  // A status broadcast does not carry the outboxes; keep the last count.
  let queued = null;

  function show(status) {
    if (status && status.queued) queued = status.queued;
    const v = view(Object.assign({}, status, { queued }));
    if (v.signedIn) {
      location.replace("sidepanel.html");
      return;
    }
    $("signin-lead").textContent = v.lead || DEFAULT_LEAD;
    $("signin-command").textContent = v.command;
    $("signin-copy").textContent = "Copy";
    renderMore(v.more);
    $("signin-debug").textContent = v.debug;
    $("signin-pairing").hidden = !v.unpaired;
    for (const [id, text] of [["signin-pair", v.pair], ["signin-queued", v.queued], ["signin-code", v.code], ["signin-state", v.state]]) {
      $(id).textContent = text;
      $(id).hidden = !text;
    }
  }

  // Further commands for the same situation, each with its own Copy.
  function renderMore(items) {
    const box = $("signin-more");
    box.textContent = "";
    for (const it of items) {
      const note = document.createElement("p");
      note.className = "note-line";
      note.textContent = it.note;
      const row = document.createElement("div");
      row.className = "command";
      const code = document.createElement("code");
      code.textContent = it.cmd;
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "btn-secondary btn-tiny";
      btn.textContent = "Copy";
      btn.addEventListener("click", async () => {
        try {
          await navigator.clipboard.writeText(it.cmd);
          btn.textContent = "Copied";
        } catch {
          // no clipboard: the command is on screen to select
        }
      });
      row.append(code, btn);
      box.append(note, row);
    }
  }

  function recheck(manual) {
    chrome.runtime.sendMessage({ type: "account_recheck", manual }, (reply) => {
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
  $("signin-recheck").addEventListener("click", () => recheck(true));
  $("signin-copy").addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText($("signin-command").textContent);
      $("signin-copy").textContent = "Copied";
    } catch {
      // no clipboard: the command is on screen to select
    }
  });
  recheck();
  // The bridge is asked again every few seconds while this screen is on: a
  // sign-in in the app, or a bridge started after the browser, flips it by
  // itself (the worker pairs this browser the moment the account is signed in).
  setInterval(() => {
    if (document.visibilityState !== "hidden") recheck();
  }, RECHECK_MS);
})(globalThis);
