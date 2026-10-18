# Publication history

Status: approved and implemented.

## Purpose

Add a desktop section named **Publication**, backed by CLI commands and MCP tools, that collects posts, comments, replies, articles, videos, and other content published by mono-agent workflows and agents across destinations. Records belong to the active profile and persist locally in SQLite.

## Approach

Use a shared publication repository and explicit publishing adapters. Supported publishing operations register their successful results automatically, regardless of whether they run inside a workflow or through a direct node command. Agents using those operations inherit registration. Agents or custom tools publishing outside mono-agent register through the CLI or MCP, with agent instructions explaining this requirement.

Alternatives considered: reconstruct history from workflow execution outputs (misses direct and external publishing and depends on output retention); require a separate registration step in every workflow (easy to forget). Shared automatic capture plus explicit registration for external publishers provides the most reliable coverage.

## Record model

A publication contains an ID, profile ID, publication kind, platform or destination, title, published body, media references, account label when available, remote ID, live URL, parent URL or ID for comments/replies, published timestamp, recorded timestamp, and source information. Source information includes workflow/execution/node IDs and agent/org/role identifiers when the execution context supplies them. Preserve source labels so history remains understandable after workflows or agents are removed.

Remote IDs and URLs may be absent when a publisher does not return them; show this honestly. Store selected publication fields rather than raw node configuration, credentials, or complete API responses. Allow arbitrary destination names for custom publishers.

## Automatic registration

Inventory publishing operations in the browser automation packages, API service/communication nodes, and legacy social command paths. Add adapters at their common success boundaries, with shared normalization and persistence. Register actual successful publications, including successful items in a partially failed batch. Reads, likes, follows, deletions, drafts, and scheduled-but-unsent content do not create publications. Private mail and direct messages remain in Communications; posts in channels and other shared destinations count as publications.

Record the exact resolved content used for each published item. Preserve the actual execution profile in the multi-profile daemon. Use remote identity where available and source/item identity otherwise to deduplicate repeated registration of the same result. Identical content deliberately published twice must remain two records.

Do not infer publishing from arbitrary HTTP requests or shell commands. Custom publishers use explicit registration after success, either through a workflow registration node or the CLI/MCP. Update agent-facing guidance to require registration for external publishing.

If a remote publication succeeds but registration fails, report an explicit tracking warning with available remote identity; do not turn the successful remote action into a retryable publishing failure, which could publish twice. Dry runs create no records.

## CLI first

Add `monoagentcli publication list`, `get <id>`, `register --stdin-json`, and `stats`. List supports search, platform/destination, kind, workflow, agent, date range, limit, and offset. Commands follow existing global `--json`, snake_case output, empty-array, error-code, and profile-scope conventions. Registration accepts an idempotency key for external publishers. Document these commands in the offline reference and AGENTS.md.

Provide a `publication.register` workflow node for custom HTTP/code/shell publishers. This node records a reported success; it does not publish content itself.

## MCP and assistant tools

Expose read-only `publication_list`, `publication_get`, and `publication_stats`. Expose `publication_register` only with the existing mutation opt-in. Reuse the repository and profile checks for assistant tools rather than duplicating storage logic. Keep org grant-mode access constrained to its existing capability model; automatic registration works without widening grants.

## Desktop UI

Add **Publication** under the sidebar's Data section using the app's existing styles and translations. The page shows newest-first publication entries, a search field, platform and kind filters, pagination, counts, and an explicit refresh action. Refresh on page activation and profile changes.

Each entry shows content preview, platform, kind, time, account when known, and the agent/workflow source. A detail view shows the complete body, media references, destination, reply parent, and source IDs. Offer an external link when a live URL is available and navigation to an existing workflow execution when available. Include loading, empty, and error states. The Wails bridge calls the CLI, as elsewhere in the desktop app.

## Validation

Verify migration and repository behavior, profile isolation, pagination/filtering, deduplication, repeat publishing, and persistence after source deletion. Exercise automatic registration for browser and API publishers, standalone node runs, per-item content, partial success, dry runs, and tracking failures without a second remote publish. Check CLI JSON/error conventions and MCP mutation gating. Test frontend filters, detail rendering, profile refresh, and empty/error states; run affected Go suites and the frontend tests/build.

## Scope limits

Initial history starts when this feature is installed. Historical external posts are not fetched automatically. External actions bypassing mono-agent require explicit registration; no claim of automatic observation is made for those actions. Engagement analytics, editing/deleting remote content, and a publishing composer are separate features.
