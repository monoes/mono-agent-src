package main

import (
	"fmt"
	"github.com/spf13/cobra"
)

func refPublicationCmd() *cobra.Command {
	return &cobra.Command{Use: "publication", Short: "Published content history, automatic capture and custom registration", Args: cobra.NoArgs, Run: func(cmd *cobra.Command, args []string) {
		fmt.Fprintln(cmd.OutOrStdout(), `Publication is the active profile's local history of successfully published content.
It stores posts, comments, replies, articles, videos and shared channel posts with
their content, destination, live URL/remote ID when returned, timestamp and source.

Read history (all local-only):
  monoagentcli --json publication list --platform reddit --kind comment --limit 50
  monoagentcli --json publication list --search "release" --workflow <id> --agent <id>
  monoagentcli --json publication list --since 2026-10-01 --until 2026-10-06
  monoagentcli --json publication get <id>
  monoagentcli --json publication stats

Built-in publishing operations record automatically through workflows, agent-granted
automations and direct node runs. Reads, likes, follows, drafts and private messages
do not create records. History starts when this feature is installed.

Custom HTTP/code/shell publishers and agents using external tools must register
each successful publication explicitly. Register only after publishing succeeds:
  monoagentcli --json publication register --stdin-json < publication.json

Example publication.json:
  {"platform":"my-blog","kind":"article","title":"Release notes",
   "body":"Published article text","url":"https://example.com/posts/release",
   "remote_id":"release","agent_id":"writer","idempotency_key":"blog:release"}

Kinds: post, comment, reply, article, video, other. Arbitrary platform names are
accepted. At least one title, body, URL or media reference is required. Never
include credentials. Use a stable idempotency_key for retrying registration;
a new publication needs a new key. Registration does not publish anything.

In workflows, connect a publication.register node after a custom publisher.
It accepts known publication fields from incoming items or per-item config
expressions, such as body="{{ $json.text }}", and attaches workflow/run/node IDs.
Items explicitly marked success:false or containing an error are skipped.

MCP: publication_list, publication_get and publication_stats are read-only.
publication_register requires mcp --allow-mutations. Assistant tools expose the
same profile-scoped history. Org grant mode retains its existing capability rules.

If publishing succeeds but automatic tracking fails, a warning reports the failure;
it must not retry the remote action just to register history. Missing live URLs
remain missing rather than being invented. External publishing outside mono-agent
cannot be observed automatically.`)
	}}
}
