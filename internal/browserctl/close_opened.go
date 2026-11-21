package browserctl

import (
	"fmt"

	"github.com/monoes/mono-agent/internal/extension"
)

// CloseOpenedTab closes tab id, the session's own tab, which its caller
// opened with OpenTab, wherever its page is now. It closes even after the page
// moved to another origin or while it is still loading, where the guarded
// CloseTab would be refused (origin_changed). Closing reads and acts on
// nothing, so it goes without the origin guard, as OpenTab's own cleanup
// does (sendRaw). Any other tab is refused, and a failed close is returned.
func (s *Session) CloseOpenedTab(id int) error {
	if id == 0 || id != s.tabID {
		return fmt.Errorf("tab %d is not this session's tab (%d): not closed", id, s.tabID)
	}
	if _, err := s.sendRaw(id, extension.CmdCloseTab, nil, DefaultTimeout); err != nil {
		return fmt.Errorf("close tab %d: %w", id, err)
	}
	s.forget(id)
	return nil
}
