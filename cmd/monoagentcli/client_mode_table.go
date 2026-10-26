package main

// clientModeClasses classifies every command path for client mode (client_mode.go). A new top
// level command has no entry and TestEveryCommandIsClassifiedForClientMode fails until it gets one;
// at run time an unclassified command is refused.
//
//	api-mapped      runs through the daemon's agent API; needs its own entry per subcommand
//	local-safe      reads embedded text only and opens no database or key store; own entry per subcommand
//	operator-only   the user's own: refused here, never forwarded; children inherit
//	refused-in-agent-mode  has no agent API route yet: refused; children inherit
//
// An entry is a decision: adding a command to api-mapped means a typed route behind it.
var clientModeClasses = map[string]string{
	// Runs through the agent API.
	"aitask":          cmAPIMapped,
	"aitask kinds":    cmAPIMapped,
	"aitask status":   cmAPIMapped,
	"aitask result":   cmAPIMapped,
	"aitask complete": cmAPIMapped,
	"aitask fail":     cmAPIMapped,
	"aitask cancel":   cmAPIMapped,

	// Embedded text only.
	"help":            cmLocalSafe,
	"version":         cmLocalSafe,
	"ref":             cmLocalSafe,
	"ref account":     cmLocalSafe,
	"ref api":         cmLocalSafe,
	"ref browser":     cmLocalSafe,
	"ref commands":    cmLocalSafe,
	"ref connections": cmLocalSafe,
	"ref crawling":    cmLocalSafe,
	"ref examples":    cmLocalSafe,
	"ref expressions": cmLocalSafe,
	"ref node":        cmLocalSafe,
	"ref nodes":       cmLocalSafe,
	"ref org":         cmLocalSafe,
	"ref publication": cmLocalSafe,
	"ref tasks":       cmLocalSafe,
	"ref templates":   cmLocalSafe,
	"ref workflow":    cmLocalSafe,

	// The user's own: they administer authority, sign-in, secrets, the daemon or the install.
	"account":            cmOperatorOnly,
	"agent-mode":         cmOperatorOnly,
	"aitask budget":      cmOperatorOnly,
	"aitask start":       cmOperatorOnly,
	"aitask submit":      cmOperatorOnly,
	"aitask submit-page": cmOperatorOnly,
	"aitask supervise":   cmOperatorOnly,
	"aitask wait":        cmOperatorOnly,
	"api":                cmOperatorOnly,
	"coder":              cmOperatorOnly,
	"connect":            cmOperatorOnly,
	"daemon":             cmOperatorOnly,
	"db":                 cmOperatorOnly,
	"extension":          cmOperatorOnly,
	"hil":                cmOperatorOnly,
	"httpapi":            cmOperatorOnly,
	"init":               cmOperatorOnly,
	"integration":        cmOperatorOnly,
	"library":            cmOperatorOnly,
	"login":              cmOperatorOnly,
	"logout":             cmOperatorOnly,
	"mcp":                cmOperatorOnly,
	"profile":            cmOperatorOnly,
	"release":            cmOperatorOnly,
	"sandbox":            cmOperatorOnly,
	"secret":             cmOperatorOnly,
	"setup":              cmOperatorOnly,
	"update":             cmOperatorOnly,

	// No agent API route yet.
	"aitask list":      cmRefused,
	"action":           cmRefused,
	"agent":            cmRefused,
	"application":      cmRefused,
	"automation":       cmRefused,
	"browser":          cmRefused,
	"capture":          cmRefused,
	"chat":             cmRefused,
	"completion":       cmRefused,
	"config":           cmRefused,
	"crawl":            cmRefused,
	"doctor":           cmRefused,
	"documents":        cmRefused,
	"export":           cmRefused,
	"image":            cmRefused,
	"jev":              cmRefused,
	"list":             cmRefused,
	"message-template": cmRefused,
	"node":             cmRefused,
	"nodejs":           cmRefused,
	"org":              cmRefused,
	"people":           cmRefused,
	"publication":      cmRefused,
	"record":           cmRefused,
	"status":           cmRefused,
	"summary":          cmRefused,
	"table":            cmRefused,
	"task":             cmRefused,
	"workflow":         cmRefused,
}
