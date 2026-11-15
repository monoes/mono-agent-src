/**
 * content.js setFiles: the two ways a target can be unusable say different
 * things. Nothing matched (this includes a file input inside an iframe, which
 * the content script does not reach: no all_frames) vs the match is not a
 * file input. Needs no browser: setFiles is cut out of content.js and run
 * with stand-ins for the page.
 */

import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import vm from "node:vm";

const HERE = dirname(fileURLToPath(import.meta.url));

async function loadSetFiles(resolveElement) {
  const src = await readFile(join(HERE, "content.js"), "utf8");
  const start = src.indexOf("async function setFiles(");
  const end = src.indexOf("\n}\n", start);
  assert.ok(start > 0 && end > start, "setFiles not found in content.js");
  const ctx = vm.createContext({ resolveElement, openedFileInput: async (e) => e, Error, atob });
  vm.runInContext(`${src.slice(start, end + 3)}; this.setFiles = setFiles;`, ctx);
  return ctx.setFiles;
}

// resolveElement as content.js has it: it throws when nothing matches.
const none = (sel) => () => {
  throw new Error(`Element not found for selector: ${sel}`);
};
const rejection = async (p) => p.then(() => "resolved", (e) => e.message);

describe("content.js setFiles error messages", () => {
  it("nothing matched: says so, names the selector and the iframe limit", async () => {
    const setFiles = await loadSetFiles(none("#up"));
    const msg = await rejection(setFiles({ selector: "#up", fileData: [] }));
    assert.match(msg, /^set_files: no element matched #up in this frame/);
    assert.match(msg, /file inputs inside iframes are not supported/);
    assert.doesNotMatch(msg, /not a file input/);
  });

  it("a match that is not a file input: says that, without the iframe hint", async () => {
    const setFiles = await loadSetFiles(() => ({ tagName: "DIV", type: undefined }));
    const msg = await rejection(setFiles({ selector: "div", fileData: [] }));
    assert.equal(msg, "set_files: the element matched is not a file input");
  });

  it("a file input still goes on to the file checks", async () => {
    const setFiles = await loadSetFiles(() => ({ tagName: "INPUT", type: "file", multiple: false }));
    const msg = await rejection(setFiles({ selector: "input", fileData: [] }));
    assert.equal(msg, "set_files: no file data provided");
  });
});
