package main

// dialogTextFor is s as a native dialog should be given it on goos. Wails v2.17.0 on Linux passes the message to
// gtk_message_dialog_new as the printf FORMAT with no arguments (and the title is set apart), so every '%' is doubled
// there; macOS (NSAlert) and Windows (MessageBox) show the text as it is. showDialog applies it to the message (not
// the title); a bump of wails must be checked against this behaviour.
func dialogTextFor(goos, s string) string {
	if goos == "windows" || goos == "darwin" {
		return s
	}
	out := make([]byte, 0, len(s)+4)
	for i := 0; i < len(s); i++ {
		out = append(out, s[i])
		if s[i] == '%' {
			out = append(out, '%')
		}
	}
	return string(out)
}
