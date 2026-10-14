# monoagentcli capability evals: nvidia/nemotron-3-super-120b-a12b:free

Started 20261014-093818, 2574.3s total, binary `/home/monoes/scratch/rw-qa-home-agents/bin/monoagentcli`.

| task | mode | result | checks | steps | seconds |
|---|---|---|---|---|---|
| aggregate-sum | cli | PASS 5/5 | validate_ok:PASS dry_run_ok:PASS run_status:PASS node_types:PASS output:PASS | 16 | 123.2 |
| aggregate-sum | mcp | PASS 5/5 | validate_ok:PASS dry_run_ok:PASS run_status:PASS node_types:PASS output:PASS | 26 | 95.9 |
| code-transform | cli | FAIL 3/6 | validate_ok:PASS dry_run_ok:PASS run_status:FAIL node_types:PASS output:FAIL output:FAIL | 12 | 61.6 |
| code-transform | mcp | FAIL 2/6 | validate_ok:PASS dry_run_ok:PASS run_status:FAIL node_types:FAIL output:FAIL output:FAIL | 30 | 98.1 |
| http-filter-csv | cli | FAIL 3/5 | validate_ok:PASS dry_run_ok:PASS run_status:FAIL node_types:PASS file:FAIL | 30 | 306.7 |
| http-filter-csv | mcp | FAIL 3/5 | validate_ok:PASS dry_run_ok:PASS run_status:FAIL node_types:PASS file:FAIL | 30 | 177.7 |
| if-routing | cli | FAIL 5/6 | validate_ok:PASS dry_run_ok:PASS run_status:PASS node_types:PASS output:PASS output:FAIL | 22 | 234.9 |
| if-routing | mcp | FAIL 2/6 | validate_ok:FAIL dry_run_ok:PASS run_status:FAIL node_types:PASS output:FAIL output:FAIL | 30 | 363.8 |
| rss-read | cli | PASS 7/7 | validate_ok:PASS dry_run_ok:PASS run_status:PASS mcp_validate_ok:PASS mcp_run_status:PASS node_types:PASS output:PASS | 13 | 51.1 |
| rss-read | mcp | PASS 7/7 | validate_ok:PASS dry_run_ok:PASS run_status:PASS mcp_validate_ok:PASS mcp_run_status:PASS node_types:PASS output:PASS | 12 | 55.9 |
| schedule-daily | cli | PASS 5/5 | validate_ok:PASS dry_run_ok:PASS node_types:PASS node_config:PASS inactive:PASS | 11 | 67.6 |
| schedule-daily | mcp | PASS 5/5 | validate_ok:PASS dry_run_ok:PASS node_types:PASS node_config:PASS inactive:PASS | 11 | 25.4 |
| set-template | cli | PASS 7/7 | validate_ok:PASS dry_run_ok:PASS run_status:PASS mcp_validate_ok:PASS mcp_run_status:PASS output:PASS output:PASS | 11 | 63.6 |
| set-template | mcp | PASS 7/7 | validate_ok:PASS dry_run_ok:PASS run_status:PASS mcp_validate_ok:PASS mcp_run_status:PASS output:PASS output:PASS | 17 | 113.8 |
| webhook-set-respond | cli | PASS 8/8 | validate_ok:PASS dry_run_ok:PASS run_status:PASS node_types:PASS node_config:PASS output:PASS output:PASS output:PASS | 14 | 89.9 |
| webhook-set-respond | mcp | PASS 8/8 | validate_ok:PASS dry_run_ok:PASS run_status:PASS node_types:PASS node_config:PASS output:PASS output:PASS output:PASS | 20 | 602.2 |

Tasks fully passed: 10/16. Checks passed: 82/98.

## Failed checks

- code-transform (cli) run_status: status FAILED, want SUCCESS; non-success nodes: n2:FAILED
- code-transform (cli) output: no successful core.code node with output; non-success nodes: n2:FAILED
- code-transform (cli) output: no successful core.code node with output; non-success nodes: n2:FAILED
- code-transform (mcp) run_status: status FAILED, want SUCCESS; non-success nodes: node-8cbfa8d6-12be-4c96-9adb-54b79d8f0536:FAILED
- code-transform (mcp) node_types: missing node types ['core.code'] (have ['code', 'http.request', 'trigger.manual'])
- code-transform (mcp) output: no successful core.code node with output; non-success nodes: node-8cbfa8d6-12be-4c96-9adb-54b79d8f0536:FAILED
- code-transform (mcp) output: no successful core.code node with output; non-success nodes: node-8cbfa8d6-12be-4c96-9adb-54b79d8f0536:FAILED
- http-filter-csv (cli) run_status: status FAILED, want SUCCESS; non-success nodes: n4:FAILED
- http-filter-csv (cli) file: cannot read /home/monoes/scratch/monoagent-evals-20261014-093818/http-filter-csv-cli-785kf_6f/out/expensive.csv: No such file or directory
- http-filter-csv (mcp) run_status: status FAILED, want SUCCESS; non-success nodes: node-cee78017-6497-42c5-b04f-baf9d8d7ef53:FAILED
- http-filter-csv (mcp) file: cannot read /home/monoes/scratch/monoagent-evals-20261014-093818/http-filter-csv-mcp-xfnvaofn/out/expensive.csv: No such file or directory
- if-routing (cli) output: missing ["premium"] in ["budget", "budget", "budget", "budget"]
- if-routing (mcp) validate_ok: exit 3: fed080-6889-4598-8178-b5670648f721": required field "assignments" is missing (set config.assignments (see `monoagentcli node schema core.set`)) node "node-31f7dc27-b5bf-42eb-992d-5e1e17cda456": required field "assignments" is missing (set config.assignments (see `monoagentcli node schema core.set`))
- if-routing (mcp) run_status: status FAILED, want SUCCESS; non-success nodes: node-31f7dc27-b5bf-42eb-992d-5e1e17cda456:FAILED
- if-routing (mcp) output: no successful core.merge node with output; non-success nodes: node-31f7dc27-b5bf-42eb-992d-5e1e17cda456:FAILED
- if-routing (mcp) output: no successful core.merge node with output; non-success nodes: node-31f7dc27-b5bf-42eb-992d-5e1e17cda456:FAILED

## Agent errors

- webhook-set-respond (mcp): openrouter: model request took longer than 1 s (no time left to retry)
