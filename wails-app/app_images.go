package main

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/monoes/mono-agent/internal/imagescan"
)

// ── Media Vault ──────────────────────────────────────────────────────────────

// Every media-vault binding shells out to `monoagentcli media …` (the old
// `image` name works too), which owns the vault_images queries and the
// vault folder. The bindings keep their GetVaultImage… names. The rows come back in
// the shape these bindings have always returned (snake_case keys, url
// /vault-image/<id>). User-supplied text (paths, labels, queries) goes
// after "--" so a leading "-" is never read as a flag.

func (a *App) GetVaultImages(limit int) ([]map[string]interface{}, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 200
	}
	return a.imageRows("media", "list", "--limit", strconv.Itoa(limit))
}

func (a *App) SearchVaultImages(query string) ([]map[string]interface{}, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	return a.imageRows("media", "search", "--", query)
}

func (a *App) imageRows(args ...string) ([]map[string]interface{}, error) {
	var out []map[string]interface{}
	if err := a.runMonoCLI("", &out, args...); err != nil {
		return nil, err
	}
	if out == nil {
		out = []map[string]interface{}{}
	}
	return out, nil
}

func (a *App) GetVaultImage(id string) (map[string]interface{}, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	var out map[string]interface{}
	if err := a.runMonoCLI("", &out, "media", "get", "--", id); err != nil {
		return nil, err
	}
	return out, nil
}

// GetVaultImageData returns a vault image as a base64 data URL (e.g.
// "data:image/png;base64,..."). This is the reliable way to display vault
// images inside the Wails WebView without relying on the HTTP asset handler.
func (a *App) GetVaultImageData(id string) (string, error) {
	if err := a.signedOut(); err != nil {
		return "", err
	}
	var out struct {
		DataURL string `json:"data_url"`
	}
	if err := a.runMonoCLI("", &out, "media", "data", "--", id); err != nil {
		return "", err
	}
	return out.DataURL, nil
}

func (a *App) AddVaultImage(srcPath, label string) (map[string]interface{}, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	args := []string{"media", "add"}
	if label != "" {
		args = append(args, "--label", label)
	}
	var out map[string]interface{}
	if err := a.runMonoCLI("", &out, append(args, "--", srcPath)...); err != nil {
		return nil, err
	}
	a.emitImagesChanged(map[string]interface{}{"added": 1})
	return out, nil
}

// emitImagesChanged tells open Media Vault pages to re-list after an edit
// made through the app; the folder watcher emits the same event for
// changes on disk.
func (a *App) emitImagesChanged(data map[string]interface{}) {
	id, err := a.profileID()
	if err != nil {
		return // no profile selected: no page to tell
	}
	data["profileID"] = id
	emitImageEvent(a, "images:changed", data)
}

// emitImageEvent is the Wails event emitter, a variable so tests can
// record the events.
var emitImageEvent = (*App).emitFolderEvent

// OpenVaultFilePicker opens a native file picker and returns the selected file path (empty if cancelled).
func (a *App) OpenVaultFilePicker() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Select Media",
		Filters: []runtime.FileFilter{
			{DisplayName: "Media", Pattern: "*.png;*.jpg;*.jpeg;*.gif;*.webp;*.bmp;*.mp3;*.wav;*.m4a;*.aac;*.ogg;*.opus;*.flac;*.mp4;*.webm;*.mov;*.m4v;*.mkv"},
		},
	})
	if err != nil {
		return ""
	}
	return path
}

// SaveVaultImageToFile opens a native Save File dialog pre-filled with suggestedName,
// then copies the vault image file to the chosen path. Returns "" if the user cancels.
func (a *App) SaveVaultImageToFile(id, suggestedName string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	// Check first, so an unknown id fails before the dialog is shown.
	item, err := a.GetVaultImage(id)
	if err != nil {
		return "error: image not found"
	}
	filename, _ := item["filename"].(string)
	dest, err := saveImageDialog(a, saveVaultItemOptions(filename, suggestedName))
	if err != nil || dest == "" {
		return ""
	}
	var out struct {
		Path string `json:"path"`
	}
	if err := a.runMonoCLI("", &out, "media", "export", "--", id, dest); err != nil {
		return "error: " + err.Error()
	}
	return out.Path
}

// saveImageDialog is the native save dialog, a variable so tests can
// answer it.
var saveImageDialog = func(a *App, opts runtime.SaveDialogOptions) (string, error) {
	return runtime.SaveFileDialog(a.ctx, opts)
}

// saveVaultItemOptions is the save dialog for the vault item stored as
// filename. A file that is not an image (audio, video, or a PDF or text file
// the extension saved) gets "Save File" and a filter of its own extension, so
// the dialog neither hides its type nor adds an image extension to the name.
func saveVaultItemOptions(filename, suggestedName string) runtime.SaveDialogOptions {
	if ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), ".")); ext != "" && !imagescan.AllowedExtensions[ext] {
		return runtime.SaveDialogOptions{
			Title:           "Save File",
			DefaultFilename: suggestedName,
			Filters:         []runtime.FileFilter{{DisplayName: strings.ToUpper(ext), Pattern: "*." + ext}},
		}
	}
	return runtime.SaveDialogOptions{
		Title:           "Save Image",
		DefaultFilename: suggestedName,
		Filters: []runtime.FileFilter{
			{DisplayName: "Images", Pattern: "*.png;*.jpg;*.jpeg;*.gif;*.webp;*.bmp;*.tif"},
		},
	}
}

// UpdateVaultImageLabel renames an image; an empty label clears it.
func (a *App) UpdateVaultImageLabel(id, label string) error {
	if err := a.signedOut(); err != nil {
		return err
	}
	args := []string{"media", "label", "--", id}
	if label != "" {
		args = append(args, label)
	}
	if err := a.runMonoCLI("", nil, args...); err != nil {
		return err
	}
	a.emitImagesChanged(map[string]interface{}{"updated": id})
	return nil
}

// DeleteVaultImage removes an image from the vault. An image in the
// profile folder keeps its file there (`image delete` removes only its row
// and keeps later syncs from adding it back).
func (a *App) DeleteVaultImage(id string) error {
	if err := a.signedOut(); err != nil {
		return err
	}
	if err := a.runMonoCLI("", nil, "media", "delete", "--", id); err != nil {
		return err
	}
	a.emitImagesChanged(map[string]interface{}{"deleted": id})
	return nil
}

func (a *App) GetVaultStats() (map[string]interface{}, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	var out map[string]interface{}
	if err := a.runMonoCLI("", &out, "media", "stats"); err != nil {
		return nil, err
	}
	return out, nil
}
