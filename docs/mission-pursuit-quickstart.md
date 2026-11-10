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
| `meter-unreadable` | the judge ledger or turn journals could not be read; clears when they read again |
| `limits` | the away limits are out of range |
| `engine-parked` | a stored strategy row failed verification and the strategy engine stopped |

The loop also stays quiet, with no waiting reason shown, once every task is done, verified or blocked and the lead has been woken once to close the mission; you close the mission yourself if it does not. A changed task earns one more such wake.

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
