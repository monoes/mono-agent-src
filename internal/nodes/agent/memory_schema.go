package agent

// MemoryNodeSchema documents the config keys MemoryNode.Execute reads — see
// AskNodeSchema and internal/tools/schemagen for the tag grammar.
type MemoryNodeSchema struct {
	Operation string `json:"operation" schema:"label=Operation,type=select,options=get|insert|delete,default=get,help=get reads a session's messages; insert adds messages; delete removes the last N or all."`

	SessionKey string `json:"session_key" schema:"label=Session Key,type=text,default={{ $json.sessionId }},help=The conversation to use， resolved per item. The default reads the sessionId field a chat trigger puts on its items."`

	Format string `json:"format" schema:"label=Format,type=select,options=simple|pairs|raw,default=simple,depends_on_key=operation,depends_on_values=get,help=simple: [{role， content}]. pairs: {human， ai} groups (n8n's simplified output). raw: also seq and created_at."`

	Group bool `json:"group" schema:"label=Group Messages,type=boolean,default=true,depends_on_key=operation,depends_on_values=get,help=One item per input item with messages and messagesCount (input fields kept)， or one item per message."`

	InsertMode string `json:"insert_mode" schema:"label=Insert Mode,type=select,options=append|override,default=append,depends_on_key=operation,depends_on_values=insert,help=append adds after the existing messages; override replaces the session with these."`

	Messages string `json:"messages" schema:"label=Messages (JSON),type=code,language=json,rows=4,depends_on_key=operation,depends_on_values=insert,placeholder=[{\"role\": \"human\"， \"content\": \"{{ $json.chatInput }}\"}],help=A list of {role， content}; role is human， ai or system (user and assistant are accepted). Both are resolved per item."`

	DeleteMode string `json:"delete_mode" schema:"label=Delete Mode,type=select,options=last_n|all,default=last_n,depends_on_key=operation,depends_on_values=delete"`

	Count float64 `json:"count" schema:"label=Messages to Delete,type=number,default=2,min=1,depends_on_key=delete_mode,depends_on_values=last_n,help=How many of the newest messages delete removes (all of them when there are fewer)."`
}
