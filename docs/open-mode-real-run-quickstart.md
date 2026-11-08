# Open-ended mode: your first real run (acceptance guide)

What this is: you start a run from a chat in **Open-ended** mode. A boss agent plans and spawns coding sessions on its own until the budget or limits run out, or you press Stop. No human approval happens during the run. The sessions run as your user (files, network, database, keychain) and add no isolation of their own. Use a throwaway profile and folder. Commands below take `--profile "$P"`.

**Do not:** use your real social or mail profile; `kill -9` the host; use `--everything` or `--lift`; switch `coder set --peercred-binding off` while a run is open (it is refused while the run is not quiescent); use `doomsday render`/`probe` as a "dry run" (probe runs the real containment steps).

## 1. Prerequisites

| OS | Needed | What it means |
|---|---|---|
| Linux | cgroup v2 and a systemd user session with delegation; run from a desktop login (not an ssh or tty `session-N.scope`) | Stop proves the run's unit is gone and prints a bare `stopped` |
| macOS | local APFS/HFS+ volume | best effort: processes held in a process group; a process that left the group is not covered |
| Windows | local NTFS volume | best effort: processes held in a Job Object; a process that left the job is not covered. The process check is also weaker than on Linux/macOS (a program running as you can defeat it); the start confirmation says so |

Both:

- Signed in to monoes.me (`monoagentcli account status`); only `doomsday stop` works signed out.
- The profile has a vault key: add any secret first (`monoagentcli --profile "$P" secret add --kind secret --name scratch-key`).
- Coder mode ready with peer-credential binding on (once, when no coder turn runs): `monoagentcli --profile "$P" coder set --peercred-binding on`, then `coder status` must show `peer-credential binding of turn tokens: on`. It refuses where the machine cannot run the binding service.
- No coder host running: `monoagentcli --profile "$P" coder host status`.
- `monoagentcli --profile "$P" doomsday status` answers `no run`.

## 2. First run, from the chat

1. Create a chat on a scratch folder and choose **Open-ended** in the chat mode switch before the first message (it cannot change later). If it says "Not available", the reason shown is the probe's; fix it or stop.
2. Fill the charter small: a goal with one tiny checkable result (for example "create NOTES.txt containing hello, commit"), at least one criterion, **Careful** limits, a **fixed budget of 1 USD**, the verifier a different model than the boss, no authority lines ticked, per-ask approval off.
3. Review: the screen shows the charter hash and lifted-line count. Type the phrase it asks for (`I understand: lift N lines, charter xxxxxxxx`, N and the first 8 hash characters from the screen), press **Start the run**, and answer the operating system's own confirmation dialog.
4. Watch: the orbit, or **Show the boss in AI agents** on the run notice, opens the boss conversation. The Control room page (sidebar, appears while a run exists) shows state, spend and the decision feed.
5. Steer by typing in the chat (it reaches the boss as untrusted guidance, never an approval, and lifts no limit). CLI equivalent: `coder sessions steer CHAT "text"`.
6. Stop: **Stop run** in the chat header (confirm), or in the Control room. CLI: `monoagentcli --profile "$P" doomsday stop`.

## 3. What Stop prints

- Linux: first line `stopped`, exit 0.
- macOS/Windows: `stopped (best effort: processes that left the group are not covered)`, exit 0. Check by hand that nothing of the run is left running.
- Failure lines (exit 1): `Stop marker NOT RECORDED`, `NOT stopped: N process(es) survive: ...`, `Stop intent NOT RECORDED`, or `NOT stopped:` followed by reasons (the run stays `cleanup-pending`). Run `doomsday stop` again; it is safe to repeat. Exit 8 means the run is stopped but its sessions or chat message were not all finished: run it again.
- If the reason says the emergency socket is missing but the host lock is held: `monoagentcli --profile "$P" coder host stop` (running turns keep going; the next host picks them up), then `doomsday stop` again.
- `database is busy` / `stop could not run`: nothing was stopped; retry, then `monoagentcli doctor`.

## 4. Check state

`doomsday status` (`no run` or the run and its state), `doomsday overview`, `doomsday feed --limit 20`, `coder sessions list` (the boss is marked `boss`; no turn may still run after Stop), `coder host status`.

## 5. Checklist to fill in and send back (per OS)

```
OS / version / install (binary or app):            monoagentcli version:
[ ] prerequisites all met (list any that were not):
[ ] Open-ended mode offered in the chat (or the reason shown):
[ ] charter preview, phrase, native dialog: worked / what went wrong:
[ ] run started; boss visible via "Show the boss in AI agents":
[ ] steer message arrived (boss reacted):
[ ] spend stayed within 1 USD (Control room / overview):
[ ] Stop from the chat header: exact first line printed:
[ ] after Stop: doomsday status = no run; coder sessions list shows nothing running;
    no leftover processes (Linux: systemctl --user list-units 'monoagent-*'):
[ ] anything surprising, with the exact text on screen:
```

Send the filled list plus the `doomsday stop` output. Do not send the database or the `~/.monoagent/doomsday/` witness file.

## 6. Known limits

- Only a stub end-to-end test exists. No real cgroup unit, Job Object or process group, and no real model run was tested by the authors; your run is the first.
- Mission goal criteria are stored and shown, but there is no automatic verifier that stops the run when they are met: the budget, the limits and Stop end it.
- macOS and Windows containment is best effort, as above.
