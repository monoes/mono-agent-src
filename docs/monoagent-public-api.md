# monoagent public API — `telorivaaiapi.monoes.me`

An OpenAI-compatible API served by `monoagentcli` on the node `91.99.106.218`.
Every request is one agent turn on the node's own subscriptions: Claude goes
through monomind, Antigravity (`agy`) through its CLI. Nothing here is billed
per token; it uses the signed-in accounts' quotas.

- **Base URL:** `https://telorivaaiapi.monoes.me/v1`
- **Auth:** `Authorization: Bearer <API key>` (keys look like `sk-ma-...`)
- **Format:** the OpenAI REST format; the OpenAI SDKs work with a changed base URL

```bash
export KEY=sk-ma-...        # your key; never commit it
export B=https://telorivaaiapi.monoes.me/v1
```

> **Security.** The server runs with `confinement any`: a key can use agy's
> native tools, which means **shell access as the `monoagent` user** on the
> node, which also holds the Claude, agy and monoes.me logins. Treat every key
> as full access, and revoke a leaked key at once. Keys cannot be limited to
> certain models.

## Endpoints

| Method and path | Purpose |
|---|---|
| `GET /v1/models` | List served models (with confinement and capabilities) |
| `GET /v1/models/{id}` | One model |
| `POST /v1/chat/completions` | Chat, JSON or `"stream": true` |
| `POST /v1/images/generations` | Generate images (agy models); add `Prefer: respond-async` to get a job id |
| `GET /v1/images/jobs/{id}` | Status and result of an image job |
| `GET /health` | Liveness, no key needed |

Without a valid key every `/v1` route answers `401 invalid_api_key`.

## Models

The list comes from the runtimes themselves and can change. Always check
`GET /v1/models` for what is served now. Ids are `<runtime>/<model>`. A bare
runtime name (`claude`) is its default model, and `agy/` is accepted for
`antigravity/`.

### Claude (chat-only, no agent overhead, supports tool calling)

| Model id | Model |
|---|---|
| `claude` / `claude/default` / `claude/sonnet` | Sonnet 5.5 |
| `claude/opus` | Opus 5.5 |
| `claude/haiku` | **Haiku 5.5** |
| `claude/fable` | Fable 5.1 |
| `claude/claude-sonnet-5` | Sonnet 5 |
| `claude/claude-opus-5` | Opus 5 |
| `claude/claude-fable-5` | Fable 5 |
| `claude/claude-opus-4-8` | Opus 4.8 |
| `claude/claude-opus-4-7` | Opus 4.7 |
| `claude/claude-opus-4-6` | Opus 4.6 |
| `claude/claude-sonnet-4-6` | Sonnet 4.6 |
| `claude/claude-haiku-4-5-20251001` | Haiku 4.5 |

Capabilities: `text`, `tools`. A model that is not in the list is a `404
model_not_found`. For example `claude/claude-haiku-5-5` and
`claude/claude-opus-5-5` do not exist; use the short aliases `claude/haiku`
and `claude/opus` for the 5.5 models.

### Antigravity / agy (unconfined, image generation, agent overhead)

| Model id | Model |
|---|---|
| `antigravity/default` | agy default model |
| `antigravity/gemini-4-argon-low` / `-medium` / `-high` | Gemini 4 Argon |
| `antigravity/gemini-3.8-flash-low` / `-medium` / `-high` | Gemini 3.8 Flash |
| `antigravity/claude-opus-5-5-low` / `-medium` / `-high` | Claude Opus 5.5 |
| `antigravity/claude-sonnet-5-5-low` / `-medium` / `-high` | Claude Sonnet 5.5 |
| `antigravity/gpt-oss-120b-medium` | GPT-OSS 120B |

Capabilities: `text`, `image` (image generation). The low/medium/high suffix
is the reasoning effort. agy does **not** serve tool calling through this API.

**Overhead:** every agy turn carries about **11.9k prompt tokens** of agy's own
built-in system prompt and tools, even for "say hi". It cannot be disabled
(removing the node's skills changed nothing). Claude models show about 2 prompt
tokens for the same call, so use Claude for plain chat.

### Not available

`codex`, `copilot` and the other runtimes are not installed on the node.
`model: "auto"` (Jev picks the model) is off: it needs `jev enable api_auto`
on the `api` profile and a decision provider for that surface that can be
used (for TypeSafe, the default, a Jev key).

## Chat

```bash
curl $B/chat/completions \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"model":"claude/haiku","messages":[{"role":"user","content":"Say hello in five words."}]}'
```

```python
from openai import OpenAI
c = OpenAI(base_url="https://telorivaaiapi.monoes.me/v1", api_key=KEY)
r = c.chat.completions.create(model="antigravity/gemini-3.8-flash-low",
                              messages=[{"role": "user", "content": "Say hello in five words."}])
print(r.choices[0].message.content)
```

```js
import OpenAI from "openai";
const c = new OpenAI({ baseURL: "https://telorivaaiapi.monoes.me/v1", apiKey: process.env.KEY });
const r = await c.chat.completions.create({
  model: "claude/opus",
  messages: [{ role: "user", content: "Say hello in five words." }],
});
console.log(r.choices[0].message.content);
```

### Streaming

`"stream": true` returns server-sent events ending with `data: [DONE]`.

```bash
curl -N $B/chat/completions -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"model":"antigravity/gemini-3.8-flash-low","stream":true,"messages":[{"role":"user","content":"Count to 5"}]}'
```

**Use streaming for anything slow.** Cloudflare cuts a request that sends no
bytes for about 100 seconds and answers 524, even though the node's turn
timeout is 10 minutes.

### Sessions (continuing a chat)

There is **no server-side session**. Requests are stateless: keep the message
list in your client and send all of it every time. Tested on Claude and agy.

```python
history = []
def chat(text, model="claude/haiku"):
    history.append({"role": "user", "content": text})
    r = c.chat.completions.create(model=model, messages=history)
    reply = r.choices[0].message.content
    history.append({"role": "assistant", "content": reply})
    return reply

chat("My name is Morteza.")
print(chat("What is my name?"))     # -> Morteza
```

- A system prompt is the first message: `{"role":"system","content":"..."}`.
- Cost grows with the history, and agy adds its fixed overhead on every turn.
- Save the list yourself to resume later, and trim or summarise long ones.
- One server-side exception: a tool-call follow-up can resume the runtime's
  session for up to 10 minutes (same key, model, tools and conversation
  prefix). You still send the full history; it falls back to a replay.

### Tool calling (Claude models only)

The standard OpenAI loop. The server never runs your functions: it returns the
call, you run it, then you send the result back as a `tool` message.

```python
tools = [{"type": "function", "function": {
    "name": "get_weather", "description": "Current weather for a city.",
    "parameters": {"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"]}}}]
messages = [{"role": "user", "content": "Weather in Paris?"}]
for _ in range(8):                       # cap the rounds yourself
    m = c.chat.completions.create(model="claude/sonnet", messages=messages, tools=tools).choices[0].message
    if not m.tool_calls:
        print(m.content); break
    messages.append(m)
    for call in m.tool_calls:
        result = {"temp_c": 21, "conditions": "fog"}     # run the function yourself
        messages.append({"role": "tool", "tool_call_id": call.id, "content": json.dumps(result)})
```

One call per response (`parallel_tool_calls` is treated as false). Tool turns
run only on Claude models; agy answers `400 unsupported_parameter`. Make tools
idempotent, since a model can repeat a call.

Tool calling was tested on Haiku, Opus, Fable and Sonnet (4.6 and 5.x). Sonnet
5.x used to answer in text that its tool was "deferred" behind `ToolSearch`,
which the runner does not allow. The node's daemon now sets
`ENABLE_TOOL_SEARCH=false` (in the systemd drop-in `env.conf`), so Claude
receives the declared functions directly. Keep that variable if you reinstall
the service.

### Accepted and rejected parameters

- Accepted but ignored: `temperature`, `max_tokens`, `stop` and similar (the
  agent runtimes have no sampling controls).
- `response_format: {"type":"json_object"}` works as a best-effort instruction.
- Rejected with `400 unsupported_parameter`: `n` above 1, `logprobs`,
  `response_format` of type `json_schema`, the legacy `functions` and
  `function_call`, the `function` role, and any non-text content part
  (including `image_url`, audio and files).
- Body limit: 2 MiB (64 KiB for image requests).

## Generate images (agy)

```bash
curl -s $B/images/generations \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"model":"antigravity/default","prompt":"A small red circle on a white background.","size":"1024x1024"}' \
  | python3 -c "import json,sys,base64; d=json.load(sys.stdin); open('out.jpg','wb').write(base64.b64decode(d['data'][0]['b64_json']))"
```

```python
import base64
r = c.images.generate(model="antigravity/default", prompt="A small red circle on a white background.",
                      size="1024x1024", response_format="b64_json")
open("out.jpg", "wb").write(base64.b64decode(r.data[0].b64_json))
```

- Response: `{"created": ..., "data": [{"b64_json": "..."}]}`. Only `b64_json` is
  returned (`response_format: "url"` and `stream: true` are 400). The runtime
  picks the format; a test returned a 1024x1024 JPEG, so check the first bytes.
- Use an agy model (`antigravity/...`, capability `image`). Claude models cannot
  make images.
- It takes **40 to 120 seconds** (87 s, 110 s and 117 s in tests) and uses about
  40k input tokens. The main host sits behind Cloudflare, which cuts a request
  after about 100 s with a **524**. **Send image requests to the direct host
  `https://telorivaaiapi-direct.monoes.me/v1/images/generations`** instead: it is
  DNS-only (no Cloudflare proxy) with a Let's Encrypt certificate and an 11 minute
  read timeout. Use the same key, and set the client timeout to 5 minutes.
  The direct host serves only `/v1/` and `/health`, so it works for chat too.
- `n` is 1 to 4 (you may get fewer), `size` is `auto` or `WxH` with each side 64
  to 8192 (a preference only). `quality`, `style`, `output_format`,
  `background` and `user` are ignored.

### Image jobs: no connection held open

An image takes 40 to 130 s, longer than a proxy such as Cloudflare keeps a request
open (about 100 s). Instead of holding the connection, ask for a **job**: send the
same request with the header `Prefer: respond-async`, then poll.

```bash
# 1. submit: answered at once, 202
curl -si $B/images/generations -H "Authorization: Bearer $KEY" -H "Content-Type: application/json"   -H "Prefer: respond-async"   -d '{"model":"antigravity/default","prompt":"A small red circle on a white background."}'
# HTTP/2 202   Location: /v1/images/jobs/imgjob_...   Retry-After: 5
# {"id":"imgjob_...","object":"image.job","status":"in_progress","created":1793620000}

# 2. poll every 5 s until status is not in_progress
curl -s $B/images/jobs/imgjob_... -H "Authorization: Bearer $KEY"
```

| `status` | Body |
|---|---|
| `in_progress` | `{"id","object":"image.job","status":"in_progress","created"}` (`Retry-After: 5`) |
| `completed` | `{"id","object":"image.job","status":"completed","created","data":[{"b64_json":"..."}]}`, the same `data` as the plain answer |
| `failed` | `{"id","object":"image.job","status":"failed","created","http_status":502,"error":{"message","type","param","code"}}`: the error the plain request would have answered, with its status in `http_status` |

Rules:

- The request is checked first, so a bad request (400), a refusal (403/404) or a
  busy server (**429**, with `Retry-After`) is answered at once, not as a job.
  A job holds one of the server's turn slots until the runtime is done.
- A job belongs to the key that made it: another key gets `404 job_not_found`.
- A finished job is kept **30 minutes** and can be read more than once. Jobs are
  kept in memory, so a restart of the daemon loses them (the poll then says
  `404 job_not_found`: submit again). At most 64 jobs are kept.
- Polling a job costs nothing on the runtime. Poll every 5 s; give up after about
  10 minutes (the turn timeout).

```python
import requests, time, base64
H = {"Authorization": f"Bearer {KEY}", "Prefer": "respond-async"}
j = requests.post(f"{B}/images/generations", headers=H,
                  json={"model": "antigravity/default", "prompt": "a red circle"}, timeout=30)
j.raise_for_status()
url = f"{B}/images/jobs/{j.json()['id']}"
while True:
    time.sleep(5)
    r = requests.get(url, headers={"Authorization": f"Bearer {KEY}"}, timeout=30).json()
    if r["status"] != "in_progress":
        break
if r["status"] == "completed":
    open("out.jpg", "wb").write(base64.b64decode(r["data"][0]["b64_json"]))
else:
    print(r["http_status"], r["error"]["code"], r["error"]["message"])
```

### Why an image request fails

| Answer | Cause | What to do |
|---|---|---|
| `429 rate_limit_exceeded` | All turn slots are busy | Retry after `Retry-After` |
| `502 image_generation_failed` | The runtime ended without saving an image. Most often **Google's image-model quota** (`429 RESOURCE_EXHAUSTED` on `gemini-3.1-flash-image`, reset in about 10 to 55 minutes; agy does not retry it) | Retry later, spaced out (e.g. every 5 minutes); it will keep failing until the quota resets |
| `400 image_generation_unsupported` | The runtime replied `NO_IMAGE_TOOL`; agy also does this after a quota error | As above |
| `524` from Cloudflare, or the client sees a dropped connection | The request ran past about 100 s on the proxied host | Use an image job, or the direct host |

The server log line for a job reads `req=... status=... detail="job imgjob_..."`.
The quota error itself is only in agy's own logs on the node
(`~/.gemini/antigravity-cli/log/`, search `RESOURCE_EXHAUSTED`).

## Sending an image to a model

An `image_url` or base64 content part is **rejected** (`400
unsupported_parameter`, "content parts of type image_url are not supported:
text only"). This is the API's rule for every model.

**Workaround, agy only:** put a **public image URL** in the text. agy downloads
and reads it with its own tools (tested: it described a PNG correctly).

```bash
curl -s $B/chat/completions -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"model":"antigravity/gemini-3.8-flash-low","messages":[{"role":"user","content":"Download https://example.com/photo.png , look at the image, and describe it."}]}'
```

- The URL must be public (use a short-lived signed link for private images).
- The call cost about 37k prompt tokens.
- It works because agy is unconfined and runs a download tool for any URL in
  the prompt: do not pass URLs from untrusted sources.

## Response headers

| Header | Meaning |
|---|---|
| `X-Monoagent-Model` | The model that answered |
| `X-Monoagent-Sandbox` | How the turn was sandboxed (non-streaming success only) |
| `X-Request-Id` | Quote it when reporting a problem; the server log has the detail |

## Errors

Body: `{"error":{"message","type","param","code"}}`.

| Status | `code` | Meaning |
|---|---|---|
| 400 | `invalid_json`, `invalid_value`, `missing_required_parameter`, `unsupported_parameter` | Bad body or unsupported parameter (see above) |
| 401 | `invalid_api_key` | Missing, unknown or revoked key |
| 403 | `policy_denied` | Model above the server's confinement, or its sandbox could not be applied |
| 404 | `model_not_found` | Unknown model id (list them with `GET /v1/models`) |
| 413 | `request_too_large` | Body over 2 MiB (64 KiB for images) |
| 429 | `rate_limit_exceeded`, `insufficient_quota` | Server full (4 turns at once, `Retry-After: 2`), or the runtime is rate limited or out of quota |
| 502 | `runtime_error` | The runtime failed (for Claude, check its login) |
| 503 | `runtime_not_available` | monomind or the runtime is missing or signed out |
| 504 | `timeout` | The turn passed 10 minutes |
| 524 | (Cloudflare) | No bytes for about 100 s: use streaming |

## Limits

- 4 turns at once, 10 minute turn timeout. Both are set with
  `monoagentcli api config set` on the node.
- Each request starts a fresh agent turn (seconds to a minute).

## Operating it (on the node)

Run as the `monoagent` user, with the file keyring enabled:
`. ~/.monoagent/env.sh`.

| Item | Where |
|---|---|
| CLI | `/usr/local/bin/monoagentcli` (profile `api`) |
| Daemon | systemd user service `monoagent-daemon`, `/v1` on `127.0.0.1:9444` |
| Tunnel | systemd service `cloudflared-monoagent`, tunnel `monoagent-api`, DNS `telorivaaiapi.monoes.me` |
| Direct host | nginx site `/etc/nginx/sites-available/telorivaaiapi-direct.monoes.me` (443 to `127.0.0.1:9444`, `/v1/` and `/health` only), DNS-only A record, certbot auto-renews |
| Logs | `journalctl --user -u monoagent-daemon`, `journalctl -u cloudflared-monoagent` |
| Keyring passphrase | `~monoagent/.monoagent/keyring-passphrase` |

```bash
monoagentcli --profile api api key create --name <app>   # prints the key once
monoagentcli --profile api api key list
monoagentcli --profile api api key revoke <id>           # effective on the next request
monoagentcli --profile api api status
monoagentcli --profile api api models
monoagentcli --profile api api config set --confinement chat-only --yes   # claude only; then `monoagentcli daemon restart`
```

Logins: Claude uses its own subscription login on the node (`claude auth
status`); re-login with `claude auth login` if turns start failing with
`runner-error`. The agy login was copied from a laptop, and a rotating token
can be invalidated by whichever side refreshes second, so re-login on the node
if agy says it is signed out. The monoes.me sign-in (`monoagentcli account
status`) must stay valid or commands refuse to run.
