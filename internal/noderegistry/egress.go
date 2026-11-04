package noderegistry

import "github.com/monoes/mono-agent/internal/workflow"

// The egress classification of every registered node type, for the grant's
// host binding (workflow.CheckEgress): in a run that carries a secret read
// under `secret agent-access --host`, a type that is none may run, a checked
// type may run when every destination its config names (destination keys, a
// connection string's hosts, a URL value) is a host the grant names, and an
// unchecked type is refused. The first pass is conservative: a type is none
// only when it is plainly local, checked only when the engine derives its
// destinations, and everything else is unchecked; TestEveryNodeTypeHasAnEgressClass
// fails for a registered type that is in neither table. A checked type's
// fixed vendor host (api.telegram.org) is not bound by the list: content
// exfiltration through an allowed fixed host is issue #1418.
type egressEntry struct {
	class workflow.Egress
	why   string
}

var egressTable = map[string]egressEntry{
	// none
	"core.aggregate":          {workflow.EgressNone, "pure data"},
	"core.compare_datasets":   {workflow.EgressNone, "pure data"},
	"core.data_table":         {workflow.EgressNone, "the profile's local data tables"},
	"core.execute_workflow":   {workflow.EgressNone, "starts another run, whose nodes are classified on their own"},
	"core.filter":             {workflow.EgressNone, "pure data"},
	"core.form":               {workflow.EgressNone, "renders a form for a person; no send"},
	"core.if":                 {workflow.EgressNone, "pure control"},
	"core.limit":              {workflow.EgressNone, "pure data"},
	"core.merge":              {workflow.EgressNone, "pure data"},
	"core.noop":               {workflow.EgressNone, "pure control"},
	"core.remove_duplicates":  {workflow.EgressNone, "pure data"},
	"core.respond_chat":       {workflow.EgressNone, "answers the caller that started the run"},
	"core.respond_webhook":    {workflow.EgressNone, "answers the caller that started the run"},
	"core.set":                {workflow.EgressNone, "pure data"},
	"core.sort":               {workflow.EgressNone, "pure data"},
	"core.split_in_batches":   {workflow.EgressNone, "pure control"},
	"core.stop_error":         {workflow.EgressNone, "pure control"},
	"core.switch":             {workflow.EgressNone, "pure control"},
	"core.wait":               {workflow.EgressNone, "pure control"},
	"data.compression":        {workflow.EgressNone, "pure data"},
	"data.convert_to_file":    {workflow.EgressNone, "pure data"},
	"data.crypto":             {workflow.EgressNone, "pure data"},
	"data.datetime":           {workflow.EgressNone, "pure data"},
	"data.extract_from_file":  {workflow.EgressNone, "reads a local file"},
	"data.html":               {workflow.EgressNone, "pure data"},
	"data.ical":               {workflow.EgressNone, "pure data"},
	"data.jwt":                {workflow.EgressNone, "pure data"},
	"data.markdown":           {workflow.EgressNone, "pure data"},
	"data.read_write_file":    {workflow.EgressNone, "local files (a secret kept in a file is #1418's later-run case)"},
	"data.rename_keys":        {workflow.EgressNone, "pure data"},
	"data.spreadsheet":        {workflow.EgressNone, "pure data"},
	"data.split_out":          {workflow.EgressNone, "pure data"},
	"data.summarize":          {workflow.EgressNone, "pure data"},
	"data.totp":               {workflow.EgressNone, "pure data"},
	"data.write_binary_file":  {workflow.EgressNone, "local files"},
	"data.xml":                {workflow.EgressNone, "pure data"},
	"applications.create":     {workflow.EgressNone, "the profile's local applications"},
	"applications.evaluate":   {workflow.EgressNone, "the profile's local applications"},
	"applications.list":       {workflow.EgressNone, "the profile's local applications"},
	"applications.prepare":    {workflow.EgressNone, "the profile's local applications"},
	"applications.set_status": {workflow.EgressNone, "the profile's local applications"},
	"applications.tag":        {workflow.EgressNone, "the profile's local applications"},
	"documents.render":        {workflow.EgressNone, "renders a document locally"},
	"image.adjust":            {workflow.EgressNone, "local image transform"},
	"image.convert":           {workflow.EgressNone, "local image transform"},
	"image.crop":              {workflow.EgressNone, "local image transform"},
	"image.info":              {workflow.EgressNone, "local image read"},
	"image.resize":            {workflow.EgressNone, "local image transform"},
	"image.thumbnail":         {workflow.EgressNone, "local image transform"},
	"image.vault_get":         {workflow.EgressNone, "local vault file"},
	"image.vault_save":        {workflow.EgressNone, "local vault file"},
	"people.save":             {workflow.EgressNone, "the profile's local people"},
	"publication.register":    {workflow.EgressNone, "records a publication locally"},
	"vault.secret_get":        {workflow.EgressNone, "local vault (refused to an unapproved run on its own)"},
	"vault.secret_save":       {workflow.EgressNone, "local vault"},
	"vector.document_loader":  {workflow.EgressNone, "sub-node configuration"},
	"vector.load_document":    {workflow.EgressNone, "local text"},
	"vector.split_text":       {workflow.EgressNone, "pure data"},
	"vector.text_splitter":    {workflow.EgressNone, "sub-node configuration"},
	"ai.output_parser":        {workflow.EgressNone, "sub-node configuration"},
	// checked
	"comm.email_read":    {workflow.EgressChecked, "destination keys (imap_host, imap_port)"},
	"comm.email_send":    {workflow.EgressChecked, "destination keys (smtp_host, smtp_port)"},
	"comm.telegram":      {workflow.EgressChecked, "photo_url is a URL value; the bot API host is fixed (#1418)"},
	"comm.twilio":        {workflow.EgressChecked, "url is a URL value; the API host is fixed (#1418)"},
	"comm.whatsapp":      {workflow.EgressChecked, "media_url is a URL value; the API host is fixed (#1418)"},
	"db.cratedb":         {workflow.EgressChecked, "connection_string hosts"},
	"db.ldap":            {workflow.EgressChecked, "destination keys (hostname, port)"},
	"db.mongodb":         {workflow.EgressChecked, "connection_string hosts (mongodb, +srv, several)"},
	"db.mssql":           {workflow.EgressChecked, "connection_string hosts"},
	"db.mysql":           {workflow.EgressChecked, "connection_string hosts (tcp(host:port))"},
	"db.oracle":          {workflow.EgressChecked, "connection_string hosts"},
	"db.postgres":        {workflow.EgressChecked, "connection_string hosts"},
	"db.questdb":         {workflow.EgressChecked, "connection_string hosts"},
	"db.redis":           {workflow.EgressChecked, "connection_string hosts (+sentinel)"},
	"db.snowflake":       {workflow.EgressChecked, "destination keys (account, base_url)"},
	"db.timescaledb":     {workflow.EgressChecked, "connection_string hosts"},
	"http.ftp":           {workflow.EgressChecked, "destination keys (host, port)"},
	"http.graphql":       {workflow.EgressChecked, "checks every hop at the send"},
	"http.request":       {workflow.EgressChecked, "checks every hop at the send"},
	"http.ssh":           {workflow.EgressChecked, "destination keys (host, port)"},
	"mcp.call":           {workflow.EgressChecked, "checks every hop at the send"},
	"queue.amqp":         {workflow.EgressChecked, "destination keys (hostname, port)"},
	"queue.kafka":        {workflow.EgressChecked, "destination keys (brokers)"},
	"queue.mqtt":         {workflow.EgressChecked, "destination keys (host, port)"},
	"queue.rabbitmq":     {workflow.EgressChecked, "destination keys (hostname, url, port)"},
	"service.discord":    {workflow.EgressChecked, "webhook_url is a URL value"},
	"service.jira":       {workflow.EgressChecked, "destination keys (base_url, domain)"},
	"service.mastodon":   {workflow.EgressChecked, "destination keys (instance, instance_url)"},
	"service.salesforce": {workflow.EgressChecked, "destination keys (instance_url)"},
	"service.shopify":    {workflow.EgressChecked, "destination keys (shop_domain)"},
	"system.rss_read":    {workflow.EgressChecked, "url is a URL value"},
	"ai.extract_page":    {workflow.EgressChecked, "url is a URL value"},
	"ai.read_page":       {workflow.EgressChecked, "url is a URL value"},
	"vector.embed":       {workflow.EgressChecked, "destination keys (base_url, url, endpoint, host)"},
	"vector.embeddings":  {workflow.EgressChecked, "destination keys (base_url, url, endpoint, host)"},
	"vector.store":       {workflow.EgressChecked, "destination keys (url, host, endpoint, index_host, control_url)"},
	"trigger.kafka":      {workflow.EgressChecked, "destination keys (brokers)"},
	"trigger.rabbitmq":   {workflow.EgressChecked, "destination keys"},
	"trigger.amqp":       {workflow.EgressChecked, "destination keys"},
	"trigger.mqtt":       {workflow.EgressChecked, "destination keys"},
	"trigger.redis":      {workflow.EgressChecked, "connection_string hosts"},
	"trigger.postgres":   {workflow.EgressChecked, "connection_string hosts"},
	// unchecked
	"agent.ask":                   {workflow.EgressUnchecked, "model vendor API"},
	"agent.memory":                {workflow.EgressNone, "verified network-free: memory.go and internal/aimemory import no network package; a local SQLite conversation store"},
	"ai.agent":                    {workflow.EgressUnchecked, "model vendor API"},
	"ai.chat":                     {workflow.EgressUnchecked, "model vendor API"},
	"ai.choose":                   {workflow.EgressUnchecked, "model vendor API"},
	"ai.classify":                 {workflow.EgressUnchecked, "model vendor API"},
	"ai.embed":                    {workflow.EgressUnchecked, "embedding vendor API"},
	"ai.extract":                  {workflow.EgressUnchecked, "model vendor API"},
	"ai.guardrails":               {workflow.EgressUnchecked, "may call a model"},
	"ai.memory":                   {workflow.EgressNone, "verified network-free: a sub-node configuration holder the engine never runs (internal/nodes/aisub imports no network package)"},
	"ai.model":                    {workflow.EgressUnchecked, "model vendor API"},
	"ai.task":                     {workflow.EgressUnchecked, "model vendor API"},
	"ai.transform":                {workflow.EgressUnchecked, "model vendor API"},
	"browser.jev":                 {workflow.EgressUnchecked, "drives the user's browser"},
	"comm.slack":                  {workflow.EgressUnchecked, "Slack API, fixed vendor host"},
	"core.code":                   {workflow.EgressUnchecked, "runs arbitrary code"},
	"core.evaluation":             {workflow.EgressUnchecked, "its judge calls a model through the monomind runner and its sheets sink calls Google Sheets"},
	"core.human_in_loop":          {workflow.EgressUnchecked, "its auto-suggest sends the item content to the Jev model client (jevconf.NewClient)"},
	"data.debug_helper":           {workflow.EgressNone, "verified network-free: generates test data; debug_helper.go imports no network package"},
	"discovery.search_jobs":       {workflow.EgressUnchecked, "job board APIs"},
	"image.edit":                  {workflow.EgressNone, "verified network-free: local image operations; no file of internal/nodes/image but remove_background_cgo.go imports a network package"},
	"image.remove_background":     {workflow.EgressUnchecked, "downloads its ONNX model and runtime from fixed hosts itself (remove_background_cgo.go)"},
	"org.ask":                     {workflow.EgressUnchecked, "runs agents"},
	"org.run":                     {workflow.EgressUnchecked, "runs agents"},
	"org.send":                    {workflow.EgressUnchecked, "sends through agents"},
	"people.sync_outlook_message": {workflow.EgressUnchecked, "Outlook API"},
	"service.airtable":            {workflow.EgressUnchecked, "vendor API, fixed host"},
	"service.asana":               {workflow.EgressUnchecked, "vendor API, fixed host"},
	"service.bluesky":             {workflow.EgressUnchecked, "vendor API, fixed host"},
	"service.devto":               {workflow.EgressUnchecked, "vendor API, fixed host"},
	"service.github":              {workflow.EgressUnchecked, "vendor API, fixed host"},
	"service.gmail":               {workflow.EgressUnchecked, "vendor API, fixed host"},
	"service.google_drive":        {workflow.EgressUnchecked, "vendor API, fixed host"},
	"service.google_sheets":       {workflow.EgressUnchecked, "vendor API, fixed host"},
	"service.hashnode":            {workflow.EgressUnchecked, "vendor API, fixed host"},
	"service.hubspot":             {workflow.EgressUnchecked, "vendor API, fixed host"},
	"service.huggingface":         {workflow.EgressUnchecked, "vendor API, fixed host"},
	"service.linear":              {workflow.EgressUnchecked, "vendor API, fixed host"},
	"service.notion":              {workflow.EgressUnchecked, "vendor API, fixed host"},
	"service.openrouter":          {workflow.EgressUnchecked, "vendor API, fixed host"},
	"service.outlook_mail":        {workflow.EgressUnchecked, "vendor API, fixed host"},
	"service.producthunt":         {workflow.EgressUnchecked, "vendor API, fixed host"},
	"service.reddit":              {workflow.EgressUnchecked, "vendor API, fixed host"},
	"service.stripe":              {workflow.EgressUnchecked, "vendor API, fixed host"},
	"service.youtube":             {workflow.EgressUnchecked, "vendor API, fixed host"},
	"system.execute_command":      {workflow.EgressUnchecked, "runs a shell command"},
}

// egressFamilies classify generated node types by prefix.
var egressFamilies = map[string]egressEntry{
	"app.": {workflow.EgressUnchecked, "generated vendor API package types: fixed vendor hosts, not derivable from config"},
}

// classifyEgress is workflow.SetEgressClassifier's function for Build.
func classifyEgress(nodeType string) (workflow.Egress, bool) {
	if e, ok := egressTable[nodeType]; ok {
		return e.class, true
	}
	for prefix, e := range egressFamilies {
		if len(nodeType) > len(prefix) && nodeType[:len(prefix)] == prefix {
			return e.class, true
		}
	}
	return workflow.EgressUnchecked, false
}
