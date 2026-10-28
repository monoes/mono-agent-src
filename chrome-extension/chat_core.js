/**
 * MonoAgent Bridge — chat core (issue #451, slice 2)
 *
 * The judgement behind the side panel's chat, kept pure so node tests it:
 * what page context may be sent and how much of it, how a progress frame
 * from `chat.send` / an event from `chat.events` becomes a message, what is
 * remembered across a closed panel or a suspended worker, and where.
 *
 * The wire (see the backend contract in the issue). An event is
 * {seq, type, payload}; on the live reply it arrives as a progress frame,
 * stage = type and detail = JSON {"seq":N,"payload":{...}}.
 *
 * Payload fields are read leniently, because the first backend is still
 * moving: text from `text` | `delta` | `content`; a tool's id from `id` |
 * `call_id` | `tool_id` and name from `name` | `tool` | `title`.
 *
 * Storage is two keys per profile, deliberately apart:
 *   chatConv:<profile>  the conversation id. Written by the worker as soon
 *                       as a first reply names it (the panel may be gone),
 *                       and by the panel on "New chat".
 *   chatLog:<profile>   {conversation, lastSeq, messages}: the transcript
 *                       the panel shows. Replaying after `lastSeq` fills in
 *                       what happened while nobody was looking.
 *
 * Runs in the panel, the worker and node; touches no DOM.
 */

(function (root) {
  "use strict";

  const {
    buildContext, stripUrl, clipBytes, redactUrl, unwrapMessage, buildTranscript, isVideoPage, pageLabel,
    TEXT_CAP, SELECTION_CAP, TRANSCRIPT_CAP, TITLE_CAP, WRAPPER_INTRO, MESSAGE_BOUNDARY,
  } = root.MonoChatContext;

  const MESSAGE_CAP = 16 * 1024; // what a person may send (bytes: the server's own limit)
  const KEEP_MESSAGES = 60; // transcript kept in storage
  const KEEP_MESSAGE_CHARS = 40000;
  const KEEP_TOOLS = 20;

  const METHOD_SEND = "chat.send";

  const str = (v) => (typeof v === "string" ? v : "");

  /** clip cuts to at most `n` UTF-16 units without splitting a surrogate pair. */
  function clip(s, n) {
    const text = str(s);
    if (text.length <= n) return text;
    let end = n;
    const c = text.charCodeAt(end - 1);
    if (c >= 0xd800 && c <= 0xdbff) end -= 1;
    return text.slice(0, end);
  }

  /** validProfile mirrors recall_bridge.js: the same ids are accepted. */
  function validProfile(id) {
    const v = typeof id === "string" ? id.trim() : "";
    return !!v && !v.includes("/") && !v.includes("\\") && !v.includes("..") && v.length <= 128;
  }


  /** checkMessage returns the trimmed message, or "" if there is nothing to send. */
  function checkMessage(text) {
    return clipBytes(str(text).trim(), MESSAGE_CAP);
  }

  // ── Frames and events ──────────────────────────────────────────────

  /** parseProgress reads a MonoAsk progress frame into an event, or null. */
  function parseProgress(progress) {
    if (!progress || typeof progress.stage !== "string" || !progress.stage) return null;
    let body = null;
    try {
      body = JSON.parse(progress.detail);
    } catch {
      return null;
    }
    if (!body || !Number.isFinite(body.seq)) return null;
    const payload = body.payload && typeof body.payload === "object" ? body.payload : {};
    const ev = { seq: body.seq, type: progress.stage, payload };
    // The wrapper may name the conversation and turn ({"seq","conversation","turn","payload"}).
    if (idOf(body.turn)) ev.turn = idOf(body.turn);
    if (idOf(body.conversation)) ev.conversation = idOf(body.conversation);
    return ev;
  }

  /** idOf reads an id that may arrive as a string or a number. */
  function idOf(v) {
    if (typeof v === "number" && Number.isFinite(v)) return String(v);
    return typeof v === "string" ? v : "";
  }

  /**
   * normalizeEvents keeps only well-formed events from a chat.events reply,
   * in seq order. `turn` is the reply's turn: seq is per turn, so each event
   * is stamped with it.
   */
  function normalizeEvents(list, turn) {
    const out = [];
    for (const e of Array.isArray(list) ? list : []) {
      if (!e || typeof e.type !== "string" || !Number.isFinite(e.seq)) continue;
      const ev = { seq: e.seq, type: e.type, payload: e.payload && typeof e.payload === "object" ? e.payload : {} };
      const t = idOf(e.turn) || idOf(turn);
      if (t) ev.turn = t;
      out.push(ev);
    }
    return out.sort((a, b) => a.seq - b.seq);
  }

  const textOf = (p) => str(p.text) || str(p.delta) || str(p.content);

  function newState(saved) {
    const s = saved && typeof saved === "object" ? saved : {};
    return {
      conversation: str(s.conversation),
      turn: str(s.turn), // the turn lastSeq belongs to: seq restarts every turn
      lastSeq: Number.isFinite(s.lastSeq) ? s.lastSeq : 0,
      messages: Array.isArray(s.messages) ? s.messages.filter(validMessage).map(cloneMessage) : [],
      nextId: Number.isFinite(s.nextId) ? s.nextId : 1,
      // Whether the conversation has read a page: its proposal cards say so (chat_proposals.js).
      readPage: !!s.readPage,
      // The page the panel showed when the turn was sent: its cards are bound to it.
      turnPage: pageOf(s.turnPage),
    };
  }

  const pageOf = (v) => (root.MonoChatProposals ? root.MonoChatProposals.pageOf(v) : null);

  function validMessage(m) {
    return m && (m.role === "user" || m.role === "assistant" || m.role === "notice") && typeof m.text === "string";
  }

  function cloneMessage(m) {
    const out = { id: str(m.id), role: m.role, text: m.text };
    if (m.role === "user" && m.page) out.page = clip(m.page, TITLE_CAP);
    if (m.role === "user" && m.shared === true) out.shared = true;
    if (m.role === "user" && m.attached) out.attached = clip(m.attached, TITLE_CAP);
    // A failed turn's notice stays one when restored (unreadAfterFailure).
    if (m.role === "notice" && m.error) out.error = true;
    if (m.role === "assistant") {
      // Kept as saved: a reply cut off by a closed panel carries on from the
      // replay, and whoever restores it closes it once the backend says the
      // turn is over (sidepanel_chat.js sync).
      out.open = !!m.open;
      out.tools = (Array.isArray(m.tools) ? m.tools : []).slice(-KEEP_TOOLS).map((t) => ({
        id: str(t.id),
        name: str(t.name),
        status: t.status === "failed" ? "failed" : t.status === "running" ? "running" : "done",
        ...(t.carded ? { carded: true } : {}),
      }));
      const P = root.MonoChatProposals;
      if (P && Array.isArray(m.proposals) && m.proposals.length) out.proposals = P.clone(m.proposals);
    }
    return out;
  }

  function push(state, msg) {
    state.messages.push(Object.assign({ id: `m${state.nextId++}` }, msg));
    return state.messages[state.messages.length - 1];
  }

  /** openAssistant is the reply being streamed, started if there is none. */
  function openAssistant(state) {
    const last = state.messages[state.messages.length - 1];
    if (last && last.role === "assistant" && last.open) return last;
    return push(state, { role: "assistant", text: "", tools: [], open: true });
  }

  /**
   * startTurn records what the person sent; the reply opens with its first
   * event. withPage: the message carried a page context, titled or not;
   * page: the page the panel showed ({tabId, url}), which the reply's cards
   * are bound to.
   */
  function startTurn(state, text, pageTitle, withPage, page, attached) {
    closeOpen(state);
    // A new turn numbers its events from 1 again; its id is learned from the
    // first event that names it, or from the final reply.
    state.turn = "";
    state.lastSeq = 0;
    const msg = { role: "user", text: str(text) };
    if (pageTitle) msg.page = pageTitle;
    // Whether this attempt carried a page, titled or not: a retry after a failure shares one only if it did.
    if (pageTitle || withPage) msg.shared = true;
    // What came with it besides words ("2 images"): said under the message, never stored as pixels.
    if (attached) msg.attached = clip(String(attached), TITLE_CAP);
    // Any page counts, with or without a title or address: its text is the page's.
    if (pageTitle || withPage) state.readPage = true;
    state.turnPage = pageOf(page);
    push(state, msg);
  }

  function closeOpen(state) {
    for (const m of state.messages) if (m.role === "assistant") m.open = false;
    for (const m of state.messages) {
      if (m.role !== "assistant") continue;
      for (const t of m.tools) if (t.status === "running") t.status = "done";
    }
  }

  /**
   * applyEvent folds one event into the state. An event at or before
   * `lastSeq` was already seen (the live stream and a replay overlap by
   * design) and changes nothing. Returns true when the state changed.
   */
  function applyEvent(state, ev) {
    if (!ev) return false;
    // seq is per turn: an event of another turn starts counting afresh.
    if (ev.turn && ev.turn !== state.turn) {
      state.turn = ev.turn;
      state.lastSeq = 0;
    }
    if (ev.conversation && !state.conversation) state.conversation = ev.conversation;
    if (ev.seq <= state.lastSeq) return false;
    state.lastSeq = ev.seq;
    const p = ev.payload || {};
    switch (ev.type) {
      case "turn.started":
      case "user.message": {
        // The user's text, for a replay of a turn this transcript lacks; not
        // added again when it already ends the transcript.
        // The backend wraps the page in front of the message; show only what was typed.
        if (textOf(p).startsWith(WRAPPER_INTRO)) state.readPage = true;
        const t = unwrapMessage(textOf(p));
        const last = state.messages[state.messages.length - 1];
        if (t && !(last && last.role === "user" && last.text === t)) {
          closeOpen(state);
          push(state, { role: "user", text: t });
        }
        return true;
      }
      case "assistant.delta": {
        // A worker's output (agentId set) is not the reply; the backend
        // leaves it out of the final text too.
        if (p.agentId) return true;
        const t = textOf(p);
        if (t) openAssistant(state).text += t;
        return true;
      }
      case "tool.started": {
        const a = openAssistant(state);
        a.tools.push({
          id: str(p.callId) || str(p.id) || str(p.call_id) || str(p.tool_id) || `t${ev.seq}`,
          name: clip(str(p.name) || str(p.tool) || str(p.title) || "tool", 80),
          status: "running",
        });
        return true;
      }
      case "tool.completed": {
        const a = openAssistant(state);
        const id = str(p.callId) || str(p.id) || str(p.call_id) || str(p.tool_id);
        const name = str(p.name) || str(p.tool) || str(p.title);
        // A runtime may reuse a call id within one reply: the call still running answers first.
        let t = id ? a.tools.find((x) => x.id === id && x.status === "running") || a.tools.find((x) => x.id === id) : null;
        if (!t) t = [...a.tools].reverse().find((x) => x.status === "running" && (!name || x.name === name));
        if (!t) {
          t = { id: id || `t${ev.seq}`, name: clip(name || "tool", 80), status: "running" };
          a.tools.push(t);
        }
        // ok is a pointer on the wire: absent means nothing was said.
        const failed =
          p.ok === false || !!p.denied || !!p.cancelled || !!p.error || !!p.is_error || p.status === "error" || p.status === "failed";
        t.status = failed ? "failed" : "done";
        // A proposal tool's answer is a card for the person (chat_proposals.js),
        // once per call: a call that made its card (a replayed answer) makes no
        // other. Cards are told apart by their nonce, never their call id.
        const P = root.MonoChatProposals;
        const card = P && !failed && !t.carded ? P.fromTool(t.name, p, t.id, state.readPage) : null;
        if (card) {
          t.carded = true;
          if (state.turnPage) card.page = Object.assign({}, state.turnPage);
          (a.proposals = a.proposals || []).push(card);
        }
        return true;
      }
      case "notice": {
        const t = str(p.message) || textOf(p);
        if (t) {
          const n = { role: "notice", text: clip(t, 500) };
          if (p.severity === "error") n.error = true;
          push(state, n);
        }
        return true;
      }
      case "turn.finished": {
        closeOpen(state);
        const status = str(p.status);
        const code = str(p.code);
        if (status === "cancelled" || status === "canceled") {
          push(state, { role: "notice", text: "Stopped." });
        } else if (code || status === "failed" || status === "error") {
          push(state, { role: "notice", text: clip(`The reply ended: ${code || status}`, 500), error: true });
        }
        return true;
      }
      case "turn.completed":
      case "turn.done":
      case "assistant.done":
        closeOpen(state);
        return true;
      case "session.bound": {
        // Real payload is {runtime, sessionId}: no conversation. Tolerated if one is named.
        const c = str(p.conversation) || str(p.conversation_id);
        if (c && !state.conversation) state.conversation = c;
        return true;
      }
      default:
        // usage.updated and anything newer: nothing to draw.
        return true;
    }
  }

  /** finishTurn closes the reply; `text` is the final text, used if no deltas arrived. */
  function finishTurn(state, text, turn) {
    if (idOf(turn) && idOf(turn) !== state.turn) {
      // The final reply names the turn the events belonged to.
      if (!state.turn) state.turn = idOf(turn);
      else {
        state.turn = idOf(turn);
        state.lastSeq = 0;
      }
    }
    const last = state.messages[state.messages.length - 1];
    const finalText = str(text);
    if (finalText && !(last && last.role === "assistant" && last.text)) {
      const a = last && last.role === "assistant" && last.open ? last : openAssistant(state);
      a.text = finalText;
    }
    closeOpen(state);
  }

  /**
   * unreadAfterFailure: no reply has come in this conversation and its last
   * turn failed (a runtime that could not sign in, a dropped bridge), so
   * nothing the person sent was read, the page included. A turn still being
   * written is not this: its page is on its way. Only an attempt that carried
   * a page is retried with one: a person who left the page out never has it
   * read by a retry. Only the explicit shared flag counts: a page title alone
   * (an old-format log) never turns sharing on.
   */
  function unreadAfterFailure(state) {
    const last = state.messages[state.messages.length - 1];
    if (!last || last.role !== "notice" || !last.error) return false;
    if (state.messages.some((m) => m.role === "assistant" && str(m.text).trim() !== "")) return false;
    const sent = state.messages.filter((m) => m.role === "user").pop();
    return !!sent && sent.shared === true;
  }

  /** failTurn closes the reply and says why in the transcript. */
  function failTurn(state, why) {
    closeOpen(state);
    push(state, { role: "notice", text: clip(str(why) || "The reply failed.", 500), error: true });
  }

  /** serialize is what is stored: bounded, and without anything mid-stream. */
  function serialize(state) {
    return {
      conversation: state.conversation,
      turn: state.turn,
      lastSeq: state.lastSeq,
      nextId: state.nextId,
      readPage: !!state.readPage,
      turnPage: state.turnPage || null,
      messages: state.messages.slice(-KEEP_MESSAGES).map((m) =>
        Object.assign({}, m, { text: clip(m.text, KEEP_MESSAGE_CHARS) })
      ),
    };
  }

  // ── Storage ────────────────────────────────────────────────────────

  const convKey = (profile) => `chatConv:${validProfile(profile) ? profile.trim() : "_"}`;
  const logKey = (profile) => `chatLog:${validProfile(profile) ? profile.trim() : "_"}`;

  async function loadState(storage, profile) {
    try {
      const got = (await storage.get([convKey(profile), logKey(profile)])) || {};
      const state = newState(got[logKey(profile)]);
      const conv = str(got[convKey(profile)]);
      // The worker may have learned the conversation after the panel last
      // saved (a first reply finished with the panel closed): keep the
      // transcript and replay from the start. A different conversation
      // (New chat elsewhere) makes the stored transcript someone else's.
      if (conv !== state.conversation) {
        if (state.conversation) {
          state.messages = [];
          state.readPage = false;
        }
        state.conversation = conv;
        state.turn = "";
        state.lastSeq = 0;
      }
      return state;
    } catch {
      return newState(null);
    }
  }

  async function saveState(storage, profile, state) {
    try {
      await storage.set({ [logKey(profile)]: serialize(state), [convKey(profile)]: state.conversation });
    } catch {
      // Storage full or gone: the chat still works, it just is not remembered.
    }
  }

  /** rememberConversation is the worker's write; it leaves the transcript alone. */
  async function rememberConversation(storage, profile, conversation) {
    if (!str(conversation)) return;
    try {
      await storage.set({ [convKey(profile)]: str(conversation) });
    } catch {
      // See saveState.
    }
  }

  root.MonoChatCore = {
    buildContext, stripUrl, clipBytes, checkMessage, parseProgress, normalizeEvents,
    newState, startTurn, applyEvent, finishTurn, failTurn, unreadAfterFailure, serialize, closeOpen,
    loadState, saveState, rememberConversation, validProfile, clip,
    convKey, logKey,
    METHOD_SEND, TEXT_CAP, SELECTION_CAP, MESSAGE_CAP, TRANSCRIPT_CAP,
    isVideoPage, pageLabel, redactUrl, unwrapMessage, buildTranscript, WRAPPER_INTRO, MESSAGE_BOUNDARY,
  };
})(globalThis);
