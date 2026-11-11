# Mission pursuit: how to try it

What this is: a mission can keep working while you are away. Every few minutes the coder host asks a second AI (the judge) whether the mission is on track, and if it is, wakes the lead for another turn. Both cost real money on your AI account. **It is OFF by default**: nothing runs until you flip the host switch AND start the pursuit of a mission yourself.

Commands below take `--profile "$P"` and a mission session id `ID` (written `3` or `#3`). Use a throwaway profile and a small mission.

## 1. Turn it on (two switches)

1. Host switch: start the coder host with `MONOAGENT_PURSUIT_JUDGE=runtime/model` set, naming the judge's model. It must be a different runtime/model name from the lead's (case and a trailing `@version` are ignored; two names for one model are not recognised). It is read once when the host starts, so set it, then restart the host.
2. Optional: `MONOAGENT_PURSUIT_WAKE_CAP_USD` is X, the most one wake-up turn may cost. Unset or empty means $2; anything else must be a number from 0.01 to 10, or the loop stays off and `coder host status` says why.
3. Confirm the goal with at least one rubric criterion (a criterion you judge yourself). An agent may only propose; you confirm. Criteria come from a JSON file: an array of `{"kind","spec","expected"}`.

```
monoagentcli --profile "$P" task session goal propose ID --criteria goal.json   # an agent or you; stays a proposal
monoagentcli --profile "$P" task session goal show ID                           # read the criteria and the hash
monoagentcli --profile "$P" task session goal confirm ID --hash HASH            # you only; final
monoagentcli --profile "$P" task session goal set ID --criteria goal.json       # propose + confirm in one step, you only
```

A confirmed goal is final; a changed goal means a new mission.

4. Start, pause, resume, look:

```
monoagentcli --profile "$P" task session pursuit start ID [--parked-max N] [--pct N] [--wakes-max N]
monoagentcli --profile "$P" task session pursuit pause ID
monoagentcli --profile "$P" task session pursuit resume ID
monoagentcli --profile "$P" task session pursuit status ID        # add --json for a machine-readable answer
```

Or do the confirmation and the start in ONE step, for exactly what you read:

```
monoagentcli --profile "$P" task session pursuit preview ID [--json]      # read only: the goal whole, the dollars, the host, the blockers, the consent digest
monoagentcli --profile "$P" task session pursuit begin ID --goal-hash HASH --consent DIGEST --chat CHAT
```

`begin` confirms the goal (if it is only a proposal) and starts the pursuit in one transaction. It refuses, writing nothing, if the goal changed (`stale`), if the model, the host switch or the costs changed since the preview (`consent_stale`), if the chat is not the mission's, if the pursuit already started, or if something blocks it: `budget-unknown`, `lead-uncapped`, `judge-pin`, `no-chat`, `host-off` (no running coder host has the loop on), `host-info-invalid` (what the host published is not a usable value), `no-rubric`. The host figures in the preview are as the host reported them; `host.json` is display-only and not proof. The desktop app starts a pursuit the same way, after a dialog and a phrase you type. **Read the dialog:** a script in the app's web page can raise the confirmation dialog at a moment of its choosing; it cannot click Yes for you, but you might click it by habit.

`start` locks the goal. The away limits count since you last acted: `--parked-max` questions waiting for you (1 to 20, default 3), `--pct` percent of the mission budget spent (1 to 100, default 20), `--wakes-max` wake-ups of the lead (1 to 50, default 6). They are fixed once the pursuit has started. Only you can start, pause or resume; an agent is refused. `status` is open to agents.

If the host switch is off, a started pursuit does nothing and the lead's check-ins go on.

## 2. What status shows

- The host line: loop ON with the judge model and X, or OFF with the reason, or "the coder host is not running" (nothing is judged or woken without a running host).
- Since you last acted: wake-ups used of the limit, parked questions used of the limit, metered spend against the budget and its percent limit.
- This host, last hour: pursuit wake-ups used of 12 and when the oldest frees; judge calls used of 12; judge rounds that failed in a row (of 3) and how the last one ended.
- Of the spend: what the judge cost (a call whose cost the runtime did not report counts at the full $0.50) and what the lead's and helpers' turns cost.
- Why it waits for you, if it does:

| Reason | Plain meaning |
|---|---|
| `parked` | too many questions wait for your answer |
| `spend` | the spend since you last acted reached its limit (a big carve to helpers also counts) |
| `wakes` | the lead was woken as often as allowed since you last acted |
| `budget-unknown` | the mission has no known budget, so no share can be measured; set one |
| `lead-uncapped` | the lead has no spend cap of its own; set a budget for its chat |
| `no-chat` | the mission has no chat to wake |
| `judge-pin` | the judge's model name equals the lead's, or the lead's model is not known |
| `judge-failing` | 3 judge rounds in a row failed; it is not called again until you act |
| `goal-check` | a goal check is running (section 7); nothing else is judged or woken until it ends, in at most about 13 minutes |
| `goal-answer` | a goal check passed and the criteria that are yours wait for your answers (section 7) |
| `verifier-pin` | the goal checker's model name equals the judge's or the lead's, or the lead's model is not known (section 7) |
| `verifier-failing` | the goal checker could not be used by 3 checks in a row |
| `cannot-check` | the goal cannot be checked here: the project folder cannot be used, or the checker could not be built |
| `meter-unreadable` | the judge ledger or turn journals could not be read; clears when they read again |
| `limits` | the away limits are out of range |
| `engine-parked` | a stored strategy row failed verification and the strategy engine stopped |

The loop also stays quiet, with no waiting reason shown, once every task is done, verified or blocked and the lead has been woken once to close the mission; you close the mission yourself if it does not. Any task change earns one more such wake, archiving a task included. A mission whose only open tasks are blocked is never woken: it waits for you.

Any waiting reason clears when you act (answer, start again, raise a limit). Right after the last allowed wake-up there may be a few seconds with no reason before `wakes` shows.

## 3. What it can cost (worst case, defaults: X = $2, judge asked to stay under $0.50)

| Scope | Worst case |
|---|---|
| One mission, one hour | about $15 (6 wake-ups x $2 + 6 judge calls x $0.50), about $16.50 with 3 failed judge rounds |
| The whole host, one hour | about $30 ($24 of wake-ups + $6 of judge calls; 12 of each an hour host-wide) |
| One mission until the away limits stop it, mission budget $20 | about $6.50 (20 percent = $4, plus one last wake-up $2 and one last judge call $0.50) |
| Same, mission budget $100 | at most about $22.50 by the 20 percent rule ($20 + $2 + $0.50); about $17 if the 6-wake limit binds first, but judged rounds that wake nobody are not counted by the wake limit, so only the first figure is a hard bound |

Only the 20 percent rule is a hard bound. Read this too:

- Helpers the lead starts are NOT covered by X. In one wake the lead can carve its whole remaining budget (up to the 80 percent share maximum) out to helpers, and that is spent outside X, bounded only by the carve limits. Carved money not yet spent counts in the 20 percent rule, so a big carve makes the pursuit wait ("spend").
- X and the judge's $0.50 are requests to the AI runtime, checked from the cost it reports afterwards. A call can overshoot by one model call; a turn that reports no cost is charged its whole cap. On a subscription plan the runtime's "cost" may not be a bill.
- Host-wide limits: at most 12 pursuit wake-ups and 12 judge calls an hour, across all missions. Per mission: 6 wake-ups since you last acted (the count does not depend on the meter).
- One wake-up queued before you act can still run after your action resets the count.

## 4. Switching it off

- One mission: `pursuit pause ID`. Nothing new starts, and the lead's check-ins do not come back; only your `resume` starts it again.
- Everything: unset `MONOAGENT_PURSUIT_JUDGE` and restart the coder host. There is no live host-wide off and no `pursuit stop`.
- A judge call or a lead turn already running is not cancelled: it finishes and is paid for (a paused pursuit drops the judge's answer).

## 5. What it cannot do

- It cannot approve anything: the judge sees counts, ids and your confirmed criteria, never an agent's text, and nothing an agent wrote reaches it as instructions.
- It cannot close the mission. Only you close it, and you are always the last judge.
- It does not change who answers a question or what happens to one nobody answers (same as for any mission); the limits above carry the brake.
- No agent can start, pause, resume or switch it.

## 6. Try it safely

```
[ ] throwaway profile and folder; a small mission budget (for example 5 USD), so 20 percent is 1 USD
[ ] the mission's chat has its own budget (else `lead-uncapped`) and a pinned model different from the judge's
[ ] host started with MONOAGENT_PURSUIT_JUDGE=<other runtime/model>, MONOAGENT_PURSUIT_WAKE_CAP_USD=0.5 (a small X)
[ ] goal with one tiny rubric criterion, confirmed; pursuit start with --wakes-max 2 --pct 10
[ ] `pursuit status ID` shows the host line ON and the judge model you set
[ ] watch status; `pursuit pause ID` when you have seen enough; unset the flag and restart the host afterwards
[ ] anything surprising, with the exact text on screen:
```

The opt-in cap smoke test (`internal/monomind/capsmoke_realrunner_test.go`, build tag `realrunner`) spends real money and is for developers only; do not run it as part of this.

## 7. Goal checks (what turns on, what it costs, what it cannot do, how to stop it)

**What it does.** With the second host switch on, the host itself checks whether the goal you locked is met, while you are away. When every task of the mission is settled (done, or verified and waiting in review, with nothing blocked and no writer branch left unmerged), the lead and its helpers are idle and nothing in the project folder has been written for two minutes, the host takes a private copy of the project and re-runs the machine criteria of the locked goal on it: `file` criteria look at the copy, `command` and `output` criteria RUN A PROGRAM on this computer, on the copy, with no network. If they all pass, a third AI (the checker) is shown only the host's measurements (results, exit codes, digests, and, for programs that ran confined, a short excerpt of what they printed) and may only veto. A pass never accepts anything: the criteria that are yours (rubric) wait for your answer (`goal check`, then `goal answer`), and the mission is closed by you alone. A failed check does not wake the lead; it is shown in `pursuit status` and `goal check`. (This version only runs the checks and records them; the wake-ups that tell the lead about a failed, rejected or accepted check come in a later step.)

**The switch.** Start the coder host with BOTH `MONOAGENT_PURSUIT_JUDGE=runtime/model` and `MONOAGENT_PURSUIT_VERIFIER=runtime/model`, the checker's model. The checker's name must differ from the judge's and from the lead's. It is OFF unless set, works only with the judge switch on, is read once when the host starts, and `coder host status` and `pursuit status` say whether it is on and why it is not.

**What it costs, at most.** Each check asks the checker one question and asks it to stay under $0.50 (a request to the AI service: a runtime that reports more is charged what it reports, up to a point; a check that never reaches the checker costs nothing; a check that is cut short, or whose cost the runtime does not report, is counted at the full $0.50). Per mission: 2 checks since you last acted, 6 since your last accept or reject. This host: 4 checks an hour, one at a time, and no check runs longer than 10 minutes. The checks count toward the 20 percent rule like the judge does. Worst case: two checks more per mission-hour while you do not act (about $16 in all, against about $15 without checks), and if you act during the hour only the host limit of four bounds it (up to two dollars more); four more per host-hour (about $32, against about $30 without checks); plus one last check ($0.50) before the away stop. A running host also uses up to two CPU cores for at most 40 minutes an hour. If three checks in a row could not use the checker (unavailable, an invalid answer, or another model than its pin), checks stop until you act; this counts across away windows.

**What it cannot do.** It cannot accept or close: a machine pass and the checker's "ok" approve nothing. The checker cannot see the lead's words, only the host's measurements, and the lead and the judge never see a program's output. Programs that ran with a confinement are fenced by your systemd user manager (a unit per program: a private copy of the project, no network, no access to your home, other processes, secrets or sockets, a memory, task and time cap, killed with its whole process group); this needs Linux with systemd 256 or later. On macOS, Windows, or Linux without that, command criteria are not run (`cannot-check`) and only `file` criteria and your own answers work. If the fence is there but fails its self-test (a broken fence), the check is refused and the status names which test failed; nothing falls back to running unfenced. The one exception is yours: `task session pursuit allow-unconfined-checks ID --on` (terminal only, per mission, never in the app) lets programs run on this computer as you, with only a process group, no network and limits, where no fence exists at all; every result then says UNCONFINED and no program output goes to the checker.

**Not yet done for real.** No real coder host has been run with the checker switch on, and the heavy self-tests (the fork bomb and the memory hog) have never been run for real: they run inside the fence at the first check after a host starts, on a machine you choose, and the package's own heavy tests run only with `MONOAGENT_CONFINER_HEAVY_TESTS=1`. The light self-tests and one whole confined check were run for real on one Linux machine (systemd 262). Treat the first run as a trial on a throwaway mission.

**Which folder.** The project is the folder of the lead's chat. Any project under your home folder is fine, and so is a folder under `/tmp` (but not `/tmp` or `/dev/shm` themselves). Refused, with everything inside them: your home folder itself and anything that contains it (`/home`, `/`); the credential folders under home (`~/.ssh`, `~/.gnupg`, `~/.aws`, `~/.azure`, `~/.kube`, `~/.docker`, `~/.config`, `~/.claude`, `~/.monomind`, `~/.local/share/keyrings`, `~/.password-store`, `~/Library/Keychains`); the host's state folder (`~/.monoagent`, always, even when the database lives elsewhere, and how its name is cased on macOS and Windows does not matter); other users' homes under `/home` and `/Users`; and the system trees (`/etc /proc /sys /dev /run /var/run /boot /root /usr /bin /sbin /lib /lib64 /var/lib`). Links are resolved first. A refused mission waits as `cannot-check`. A project with more than 200,000 files is not checked either (the host log says `too-many-files`). A tree is read in full when it has changed and at least every ten minutes while it has not.

**Read before you turn it on.** The criteria you confirm are text (`go test ./...`, `sh check.sh`); the files they run are written by the workers after you confirm. A confined run cannot reach your files or the network, so `go test ./...` fails where it needs to download modules; use criteria that are self-contained. Before the first check after the host starts, and then at most every ten minutes, the fence proves itself: light tests (it can read and write only the copy, sees no secrets, sockets, other processes or network) before every check, and heavy ones (a fork bomb and a memory hog run INSIDE a unit, to see its task and memory caps hold, and a child that tries to outlive the check) at the first check and every ten minutes after, which can take a few minutes and a couple of gigabytes of memory for a moment. If any test does not hold, no command is run and the status names which one. The package's own heavy tests are for a machine you choose: they run only with `MONOAGENT_CONFINER_HEAVY_TESTS=1`, never on a shared computer.

**How to stop it.** One mission: `pursuit pause ID` (a check that is running is cancelled within seconds and counted at $0.50). Everything: unset `MONOAGENT_PURSUIT_VERIFIER` and restart the coder host (a check that was running is ended as abandoned at the next start). There is no live off.
