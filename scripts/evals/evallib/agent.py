"""A minimal built-in agent loop over OpenRouter chat completions (OpenAI tool
calling). Deterministic harness, not a product: the model gets exactly one way
to act, chosen by the task mode:

  cli  a `monoagentcli` tool that runs the given binary (argv list, no shell)
       inside the sandbox, plus `write_file` for drafting workflow JSON in the
       sandbox work dir
  mcp  the tools of `monoagentcli mcp --allow-mutations` that author and run
       workflows (workflow_*, node_*, docs, hil_*), proxied as-is

Both get a `done` tool. The loop stops at done, at a reply with no tool call,
at the step budget, or at the time budget.
"""

import json
import os
import threading
import time
import urllib.error
import urllib.request

OPENROUTER_URL = "https://openrouter.ai/api/v1/chat/completions"
MAX_TOOL_OUTPUT = 6000
MCP_TOOL_PREFIXES = ("workflow_", "node_", "docs", "hil_")

SYSTEM = """You are an automation engineer working with MonoAgent, a workflow
automation engine (an n8n alternative). Build the workflow the user asks for.
Rules:
- Save the workflow in MonoAgent with exactly the name the user gives. A JSON
  file you only wrote or validated is not saved; check that it shows up in
  the saved workflow list before you finish.
- Do not activate it and do not leave a daemon running; the grader activates
  and runs it on its own.
- Everything runs offline against local fixture URLs; do not use other hosts.
- Check your work (validate it, look at node schemas) before you finish.
- When you are finished, call the done tool with a one-line summary."""

CLI_HINT = """You act through the `monoagentcli` tool, which runs the
monoagentcli binary with the argument list you give (no shell, no pipes).
`monoagentcli ref` is the built-in documentation (ref workflow, ref nodes,
ref node <type>, ref expressions). Use write_file to create a JSON file in the
working directory and pass its file name to commands such as
`workflow import --file <name>` or `workflow validate --file <name>`."""

MCP_HINT = """You act through MonoAgent's MCP tools. The docs tool is the
built-in documentation (topics: workflow, nodes, expressions)."""


def _done_tool():
    return {"type": "function", "function": {
        "name": "done", "description": "Finish the task.",
        "parameters": {"type": "object", "properties": {"summary": {"type": "string"}},
                       "required": ["summary"]}}}


def _cli_tools():
    return [
        {"type": "function", "function": {
            "name": "monoagentcli",
            "description": "Run monoagentcli with these arguments, e.g. [\"ref\",\"workflow\"]. Returns exit code, stdout and stderr.",
            "parameters": {"type": "object", "properties": {
                "args": {"type": "array", "items": {"type": "string"}},
                "stdin": {"type": "string", "description": "Optional text piped to stdin"}},
                "required": ["args"]}}},
        {"type": "function", "function": {
            "name": "write_file",
            "description": "Write a text file into the working directory (a plain file name, no directories).",
            "parameters": {"type": "object", "properties": {
                "name": {"type": "string"}, "content": {"type": "string"}},
                "required": ["name", "content"]}}},
        _done_tool(),
    ]


def _clip(text):
    if len(text) <= MAX_TOOL_OUTPUT:
        return text
    half = MAX_TOOL_OUTPUT // 2
    return text[:half] + "\n...[%d chars cut]...\n" % (len(text) - 2 * half) + text[-half:]


def _fetch(req, deadline):
    """The response body of req. urlopen's timeout bounds each socket read,
    not the request: a slow model behind keep-alive bytes never trips it. The
    request runs in a worker thread and is abandoned at the deadline (and
    after at most 180 s)."""
    out = {}

    def work():
        try:
            with urllib.request.urlopen(req, timeout=180) as r:
                out["body"] = r.read()
        except BaseException as e:  # handed to the caller
            out["err"] = e

    t = threading.Thread(target=work, daemon=True)
    t.start()
    wait = 180.0 if deadline is None else max(1.0, min(180.0, deadline - time.time()))
    t.join(wait)
    if t.is_alive():
        raise TimeoutError("model request took longer than %.0f s" % wait)
    if "err" in out:
        raise out["err"]
    return out["body"]


class Agent:
    def __init__(self, model, api_key, max_steps=30, max_seconds=600):
        self.model, self.api_key = model, api_key
        self.max_steps, self.max_seconds = max_steps, max_seconds

    def _chat(self, messages, tools, deadline=None):
        body = json.dumps({"model": self.model, "messages": messages, "tools": tools,
                           "temperature": 0}).encode()
        delay = 5
        for attempt in range(6):
            req = urllib.request.Request(OPENROUTER_URL, data=body, headers={
                "Authorization": "Bearer " + self.api_key, "Content-Type": "application/json",
                "X-Title": "monoagent-evals"})
            try:
                data = json.loads(_fetch(req, deadline))
                if data.get("choices"):
                    return data
                err = data.get("error") or data
            except urllib.error.HTTPError as e:
                err = "HTTP %d %s" % (e.code, e.read()[:300])
                if e.code not in (408, 429, 500, 502, 503, 504):
                    raise RuntimeError(err)
            except (urllib.error.URLError, TimeoutError, ValueError) as e:
                err = str(e)
            if attempt == 5:
                raise RuntimeError("openrouter: %s" % err)
            if deadline is not None and time.time() + delay >= deadline:
                raise RuntimeError("openrouter: %s (no time left to retry)" % err)
            time.sleep(delay)
            delay = min(delay * 2, 60)

    def run(self, sb, mode, prompt):
        """Returns {steps, seconds, finished, error, transcript}."""
        mcp = None
        if mode == "mcp":
            mcp = sb.mcp()
            tools = [{"type": "function", "function": {
                "name": t["name"], "description": t.get("description", "")[:1000],
                "parameters": t.get("inputSchema") or {"type": "object", "properties": {}}}}
                for t in mcp.tools() if t["name"].startswith(MCP_TOOL_PREFIXES)]
            tools.append(_done_tool())
            hint = MCP_HINT
        else:
            tools, hint = _cli_tools(), CLI_HINT
        messages = [{"role": "system", "content": SYSTEM + "\n\n" + hint},
                    {"role": "user", "content": prompt}]
        start, steps, finished, error = time.time(), 0, False, ""
        try:
            while steps < self.max_steps and time.time() - start < self.max_seconds and not finished:
                steps += 1
                msg = self._chat(messages, tools, start + self.max_seconds)["choices"][0]["message"]
                calls = msg.get("tool_calls") or []
                messages.append({"role": "assistant", "content": msg.get("content") or "",
                                 **({"tool_calls": calls} if calls else {})})
                if not calls:
                    finished = True
                    break
                for call in calls:
                    name = call["function"]["name"]
                    out = self._dispatch(sb, mcp, name, call["function"].get("arguments") or "{}")
                    if name == "done":
                        finished = True
                    messages.append({"role": "tool", "tool_call_id": call.get("id", ""), "content": out})
        except Exception as e:
            error = str(e)[:500]
        finally:
            if mcp:
                mcp.close()
        return {"steps": steps, "seconds": round(time.time() - start, 1), "finished": finished,
                "error": error, "transcript": messages}

    def _dispatch(self, sb, mcp, name, raw_args):
        try:
            args = json.loads(raw_args) if raw_args.strip() else {}
        except ValueError as e:
            return "error: tool arguments are not valid JSON: %s" % e
        if name == "done":
            return "ok"
        if mcp is not None:
            if not name.startswith(MCP_TOOL_PREFIXES):
                return "error: unknown tool %s" % name
            is_err, text = mcp.call(name, args)
            return _clip(("error: " if is_err else "") + text)
        if name == "write_file":
            fname = os.path.basename(str(args.get("name", "")))
            if not fname or fname in (".", ".."):
                return "error: give a plain file name"
            with open(os.path.join(sb.work, fname), "w", encoding="utf-8") as f:
                f.write(str(args.get("content", "")))
            return "wrote %s (%d bytes)" % (fname, len(str(args.get("content", ""))))
        if name == "monoagentcli":
            argv = [str(a) for a in (args.get("args") or [])]
            if argv and argv[0] in ("monoagentcli", "monoagent"):
                argv = argv[1:]
            r = sb.cli(argv, stdin=args.get("stdin"), timeout=120)
            return _clip("exit %d\n--- stdout\n%s\n--- stderr\n%s" % (r.code, r.stdout, _strip_logs(r.stderr)))
        return "error: unknown tool %s" % name


def _strip_logs(stderr):
    """Drop the migration log lines a fresh HOME prints on its first command."""
    keep = [ln for ln in stderr.splitlines()
            if "applied migration" not in ln and "storage: reconcile" not in ln]
    return "\n".join(keep)
