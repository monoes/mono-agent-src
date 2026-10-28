/**
 * MonoAgent Bridge — YouTube transcript (the "Save video summary" capture)
 *
 * collect() is the whole video half of a capture: resolve the video's record
 * (youtube_video.js), choose a caption track, fetch and parse it, and render
 * transcript.md. The page reads and fetches arrive as injected functions, so
 * the flow runs in node against fixtures (youtube_video.test.mjs).
 *
 * Caption tracks. The player response lists them with a baseUrl. Since 2025
 * the web player's baseUrls carry `exp=xpe` and answer 200 with an EMPTY body
 * unless a proof-of-origin token the player computes is attached. So an
 * empty answer is not "no captions": the same video is asked for again
 * through the player endpoint as the mobile client, whose track URLs carry no
 * such requirement (FALLBACK_CLIENTS). Both requests go from the user's own
 * tab, to youtube.com, exactly as the player's own would.
 */

(function (root) {
  "use strict";

  const V = () => root.MonoYouTubeVideo;

  // Tried in order when the page's own track URLs come back empty.
  const FALLBACK_CLIENTS = [
    { clientName: "ANDROID", clientVersion: "20.10.38", androidSdkVersion: 30 },
    { clientName: "IOS", clientVersion: "20.10.4", deviceModel: "iPhone16,2" },
  ];

  const baseLang = (code) => String(code || "").toLowerCase().split(/[-_]/)[0];

  function tracksOf(player) {
    const r = player && player.captions && player.captions.playerCaptionsTracklistRenderer;
    return (r && Array.isArray(r.captionTracks) && r.captionTracks.filter((t) => t && t.baseUrl)) || [];
  }

  /**
   * spokenLanguage guesses the language the video is spoken in. An
   * auto-generated (asr) track is generated FROM the audio, so a lone one is
   * the best evidence there is. An auto-dubbed video has one per dubbed
   * language, listed alphabetically: there the default audio track (the
   * original) and its default caption say which one is spoken.
   */
  function spokenLanguage(player, tracks) {
    const asr = tracks.filter((t) => t.kind === "asr");
    if (asr.length === 1) return asr[0].languageCode;
    const r = player && player.captions && player.captions.playerCaptionsTracklistRenderer;
    const audios = r && Array.isArray(r.audioTracks) ? r.audioTracks : [];
    const audio = audios[r && typeof r.defaultAudioTrackIndex === "number" ? r.defaultAudioTrackIndex : 0] || audios[0];
    const idx = audio && typeof audio.defaultCaptionTrackIndex === "number" ? audio.defaultCaptionTrackIndex : -1;
    if (tracks[idx] && tracks[idx].languageCode) return tracks[idx].languageCode;
    return asr.length ? asr[0].languageCode : null;
  }

  /**
   * pickTrack prefers a human-made track in the video's own language, then
   * the auto-generated one, then any human-made track, then anything.
   */
  function pickTrack(tracks, lang) {
    if (!tracks.length) return null;
    const want = baseLang(lang);
    const inLang = want ? tracks.filter((t) => baseLang(t.languageCode) === want) : [];
    return (
      inLang.find((t) => t.kind !== "asr") ||
      inLang[0] ||
      tracks.find((t) => t.kind !== "asr") ||
      tracks[0]
    );
  }

  const ENTITIES = { amp: "&", lt: "<", gt: ">", quot: '"', apos: "'", nbsp: " " };
  function decodeEntities(s) {
    return String(s).replace(/&(#x?[0-9a-f]+|[a-z]+);/gi, (all, e) => {
      if (e[0] === "#") {
        const n = e[1] === "x" || e[1] === "X" ? parseInt(e.slice(2), 16) : parseInt(e.slice(1), 10);
        return Number.isFinite(n) ? String.fromCodePoint(n) : all;
      }
      return ENTITIES[e.toLowerCase()] ?? all;
    });
  }

  const squash = (s) => String(s || "").replace(/\s+/g, " ").trim();

  /** parseJson3 reads fmt=json3: events with tStartMs and segs of utf8. */
  function parseJson3(text) {
    let doc;
    try {
      doc = JSON.parse(text);
    } catch {
      return [];
    }
    const out = [];
    for (const ev of (doc && doc.events) || []) {
      if (!ev.segs) continue;
      const said = squash(ev.segs.map((s) => s.utf8 || "").join(""));
      if (said) out.push({ start: (ev.tStartMs || 0) / 1000, text: said });
    }
    return out;
  }

  /** parseXml reads the legacy <text start=… dur=…> and srv3 <p t=… d=…> formats. */
  function parseXml(text) {
    const out = [];
    const src = String(text || "");
    const legacy = /<text\b[^>]*\bstart="([\d.]+)"[^>]*>([\s\S]*?)<\/text>/g;
    let m;
    while ((m = legacy.exec(src))) {
      const said = squash(decodeEntities(m[2].replace(/<[^>]+>/g, "")));
      if (said) out.push({ start: Number(m[1]), text: said });
    }
    if (out.length) return out;
    const srv3 = /<p\b[^>]*\bt="(\d+)"[^>]*>([\s\S]*?)<\/p>/g;
    while ((m = srv3.exec(src))) {
      const said = squash(decodeEntities(m[2].replace(/<[^>]+>/g, "")));
      if (said) out.push({ start: Number(m[1]) / 1000, text: said });
    }
    return out;
  }

  function withFormat(baseUrl, fmt) {
    const stripped = String(baseUrl).replace(/([?&])fmt=[^&]*(&|$)/, (all, lead, tail) => (tail ? lead : ""));
    return fmt ? `${stripped}${stripped.includes("?") ? "&" : "?"}fmt=${fmt}` : stripped;
  }

  /** fetchTrack tries json3 then XML; returns [] when both come back empty. */
  async function fetchTrack(track, fetchText) {
    for (const [fmt, parse] of [["json3", parseJson3], ["", parseXml]]) {
      try {
        const res = await fetchText(withFormat(track.baseUrl, fmt));
        if (res && res.status >= 200 && res.status < 300 && res.text) {
          const segments = parse(res.text);
          if (segments.length) return segments;
        }
      } catch {
        // Next format.
      }
    }
    return [];
  }

  /** fallbackTracks asks the player endpoint as a mobile client. */
  async function fallbackTracks(id, fetchText) {
    for (const client of FALLBACK_CLIENTS) {
      try {
        const res = await fetchText("/youtubei/v1/player?prettyPrint=false", {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ context: { client: Object.assign({ hl: "en", gl: "US" }, client) }, videoId: id }),
        });
        if (!res || !res.text) continue;
        const tracks = tracksOf(JSON.parse(res.text));
        if (tracks.length) return tracks;
      } catch {
        // Next client.
      }
    }
    return [];
  }

  const mdEscape = (s) => String(s || "").replace(/([\\`*_[\]<>#])/g, "\\$1");

  /**
   * transcriptMarkdown renders timestamped paragraphs (a new one every
   * ~20s, and at every chapter), each opening with a deep link to its second.
   */
  function transcriptMarkdown(fields, segments, track, opts) {
    const clock = V().clock;
    const id = fields.videoId;
    const watch = `https://www.youtube.com/watch?v=${id}`;
    const every = (opts && opts.paragraphSeconds) || 20;
    const lines = [`# Transcript: ${mdEscape(fields.title || id)}`, ""];
    const facts = [`**Video:** ${watch}`];
    if (fields.channel) facts.push(`**Channel:** ${mdEscape(fields.channel)}`);
    if (fields.publishDate) facts.push(`**Published:** ${fields.publishDate}`);
    if (fields.duration) facts.push(`**Duration:** ${fields.duration}`);
    if (track) {
      const name = V().txt(track.name) || track.languageCode;
      // YouTube's own name for an asr track usually already says so.
      const auto = track.kind === "asr" && !/auto/i.test(name) ? " (auto-generated)" : "";
      facts.push(`**Captions:** ${mdEscape(name)}${auto}`);
    }
    lines.push(facts.join("  \n"), "");

    const chapters = (fields.chapters || []).slice().sort((a, b) => a.start - b.start);
    let nextChapter = 0;
    let para = null;
    const flush = () => {
      if (!para) return;
      const at = Math.floor(para.start);
      lines.push(`[${clock(at)}](${watch}&t=${at}s) ${mdEscape(para.text.join(" "))}`, "");
      para = null;
    };
    for (const seg of segments) {
      let heading = null;
      while (nextChapter < chapters.length && chapters[nextChapter].start <= seg.start) {
        heading = chapters[nextChapter++];
      }
      if (heading) {
        flush();
        lines.push(`## ${mdEscape(heading.title)} (${clock(heading.start)})`, "");
      }
      if (para && seg.start - para.start >= every) flush();
      if (!para) para = { start: seg.start, text: [] };
      para.text.push(seg.text);
    }
    flush();
    return lines.join("\n").trimEnd() + "\n";
  }

  /**
   * collect resolves the video, its transcript and transcript.md.
   *
   *   io.url            the tab's URL
   *   io.readPage()     → readPageState()'s result (MAIN world)
   *   io.fetchText(url, init) → { status, text }, from the page
   *
   * Returns { fields, transcript: { markdown, language, languageName,
   * autoGenerated, segments, source } | null, warnings }. Throws only when
   * the URL is not a video or no record of it could be found at all.
   */
  async function collect(io) {
    const Vid = V();
    const id = Vid.videoIdOf(io.url);
    if (!id) throw new Error("not a YouTube video page");
    const warnings = [];

    let state = null;
    try {
      state = await io.readPage();
    } catch (err) {
      warnings.push(`youtube: could not read the player (${err.message}); fetched the watch page instead`);
    }
    let { player, data } = Vid.resolveState(state, id);
    if (!player || !data) {
      // Stale globals after in-app navigation, or no player at all: ask the
      // site for this video's page and read the record from its HTML. The
      // player API usually has the current video even then, but
      // ytInitialData (the chapters) is only ever the first video's.
      try {
        const res = await io.fetchText(`/watch?v=${encodeURIComponent(id)}`);
        const html = (res && res.text) || "";
        const fetched = Vid.resolveState(
          { players: [Vid.extractJsonAssignment(html, "ytInitialPlayerResponse")].filter(Boolean), data: Vid.extractJsonAssignment(html, "ytInitialData") },
          id
        );
        player = player || fetched.player;
        data = data || fetched.data;
      } catch (err) {
        warnings.push(`youtube: refetching the watch page failed: ${err.message}`);
      }
    }
    if (!player) throw new Error(`no player record for video ${id}`);
    const fields = Vid.videoFields(player, data);

    let tracks = tracksOf(player);
    const lang = spokenLanguage(player, tracks);
    let track = pickTrack(tracks, lang);
    let segments = track ? await fetchTrack(track, io.fetchText) : [];
    let source = "page";
    if (track && !segments.length) {
      tracks = await fallbackTracks(id, io.fetchText);
      const alt = pickTrack(tracks, lang || track.languageCode);
      if (alt) {
        segments = await fetchTrack(alt, io.fetchText);
        if (segments.length) {
          track = alt;
          source = "player-api";
        }
      }
    }

    let transcript = null;
    if (!track) {
      warnings.push("youtube: this video has no captions, so there is no transcript — saved the video's details only");
    } else if (!segments.length) {
      warnings.push("youtube: the caption track could not be downloaded, so there is no transcript — saved the video's details only");
    } else {
      transcript = {
        markdown: transcriptMarkdown(fields, segments, track),
        language: track.languageCode || null,
        languageName: Vid.txt(track.name) || null,
        autoGenerated: track.kind === "asr",
        segments: segments.length,
        source,
        cues: segments, // {start, text}: the chat sends them as plain text
      };
    }
    return { fields, transcript, warnings };
  }

  // meta.json is read by every capture listing; a description is not a
  // reason for it to be megabytes.
  const MAX_META_DESCRIPTION = 5000;

  /**
   * captureVideo is collect() shaped for capture.js: the fields meta.video
   * carries, transcript.md's text, and warnings. It never throws; a failure
   * is a warning and a meta.video that says what is missing.
   *
   *   yt.readPage(tabId)                → readPageState() in the MAIN world
   *   yt.fetchText(tabId, url, init)    → { status, text } fetched by the page
   */
  async function captureVideo(url, tabId, yt) {
    try {
      const out = await collect({
        url,
        readPage: () => yt.readPage(tabId),
        fetchText: (u, init) => yt.fetchText(tabId, u, init),
      });
      const f = out.fields;
      const t = out.transcript;
      const meta = Object.assign({}, f, {
        description: f.description.length > MAX_META_DESCRIPTION ? `${f.description.slice(0, MAX_META_DESCRIPTION)}…` : f.description,
        transcript: t
          ? { available: true, language: t.language, languageName: t.languageName, autoGenerated: t.autoGenerated, segments: t.segments, source: t.source }
          : { available: false, reason: out.warnings.join("; ") || "no captions" },
      });
      return { meta, markdown: t ? t.markdown : null, warnings: out.warnings };
    } catch (err) {
      return { meta: null, markdown: null, warnings: [`video details skipped: ${err.message}`] };
    }
  }

  root.MonoYouTubeTranscript = {
    collect, captureVideo, pickTrack, spokenLanguage, tracksOf, parseJson3, parseXml, withFormat,
    transcriptMarkdown, decodeEntities, FALLBACK_CLIENTS,
  };
})(globalThis);
