// The "Save video summary" capture's video half: the record, the chapters and
// the transcript. Fixtures in fixtures/youtube/ are trimmed from real watch
// pages (readPageState() output, the mobile-client player response, and the
// json3/XML caption bodies) captured 2026-09-22.

import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import { loadExtensionScripts } from "./test_helpers.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const fixture = (name) => readFileSync(join(HERE, "fixtures", "youtube", name), "utf8");
const json = (name) => JSON.parse(fixture(name));

const g = loadExtensionScripts(["youtube_video.js", "youtube_transcript.js"]);
const V = g.MonoYouTubeVideo;
const T = g.MonoYouTubeTranscript;

const ZOO = "https://www.youtube.com/watch?v=jNQXAC9IVRw";
const NN = "https://www.youtube.com/watch?v=aircAruvnKk";

/**
 * io fakes the page: readPage returns a fixture state, and fetchText answers
 * by URL. The page's own track URLs answer empty, the way YouTube's do
 * without a proof-of-origin token, unless a test says otherwise.
 */
function io(url, state, routes = {}) {
  const calls = [];
  return {
    calls,
    url,
    readPage: async () => state,
    fetchText: async (u, init) => {
      calls.push({ url: u, init });
      for (const [pattern, answer] of Object.entries(routes)) {
        if (u.includes(pattern)) return typeof answer === "function" ? answer(u, init) : answer;
      }
      return { status: 200, text: "" };
    },
  };
}

test("videoIdOf knows watch, shorts, live and youtu.be, and nothing else", () => {
  assert.equal(V.videoIdOf(ZOO), "jNQXAC9IVRw");
  assert.equal(V.videoIdOf("https://m.youtube.com/watch?v=jNQXAC9IVRw&t=4"), "jNQXAC9IVRw");
  assert.equal(V.videoIdOf("https://www.youtube.com/shorts/abcDEF12345"), "abcDEF12345");
  assert.equal(V.videoIdOf("https://www.youtube.com/live/abcDEF12345?si=x"), "abcDEF12345");
  assert.equal(V.videoIdOf("https://youtu.be/jNQXAC9IVRw"), "jNQXAC9IVRw");
  assert.equal(V.videoIdOf("https://www.youtube.com/"), "");
  assert.equal(V.videoIdOf("https://www.youtube.com/@3blue1brown"), "");
  assert.equal(V.videoIdOf("https://notyoutube.test/watch?v=jNQXAC9IVRw"), "");
  assert.equal(V.videoIdOf("https://www.youtube.com/watch?v=<script>"), "");
});

test("videoFields reads the record: title, channel, date, duration, chapters", () => {
  const state = json("nn.state.json");
  const f = V.videoFields(state.players[0], state.data);
  assert.equal(f.videoId, "aircAruvnKk");
  assert.match(f.title, /But what is a neural network/);
  assert.equal(f.channel, "3Blue1Brown");
  assert.match(f.channelUrl, /^http:\/\/www\.youtube\.com\/@3blue1brown|youtube\.com/);
  assert.match(f.publishDate, /^2017-10-05/);
  assert.equal(f.durationSeconds, 1120);
  assert.equal(f.duration, "18:40");
  assert.equal(f.chapterSource, "youtube");
  assert.equal(f.chapters.length, 12);
  assert.deepEqual(f.chapters[1], { start: 67, title: "Series preview" });
  assert.ok(f.description.length > 0);
});

test("chapters fall back to the description's 0:00 lines", () => {
  const state = json("zoo.state.json");
  // The page's own chapter markers are what YouTube made of these lines;
  // without them (a refetch that failed, say) the lines are read directly.
  assert.equal(V.videoFields(state.players[0], state.data).chapterSource, "youtube");
  const f = V.videoFields(state.players[0], null);
  assert.equal(f.chapterSource, "description");
  assert.deepEqual(f.chapters.map((c) => c.start), [0, 5, 17]);
  assert.equal(f.chapters[0].title, "Intro");
  // Not chapters: fewer than two, or not starting at zero.
  assert.deepEqual(V.chaptersFromDescription("1:00 Late start\n2:00 Later"), []);
  assert.deepEqual(V.chaptersFromDescription("0:00 Only one"), []);
});

test("chapters come from the chapters panel when the player bar has none", () => {
  const data = {
    engagementPanels: [{ engagementPanelSectionListRenderer: { panelIdentifier: "engagement-panel-macro-markers-description-chapters", content: { items: [
      { macroMarkersListItemRenderer: { title: { simpleText: "Start" }, timeDescription: { simpleText: "0:00" }, onTap: { watchEndpoint: { startTimeSeconds: 0 } } } },
      { macroMarkersListItemRenderer: { title: { simpleText: "Middle" }, timeDescription: { simpleText: "1:02:30" } } },
    ] } } }],
  };
  assert.deepEqual(V.chaptersFromData(data), [{ start: 0, title: "Start" }, { start: 3750, title: "Middle" }]);
});

test("pickTrack prefers a human track in the spoken language, then the auto one", () => {
  const tracks = [
    { languageCode: "ar", baseUrl: "a" },
    { languageCode: "en", baseUrl: "b" },
    { languageCode: "en", kind: "asr", baseUrl: "c" },
    { languageCode: "de", baseUrl: "d" },
  ];
  assert.equal(T.spokenLanguage({}, tracks), "en");
  assert.equal(T.pickTrack(tracks, "en").baseUrl, "b");
  // Only an auto-generated track in the spoken language: take it.
  const asrOnly = [{ languageCode: "de", baseUrl: "d" }, { languageCode: "fr", kind: "asr", baseUrl: "f" }];
  assert.equal(T.pickTrack(asrOnly, T.spokenLanguage({}, asrOnly)).baseUrl, "f");
  // No spoken-language evidence: a human track beats an arbitrary one.
  assert.equal(T.pickTrack([{ languageCode: "x", kind: "asr", baseUrl: "1" }, { languageCode: "y", baseUrl: "2" }], null).baseUrl, "2");
  assert.equal(T.pickTrack([], "en"), null);
});

// An auto-dubbed video (kqG6XqOAlt4): one auto-generated track per dubbed
// audio language, listed alphabetically, so the first is Arabic. The original
// audio is the default audio track, and its default caption is the English one.
test("spokenLanguage reads an auto-dubbed video's original audio, not its first auto track", () => {
  const langs = ["ar", "bn", "nl", "en", "fr", "de", "iw", "hi", "id", "it", "ja", "ko", "ml", "pl", "pt", "pa", "ru", "es", "ta", "te", "uk"];
  const tracks = langs.map((l) => ({ languageCode: l, kind: "asr", baseUrl: l }));
  const dub = (id) => ({ audioTrackId: id, defaultCaptionTrackIndex: 3, hasDefaultTrack: true });
  const player = {
    captions: {
      playerCaptionsTracklistRenderer: {
        captionTracks: tracks,
        audioTracks: [dub("bn.10"), dub("id.10"), dub("ar.10"), dub("en-US.4")],
        defaultAudioTrackIndex: 3,
      },
    },
  };
  assert.equal(T.spokenLanguage(player, tracks), "en");
  assert.equal(T.pickTrack(tracks, T.spokenLanguage(player, tracks)).baseUrl, "en");
  // No audio-track word at all: the first auto track is still the best guess.
  assert.equal(T.spokenLanguage({}, tracks), "ar");
  // One auto track: it is generated from the audio, whatever the audio tracks say.
  const one = [{ languageCode: "de", baseUrl: "d" }, { languageCode: "fr", kind: "asr", baseUrl: "f" }];
  const says = { captions: { playerCaptionsTracklistRenderer: { captionTracks: one, audioTracks: [{ defaultCaptionTrackIndex: 0 }], defaultAudioTrackIndex: 0 } } };
  assert.equal(T.spokenLanguage(says, one), "fr");
});

test("parseJson3 and parseXml read real caption bodies", () => {
  const segs = T.parseJson3(fixture("zoo.json3"));
  assert.ok(segs.length >= 5);
  assert.deepEqual(segs[0], { start: 1.2, text: "All right, so here we are, in front of the elephants" });
  const xml = T.parseXml(fixture("nn.xml"));
  assert.equal(xml.length, 12);
  assert.equal(xml[0].start, 4.22);
  assert.equal(T.parseXml('<timedtext><body><p t="1500" d="10">it&#39;s &amp; <s>ok</s></p></body></timedtext>')[0].text, "it's & ok");
  assert.deepEqual(T.parseJson3("not json"), []);
});

test("withFormat replaces any fmt already on the URL", () => {
  assert.equal(T.withFormat("https://y.test/t?v=1&fmt=srv3&lang=en", "json3"), "https://y.test/t?v=1&lang=en&fmt=json3");
  assert.equal(T.withFormat("https://y.test/t?v=1&fmt=srv3", ""), "https://y.test/t?v=1");
});

test("collect: page tracks come back empty, so the mobile-client tracks are used", async () => {
  const fake = io(ZOO, json("zoo.state.json"), {
    "/youtubei/v1/player": { status: 200, text: fixture("zoo.android.json") },
    // Only the fallback URLs (no exp=xpe) return a body.
    "fmt=json3": (u) => ({ status: 200, text: u.includes("exp=xpe") ? "" : fixture("zoo.json3") }),
  });
  const out = await T.collect(fake);
  assert.equal(out.fields.title, "Me at the zoo");
  assert.equal(out.transcript.source, "player-api");
  assert.ok(out.transcript.cues.length === out.transcript.segments && out.transcript.cues.length > 1, "the cues are handed back for the chat");
  assert.match(out.transcript.cues[0].text, /All right, so here we are/);
  assert.equal(out.transcript.language, "en");
  assert.equal(out.transcript.autoGenerated, false);
  const md = out.transcript.markdown;
  assert.match(md, /^# Transcript: Me at the zoo/);
  assert.match(md, /\*\*Video:\*\* https:\/\/www\.youtube\.com\/watch\?v=jNQXAC9IVRw/);
  assert.match(md, /## Intro \(0:00\)/);
  assert.match(md, /\[0:01\]\(https:\/\/www\.youtube\.com\/watch\?v=jNQXAC9IVRw&t=1s\) All right, so here we are/);
  assert.match(md, /## The cool thing \(0:05\)\n\n\[0:05\]/);
  const player = fake.calls.find((c) => c.url.includes("/youtubei/v1/player"));
  assert.equal(JSON.parse(player.init.body).context.client.clientName, "ANDROID");
  assert.deepEqual(out.warnings, []);
});

test("collect: a page track that answers is used as-is", async () => {
  const fake = io(NN, json("nn.state.json"), { "fmt=json3": { status: 200, text: fixture("nn.json3") } });
  const out = await T.collect(fake);
  assert.equal(out.transcript.source, "page");
  assert.equal(out.transcript.language, "en", "the spoken language, not the first track (ar)");
  assert.ok(!fake.calls.some((c) => c.url.includes("/youtubei/")));
  assert.match(out.transcript.markdown, /## Introduction example \(0:00\)/);
});

test("collect: stale globals after in-app navigation refetch the watch page", async () => {
  // The tab is on NN, but the page's globals still describe the zoo video.
  const stale = json("zoo.state.json");
  const nn = json("nn.state.json");
  const html = `<html><script>var ytInitialPlayerResponse = ${JSON.stringify(nn.players[0])};var meta = "};";</script>` +
    `<script>window["ytInitialData"] = ${JSON.stringify(nn.data)};</script></html>`;
  const fake = io(NN, stale, {
    "/watch?v=aircAruvnKk": { status: 200, text: html },
    "fmt=json3": { status: 200, text: fixture("nn.json3") },
  });
  const out = await T.collect(fake);
  assert.equal(out.fields.videoId, "aircAruvnKk");
  assert.equal(out.fields.chapters.length, 12, "chapters from the refetched ytInitialData");
  assert.ok(out.transcript);
});

test("collect: a current player with stale ytInitialData refetches for the chapters only", async () => {
  // What a real in-app navigation leaves (seen live 2026-09-22): the player
  // API has moved on to the new video, ytInitialData still has the first.
  const nn = json("nn.state.json");
  const zoo = json("zoo.state.json");
  const state = { href: NN, players: [nn.players[0], zoo.players[0]], data: zoo.data };
  const html = `<script>var ytInitialData = ${JSON.stringify(nn.data)};</script>`;
  const fake = io(NN, state, { "/watch?v=aircAruvnKk": { status: 200, text: html }, "fmt=json3": { status: 200, text: fixture("nn.json3") } });
  const out = await T.collect(fake);
  assert.equal(out.fields.title, nn.players[0].videoDetails.title);
  assert.equal(out.fields.chapterSource, "youtube");
  assert.equal(out.fields.chapters.length, 12);
  assert.equal(fake.calls.filter((c) => c.url.startsWith("/watch")).length, 1);
});

test("collect: fresh globals need no refetch", async () => {
  const fake = io(NN, json("nn.state.json"), { "fmt=json3": { status: 200, text: fixture("nn.json3") } });
  await T.collect(fake);
  assert.ok(!fake.calls.some((c) => c.url.startsWith("/watch")));
});

test("transcript.md does not say auto-generated twice", () => {
  const md = T.transcriptMarkdown({ videoId: "x", title: "t" }, [{ start: 0, text: "hi" }], { languageCode: "en", kind: "asr", name: { simpleText: "English (auto-generated)" } });
  assert.match(md, /\*\*Captions:\*\* English \(auto-generated\)\n/);
  const plain = T.transcriptMarkdown({ videoId: "x", title: "t" }, [{ start: 0, text: "hi" }], { languageCode: "de", kind: "asr", name: { simpleText: "Deutsch" } });
  assert.match(plain, /Deutsch \(auto-generated\)/);
});

test("collect: no captions still returns the record, and says so", async () => {
  const state = json("zoo.state.json");
  state.players[0].captions = null;
  const out = await T.collect(io(ZOO, state));
  assert.equal(out.transcript, null);
  assert.equal(out.fields.title, "Me at the zoo");
  assert.match(out.warnings.join("\n"), /no captions/);
});

test("collect: captions that never download are reported, not invented", async () => {
  const out = await T.collect(io(ZOO, json("zoo.state.json"), { "/youtubei/v1/player": { status: 200, text: "{}" } }));
  assert.equal(out.transcript, null);
  assert.match(out.warnings.join("\n"), /could not be downloaded/);
});

test("collect refuses a page that is not a video", async () => {
  await assert.rejects(T.collect(io("https://www.youtube.com/", {})), /not a YouTube video/);
});

test("extractJsonAssignment survives braces and `};` inside strings", () => {
  const html = `x; var ytInitialPlayerResponse = {"a":"}; {","b":{"c":"\\"}"}};var other = 1;`;
  assert.deepEqual(V.extractJsonAssignment(html, "ytInitialPlayerResponse"), { a: "}; {", b: { c: '"}' } });
  assert.equal(V.extractJsonAssignment("nothing here", "ytInitialPlayerResponse"), null);
});
