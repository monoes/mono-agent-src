package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// The chat an open-ended start is for. Either the id of a chat that exists, or the spec of one the start makes after the
// person said yes (so a declined start leaves no chat). Either way the chat is part of what the one-time token covers and what
// the native dialog names: a token issued for one chat cannot start a run for another.

// chatNameRe is a runtime or effort name; chatModelRe a model id (which may carry a slash or a colon).
var (
	chatNameRe  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,40}$`)
	chatModelRe = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,80}$`)
)

// chatFolderShown is the longest folder the dialog shows whole.
const chatFolderShown = 200

// chatSpec is a chat to be made: the same inputs as CreateChatConversationAccess takes, with the mode fixed to open.
type chatSpec struct {
	Runtime string `json:"runtime"`
	Model   string `json:"model"`
	Effort  string `json:"effort"`
	Access  string `json:"access"`
	Cwd     string `json:"cwd"`
}

// startChat is the parsed chat of a start: key is what the token covers, text what the dialog says, spec set when the chat is
// to be made.
type startChat struct {
	key, text string
	spec      *chatSpec
}

// parseChat takes exactly one of an existing chat's id and a chat spec (JSON).
func parseChat(chatID, specJSON string) (startChat, error) {
	switch {
	case chatID != "" && specJSON != "":
		return startChat{}, errors.New("a start is for one chat: an id or a spec, not both")
	case chatID != "":
		if err := checkChatID(chatID); err != nil {
			return startChat{}, err
		}
		return startChat{key: "chat:" + chatID, text: "the chat " + chatID}, nil
	case specJSON == "":
		return startChat{}, errors.New("a start needs the chat it starts from")
	}
	var sp chatSpec
	dec := json.NewDecoder(bytes.NewReader([]byte(specJSON)))
	dec.DisallowUnknownFields()
	if len(specJSON) > 4096 || !utf8.ValidString(specJSON) || dec.Decode(&sp) != nil || dec.More() {
		return startChat{}, errors.New("the chat to make is not one valid description")
	}
	// What the dialog shows is what is made: every field is plain text as it is (nothing hidden, nothing collapsed), the runtime
	// and the model are names, the access one of the levels above scoped, the folder an absolute clean path short enough to show whole.
	for _, v := range []string{sp.Runtime, sp.Model, sp.Effort, sp.Access, sp.Cwd} {
		if cleanText(v, 1<<16) != v {
			return startChat{}, errors.New("the chat to make has hidden, odd-spaced or control characters in it")
		}
	}
	switch {
	case !chatNameRe.MatchString(sp.Runtime):
		return startChat{}, errors.New("an open-ended chat needs a coder runtime (a plain name)")
	case sp.Model != "" && !chatModelRe.MatchString(sp.Model), sp.Effort != "" && !chatNameRe.MatchString(sp.Effort):
		return startChat{}, errors.New("the model or effort of the chat to make is not a plain name")
	case sp.Access != "coding" && sp.Access != "workspace" && sp.Access != "full":
		return startChat{}, errors.New("an open-ended chat needs an access level above scoped (coding, workspace or full)")
	case sp.Cwd != "" && (!filepath.IsAbs(sp.Cwd) || filepath.Clean(sp.Cwd) != sp.Cwd || utf8.RuneCountInString(sp.Cwd) > chatFolderShown):
		return startChat{}, errors.New("the folder of the chat to make is an absolute, clean path of at most 200 characters")
	}
	norm, _ := json.Marshal(sp)
	folder := sp.Cwd
	if folder == "" {
		folder = "the profile's folder"
	}
	model, effort := sp.Model, sp.Effort
	if model == "" {
		model = "default"
	}
	if effort == "" {
		effort = "default"
	}
	return startChat{key: "new:" + string(norm), spec: &sp,
		text: "a NEW chat on " + sp.Runtime + " (model " + model + ", effort " + effort + ", access " + sp.Access + ", folder " + folder + ")"}, nil
}

// withChat adds the chat the start made to the start document, so the page can bind it.
func withChat(doc string, c *Conversation) string {
	if c == nil {
		return doc
	}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(doc), &m) != nil {
		return doc
	}
	m["chat"], _ = json.Marshal(c)
	b, _ := json.Marshal(m)
	return strings.TrimSpace(string(b))
}

// docState is the state of a start document ("started", "not_started" or "unknown"), "" when it cannot be read.
func docState(doc string) string {
	var d struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal([]byte(doc), &d)
	return d.State
}
