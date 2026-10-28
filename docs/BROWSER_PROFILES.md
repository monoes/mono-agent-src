# Many profiles per browser

Every browser profile with the MonoAgent Bridge extension installed (a Chrome
profile, an Edge profile, a separate browser) connects to the bridge on its
own. Choose which monoagent profiles run in it and those profiles' browser
actions (workflow browser nodes, `capture page`, `login`, `node run`,
`application apply`) run there, signed in as that browser's accounts.

There is no limit on how many monoagent profiles share a browser. Profiles
that share one share its logged-in accounts. Profiles on different browsers
run at the same time, including two accounts on the same site.

## Set it up

1. Run `monoagentcli extension install`, then load it in each browser
   profile (`chrome://extensions` → Developer mode → Load unpacked →
   `~/.monoagent/extension`) and pair it: `monoagentcli extension pair --open`
   opens a pairing page that pairs the browser it opens in (or paste the output
   of `monoagentcli extension pair` into the side panel's Connection settings).
2. Choose the profiles, in whichever place suits you:
   - **Desktop app → Settings → Browsers.** One row per monoagent profile;
     pick the browser it runs in (or "Any browser (default)"). Several
     profiles can pick the same browser.
   - **The browser's side panel → Connection settings → Automations in this
     browser.** Tick every profile this browser should run. Give the browser
     a name you'll recognise.
   - **A terminal:**
     - `monoagentcli extension assign <profile> "<browser name or id>"` makes
       that browser the only one for the profile (other profiles in the
       browser are untouched). `extension assign <profile>` with no browser
       removes the profile from every browser, so it uses the default browser.
     - `monoagentcli extension bind "<browser>" <profile>` also runs the
       profile in that browser, keeping any other browser it already had.
     - `monoagentcli extension unbind "<browser>" <profile>` stops running
       that profile in the browser; without a profile it clears the browser's
       whole list and it becomes a default browser again.
3. Check: `monoagentcli extension browsers` (add `--json` for `profile_ids`).

`bind`, `unbind` and `assign` change which browser (and so which logged-in
accounts) a profile acts as, so they are the user's own: an agent, the chat or
a workflow run is refused. See SECURITY.md.

The choice is stored in the browser's own extension, so it survives restarts
but can only be changed while that browser is attached; `assign` tells you
when the browser you name is not.

## Which browser runs what

For a profile P, the bridge picks:

1. the most recently connected browser whose profiles include P;
2. otherwise the most recently connected browser with no profiles (a *default
   browser*);
3. never a browser that runs only other profiles. If that's all there is, the
   run fails with "no browser is set up for profile P" and the fix.

If P is listed in two or more browsers the newest wins and `extension
browsers` flags them (sharing a browser between profiles is not a conflict;
the same profile in two browsers is); use `assign` to settle on one. Commands with no profile
go to the newest default browser, else the newest browser.

Removing a profile from a browser (`unbind`, `assign`, or unticking it in the
side panel) stops new commands for it from going there at once. A run stays
in the browser it started in, even if you change that browser's profiles
mid-run; a command already sent is not recalled. Changing them never reconnects the browser, so it can't
drop a running workflow's commands.

The first profile a browser lists is also the default for captures saved from
that browser.

## Limiting which profiles a browser may claim

A browser that paired itself has a token of its own, and by default that token
may claim any profile. To limit it:

    monoagentcli extension browsers allow <id> Work Personal   # only these
    monoagentcli extension browsers allow <id>                 # none of its own (default browser only)
    monoagentcli extension browsers allow <id> --any           # lift the limit
    monoagentcli extension approve <claim> --profiles Work     # limit it when pairing

`extension browsers list` shows what each browser may run. After that, a
browser that names a profile outside its set is refused: its connection is
closed with the unauthorized code, a binding it sends is not applied, and
`bind`/`assign`/`set_binding` for a profile outside the set fail with "not
allowed" without reaching the browser. A browser running a profile you have
since removed from its set is disconnected within a few seconds. Unbound
(default) browsers work as before.

Browsers paired before this existed, and browsers approved without
`--profiles`, are not limited until you set a limit (they show "any
profile"). The shared `extension.token` is never limited. Only you can change
a limit: the command is operator-only like `assign` and `bind`.

## Seeing every profile at once

The desktop dashboard has a **This profile / All profiles** toggle. All
profiles adds up every profile's counts, labels each workflow, run and
account with its profile, and adds a Profiles card with a **Switch** button
for each. Rows from other profiles are read-only (run, stop and open act on
the profile the app is showing); **Switch** takes you there.

The CLI side is `--all-profiles` on `summary`, `workflow list`,
`workflow executions --all` and `org summary`.

## Upgrading

Sharing a browser between profiles needs extension 1.12.0: reload the
extension from the browser's extensions page (`chrome://extensions` or
`edge://extensions`) in every browser profile, and restart the daemon or
`monoagentcli extension serve`.

Upgrade the bridge and the extension together. An older bridge routes only by
the first profile a browser reports, so the browser's other profiles fall to
the default browser there.

Everything older keeps working:

- An extension before 1.12.0 (but 1.5.0 or later) runs one profile. Its choice
  is read as a list of one. `bind` and `assign` refuse to give it a second
  profile and tell you to reload it.
- A bridge from before this change still routes by the first profile each
  browser reports. `bind`, `unbind` and `assign` stop with "restart the
  bridge" when the bridge cannot say which profiles a 1.12.0 browser runs,
  rather than overwrite them.
- An older `monoagentcli` against a new bridge sees each browser's first
  profile as `profile_id`.
- Extensions before 1.5.0 keep working as a single default browser, and old
  bridges ignore bindings until restarted.
