/**
 * MonoAgent Bridge — saving a page's file to the vault (issue #451)
 *
 *   vault_save_proposal {profile, tab: {tabId, url}, proposal}
 *     -> {ok, status: "saved"|"refused"|"retry", created?, id?, feedback}
 *
 * The side panel's chat may propose saving an item from the page (an image,
 * a linked PDF or text file, an audio or video file) or the text the person has selected
 * (propose_vault_save). The card names the item; it carries no bytes. Only
 * the person's click on the card's Save to vault sends this message, and only
 * from the panel. Here, at that moment:
 *
 *   1. the bridge must take vault.save (ping lists it);
 *   2. an address must be on the page the person is on now: every frame of
 *      the panel's tab is asked (itemOnPage) whether the page shows it, links
 *      to it, or is it; a selection is read from the page now, never taken
 *      from the chat;
 *   3. the worker fetches the address itself (http/https), with the
 *      person's cookies only from the page's own origin, at most MAX_BYTES
 *      (MonoAgent's own cap: base64 of it fits the bridge's 32 MiB frame);
 *   4. the bytes decide the type: PNG, JPEG, GIF, WebP, PDF, MP3, WAV, Ogg,
 *      MP4/M4A and WebM by their magic numbers, text, Markdown and CSV only
 *      when they are UTF-8 text;
 *   5. vault.save goes under the card's key (MonoChatProposals.clientId), so
 *      a second click saves nothing twice.
 *
 * "refused" is final (the bridge's invalid_input, quota_exceeded and
 * storage_conflict, too big, not a type the vault takes); "retry" keeps the
 * card for another click (no profile, no bridge, not on this page,
 * rate_limited, unavailable, internal). There is no outbox: a
 * multi-megabyte file does not belong in chrome.storage.
 *
 * After ask.js, capture_profile.js, private_net.js and chat_proposals.js.
 */

(function (root) {
  "use strict";

  const METHOD = "vault.save";
  const MAX_BYTES = 20 * 1024 * 1024;
  const FETCH_TIMEOUT_MS = 30000;
  const SAVE_TIMEOUT_MS = 60000;
  const NAME_BYTES = 255;
  // The vault's types, and the extension a name gets for each.
  const EXTENSIONS = {
    "image/png": ["png"],
    "image/jpeg": ["jpg", "jpeg"],
    "image/gif": ["gif"],
    "image/webp": ["webp"],
    "application/pdf": ["pdf"],
    "text/plain": ["txt"],
    "text/markdown": ["md", "markdown"],
    "text/csv": ["csv"],
    "audio/mpeg": ["mp3"],
    "audio/wav": ["wav"],
    "audio/ogg": ["ogg", "oga", "opus"],
    "audio/mp4": ["m4a"],
    "video/mp4": ["mp4", "m4v"],
    "video/webm": ["webm"],
  };

  let deps = null;

  const retry = (text) => ({ ok: false, status: "retry", feedback: { level: "warn", text } });
  const refuse = (text) => ({ ok: false, status: "refused", feedback: { level: "error", text } });
  const fail = (message, final) => Object.assign(new Error(message), { final: !!final });

  /**
   * itemOnPage runs in each frame of the page (chrome.scripting): whether the
   * page is, shows or links to `wanted`. Addresses are compared absolute,
   * without their fragment, and with their percent-encoding undone.
   */
  function itemOnPage(wanted) {
    const key = (u) => {
      try {
        const x = new URL(u, document.baseURI);
        x.hash = "";
        try {
          return decodeURI(x.href);
        } catch {
          return x.href;
        }
      } catch {
        return "";
      }
    };
    const want = key(wanted);
    if (!want) return false;
    const is = (u) => !!u && key(u) === want;
    const srcset = (s) => String(s || "").split(",").map((c) => c.trim().split(/\s+/)[0]);
    if (is(location.href)) return true;
    for (const img of document.querySelectorAll("img")) {
      if (is(img.currentSrc) || is(img.getAttribute("src")) || srcset(img.getAttribute("srcset")).some(is)) return true;
    }
    for (const s of document.querySelectorAll("picture source[srcset]")) if (srcset(s.getAttribute("srcset")).some(is)) return true;
    for (const a of document.querySelectorAll("a[href]")) if (is(a.getAttribute("href"))) return true;
    for (const e of document.querySelectorAll("embed[src], iframe[src], object[data], video[poster], video[src], audio[src], video source[src], audio source[src]")) {
      if (is(e.getAttribute("src")) || is(e.getAttribute("data")) || is(e.getAttribute("poster"))) return true;
    }
    return false;
  }

  /** readSelection runs in each frame of the page: the text selected there now. */
  function readSelection() {
    return String(globalThis.getSelection ? globalThis.getSelection() : "");
  }

  async function inFrames(tabId, func, args) {
    try {
      return (await chrome.scripting.executeScript({ target: { tabId, allFrames: true }, func, args: args || [] })) || [];
    } catch {
      throw fail("this page cannot be read. Open the page the file is on, then press Save again");
    }
  }

  /** readCapped reads a response's body, giving up past MAX_BYTES. */
  async function readCapped(res) {
    const reader = res.body.getReader();
    const parts = [];
    let size = 0;
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.length;
      if (size > MAX_BYTES) {
        await reader.cancel().catch(() => {});
        throw tooBig();
      }
      parts.push(value);
    }
    const out = new Uint8Array(size);
    let at = 0;
    for (const p of parts) {
      out.set(p, at);
      at += p.length;
    }
    return out;
  }

  const mb = (n) => `${(n / (1024 * 1024)).toFixed(1)} MB`;
  const tooBig = () => fail(`the file is too big for the side panel (over ${mb(MAX_BYTES)}). Save it from MonoAgent instead`, true);

  function isText(bytes) {
    if (bytes.includes(0)) return false;
    try {
      new TextDecoder("utf-8", { fatal: true }).decode(bytes);
      return true;
    } catch {
      return false;
    }
  }

  /** sniff is the item's type from its bytes (and, for text, what it was served as or called); "" when the vault does not take it. */
  function sniff(bytes, contentType, url) {
    const at = (sig, off) => sig.every((b, i) => bytes[(off || 0) + i] === b);
    if (at([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])) return "image/png";
    if (at([0xff, 0xd8, 0xff])) return "image/jpeg";
    if (at([0x47, 0x49, 0x46, 0x38])) return "image/gif";
    if (at([0x52, 0x49, 0x46, 0x46]) && at([0x57, 0x45, 0x42, 0x50], 8)) return "image/webp";
    if (at([0x25, 0x50, 0x44, 0x46, 0x2d])) return "application/pdf";
    const declared = String(contentType || "").split(";")[0].trim().toLowerCase();
    const ext = extensionOf(fileOf(url));
    // Audio and video, by the same signatures the bridge checks (Go's
    // http.DetectContentType): anything else is refused there.
    if (at([0x49, 0x44, 0x33]) || isMp3Frame(bytes)) return "audio/mpeg";
    if (at([0x52, 0x49, 0x46, 0x46]) && at([0x57, 0x41, 0x56, 0x45], 8)) return "audio/wav";
    if (at([0x4f, 0x67, 0x67, 0x53, 0x00])) return "audio/ogg";
    if (at([0x1a, 0x45, 0xdf, 0xa3])) return "video/webm";
    if (isMp4(bytes)) return declared === "audio/mp4" || declared === "audio/x-m4a" || ext === "m4a" ? "audio/mp4" : "video/mp4";
    const text =
      declared === "text/markdown" || ext === "md" || ext === "markdown" ? "text/markdown"
        : declared === "text/csv" || ext === "csv" ? "text/csv"
          : declared === "text/plain" || ext === "txt" ? "text/plain"
            : "";
    return text && isText(bytes) ? text : "";
  }

  /** isMp3Frame: bytes start with an MPEG audio frame header (sync, a real version, layer III, a real bitrate and rate). */
  function isMp3Frame(b) {
    if (b.length < 4 || b[0] !== 0xff || (b[1] & 0xe0) !== 0xe0) return false;
    const version = (b[1] >> 3) & 3;
    const layer = (b[1] >> 1) & 3;
    const bitrate = b[2] >> 4;
    const rate = (b[2] >> 2) & 3;
    return version !== 1 && layer === 1 && bitrate !== 0 && bitrate !== 15 && rate !== 3;
  }

  /** isMp4: an ISO base media "ftyp" box naming an mp4 brand (what MP4 and M4A files start with). */
  function isMp4(b) {
    if (b.length < 12) return false;
    const size = ((b[0] << 24) | (b[1] << 16) | (b[2] << 8) | b[3]) >>> 0;
    if (size % 4 !== 0 || size > b.length || b[4] !== 0x66 || b[5] !== 0x74 || b[6] !== 0x79 || b[7] !== 0x70) return false;
    for (let st = 8; st + 3 <= size; st += 4) {
      if (st === 12) continue;
      if (b[st] === 0x6d && b[st + 1] === 0x70 && b[st + 2] === 0x34) return true;
    }
    return false;
  }

  function fileOf(url) {
    try {
      return decodeURIComponent(new URL(url).pathname.split("/").filter(Boolean).pop() || "");
    } catch {
      return "";
    }
  }

  const extensionOf = (name) => {
    const m = /\.([A-Za-z0-9]{1,10})$/.exec(name || "");
    return m ? m[1].toLowerCase() : "";
  };

  /**
   * fileName is a name the vault takes: no path separators, control or bidi
   * characters, at most NAME_BYTES, ending in an extension of its type.
   */
  function fileName(name, mime) {
    const exts = EXTENSIONS[mime] || [];
    let base = String(name || "")
      .replace(/[\\/]/g, "-")
      .replace(/[\u0000-\u001f\u007f؜‎‏‪-‮⁦-⁩]/g, "")
      .replace(/\s+/g, " ")
      .trim()
      .replace(/^\.+/, "");
    if (!base) base = "file";
    const suffix = exts.includes(extensionOf(base)) ? "" : `.${exts[0] || "bin"}`;
    const room = NAME_BYTES - new TextEncoder().encode(suffix).length;
    while (new TextEncoder().encode(base).length > room) base = [...base].slice(0, -1).join("");
    return base + suffix;
  }

  // Base64 in 3 MiB slices, a multiple of 3 bytes so no slice is padded: a
  // binary string the size of the whole file (20 MiB) is never built.
  const B64_SLICE = 3 * 1024 * 1024;

  function toBase64(bytes) {
    const parts = [];
    for (let i = 0; i < bytes.length; i += B64_SLICE) {
      const slice = bytes.subarray(i, i + B64_SLICE);
      let bin = "";
      for (let j = 0; j < slice.length; j += 0x8000) bin += String.fromCharCode.apply(null, slice.subarray(j, j + 0x8000));
      parts.push(btoa(bin));
    }
    return parts.join("");
  }

  /**
   * fetchItem fetches an address that is on the page, capped and typed.
   * From a public page it follows no redirect: one could lead to the
   * person's own network, which only a page on that network may reach. It
   * sends the person's cookies only to the page's own origin (withCookies):
   * a page must not get a request to another site made with them.
   */
  async function fetchItem(tabId, url, follow, withCookies) {
    const found = await inFrames(tabId, itemOnPage, [url]);
    if (!found.some((r) => r && r.result === true)) {
      throw fail("that file is not on the page you are on. Open the page it is on, then press Save again");
    }
    const abort = new AbortController();
    const timer = setTimeout(() => abort.abort(), FETCH_TIMEOUT_MS);
    try {
      let res;
      try {
        res = await fetch(url, { credentials: withCookies ? "include" : "omit", redirect: follow ? "follow" : "manual", signal: abort.signal });
      } catch (err) {
        throw fail(`the file could not be fetched (${(err && err.message) || err})`);
      }
      if (res.type === "opaqueredirect" || (res.status >= 300 && res.status < 400)) {
        throw fail("that address redirects to another one: open the file itself, then save it from there", true);
      }
      if (!res.ok) throw fail(`the site answered ${res.status} for that file`);
      const declared = Number(res.headers.get("content-length"));
      if (declared > MAX_BYTES) throw tooBig();
      const bytes = await readCapped(res);
      const mime = sniff(bytes, res.headers.get("content-type"), url);
      if (!mime) throw fail("the vault takes images (PNG, JPEG, GIF, WebP), audio (MP3, WAV, Ogg, M4A), video (MP4, WebM), PDFs and text files, and this is none of them", true);
      return { bytes, mime, name: fileOf(url), source: url };
    } finally {
      clearTimeout(timer);
    }
  }

  /** selectionItem is the text selected in the page now, as a text file. */
  async function selectionItem(tabId, pageUrl) {
    const frames = await inFrames(tabId, readSelection);
    const main = frames.find((r) => r && r.frameId === 0 && r.result) || frames.find((r) => r && r.result);
    const text = main ? String(main.result) : "";
    if (!text.trim()) throw fail("there is no text selected. Select it on the page, then press Save again");
    const bytes = new TextEncoder().encode(text);
    if (bytes.length > MAX_BYTES) throw tooBig();
    const page = /^https?:/i.test(pageUrl || "") ? pageUrl : "";
    let host = "";
    try {
      host = new URL(page).hostname;
    } catch {
      host = "";
    }
    return { bytes, mime: "text/plain", name: host ? `Selection from ${host}` : "Selection", source: page };
  }

  /**
   * saveRefusal is what a refused vault.save means for the card. Final, as the
   * same save is refused again until the person acts: invalid_input,
   * quota_exceeded (data {limit, max}: delete some saved files) and
   * storage_conflict (stray files block the vault folder). Another click:
   * rate_limited (data {retry_after}: the seconds until one more save fits),
   * unavailable, internal, no answer.
   */
  function saveRefusal(err) {
    const why = (err && err.message) || "MonoAgent did not answer";
    const data = (err && err.data) || {};
    switch (err && err.code) {
      case "invalid_input":
      case "storage_conflict":
        return refuse(`Not saved: ${why}`);
      case "quota_exceeded":
        return refuse(`Not saved: ${quotaText(data) || why}`);
      case "rate_limited":
        if (count(data.retry_after)) {
          const secs = Math.min(data.retry_after, MAX_WAIT_S);
          return retry(`Not saved yet: MonoAgent takes only so many saves a minute from the browser. Try again in ${secs} s`);
        }
        break;
    }
    return retry(`Not saved yet: ${why}. Press Save to try again`);
  }

  // A wait or a limit is shown only as a plain positive whole number (a
  // number, not a string; a safe integer, not 1e308), and a wait at most an
  // hour; anything else shows MonoAgent's generic words.
  const MAX_WAIT_S = 3600;
  const count = (v) => Number.isSafeInteger(v) && v > 0;

  /** quotaText names the limit a full vault reached; "" without one. */
  function quotaText(data) {
    const max = data.max;
    if (!count(max)) return "";
    if (data.limit === "items") {
      return `this profile already holds ${max.toLocaleString("en-US")} files saved from the browser, the most it takes. Delete some in the Media Vault to save more`;
    }
    if (data.limit === "bytes") {
      const GiB = 1024 ** 3;
      const MiB = 1024 ** 2;
      const size = max >= GiB && max % GiB === 0 ? `${max / GiB} GiB` : max >= MiB && max % MiB === 0 ? `${max / MiB} MiB` : `${max} bytes`;
      return `files saved from the browser would pass this profile's ${size} limit. Delete some in the Media Vault to save more`;
    }
    return "";
  }

  async function profileName(id) {
    try {
      const { profiles } = await root.MonoCaptureProfile.load(deps.storage);
      const match = profiles.find((p) => p.id === id);
      return match ? match.name : "";
    } catch {
      return "";
    }
  }

  async function save(msg) {
    const P = root.MonoChatProposals;
    const Net = root.MonoPrivateNet;
    const raw = msg && msg.proposal;
    const card = P && raw && raw.kind === "vault" ? P.clone([Object.assign({}, raw, { state: "pending" })])[0] : null;
    if (!card) return refuse("Not saved: that is not a file the chat proposed");
    const id = typeof msg.profile === "string" ? msg.profile.trim() : "";
    const profile = root.MonoCaptureProfile.isValidProfileId(id) ? id : "";
    if (!profile) return retry('Choose a profile first: pick one under "Saving into"');
    if (!deps || !deps.isConnected()) return retry("MonoAgent isn't connected: start it, then press Save again");
    if (!(await root.MonoAsk.supports(METHOD))) return retry("This MonoAgent cannot save to the vault yet: update it, then press Save again");
    const tabId = msg.tab && Number.isInteger(msg.tab.tabId) ? msg.tab.tabId : 0;
    if (!tabId) return retry("Open the page the file is on, then press Save again");

    const tab = await chrome.tabs.get(tabId).catch(() => null);
    const pageUrl = (tab && tab.url) || "";
    // A public page may not have the extension fetch from the person's own
    // network. A page is on that network only as an http(s) page on a private
    // IP literal or localhost (private_net.js isPrivatePage): a file, a blob,
    // about:, data:, the browser's own pages, and a page on a wildcard-DNS,
    // tailnet or office name count as public.
    const parse = (u) => {
      try {
        return new URL(u);
      } catch {
        return null;
      }
    };
    const page = parse(pageUrl);
    const item = card.url ? parse(card.url) : null;
    const pageWeb = !!page && (page.protocol === "http:" || page.protocol === "https:") && !!page.hostname;
    const pagePrivate = pageWeb && Net.isPrivatePage(page.hostname);
    const sameOrigin = pageWeb && !!item && item.origin === page.origin;
    if (item && Net.isPrivateHost(item.hostname) && !pagePrivate) {
      return refuse(`Not saved: ${item.hostname} is on a private network and this page is not, so MonoAgent will not fetch it from here`);
    }

    let file;
    try {
      file = card.url ? await fetchItem(tabId, card.url, pagePrivate, sameOrigin) : await selectionItem(tabId, pageUrl);
    } catch (err) {
      return err.final ? refuse(`Not saved: ${err.message}`) : retry(`Not saved: ${err.message}`);
    }
    const name = fileName(card.name || file.name, file.mime);
    const params = { client_id: P.clientId(card, profile), profile, name, mime: file.mime, data: toBase64(file.bytes) };
    if (file.source) params.source_url = file.source;
    let data;
    try {
      data = await root.MonoAsk.request(METHOD, params, { timeoutMs: SAVE_TIMEOUT_MS, idleTimeoutMs: SAVE_TIMEOUT_MS });
    } catch (err) {
      return saveRefusal(err);
    }
    const where = await profileName(profile);
    const created = !(data && data.created === false);
    const text = `${created ? "Saved" : "Already saved"} to the vault${where ? ` in ${where}` : ""}: ${(data && data.name) || name}`;
    return { ok: true, status: "saved", created, id: (data && data.id) || null, feedback: { level: "ok", text } };
  }

  /** isExtensionPage: one of this extension's own pages (the side panel). */
  function isExtensionPage(sender) {
    const base = chrome.runtime.getURL ? chrome.runtime.getURL("") : "";
    return !!base && String(sender.url || "").startsWith(base);
  }

  function install(d) {
    deps = d;
    chrome.runtime.onMessage.addListener((msg, sender, respond) => {
      if (!msg || msg.type !== "vault_save_proposal") return false;
      // Only this extension's own pages: a page cannot save into the vault.
      if (!sender || sender.id !== chrome.runtime.id || !isExtensionPage(sender)) return false;
      save(msg).then(respond, (err) => respond(retry(`Not saved: ${(err && err.message) || err}`)));
      return true;
    });
  }

  root.MonoVault = { install, save, itemOnPage, sniff, fileName, toBase64, MAX_BYTES, METHOD };
})(globalThis);
