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
	c, err := s.resolve(t)
	if err != nil {
		return nil, err
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

	if err := s.writeCommandTo(c, cmd); err != nil {
		return nil, err
	}
	s.logger.Debug().Str("id", cmd.ID).Str("type", cmd.Type).Str("instance", c.id).Msg("command sent")

	select {
	case resp := <-ch:
		if !resp.Success {
			return resp, fmt.Errorf("extension error: %s", resp.Error)
		}
		if cmd.Type == CmdSetBinding {
			// The extension stored it; mirror it here so the very next
			// command routes by it rather than by the auth frame from
			// whenever this socket opened.
			c.applyBindingParams(cmd.Params)
		}
		return resp, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("command %s timed out after %s", cmd.Type, timeout)
	}
}

// writeCommand marshals cmd and writes it to the browser t resolves to.
func (s *Server) writeCommand(t Target, cmd *Command) error {
	c, err := s.resolve(t)
	if err != nil {
		return err
	}
	return s.writeCommandTo(c, cmd)
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
	Profiles []string `json:"profiles"` // the whole set, extension >= 1.8.0
	Label    string   `json:"label"`
}

// serveBinding applies a binding the user changed in the side panel.
func (s *Server) serveBinding(c *extConn, msg []byte) {
	var f bindingFrame
	if err := json.Unmarshal(msg, &f); err != nil {
		s.logger.Warn().Err(err).Msg("invalid binding frame")
		return
	}
	c.setBinding(profilesFromWire(f.Profiles, f.Profile), f.Label)
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
