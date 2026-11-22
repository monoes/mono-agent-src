# Pluggable decision models — plan for parallel implementation

- **Status**: implemented (2026-11-22) on `feat/decision-providers`, after two review rounds; see §10
  "As built" for what differs from v1. v1 (2026-11-21) was written from three research passes
  (mono-agent call sites, monomind org runtime, decision-model landscape). Not yet reviewed by the
  owner.
- **Branch**: `feat/decision-providers`, cut from `origin/master` at `9c41d0c67`. Never pushed or
  merged by an agent.
- **Next migration**: `170` (WS0 only). Re-check master before merge.

## 1. Goal

Today every decision in mono-agent goes to one provider: TypeSafe Jev. The user wants to
(a) use whatever decision model they like, and (b) have decision models usable in more places —
as a workflow node and as a member of an org.

A *decision model* here is anything that answers the three typed questions Jev defines —
`choice` (1 of N), `noul` (yes/no), `score` (a level on an ordered rubric) — about a JSON state,
with a probability per option, and **never generates free text**.

## 2. What research established

1. **No other product is a drop-in Jev.** Cohere Classify is deprecated, Patronus GLIDER is retired,
   routers (Not Diamond, Martian, RouteLLM) only pick an LLM. "Other decision models" in practice
   means: a general LLM read through logprobs, an LLM constrained to an enum, or a small zero-shot
   classifier.
2. **One generic OpenAI-compatible adapter covers most users**: OpenAI, Ollama (`/v1`, ≥ 0.12.11),
   llama.cpp, vLLM, OpenRouter, Together, Fireworks, LM Studio, Groq, and self-hosted judge models
   (Prometheus, Selene, Granite Guardian, ShieldGemma).
3. **Providers differ in capability** — Groq rejects `logprobs`, Anthropic has none, Fireworks caps
   alternatives at 5, most cap at 20, reasoning models often return none. So capabilities are
   **probed**, not hardcoded.
4. **mono-agent has one choke point.** ~27 call sites take a concrete `*jev.Client`; all but two
   get it from `jevconf.ResolveClient` (`internal/jev/jevconf/key.go:236`). `jev.Question` /
   `jev.Answer` / `jev.Response` are already a provider-neutral contract, and `Client.Ask` already
   validates every answer against its question.
5. **monomind has a separate client** (`.claude/helpers/jev-picker.cjs`, CommonJS, copied into user
   projects by `init`) with env-only config. A decision model in an org must be a role **kind**
   (like `kind: "endpoint"`), not a runtime: runtimes must stream assistant text and tool calls.
6. **Org approvals are decided in mono-agent** (`internal/orgdecide`, interface `DeciderImpl`), not
   in monomind.

## 3. Decisions

| # | Decision |
|---|---|
| D1 | **The seam is inside `jev.Client`, not at the call sites.** `Client` gains a `Backend` field. `nil` means TypeSafe's wire protocol, byte-for-byte today's behaviour. `Ask` keeps validation, `repairChoice`, latency and `OnResult` for every backend. No call site, and none of the ~35 test files using `jevtest`, changes type. |
| D2 | **Names stay.** Package `jev`, tables `jev_usage`/`jev_suggestions`, settings `jev.<profile>.…`, the CLI group `jev`, node type `browser.jev`, resolver prefix `jev:` are kept. "Jev" becomes the name of the decision feature; TypeSafe is its default provider. Renaming is a non-goal (it would break stored data, org files, and string-parsed couplings). |
| D3 | **Doctrine widens by one sentence, not in kind.** AGENTS.md admits Jev as a *non-generative decision provider*. Other providers are admitted on the same terms: an adapter may only return an option the code enumerated. The generic adapter requests **one token** (`max_tokens: 1`) and discards anything that is not an allowed label. No adapter ever returns provider text to a caller. All text generation stays on local agents via monomind. |
| D4 | **Default is unchanged.** With no provider configured, the active provider is `typesafe` and every surface behaves exactly as today. Each workstream has a test proving it. |
| D5 | **A key goes only to its own provider's host.** A provider record binds `{base_url, secret_name}`. Only the operator's own terminal may add or edit a provider (CLI group `jev` is already `cmRefused` in client/agent mode; `jev provider add|remove|use` are `ClassGlobal`). `base_url` must be https, or http to loopback (the existing `checkOverride` rule). A step's `api_key` config never pairs with a non-default provider. Extend `source_pin_test.go` and the `jevtest/callers` exfil suites to every provider. |
| D6 | **Probabilities are computed in Go, identically for all providers**, with Jev's published formulas: choice `confidence = (p_max − 1/n)/(1 − 1/n)`; score `max(0, 1 − Σ pᵢ·|i−m| / MAD_unif)`; score value = expected level. Gates keep comparing the **top probability** (`jev.Top`). |
| D7 | **Every answer says how its probability was obtained**: `native` (TypeSafe), `logprob`, `sampled` (k repeats of an answer-only model, default k = 5), or `none`. It is recorded in `jev_usage` and shown in status. A provider whose kind is `none` reports probability 0 for gating purposes, so it can never pass a threshold > 0 — surfaces fall back to what they do today. |
| D8 | **Adapters hide capability gaps.** A request with N questions is fanned out (≤ 8 in flight) when the provider takes one question per request. A choice with more options than the provider's top-k allows is answered hierarchically (groups, then members) inside the adapter. Call sites stay unaware. |
| D9 | **Provider choice is layered**: per-call explicit (`provider` in a node's config / org decider) → per-profile per-surface (`jev.<profile>.<surface>.provider`) → machine default (`jev.provider`) → `typesafe`. |
| D10 | **Egress consent names the host.** `jev enable <surface>` already prints what leaves the machine; it now also prints *where to*. Switching the machine default to a non-loopback provider re-prompts for every enabled surface (`--yes` in non-interactive use). |
| D11 | **No network in tests.** Adapters are tested against `httptest` fakes of each wire format. Real-API tests are opt-in by env, like `browserjev/e2e_test.go`. |
| D12 | **The org member is monomind's to add.** mono-agent ships the decider-provider field now; the `kind: "decision"` role is filed as a monomind issue (§7) and mono-agent's side waits on its capability string. Until then the same thing is buildable today: an `endpoint` role pointing at a mono-agent workflow that contains the new `ai.decide` node. |

**Open to the owner** (defaults apply):

| # | Question | Default |
|---|---|---|
| O1 | Allow plain-http providers on a private LAN address (a home Ollama box)? | No — https or loopback only. |
| O2 | May a `sampled` provider drive auto-decide surfaces (HIL `auto_decide`, org decider, people links)? | Yes, labelled; a unanimous 5-of-5 is p = 1.0. |
| O3 | Ship a Gemini-native adapter? | No — 3.x returns no logprobs; reachable through OpenRouter. |

## 4. Architecture

```
call site ──► jevconf.NewClient(ctx, db, profile, key, model, surface)
                 │  picks provider (D9), resolves that provider's key (D5)
                 ▼
            *jev.Client{Backend}  ── Ask(): validate · repair · OnResult (unchanged)
                 │
     ┌───────────┼──────────────┬─────────────────┐
  nil = TypeSafe  openaicompat   anthropic         hfzeroshot
  /v1/systemone   logprobs,      enum schema,      /zero-shot
  (native)        1 token        k-sampled         choice + noul
```

### 4.1 `internal/jev` (WS0)

```go
// backend.go
type Backend interface {
    // Answer returns one validated-shape Answer per question. It must only ever
    // name options present in the question.
    Answer(ctx context.Context, req *Request) (*Response, error)
    Capabilities() Capabilities
}
type ProbabilityKind string // "native" | "logprob" | "sampled" | "none"
type Capabilities struct {
    Kinds               []string        // TypeChoice, TypeNoul, TypeScore
    Probability         ProbabilityKind
    MaxChoiceOptions    int             // per single request; adapter chunks above it
    QuestionsPerRequest int             // 0 = unbounded
    Models              bool            // supports listing models
}
```

- `Client` gains `Backend Backend` and `Provider string` (display name, default `"typesafe"`).
  `Ask` calls `c.Backend.Answer` when set, else today's `post`. `Models` delegates likewise.
- `Response` gains `Provider string` and `Probability ProbabilityKind` (both `json:"-"`-safe additions);
  `Usage` gains `CostUSD *float64` (nil = unknown).
- `probmath.go`: `ChoiceConfidence`, `ScoreConfidence`, `ExpectedScore`, `Renormalise(map)`,
  `FromLogprobs(top []TokenLogprob, labels []string) (probs map[string]float64, coverage float64)`
  — label variants (`A`, ` A`, `a`) summed, renormalised over the allowed set.
- A question kind the backend lacks → `ErrUnsupported` (wraps into the caller's normal failure path).

### 4.2 Provider config — `internal/jev/jevconf/provider.go` (WS0)

```go
type Provider struct {
    Name       string `json:"name"`        // "typesafe" is built in and reserved
    Kind       string `json:"kind"`        // typesafe | openai | anthropic | hf-zeroshot
    BaseURL    string `json:"base_url"`
    Model      string `json:"model"`
    SecretName string `json:"secret_name"` // machine-vault entry; "" = keyless (local server)
    Caps       jev.Capabilities `json:"caps"` // filled by Probe
    USDPerMTokIn, USDPerMTokOut float64    // optional, for usage estimates
}
func Providers(db) ([]Provider, error)            // settings key "jev.providers" (JSON)
func SaveProvider(ctx, db, Provider) error        // operator-only (MayNameHost)
func RemoveProvider(ctx, db, name) error
func ActiveProvider(db, profileID, Surface) string // D9 layers 2–4
func SetDefaultProvider / SetSurfaceProvider
func RegisterKind(kind string, build func(Provider, key string) (jev.Backend, error))
```

- `ResolveClient` gains the provider step: `typesafe` → today's path untouched; otherwise resolve the
  provider's own secret from the machine vault and call the registered builder. An explicit
  step-config key with a non-`typesafe` provider is refused (D5).
- `NewClientFor(ctx, db, profileID, provider, model, surface)` for call sites that pass an explicit
  provider (nodes, org decider).
- Migration `170_jev_usage_provider.sql`: `ALTER TABLE jev_usage ADD COLUMN provider TEXT`,
  `output_tokens INTEGER`, `probability_kind TEXT`, `cost_usd REAL` (all nullable). `Recorder` and
  `UsageSince` use them; `USDPerInputToken` stays the TypeSafe default.
- `internal/jev/jevtest`: `FakeBackend` (an in-process `jev.Backend`) alongside the HTTP fake.

### 4.3 Adapters — `internal/jev/providers/…`

| Package | WS | Wire | Probability | Notes |
|---|---|---|---|---|
| `openaicompat` | WS1 | `POST {base}/chat/completions`, `logprobs:true`, `top_logprobs:20`, `max_tokens:1`, `temperature:0` | `logprob`; `sampled` when probe finds none | Labels: `A…` for choice, digits for score, `Yes`/`No` for noul. State first, question last (prefix caching). Read `choices[0].logprobs.content[0].top_logprobs`. `Probe` sends one tiny noul and records whether logprobs came back and how many alternatives; a 400 on `logprobs` retries without and records `sampled`. Coverage < 0.5 → `ErrInvalidAnswer`. `GET {base}/models` for `Models`. |
| `anthropic` | WS2 | `POST /v1/messages` with a JSON-schema enum output (forced tool with `strict` as fallback) | `sampled`, k = 5 | Header `x-api-key`, `anthropic-version`. Default model: the latest Haiku. |
| `hfzeroshot` | WS2 | `{"inputs", "parameters":{"candidate_labels","multi_label"}}` → `[{label,score}]` (also accept `{labels[],scores[]}`) | `native` (softmax) | `choice` and `noul` only; `score` → `ErrUnsupported`. |

Each adapter registers itself with `jevconf.RegisterKind` from an `init()` in a single
`internal/jev/providers/all` import, added to the binaries.

## 5. Integration points (mono-agent)

| Where | WS | Change |
|---|---|---|
| CLI `jev provider list\|add\|remove\|use\|test\|probe`; `jev status`/`usage` show provider, host, probability kind; `jev enable` names the host (D10); `--provider` on `jev ask`, `jev enable` | WS3 | New file `internal/cli/jevcli/provider.go`. `profilegate` classes for the new verbs. Doctor: `jev.api` checks the active provider. `summary` adds `provider`. |
| Workflow nodes | WS4 | `provider` config field (optional, default = D9 resolution) on `ai.choose`, `browser.jev`, `applications.evaluate` (`runtime: "jev[:model]"` keeps working), HIL `auto_decide`. **New node `ai.decide`**: one typed question (`kind: noul\|score\|choice`), per item; handles `yes`/`no`/`low_confidence` for noul, `level_<i>`/`low_confidence` for score, option ids for choice. `ai.choose` `maxOptions` stays 255 (adapters chunk). Follow the 10-step node checklist (registry, schemagen manifest, `orggrant/outbound.go` read-only list, `publishes_pin_test`, schema coverage, palette, ref catalog, frontend `nodeConfigFields.js`, exfil test). |
| Org decider | WS5 | `orgdecide.Decider` gains `Provider string`; `defaultDecider` uses `NewClientFor`. `org autonomy set --decider jev --decider-provider <name>`; MCP org-autonomy tool gains `decider_provider`. Resolver label becomes `jev:<provider>/<model>` for non-default providers — and `cli/org/autonomy_decisions.go` regex + `DecisionsFeed.jsx` are updated together with a test. Cost uses the provider's rate. `dynorg.JevChooser` and `openaiapi` auto: no code change beyond labels (they resolve through D9). |
| GUI | WS6 | `wails-app/app_jev.go`: `JevProviders`, `JevAddProvider`, `JevRemoveProvider`, `JevUseProvider`, `JevProbeProvider` (all shell out to the CLI, doctrine D11 of the Jev plan). Settings → Jev: provider list with kind, host, model, probability badge; add-provider form with presets (OpenAI, Ollama, OpenRouter, llama.cpp, vLLM, Anthropic, HF zero-shot); per-surface provider override. Autonomy bar: provider select beside the Jev decider. en.json keys. |
| Docs | WS7 | AGENTS.md "TypeSafe Jev (decisions only)" → "Decision models (Jev)": D3, D5, D7; README; `docs/monoagent-public-api.md` if it names Jev; CHANGELOG. |

## 6. Workstreams and order

```
WS0 foundation ──┬── WS1 openaicompat
(serial, first)  ├── WS2 anthropic + hfzeroshot
                 ├── WS3 CLI + doctor + summary
                 ├── WS4 nodes + ai.decide
                 ├── WS5 org decider
                 ├── WS6 GUI            (needs WS3's CLI JSON — contract in §6.1)
                 └── WS7 docs
integration: merge WS1…WS7 into feat/decision-providers, full build + test + review
```

File ownership is disjoint except `internal/jev/providers/all` (WS1 creates; WS2 adds lines) and
`AGENTS.md` (WS7 only). Each workstream: TDD, files < 500 lines, `go build ./... && go vet ./... &&
gofmt -l .` clean, its packages' tests green, conventional commits, no push.

### 6.1 CLI JSON contract (WS3 produces, WS6 consumes)

```
jev provider list --json  → {"default":"typesafe","providers":[{"name","kind","base_url","model",
                             "key_set":bool,"builtin":bool,
                             "caps":{"kinds":[],"probability":"","max_choice_options":0}}]}
jev provider add <name> --kind K --base-url U --model M [--key-stdin] [--no-probe]  → the provider object
jev provider use <name> [--surface S]  → {"default"|"surface","provider"}
jev provider probe <name>  → {"ok":bool,"caps":{…},"latency_ms":0,"error":""}
jev provider remove <name> → {"removed":true}
```

## 7. monomind issue (filed from this plan)

**Title**: `feat(decision): pluggable decision-model providers and a "decision" org role kind`

1. **Providers.** Replace the closed `'custom' | 'typesafe'` union (`src/decision/jev.ts:48`) and
   `resolveProviders` (`.claude/helpers/jev-picker.cjs:118`) with a provider registry in the CJS
   helper (the hook loads only CJS): kinds `systemone` (today), `openai` (logprobs, one token),
   `anthropic` (enum, sampled), `hf-zeroshot`. Config through `monomind providers configure`
   (named, file-backed) in addition to env. Keep `pick`/`acceptAgent`/`acceptSkills` provider-neutral;
   compute confidence in one place. Add per-provider latency/token/cost to `route-outcomes.jsonl`.
   Collateral: doctor checks, `__tests__/setup/no-jev-env.setup.ts`, `doc/privacy.md` +
   `privacy-claims.test.ts`, catalog egress target `jev`.
2. **`kind: "decision"` role.** Extend `RoleSchema.kind` (`src/orgrt/types-role.ts:160`) with
   `decision` and a `decision` block `{provider, model, question: choice|noul|score, criteria,
   threshold, fallback_role}`; new `src/orgrt/decision-roles.ts` modelled on `endpoint-roles.ts`
   (sessionless, answers mail synchronously, replies on the endpoint reply path); generalise
   `isEndpointRole` call sites to `isSessionlessRole` (incl. the literal check at
   `role-slot.ts:97-99`). Make it consultable: decider kind `role:<id>` in `DECIDER_KINDS`,
   `createGate` with `resolvedBy: "decision:<role>"`, `resolverLabel` regex. Capability string
   `org-decision-roles`.
3. **Cross-repo contract.** mono-agent decides approvals; the decider kind and the `autonomy` block
   shape must agree on both sides. mono-agent ships `decider.provider` first.
4. **Found on the way.** `RUNTIME_KINDS` in `src/orgrt/role-slot.ts:105` is missing `kilo`.

## 8. Risks

- **Uncalibrated numbers.** Chat-model token probabilities are overconfident and position-biased; a
  0.9 threshold tuned on Jev is not the same bar on another model. Mitigation: D7 labelling, the
  status page says so, thresholds stay per-surface and user-set.
- **Cost shape.** Jev answers N questions in one request; logprob adapters make N. `browser.jev`
  (1 + one question per operation, per step) is the heavy case — documented, and usage shows it.
- **Prompt injection.** Unchanged in kind (state hygiene `untrusted_*` already applies); one-token
  output bounds the damage to a wrong pick among enumerated options.
- **Key exfiltration.** The main new surface; D5 plus extended pins and exfil suites.

## 9. Done means

- With no provider configured: full test suite as before, no behaviour change.
- `jev provider add local --kind openai --base-url http://localhost:11434/v1 --model <m>` then
  `jev provider use local` makes every enabled surface, `ai.choose`, `ai.decide`, `browser.jev` and
  the org decider answer from that model, with usage rows naming the provider.
- GUI can add, probe, switch and remove providers.
- monomind issue filed.

## 10. As built

The decisions above are kept as written. Two review rounds (a correctness and a security review of
the integrated branch, each followed by fix lanes) changed the following. AGENTS.md "Decision
models (Jev)" describes the result.

**Probabilities (D6, D7, O2).**
- Rule: a provider may understate its confidence, never overstate it. `jev.Conservative` keeps the
  answer at the raw mass the model put on it; mass on tokens that are no label is not renormalised
  away, at every level of a hierarchical choice. Coverage below 0.5 is still refused.
- A reply with fewer than two log-probability alternatives is not evidence: the adapter samples.
  `learnK` and `Probe` ignore such replies. A 400 on log-probabilities is retried once with
  `top_logprobs: 5` before sampling.
- Sampled answers divide by the samples asked, not the valid ones, and are add-one smoothed
  (`jev.SampledProbs`: (count + 1) / (asked + options)). Five agreeing answers read 6/7 ≈ 0.857 on
  two options and 0.75 on three. O2's "a unanimous 5-of-5 is p = 1.0" no longer holds, and
  Anthropic's early stop after three agreeing samples is gone.
- A response is `sampled` when any question in it was sampled.
- New kind `softmax` (`jev.ProbSoftmax`): `hf-zeroshot` reports it instead of `native`; Go code
  treats it like `logprob`.
- Gates use `jev.GateValue` everywhere (`jevpick`, `jev retry`).
- `people_links` records a permanent "not the same" only from `native` or `logprob`.

**Keys and hosts (D5).**
- Keyless only on this computer: a provider with no `secret_name` must have a loopback `base_url`,
  checked when saved and when its client is built.
- `jev provider add --key-stdin` records the key's origin in the encrypted vault entry. At use
  time the key is refused when another record names the same entry at another origin, or when the
  entry's origin is not its record's. This is defence in depth against a process that writes the
  database file, not a guarantee.
- Adapters are pinned to `jev.KeyedHTTPClient` by an AST test.

**Consent (D10).**
- A default switch to a host that is not this computer always asks, even when nothing is listed.
  It lists enabled surfaces in every profile, org deciders of kind jev without their own provider,
  and fixed lines for workflow nodes and dynamic-org chats without a provider.
- `jev provider add` over an existing name asks when the host changes, or when a provider in use
  changes kind, base URL or key entry. `--yes` answers. The GUI shows its own confirm and passes
  `--yes`.
- `jev provider use --surface S` and `jev enable S --provider P` refuse a provider whose probed
  kinds lack one S asks (`jevconf.SurfaceKinds`).
- `ai.decide` checks `max_choice_options` at configuration time. `MaxChoiceOptions` is a hard cap;
  `OptionsPerRequest` drives the hierarchical split.

**Calls (D3, D8, D9).**
- A node's `model` goes to a non-TypeSafe provider only when the node also names that provider, and
  never a `jev-*` name; otherwise the provider record's model.
- `ai.choose`, `ai.decide` and HIL `auto_decide` refuse a provider on another host in a run an AI
  started or wrote (`decisionclient.ErrRemoteFromAIRun`). `internal/orggrant` keeps classifying them
  read-only, since the classification cannot see which provider a node resolves to; the check runs
  at run time instead.
- Adapter errors carry the HTTP status and a short code only (`jev.HTTPError`). Stored rationales
  and HIL skip reasons of a non-TypeSafe provider carry only `jevconf.ErrorClass`.
- Every adapter honours the client's retry budget (`jev.WithRetries`). Samples run concurrently.
  The org decider timeout triples, and HIL times each item on its own, for a provider that samples
  or takes one question per request.
- `api_auto`: a pick with no probability lets the rule decide and does not trip the breaker. The
  surface's availability follows its own provider.
- Labels come from `jev.ResponseLabel`, with the model the adapter actually used; with no model
  known, the provider's name alone.
- OpenAI reasoning models get `max_completion_tokens` (16, then 256) and no `temperature`.

**Status and GUI.**
- `jev status` adds `provider_pinned` per surface and `provider_key_source` and `provider_error`
  for a non-TypeSafe default.
- The GUI mirrors the consents, the keyless rule, the capability pre-filter, pinned surfaces, the
  sampled-threshold warnings and the HIL skip reasons.

**Open (not fixed, owner's call).**
- A sampled provider cannot reach a 0.9 threshold on 5 samples. The default thresholds of HIL,
  people links and asks are 0.9, and the org decider's 0.8 at level mid is out of reach on three
  verdicts, so such a provider only suggests there. Raise the sample count or lower those
  thresholds.
- There is no CLI to set the `decider` or `dynorg` surface's provider for a profile.
- A vault entry named with `--secret-name` carries no bound origin.
- The monomind side is tracked as monomind issue #835.
