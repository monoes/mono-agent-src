"""Independent scorer: finds the workflow a task asked for and runs the task's
checks against it by calling monoagentcli (and its MCP server) itself. It never
trusts what the agent said it did.

Check types (a task's "checks" list):
  validate_ok                 workflow validate <id> exits 0
  dry_run_ok                  workflow run <id> --dry-run exits 0
  run_status    equals        workflow run --json <id> ends with this status
  node_types    include       the workflow has a node of every listed type
  node_config   node_type, key, equals | in | contains
  inactive                    the workflow is saved inactive
  output        path, equals | contains | count [, node_type]
                              items of the run's node of node_type (default: the
                              last node that succeeded with items); path is
                              [*] / [n] / .key segments over the item JSON list
  file          path, exists | contains | not_contains | lines
  mcp_validate_ok             MCP workflow_validate {id} says valid
  mcp_run_status equals       MCP workflow_run {id} ends with this status
"""

import json
import re

RUN_CHECKS = {"run_status", "output", "file"}


def find_workflow(sb, name):
    res = sb.cli(sb.p + ["--json", "workflow", "list"])
    wfs = res.json()
    if isinstance(wfs, dict):
        wfs = wfs.get("workflows") or []
    matches = [w for w in (wfs or []) if isinstance(w, dict) and w.get("name") == name]
    if not matches:
        return None
    matches.sort(key=lambda w: w.get("updated_at") or "")
    return matches[-1]


def get_path(value, path):
    """Walk [*] / [n] / .key segments. [*] maps the rest of the path over a list."""
    segs = re.findall(r"\[\*\]|\[\d+\]|[^.\[\]]+", path or "")
    return _walk(value, segs)


def _walk(value, segs):
    if not segs:
        return value
    seg, rest = segs[0], segs[1:]
    if seg == "[*]":
        if not isinstance(value, list):
            return None
        return [_walk(v, rest) for v in value]
    if seg.startswith("["):
        i = int(seg[1:-1])
        if not isinstance(value, list) or i >= len(value):
            return None
        return _walk(value[i], rest)
    if not isinstance(value, dict):
        return None
    return _walk(value.get(seg), rest)


def compare(actual, chk):
    """Apply equals / in / contains / count. Returns (ok, detail)."""
    if "equals" in chk:
        ok = _norm(actual) == _norm(chk["equals"])
        return ok, "got %s, want %s" % (_short(actual), _short(chk["equals"]))
    if "in" in chk:
        ok = _norm(actual) in [_norm(v) for v in chk["in"]]
        return ok, "got %s, want one of %s" % (_short(actual), _short(chk["in"]))
    if "contains" in chk:
        want = chk["contains"] if isinstance(chk["contains"], list) else [chk["contains"]]
        if isinstance(actual, list):
            have = [_norm(a) for a in actual]
            missing = [w for w in want if _norm(w) not in have]
        else:
            text = actual if isinstance(actual, str) else json.dumps(actual)
            missing = [w for w in want if str(w) not in text]
        return not missing, "missing %s in %s" % (_short(missing), _short(actual))
    if "count" in chk:
        n = len(actual) if isinstance(actual, (list, str, dict)) else None
        return n == chk["count"], "got %s items, want %s" % (n, chk["count"])
    return actual is not None, "got %s" % _short(actual)


def _norm(v):
    """Numbers compare by value whatever their JSON spelling (15 == 15.0 == "15")."""
    if isinstance(v, bool) or v is None:
        return v
    if isinstance(v, (int, float)):
        return float(v)
    if isinstance(v, str):
        try:
            return float(v) if re.fullmatch(r"-?\d+(\.\d+)?", v.strip()) else v
        except ValueError:
            return v
    if isinstance(v, list):
        return [_norm(x) for x in v]
    if isinstance(v, dict):
        return {k: _norm(x) for k, x in v.items()}
    return v


def _short(v, n=160):
    s = json.dumps(v, default=str)
    return s if len(s) <= n else s[:n] + "..."


def node_items(run, node_type=None):
    nodes = (run or {}).get("nodes") or []
    if node_type:
        cands = [n for n in nodes if n.get("type") == node_type and n.get("status") == "SUCCESS"]
        cands = [n for n in cands if n.get("output_items")] or cands
    else:
        cands = [n for n in nodes if n.get("status") == "SUCCESS" and n.get("output_items")]
    if not cands:
        return None
    return [it.get("json") for it in (cands[-1].get("output_items") or [])]


# MCP tools that take no profile argument: globalToolsExpected in
# internal/mcp/profile_required_test.go, kept equal by scorer_global_tools_test.go there.
MCP_GLOBAL_TOOLS = frozenset([
    "api_config_apply", "api_config_get", "api_config_set", "docs", "node_list", "node_schema",
    "template_list", "workflow_template_list", "workflow_template_show",
])


class Scorer:
    def __init__(self, sb, task):
        self.sb, self.task = sb, task
        self.wf = None
        self.full = None
        self.run = None
        self.run_error = ""

    def score(self):
        name = self.task.get("workflow_name") or "eval-" + self.task["id"]
        self.wf = find_workflow(self.sb, name)
        results = []
        if not self.wf:
            for chk in self.task["checks"]:
                results.append(self._r(chk, False, "no workflow named %r was saved" % name))
            return results
        got = self.sb.cli(self.sb.p + ["--json", "workflow", "get", self.wf["id"]])
        self.full = got.json() or {}
        if any(c["type"] in RUN_CHECKS for c in self.task["checks"]):
            self._run()
        for chk in self.task["checks"]:
            try:
                ok, detail = getattr(self, "c_" + chk["type"])(chk)
            except Exception as e:  # a broken check must show, not crash the suite
                ok, detail = False, "check error: %s" % e
            results.append(self._r(chk, ok, detail))
        return results

    @staticmethod
    def _r(chk, ok, detail):
        label = chk.get("label") or chk["type"]
        return {"check": label, "type": chk["type"], "pass": bool(ok), "detail": "" if ok else detail}

    def _input_args(self):
        inp = self.task.get("input")
        return ["--input", self.sb.subst(json.dumps(inp))] if inp is not None else []

    def _run(self):
        wid = self.wf["id"]
        if not self.full.get("is_active"):
            act = self.sb.cli(self.sb.p + ["workflow", "activate", wid])
            if act.code != 0:
                self.run_error = "activate exit %d: %s" % (act.code, act.stderr.strip()[-300:])
                return
        res = self.sb.cli(self.sb.p + ["--json", "workflow", "run", wid, "--timeout", "90s"] + self._input_args(),
                          timeout=150)
        self.run = res.json()
        if self.run is None:
            self.run_error = "run exit %d: %s" % (res.code, res.stderr.strip()[-300:])

    def _nodes(self):
        return self.full.get("nodes") or []

    @staticmethod
    def _ntype(n):
        return n.get("node_type") or n.get("type")

    def c_validate_ok(self, chk):
        r = self.sb.cli(self.sb.p + ["workflow", "validate", self.wf["id"]])
        return r.code == 0, "exit %d: %s" % (r.code, (r.stdout + r.stderr).strip()[-300:])

    def c_dry_run_ok(self, chk):
        r = self.sb.cli(self.sb.p + ["workflow", "run", self.wf["id"], "--dry-run"])
        return r.code == 0, "exit %d: %s" % (r.code, (r.stdout + r.stderr).strip()[-300:])

    def c_run_status(self, chk):
        if self.run is None:
            return False, self.run_error or "no run"
        st = self.run.get("status")
        return st == chk["equals"], "status %s, want %s; %s" % (st, chk["equals"], self._failed_nodes())

    def _failed_nodes(self):
        bad = [n.get("node_id") + ":" + str(n.get("status")) for n in (self.run or {}).get("nodes") or []
               if n.get("status") not in ("SUCCESS",)]
        return "non-success nodes: " + ", ".join(bad) if bad else ""

    def c_node_types(self, chk):
        have = {self._ntype(n) for n in self._nodes()}
        missing = [t for t in chk["include"] if t not in have]
        return not missing, "missing node types %s (have %s)" % (missing, sorted(t for t in have if t))

    def c_node_config(self, chk):
        nodes = [n for n in self._nodes() if self._ntype(n) == chk["node_type"]]
        if not nodes:
            return False, "no %s node" % chk["node_type"]
        last = (False, "")
        for n in nodes:
            cfg = n.get("config") or {}
            if isinstance(cfg, str):
                cfg = json.loads(cfg or "{}")
            val = get_path(cfg, chk["key"])
            want = dict(chk)
            for k in ("equals", "contains"):
                if isinstance(want.get(k), str):
                    want[k] = self.sb.subst(want[k])
            last = compare(val, want)
            if last[0]:
                return last
        return last

    def c_inactive(self, chk):
        return not self.full.get("is_active"), "workflow is active"

    def c_output(self, chk):
        if self.run is None:
            return False, self.run_error or "no run"
        items = node_items(self.run, chk.get("node_type"))
        if items is None:
            return False, "no successful %s node with output; %s" % (chk.get("node_type") or "", self._failed_nodes())
        return compare(get_path(items, chk.get("path", "")), chk)

    def c_file(self, chk):
        path = self.sb.subst(chk["path"])
        try:
            with open(path, encoding="utf-8", errors="replace") as f:
                text = f.read()
        except OSError as e:
            return False, "cannot read %s: %s" % (path, e.strerror)
        missing = [s for s in chk.get("contains", []) if s not in text]
        present = [s for s in chk.get("not_contains", []) if s in text]
        lines = [ln for ln in text.splitlines() if ln.strip()]
        if "lines" in chk and len(lines) != chk["lines"]:
            return False, "%d non-empty lines, want %d" % (len(lines), chk["lines"])
        return not missing and not present, "missing %s, unexpected %s in %s" % (missing, present, _short(text))

    def _mcp_call(self, name, args):
        client = self.sb.mcp()
        try:
            # Every MCP tool but the machine-wide ones (internal/mcp
            # globalToolsExpected) names the profile it acts on.
            if name not in MCP_GLOBAL_TOOLS:
                args = dict(args, profile=self.sb.profile)
            return client.call(name, args)
        finally:
            client.close()

    def c_mcp_validate_ok(self, chk):
        is_err, text = self._mcp_call("workflow_validate", {"id": self.wf["id"]})
        try:
            valid = json.loads(text).get("valid") is True
        except (ValueError, AttributeError):
            valid = False
        return valid and not is_err, _short(text)

    def c_mcp_run_status(self, chk):
        args = {"id": self.wf["id"], "timeout_seconds": 90}
        if self.task.get("input") is not None:
            args["input"] = json.loads(self.sb.subst(json.dumps(self.task["input"])))
        if not self.full.get("is_active"):
            self.sb.cli(self.sb.p + ["workflow", "activate", self.wf["id"]])
        is_err, text = self._mcp_call("workflow_run", args)
        try:
            st = json.loads(text).get("status")
        except (ValueError, AttributeError):
            st = None
        return st == chk["equals"] and not is_err, "status %s: %s" % (st, _short(text))
