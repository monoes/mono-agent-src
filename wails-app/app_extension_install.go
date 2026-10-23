package main

// Settings → Browser extension: where the extension that ships with the
// app is unpacked, and the steps to load it in the browser. Everything
// shells out to `monoagentcli extension info|install`.

// ExtensionInfo mirrors `extension info --json`.
type ExtensionInfo struct {
	Path      string `json:"path"`
	Installed bool   `json:"installed"`
	Current   bool   `json:"current"`
	Version   string `json:"version"`
}

// GetExtensionInfo says where the extension is unpacked. A missing or out of
// date copy is unpacked first (from the CLI's own bundle, no download), so
// the path it returns always holds the extension to load.
func (a *App) GetExtensionInfo() (*ExtensionInfo, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	var info ExtensionInfo
	if err := a.globalJSON(profileCLITimeout, &info, "extension", "info"); err != nil {
		return nil, err
	}
	if !info.Current {
		return a.InstallExtension()
	}
	return &info, nil
}

// InstallExtension unpacks the bundled extension again (after an update) and
// returns the fresh info.
func (a *App) InstallExtension() (*ExtensionInfo, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	var info ExtensionInfo
	if err := a.globalJSON(profileCLITimeout, &info, "extension", "install"); err != nil {
		return nil, err
	}
	return &info, nil
}
