package httpnodes

// credential_platform: ftp
//
// FTPNodeSchema documents the config keys FTPNode.Execute reads out of its
// map[string]interface{} config — see
// internal/nodes/control.SetNodeSchema's doc comment for why this is a
// companion struct rather than the runtime config, and
// internal/tools/schemagen for the tag grammar.
//
// The hand-written schemas/http.ftp.json this replaces marks "username" and
// "password" as required=true, but FTPNode.Execute only calls conn.Login at
// all when username is non-empty ("if username != "" { ... }") — an empty
// username is accepted and the node proceeds without authenticating
// (anonymous FTP). Marked not-required here to match actual behavior.
type FTPNodeSchema struct {
	Host string `json:"host" schema:"label=FTP Host,type=text,required"`

	Port float64 `json:"port" schema:"label=Port,type=number,default=21"`

	Username string `json:"username" schema:"label=Username,type=text,help=Leave blank for anonymous FTP (no login is attempted)."`

	Password string `json:"password" schema:"label=Password,type=password"`

	Operation string `json:"operation" schema:"label=Operation,type=select,required,options=list|download|upload|delete|rename,default=list"`

	RemotePath string `json:"remote_path" schema:"label=Remote Path,type=text,required,placeholder=/remote/dir/file.csv,depends_on_key=operation,depends_on_values=list|download|upload|delete"`

	OldPath string `json:"old_path" schema:"label=Old Path,type=text,required,placeholder=/remote/dir/old.csv,depends_on_key=operation,depends_on_values=rename"`

	NewPath string `json:"new_path" schema:"label=New Path,type=text,required,placeholder=/remote/archive/new.csv,depends_on_key=operation,depends_on_values=rename"`

	CreateDirectories bool `json:"create_directories" schema:"label=Create Directories,type=boolean,default=false,help=Create the missing parent directories of new_path before renaming.,depends_on_key=operation,depends_on_values=rename"`

	LocalPath string `json:"local_path" schema:"label=Local File Path,type=text,help=Required for upload. For download， saves to this path instead of returning base64 contents.,depends_on_key=operation,depends_on_values=upload|download"`
}
