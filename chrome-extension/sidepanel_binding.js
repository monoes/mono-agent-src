/**
 * MonoAgent Bridge — the side panel's "Automations in this browser" field.
 *
 * Draws the binding checklist (any number of profiles may share this
 * browser) from the same profile list the capture picker uses, and saves straight to chrome.storage.local. The worker notices the
 * write and tells the bridge (browser_binding.js), so there is one path for
 * a binding change whoever makes it.
 */

(function (root) {
  "use strict";

  function install(opts) {
    const B = root.MonoBrowserBinding;
    const doc = opts.doc;
    const list = doc.getElementById("binding-profile");
    const label = doc.getElementById("binding-label");
    const save = doc.getElementById("binding-save");
    const msg = doc.getElementById("binding-msg");
    let state = { instance: "", profile: "", profiles: [], label: "" };

    function say(tone, text) {
      msg.dataset.tone = tone;
      msg.textContent = text;
    }

    // What is ticked right now. Before the first redraw, the saved set.
    function ticked() {
      const boxes = Array.from(list.querySelectorAll("input[type=checkbox]"));
      return boxes.length ? boxes.filter((b) => b.checked).map((b) => b.value) : state.profiles;
    }

    function redraw() {
      const keep = list.children.length ? ticked() : state.profiles;
      list.textContent = "";
      const choices = B.choices(opts.profiles(), keep);
      if (!choices.length) {
        const none = doc.createElement("p");
        none.className = "note-line";
        none.textContent = "No profiles yet.";
        list.appendChild(none);
        return;
      }
      for (const c of choices) {
        const row = doc.createElement("label");
        row.className = "check-row";
        const box = doc.createElement("input");
        box.type = "checkbox";
        box.value = c.id;
        box.checked = c.checked;
        const text = doc.createElement("span");
        text.textContent = c.name;
        row.appendChild(box);
        row.appendChild(text);
        list.appendChild(row);
      }
    }

    async function refresh() {
      state = await B.load(opts.storage, { userAgent: opts.userAgent });
      label.value = state.label;
      list.textContent = ""; // drop unsaved ticks: storage is the truth
      redraw();
    }

    save.addEventListener("click", async () => {
      try {
        const profiles = ticked();
        await B.setBinding(opts.storage, { profiles, label: label.value });
        await refresh();
        say("ok", profiles.length
          ? "Saved. These profiles' automations now open their tabs in this browser."
          : "Saved. This is a default browser again.");
      } catch (err) {
        say("error", err.message);
      }
    });

    return { refresh, redraw };
  }

  root.MonoPanelBinding = { install };
})(globalThis);
