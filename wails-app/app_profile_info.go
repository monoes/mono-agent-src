// wails-app/app_profile_info.go
//
// The Info page's bindings. All logic lives in `monoagentcli profile info`;
// these methods only forward to it (see app_documents.go).
package main

import (
	"encoding/base64"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
)

// maxProfileInfoPhotoBytes mirrors the CLI's own 10MB cap on a photo.
const maxProfileInfoPhotoBytes = 10 * 1024 * 1024

// ProfileInfoGroup is one section of the Info page.
type ProfileInfoGroup struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Scope string `json:"scope"` // "person", "business", or "" for both
}

// ProfileInfoField is one entry of the field catalog, the only list of
// fields the page renders from.
type ProfileInfoField struct {
	Key          string   `json:"key"`
	Label        string   `json:"label"`
	Group        string   `json:"group"`
	Kind         string   `json:"kind"`
	Options      []string `json:"options"`
	Sensitive    bool     `json:"sensitive"`
	Card         bool     `json:"card"`
	Autocomplete string   `json:"autocomplete"`
}

// ProfileInfoCatalog is `profile info fields`.
type ProfileInfoCatalog struct {
	Groups []ProfileInfoGroup `json:"groups"`
	Fields []ProfileInfoField `json:"fields"`
}

// ProfileInfoEntry is one stored value. Masked values ("•••• 4567") are
// display-only until revealed.
type ProfileInfoEntry struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Masked    bool   `json:"masked"`
	Source    string `json:"source"`
	Origin    string `json:"origin"`
	UpdatedAt string `json:"updated_at"`
}

// ProfileInfoData is `profile info get`.
type ProfileInfoData struct {
	Profile string             `json:"profile"`
	Entries []ProfileInfoEntry `json:"entries"`
}

// ProfileInfoFilled is one field the AI filled.
type ProfileInfoFilled struct {
	Key    string `json:"key"`
	Origin string `json:"origin"`
}

// ProfileInfoAIFillResult is `profile info ai-fill`.
type ProfileInfoAIFillResult struct {
	Asked   int                 `json:"asked"`
	Filled  []ProfileInfoFilled `json:"filled"`
	Skipped int                 `json:"skipped"`
}

// GetProfileInfoFields returns the field catalog.
func (a *App) GetProfileInfoFields() (*ProfileInfoCatalog, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	var out ProfileInfoCatalog
	if err := a.runMonoCLI("", &out, "profile", "info", "fields"); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetProfileInfo returns the stored values with sensitive ones masked.
func (a *App) GetProfileInfo() (*ProfileInfoData, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	var out ProfileInfoData
	if err := a.runMonoCLI("", &out, "profile", "info", "get"); err != nil {
		return nil, err
	}
	return &out, nil
}

// RevealProfileInfo returns one value in full. The caller must not cache it.
func (a *App) RevealProfileInfo(key string) (string, error) {
	if err := a.signedOut(); err != nil {
		return "", err
	}
	var out ProfileInfoData
	if err := a.runMonoCLI("", &out, "profile", "info", "get", key, "--reveal"); err != nil {
		return "", err
	}
	for _, e := range out.Entries {
		if e.Key == key {
			return e.Value, nil
		}
	}
	return "", nil
}

// SetProfileInfo stores value for key (through stdin, so newlines and long
// text survive). An empty value clears the field. The CLI's validation
// message comes back as the error.
func (a *App) SetProfileInfo(key, value string) error {
	if err := a.signedOut(); err != nil {
		return err
	}
	if value == "" {
		return a.ClearProfileInfo(key)
	}
	return a.runMonoCLI(value, nil, "profile", "info", "set", key, "--stdin")
}

// ClearProfileInfo removes the stored value for key.
func (a *App) ClearProfileInfo(key string) error {
	if err := a.signedOut(); err != nil {
		return err
	}
	return a.runMonoCLI("", nil, "profile", "info", "rm", key)
}

// SetProfileInfoPhoto stores the file at path as the photo for key.
func (a *App) SetProfileInfoPhoto(key, path string) error {
	if err := a.signedOut(); err != nil {
		return err
	}
	return a.runMonoCLI("", nil, "profile", "info", "photo", key, path)
}

// GetProfileInfoPhoto returns the stored photo for key as a data URL, or ""
// when there is none. A PDF comes back as a data URL too; the page shows a
// file chip for it.
func (a *App) GetProfileInfoPhoto(key string) (string, error) {
	if err := a.signedOut(); err != nil {
		return "", err
	}
	var out struct {
		Path string `json:"path"`
	}
	if err := a.runMonoCLI("", &out, "profile", "info", "photo-path", key); err != nil {
		return "", err
	}
	if out.Path == "" {
		return "", nil
	}
	st, err := os.Stat(out.Path)
	if err != nil {
		return "", err
	}
	if st.Size() > maxProfileInfoPhotoBytes {
		return "", fmt.Errorf("photo is larger than 10MB")
	}
	b, err := os.ReadFile(out.Path)
	if err != nil {
		return "", err
	}
	ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(out.Path)))
	if ct == "" {
		ct = "application/octet-stream"
	}
	return "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(b), nil
}

// AIFillProfileInfo asks the AI to fill empty fields from the profile's
// documents. Slow (minutes); the CLI never overwrites typed values.
func (a *App) AIFillProfileInfo(runtime, model string) (*ProfileInfoAIFillResult, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	args := []string{"profile", "info", "ai-fill"}
	if runtime != "" {
		args = append(args, "--runtime", runtime)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	var out ProfileInfoAIFillResult
	if err := a.runMonoCLI("", &out, args...); err != nil {
		return nil, err
	}
	return &out, nil
}
