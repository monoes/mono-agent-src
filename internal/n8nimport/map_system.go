package n8nimport

import (
	"strings"
)

func init() {
	Register("n8n-nodes-base.executeCommand", mapExecuteCommand)
	Register("n8n-nodes-base.ssh", mapSSH)
	Register("n8n-nodes-base.ftp", mapFTP)
	Register("n8n-nodes-base.rssFeedRead", mapRSSRead)
	Register("n8n-nodes-base.emailSend", mapEmailSend)
	Register("n8n-nodes-base.spreadsheetFile", mapSpreadsheetFile)
	Register("n8n-nodes-base.writeBinaryFile", mapWriteBinaryFile)
}

// exitCodeStep adds n8n's exitCode field next to mono's exit_code.
var exitCodeStep = ChainStep{Type: "core.code", Config: codeConfig(wrapCode(
	"return items.map(function (it) { var o = Object.assign({}, it.json); o.exitCode = o.exit_code; return o; });\n"))}

// mapExecuteCommand runs the command through a shell, as n8n does.
func mapExecuteCommand(c *MapContext) Mapping {
	cfg := map[string]interface{}{"command": "sh", "args": []interface{}{"-c", c.Str("command", "")}}
	return Mapping{Type: "system.execute_command", Config: cfg, EntrySuffix: " (run)", Chain: []ChainStep{exitCodeStep}}
}

func mapSSH(c *MapContext) Mapping {
	cred := c.Credential("ssh", "sshPassword", "sshPrivateKey")
	c.Ignore("authentication")
	switch res := c.Literal("resource", "command"); res {
	case "command":
	case "file":
		return mapSSHFile(c, cred)
	default:
		return c.OperationUnsupported(res, c.Literal("operation", ""))
	}
	c.Ignore("operation")
	cmd := c.Str("command", "")
	if cwd := c.Str("cwd", "/"); cwd != "/" && cwd != "" {
		cmd = "cd " + cwd + " && " + cmd
	}
	return Mapping{Type: "http.ssh", Config: map[string]interface{}{"credential_id": cred, "command": cmd},
		EntrySuffix: " (run)", Chain: []ChainStep{exitCodeStep}}
}

// mapSSHFile maps the SFTP upload/download of n8n's file resource.
func mapSSHFile(c *MapContext, cred string) Mapping {
	op := c.Literal("operation", "upload")
	if op != "upload" && op != "download" {
		return c.OperationUnsupported("file", op)
	}
	cfg := map[string]interface{}{"credential_id": cred, "resource": "file", "operation": op,
		"path": c.Str("path", ""), "binary_property": c.Str("binaryPropertyName", "data")}
	if c.Has("options.fileName") {
		cfg["file_name"] = c.Str("options.fileName", "")
	}
	return Mapping{Type: "http.ssh", Config: cfg}
}

func mapFTP(c *MapContext) Mapping {
	proto := c.Literal("protocol", "ftp")
	cred := c.Credential("ftp", "ftp", "sftp")
	if proto == "sftp" {
		c.Warn("protocol", "sftp", "http.ftp speaks FTP only")
	}
	op := c.Literal("operation", "download")
	cfg := map[string]interface{}{"credential_id": cred, "operation": op, "remote_path": c.Str("path", "/")}
	switch op {
	case "list":
		if c.Bool("recursive", false) {
			c.Unsupported("recursive")
		}
	case "delete":
		c.Ignore("options.folder", "options.recursive")
	case "download":
		c.Ignore("binaryPropertyName", "options.enableConcurrentReads", "options.maxConcurrentReads", "options.chunkSize")
		c.Note("the downloaded file is returned base64-encoded in the item field 'contents'")
	case "upload":
		if c.Bool("binaryData", true) {
			c.Warn("binaryData", true, "set local_path to the file to upload (n8n uploaded binary data)")
			c.Ignore("binaryPropertyName")
		} else {
			c.Warn("fileContent", c.Raw("fileContent"), "http.ftp uploads a local file; write the content to local_path first")
		}
		cfg["local_path"] = ""
	case "rename":
		delete(cfg, "remote_path")
		cfg["old_path"], cfg["new_path"] = c.Str("oldPath", ""), c.Str("newPath", "")
		if c.Bool("options.createDirectories", false) {
			cfg["create_directories"] = true
		}
	default:
		return c.OperationUnsupported("", op)
	}
	c.Ignore("options.timeout")
	return Mapping{Type: "http.ftp", Config: cfg}
}

func mapRSSRead(c *MapContext) Mapping {
	if c.Has("options.customFields") {
		c.Unsupported("options.customFields")
	}
	c.Ignore("options.ignoreSSL")
	return Mapping{Type: "system.rss_read", Config: map[string]interface{}{"url": c.Str("url", "")}}
}

func mapEmailSend(c *MapContext) Mapping {
	cfg := map[string]interface{}{"credential_id": c.Credential("smtp", "smtp")}
	cfg["from"] = c.Str("fromEmail", "")
	cfg["to"] = c.Str("toEmail", "")
	cfg["subject"] = c.Str("subject", "")
	cc, bcc := "ccEmail", "bccEmail"
	if c.version(2) {
		cc, bcc = "options.ccEmail", "options.bccEmail"
		c.Ignore("resource", "options.appendAttribution", "options.allowUnauthorizedCerts")
		if op := c.Literal("operation", "send"); op != "send" {
			return c.OperationUnsupported("email", op)
		}
		def := "text"
		if c.version(2.1) {
			def = "html"
		}
		switch c.Literal("emailFormat", def) {
		case "html":
			cfg["body"], cfg["body_type"] = c.Str("html", ""), "html"
		case "both":
			cfg["body"], cfg["body_type"] = c.Str("html", ""), "html"
			c.Warn("emailFormat", "both", "only the HTML part is sent")
			c.Ignore("text")
		default:
			cfg["body"], cfg["body_type"] = c.Str("text", ""), "text"
		}
		for _, p := range []string{"options.attachments", "options.fileAttachments", "options.replyTo"} {
			if c.Has(p) {
				c.Unsupported(p)
			}
		}
	} else {
		if c.Has("html") && c.Raw("html") != "" {
			cfg["body"], cfg["body_type"] = c.Str("html", ""), "html"
			c.Ignore("text")
		} else {
			cfg["body"], cfg["body_type"] = c.Str("text", ""), "text"
		}
		for _, p := range []string{"attachments", "options.replyTo"} {
			if c.Has(p) {
				c.Unsupported(p)
			}
		}
		c.Ignore("options.allowUnauthorizedCerts")
	}
	if c.Has(cc) {
		cfg["cc"] = c.Str(cc, "")
	}
	if c.Has(bcc) {
		cfg["bcc"] = c.Str(bcc, "")
	}
	return Mapping{Type: "comm.email_send", Config: cfg}
}

// spreadsheetOp picks data.spreadsheet's operation for a file format.
func spreadsheetOp(c *MapContext, write bool, format string) (string, bool) {
	kind := ""
	switch strings.ToLower(format) {
	case "csv":
		kind = "csv"
	case "xlsx", "xls", "autodetect", "":
		kind = "xlsx"
		if format == "autodetect" || format == "" {
			c.Note("the file format is detected from file_path's extension: use .csv for CSV")
		}
	default:
		return "", false
	}
	if write {
		return "write_" + kind, true
	}
	return "read_" + kind, true
}

func mapSpreadsheetFile(c *MapContext) Mapping {
	op := c.Literal("operation", "fromFile")
	write := op == "toFile"
	def := "autodetect"
	if write || !c.version(2) {
		def = "xls"
	}
	format := c.Literal("fileFormat", def)
	monoOp, ok := spreadsheetOp(c, write, format)
	if !ok {
		return c.OperationUnsupported("", op+" "+format)
	}
	return spreadsheetMapping(c, monoOp, write)
}

func spreadsheetMapping(c *MapContext, monoOp string, write bool) Mapping {
	cfg := map[string]interface{}{"operation": monoOp, "has_header": c.Bool("options.headerRow", true)}
	c.Ignore("binaryPropertyName", "options.compression", "options.delimiter", "options.encoding", "options.includeEmptyCells",
		"options.rawData", "options.readAsString", "options.range", "options.maxRowCount", "options.fromLine")
	if s := c.Literal("options.sheetName", ""); s != "" {
		cfg["sheet_name"] = s
	}
	if write {
		cfg["file_path"] = c.Str("options.fileName", "")
		c.Note("the spreadsheet is written to file_path on disk (n8n produced binary data)")
	} else {
		cfg["file_path"] = ""
		c.Warn("binaryPropertyName", c.Literal("binaryPropertyName", "data"), "data.spreadsheet reads a file from disk: set file_path (n8n read the file from binary data)")
	}
	return Mapping{Type: "data.spreadsheet", Config: cfg}
}

func mapWriteBinaryFile(c *MapContext) Mapping {
	if c.Bool("options.append", false) {
		c.Unsupported("options.append")
	}
	field := c.Literal("dataPropertyName", "data")
	c.Warn("dataPropertyName", field, "data.write_binary_file writes base64 text from an item field, not n8n binary data")
	return Mapping{Type: "data.write_binary_file", Config: map[string]interface{}{"file_path": c.Str("fileName", ""), "field": field, "encoding": "base64"}}
}
