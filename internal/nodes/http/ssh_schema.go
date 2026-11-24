package httpnodes

// credential_platform: ssh
//
// SSHNodeSchema documents the config keys SSHNode.Execute reads out of its
// map[string]interface{} config — see
// internal/nodes/control.SetNodeSchema's doc comment for why this is a
// companion struct rather than the runtime config, and
// internal/tools/schemagen for the tag grammar.
//
// Two fields have no entry in the hand-written schemas/http.ssh.json this
// replaces, both of which Execute has always read:
//
//   - "timeout_seconds" (default 30 in code) bounds connecting (the dial,
//     the handshake, opening the session and its environment request, all
//     together) and then, again, how long the remote command may run before
//     the node cancels it and returns an error.
//   - "known_hosts" switches host key verification from trust on first use
//     (~/.monoagent/known_hosts) to a real known_hosts file check.
//     "allow_unknown_host_key" turns the check off (ssh.InsecureIgnoreHostKey).
type SSHNodeSchema struct {
	Host string `json:"host" schema:"label=SSH Host,type=text,required"`

	Port float64 `json:"port" schema:"label=Port,type=number,default=22"`

	Username string `json:"username" schema:"label=Username,type=text,required"`

	Password string `json:"password" schema:"label=Password,type=password"`

	PrivateKey string `json:"private_key" schema:"label=Private Key (PEM),type=textarea,rows=6"`

	Passphrase string `json:"passphrase" schema:"label=Key Passphrase,type=password"`

	KnownHosts string `json:"known_hosts" schema:"label=Known Hosts File,type=text,help=Path to a known_hosts file used to verify the server's host key. If left blank， the key a host shows the first time is remembered in ~/.monoagent/known_hosts and a different key later is refused."`

	AllowUnknownHostKey bool `json:"allow_unknown_host_key" schema:"label=Allow Unknown Host Key,type=boolean,help=Turns host key verification off when no Known Hosts File is set (insecure: credentials go to whichever host answers)."`

	Resource string `json:"resource" schema:"label=Resource,type=select,options=command|file,default=command,help=command runs a shell command; file uploads or downloads a file over SFTP."`

	Command string `json:"command" schema:"label=Command,type=text,required,placeholder=ls -la /var/log,depends_on_key=resource,depends_on_values=command"`

	Operation string `json:"operation" schema:"label=File Operation,type=select,options=upload|download,default=upload,depends_on_key=resource,depends_on_values=file"`

	Path string `json:"path" schema:"label=Remote Path,type=text,placeholder=/home/user/uploads,help=upload: the remote directory the file goes into. download: the full remote file path. A leading ~/ is the login user's home.,depends_on_key=resource,depends_on_values=file"`

	BinaryProperty string `json:"binary_property" schema:"label=Binary Property,type=text,default=data,help=upload: the input item's binary property (or base64 field) to send. download: the property the file is written to.,depends_on_key=resource,depends_on_values=file"`

	FileName string `json:"file_name" schema:"label=File Name,type=text,help=upload: the remote file name (default the item's file_name， else the binary property name). download: the output file_name (default the remote base name).,depends_on_key=resource,depends_on_values=file"`

	TimeoutSeconds float64 `json:"timeout_seconds" schema:"label=Timeout (seconds),type=number,default=30,help=Connecting must finish within this; then the command is cancelled if it runs longer than this."`
}
