package extension

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
)

// SendCommandTo sends a command to the browser t resolves to and waits for
// its response. If the response reports failure, an error is returned.
func (s *Server) SendCommandTo(t Target, cmd *Command, timeout time.Duration) (*Response, error) {
	if cmd.ID == "" {
		cmd.ID = uuid.New().String()
	}
	// A set_binding is an administrative binding operation: it may name a browser by
	// instance alone, and its profiles are checked against the allowed set below.
	c, err := s.resolveWith(t, cmd.Type == CmdSetBinding)
	if err != nil {
		return nil, err
	}
	// The relay forwards this with only the relay token, so the browser's
	// allowed set is enforced here, where every path to a socket passes.
	if cmd.Type == CmdSetBinding {
		if out := c.outsideAllowed(setBindingNames(cmd.Params)); len(out) > 0 {
			return nil, errNotAllowed(out)
		}
	}

	// An unbind is effective at once: the bridge stops routing the removed
	// profiles to this browser before the extension has stored the change,
	// and puts the old set back if the change fails. Additions wait for the
	// extension's answer.
	var restore func()
	if cmd.Type == CmdSetBinding {
		c.bindMu.Lock()
		defer c.bindMu.Unlock()
		restore = c.narrowBindingParams(cmd.Params)
	}

	ch := make(chan *Response, 1)
	s.pendMu.Lock()
	s.pending[cmd.ID] = ch
	s.pendMu.Unlock()
	defer func() {
		s.pendMu.Lock()
		delete(s.pending, cmd.ID)
		s.pendMu.Unlock()
	}()

	// A set_binding is sent only if its profiles are still allowed at this
	// moment (the owner may have narrowed the set while it waited for bindMu).
	var werr error
	sent := true
	if cmd.Type == CmdSetBinding {
		names := setBindingNames(cmd.Params)
		sent, werr = c.checkedWrite(func() bool { return c.allowedNow(names) }, func() error { return s.writeCommandTo(c, cmd) })
	} else {
		werr = s.deliver(c, t.Profile, cmd)
	}
	if !sent {
		restore()
		return nil, errNotAllowed(setBindingNames(cmd.Params))
	}
	if werr != nil {
		if restore != nil {
			restore()
		}
		return nil, werr
	}
	s.logger.Debug().Str("id", cmd.ID).Str("type", cmd.Type).Str("instance", c.id).Msg("command sent")

	select {
	case resp := <-ch:
		if !resp.Success {
			if restore != nil {
				restore()
			}
			return resp, fmt.Errorf("extension error: %s", resp.Error)
		}
		if cmd.Type == CmdSetBinding {
			// The extension stored it; mirror it here so the very next
			// command routes by it rather than by the auth frame from
			// whenever this socket opened. If the owner narrowed the allowed set
			// while it was in flight, the browser now holds a binding it may not:
			// drop its socket instead of mirroring it.
			if !c.whileAllowed(setBindingNames(cmd.Params), func() { c.applyBindingParams(cmd.Params) }) {
				c.closeUnauthorized("this browser is not allowed to run a profile it was given")
				return nil, errNotAllowed(setBindingNames(cmd.Params))
			}
		}
		return resp, nil
	case <-time.After(timeout):
		// No restore: the change may have been stored, and a browser that
		// is routed too narrowly fails closed (its next binding frame or
		// reconnect puts the truth back).
		return nil, fmt.Errorf("command %s timed out after %s", cmd.Type, timeout)
	}
}

// writeCommand marshals cmd and writes it to the browser t resolves to.
func (s *Server) writeCommand(t Target, cmd *Command) error {
	c, err := s.resolve(t)
	if err != nil {
		return err
	}
	return s.deliver(c, t.Profile, cmd)
}

func (s *Server) writeCommandTo(c *extConn, cmd *Command) error {
	data, err := json.Marshal(cmd)
	if err != nil {
		return fmt.Errorf("marshal command: %w", err)
	}
	if err := c.write(data); err != nil {
		return fmt.Errorf("write command: %w", err)
	}
	return nil
}

// CreateTabFor opens a tab at url in the browser t resolves to.
func (s *Server) CreateTabFor(t Target, url string) (int, error) {
	resp, err := s.SendCommandTo(t, &Command{
		Type:   CmdCreateTab,
		Params: map[string]interface{}{"url": url},
	}, 30*time.Second)
	if err != nil {
		return 0, err
	}
	return parseTabID(resp)
}

// CloseTabFor closes a tab in the browser t resolves to.
func (s *Server) CloseTabFor(t Target, tabID int) error {
	_, err := s.SendCommandTo(t, &Command{Type: CmdCloseTab, TabID: tabID}, 30*time.Second)
	return err
}

// parseTabID reads create_tab's answer.
func parseTabID(resp *Response) (int, error) {
	dataMap, _ := resp.Data.(map[string]interface{})
	if dataMap == nil {
		return 0, fmt.Errorf("create_tab response missing data")
	}
	tabIDRaw, ok := dataMap["tabId"]
	if !ok {
		return 0, fmt.Errorf("create_tab response missing tabId")
	}
	tabID, ok := tabIDRaw.(float64)
	if !ok {
		return 0, fmt.Errorf("tabId is not a number: %T", tabIDRaw)
	}
	return int(tabID), nil
}

// bindingFrame is the extension's kind:"binding" push.
type bindingFrame struct {
	Kind     string   `json:"kind"`
	Profile  string   `json:"profile"`  // first id; all an older extension sends
	Profiles []string `json:"profiles"` // the whole set, extension >= 1.12.0, see SupportsProfileSets
	Label    string   `json:"label"`
}

// serveBinding applies a binding the user changed in the side panel.
func (s *Server) serveBinding(c *extConn, msg []byte) {
	var f bindingFrame
	if err := json.Unmarshal(msg, &f); err != nil {
		s.logger.Warn().Err(err).Msg("invalid binding frame")
		return
	}
	profiles := profilesFromWire(f.Profiles, f.Profile)
	if !c.whileAllowed(profiles, func() { c.setBinding(profiles, f.Label) }) {
		s.logger.Warn().Str("instance", c.id).Msg("binding frame names a profile outside the browser's allowed set: closing it")
		c.closeUnauthorized("this browser is not allowed to run a profile it asked for")
		return
	}
	// Off the read loop: the profile source may take seconds to answer, and
	// this browser's frames must not wait for it.
	go s.dropDeletedBinding(c)
	s.logger.Info().Str("instance", c.id).Strs("profiles", c.boundProfiles()).Msg("browser binding changed")
}

// applyBindingParams mirrors a set_binding the extension accepted. A
// missing label keeps the current one; so does a command naming neither
// profiles nor profile keep the current set.
func (c *extConn) applyBindingParams(p map[string]interface{}) {
	c.mu.Lock()
	label, profiles := c.label, slices.Clone(c.profiles)
	c.mu.Unlock()
	if l, ok := p["label"].(string); ok {
		label = l
	}
	if list, ok := wireStrings(p["profiles"]); ok {
		profiles = list
	} else if one, ok := p["profile"].(string); ok {
		profiles = cleanProfiles([]string{one})
	}
	// An extension before sets keeps only the first id it was sent, so that
	// is all the bridge may believe it runs, whatever the command named.
	if profiles = cleanProfiles(profiles); len(profiles) > 1 && !SupportsProfileSets(c.version) {
		profiles = profiles[:1]
	}
	c.setBinding(profiles, label)
}

// wireStrings reads a JSON array of strings out of a command's params,
// which hold either []string (built in Go) or []interface{} (decoded).
func wireStrings(v any) ([]string, bool) {
	switch list := v.(type) {
	case []string:
		return list, true
	case []interface{}:
		out := make([]string, 0, len(list))
		for _, e := range list {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out, true
	}
	return nil, false
}

// narrowBindingParams applies the removals a set_binding will make (the
// intersection of the current set and the new one) and returns a func that
// puts back those ids that are still absent, leaving any change made in the
// meantime alone. Nothing is removed when the command names no set. The
// caller holds c.bindMu.
func (c *extConn) narrowBindingParams(p map[string]interface{}) func() {
	var next []string
	if list, ok := wireStrings(p["profiles"]); ok {
		next = cleanProfiles(list)
	} else if one, ok := p["profile"].(string); ok {
		next = cleanProfiles([]string{one})
	} else {
		return func() {}
	}
	c.mu.Lock()
	prev := slices.Clone(c.profiles)
	var kept, removed []string
	for _, id := range prev {
		if slices.Contains(next, id) {
			kept = append(kept, id)
		} else {
			removed = append(removed, id)
		}
	}
	if len(removed) > 0 {
		c.profiles = kept
	}
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		// Back in their old places, then whatever else arrived since.
		var merged []string
		for _, id := range prev {
			if slices.Contains(c.profiles, id) || slices.Contains(removed, id) {
				merged = append(merged, id)
			}
		}
		for _, id := range c.profiles {
			if !slices.Contains(merged, id) {
				merged = append(merged, id)
			}
		}
		c.profiles = merged
	}
}
