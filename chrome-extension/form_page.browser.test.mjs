/**
 * form_page.js, in a real browser: what it scans, what it leaves alone, and
 * that a filled value reaches the page's own input listeners.
 * WITHOUT CHROME the suite skips (see highlight_page.browser.test.mjs).
 */

import { after, describe, it } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { fileUrl, findChrome, launch } from "./browser_harness.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const CHROME = findChrome();
let browser = null;
let why = "no Chrome found — set CHROME_PATH to run these";
if (CHROME) {
  try {
    browser = await launch(CHROME);
  } catch (error) {
    why = `${CHROME} would not start: ${error.message.split("\n")[0]} (MONO_CHROME_NO_SANDBOX=1 may help)`;
  }
}
const skip = browser ? false : why;
let dir = null;
after(async () => {
  if (browser) await browser.close();
  if (dir) await rm(dir, { recursive: true, force: true }).catch(() => {});
});

const FORM = `<!doctype html><meta charset=utf-8><body>
<label for=fn>First Name</label><input id=fn name=first placeholder=Required>
<label>Last Name <input name=last value="Typed already"></label>
<label for=t>Title</label><select id=t name=title><option>Mr.</option><option>Ms.</option></select>
<input type=email aria-label="Email address" name=email autocomplete=email>
<input type=password name=pw>
<input type=checkbox name=agree>
<input name=hiddenish style="display:none">
<textarea name=bio aria-label=Bio></textarea>
<script>
  window.__events = [];
  document.addEventListener("input", (e) => window.__events.push(e.target.name || e.target.id));
</script>`;

describe("form_page.js in a browser", { skip, concurrency: 1 }, () => {
  async function open() {
    dir = await mkdtemp(join(tmpdir(), "mono-form-"));
    const page = join(dir, "form.html");
    await writeFile(page, FORM, "utf8");
    await browser.navigate(fileUrl(page));
    await browser.evaluate(await readFile(join(HERE, "form_page.js"), "utf8"));
  }
  const json = async (expr) => JSON.parse(await browser.evaluate(`JSON.stringify(${expr})`));

  it("describes only the fields a person would fill", async () => {
    await open();
    const scan = await json("MonoFormPage.scan()");
    const by = Object.fromEntries(scan.fields.map((f) => [f.name, f]));
    assert.deepEqual(Object.keys(by).sort(), ["bio", "email", "first", "last", "title"]);
    assert.equal(by.first.label, "First Name");
    assert.equal(by.last.label, "Last Name");
    assert.equal(by.last.filled, true);
    assert.equal(by.email.label, "Email address");
    assert.deepEqual(by.title.options, ["Mr.", "Ms."]);
  });

  it("fills like typing, leaves typed text alone, and cleans up after itself", async () => {
    await open();
    const scan = await json("MonoFormPage.scan()");
    const id = (name) => scan.fields.find((f) => f.name === name).id;
    const done = await json(
      `MonoFormPage.apply(${JSON.stringify([
        { id: id("first"), value: "Ada" },
        { id: id("last"), value: "Overwritten" },
        { id: id("title"), value: "Ms." },
        { id: id("title"), value: "Nope" },
        { id: "m99", value: "x" },
      ])})`,
    );
    assert.equal(done.filled, 2);
    assert.equal(await browser.evaluate('document.querySelector("[name=first]").value'), "Ada");
    assert.equal(await browser.evaluate('document.querySelector("[name=last]").value'), "Typed already");
    assert.equal(await browser.evaluate('document.querySelector("[name=title]").value'), "Ms.");
    assert.deepEqual(await json("window.__events"), ["first", "title"]);
    assert.equal(await browser.evaluate('document.querySelectorAll("[data-mono-fill]").length'), 0);
  });
});
