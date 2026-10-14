package telegram

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/browser"

	botpkg "github.com/monoes/mono-agent/internal/bot"
)

// TelegramBot implements botpkg.BotAdapter for Telegram Web.
type TelegramBot struct{}

func init() {
	botpkg.PlatformRegistry["TELEGRAM"] = func() botpkg.BotAdapter {
		return &TelegramBot{}
	}
}

// Platform returns the canonical platform name.
func (b *TelegramBot) Platform() string {
	return "TELEGRAM"
}

// LoginURL returns the Telegram Web login URL.
func (b *TelegramBot) LoginURL() string {
	return "https://web.telegram.org/"
}

// IsLoggedIn checks whether the user is authenticated on Telegram Web by
// looking for elements that appear only after successful login.
func (b *TelegramBot) IsLoggedIn(page browser.PageInterface) (bool, error) {
	selectors := []string{
		// Chat list sidebar present when logged in.
		".chat-list",
		// Left column layout container.
		"div.LeftColumn",
		// Hash-based IM navigation link.
		"a[href='#/im']",
		// Global search input.
		"#telegram-search-input",
		// Chat folders / tabs.
		"div.chat-folders-container",
		// Sidebar header with search.
		"div.sidebar-header",
		// Peer titles in chat list.
		"div.chatlist-chat",
	}

	for _, sel := range selectors {
		has, err := page.Has(sel)
		if err != nil {
			continue
		}
		if has {
			return true, nil
		}
	}

	// Check for authentication screen elements — if present, we are NOT logged in.
	authSelectors := []string{
		"div.auth-image",
		"input[type='tel']",
		"div.phone-wrapper",
		"button.btn-primary:has-text('Next')",
	}
	for _, sel := range authSelectors {
		has, err := page.Has(sel)
		if err != nil {
			continue
		}
		if has {
			return false, nil
		}
	}

	return false, nil
}

// ResolveURL converts a relative Telegram URL to an absolute URL. If the URL
// is already absolute it is returned unchanged.
func (b *TelegramBot) ResolveURL(rawURL string) string {
	if strings.HasPrefix(rawURL, "/") {
		return "https://web.telegram.org" + rawURL
	}
	return rawURL
}

// ExtractUsername parses a Telegram URL and returns the username. Supported
// formats include:
//   - https://t.me/username
//   - https://telegram.me/username
//   - https://web.telegram.org/k/#@username
//   - @username
//   - /username
func (b *TelegramBot) ExtractUsername(pageURL string) string {
	// Handle bare @username references.
	if strings.HasPrefix(pageURL, "@") {
		return strings.TrimPrefix(pageURL, "@")
	}

	parsed, err := url.Parse(pageURL)
	if err != nil {
		return ""
	}

	host := strings.ToLower(parsed.Hostname())

	// Handle t.me and telegram.me direct links.
	if host == "t.me" || host == "telegram.me" {
		trimmed := strings.Trim(parsed.Path, "/")
		if trimmed == "" {
			return ""
		}
		segments := strings.Split(trimmed, "/")
		if len(segments) > 0 && segments[0] != "" {
			// Skip known non-username paths.
			first := segments[0]
			if first == "joinchat" || first == "addstickers" || first == "share" {
				return ""
			}
			return first
		}
		return ""
	}

	// Handle web.telegram.org URLs where username may be in the fragment.
	if host == "web.telegram.org" {
		fragment := parsed.Fragment
		if fragment != "" {
			// Fragment might be @username or #@username or /im?p=@username.
			fragment = strings.TrimPrefix(fragment, "#")
			fragment = strings.TrimPrefix(fragment, "/")
			fragment = strings.TrimPrefix(fragment, "@")
			fragment = strings.Trim(fragment, "/")
			if fragment != "" {
				segments := strings.Split(fragment, "/")
				// The last segment is likely the username, strip leading @.
				for _, seg := range segments {
					seg = strings.TrimPrefix(seg, "@")
					if seg != "" && seg != "im" && seg != "k" {
						return seg
					}
				}
			}
		}

		// Also check path segments for @username.
		trimmed := strings.Trim(parsed.Path, "/")
		segments := strings.Split(trimmed, "/")
		for _, seg := range segments {
			if strings.HasPrefix(seg, "@") {
				return strings.TrimPrefix(seg, "@")
			}
		}

		return ""
	}

	// Handle bare /username paths.
	trimmed := strings.Trim(parsed.Path, "/")
	if trimmed != "" {
		segments := strings.Split(trimmed, "/")
		candidate := strings.TrimPrefix(segments[0], "@")
		if candidate != "" {
			return candidate
		}
	}

	return ""
}

// SearchURL returns the Telegram Web IM page. Telegram does not have a
// dedicated keyword search URL, so we return the main messaging view.
func (b *TelegramBot) SearchURL(keyword string) string {
	return "https://web.telegram.org/#/im"
}

// SendMessage opens a chat with the specified user in Telegram Web and sends
// a message.
func (b *TelegramBot) SendMessage(ctx context.Context, page browser.PageInterface, username, message string) error {
	if username == "" {
		return fmt.Errorf("telegram: username is required")
	}
	if message == "" {
		return fmt.Errorf("telegram: message is required")
	}

	// Navigate to the user's chat using the web.telegram.org hash routing.
	chatURL := fmt.Sprintf("https://web.telegram.org/k/#@%s", url.PathEscape(username))
	err := page.Navigate(chatURL)
	if err != nil {
		return fmt.Errorf("telegram: failed to navigate to chat: %w", err)
	}
	err = page.WaitLoad()
	if err != nil {
		return fmt.Errorf("telegram: chat page did not load: %w", err)
	}
	time.Sleep(3 * time.Second)

	// Look for the message input area.
	inputSelectors := []string{
		"div.input-message-container div[contenteditable='true']",
		"div.chat-input div[contenteditable='true']",
		"div[contenteditable='true'].input-message-input",
	}

	var msgInput browser.ElementHandle
	for _, sel := range inputSelectors {
		el, findErr := page.Element(sel, 5*time.Second)
		if findErr == nil && el != nil {
			msgInput = el
			break
		}
	}

	if msgInput == nil {
		// Fallback: try searching for the user via the sidebar search.
		searchSelectors := []string{
			"#telegram-search-input",
			"input.input-search",
			"div.sidebar-header input[type='text']",
		}

		var searchInput browser.ElementHandle
		for _, sel := range searchSelectors {
			el, findErr := page.Element(sel, 5*time.Second)
			if findErr == nil && el != nil {
				searchInput = el
				break
			}
		}

		if searchInput == nil {
			return fmt.Errorf("telegram: could not find search input or message input")
		}

		err = searchInput.Click()
		if err != nil {
			return fmt.Errorf("telegram: failed to focus search input: %w", err)
		}
		time.Sleep(500 * time.Millisecond)

		err = searchInput.Input(username)
		if err != nil {
			return fmt.Errorf("telegram: failed to type username in search: %w", err)
		}
		time.Sleep(2 * time.Second)

		// Click the first matching result in the search results.
		resultSelectors := []string{
			"div.search-group .chatlist-chat",
			"div.chatlist-container .chatlist-chat",
			"a.chatlist-chat",
		}

		resultClicked := false
		for _, sel := range resultSelectors {
			resultEl, rErr := page.Element(sel, 5*time.Second)
			if rErr == nil && resultEl != nil {
				if clickErr := resultEl.Click(); clickErr == nil {
					resultClicked = true
					break
				}
			}
		}

		if !resultClicked {
			return fmt.Errorf("telegram: could not find user %q in search results", username)
		}
		time.Sleep(2 * time.Second)

		// Re-locate the message input.
		for _, sel := range inputSelectors {
			el, findErr := page.Element(sel, 5*time.Second)
			if findErr == nil && el != nil {
				msgInput = el
				break
			}
		}

		if msgInput == nil {
			return fmt.Errorf("telegram: could not find message input field")
		}
	}

	// Focus and type the message.
	err = msgInput.Click()
	if err != nil {
		return fmt.Errorf("telegram: failed to focus message input: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	err = msgInput.Input(message)
	if err != nil {
		return fmt.Errorf("telegram: failed to type message: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Send the message by clicking the send button.
	sendBtnSelectors := []string{
		"button.send",
		"button.btn-send",
		"button[class*='send']",
		"span.tgico-send",
	}

	sent := false
	for _, sel := range sendBtnSelectors {
		sendBtn, sErr := page.Element(sel, 3*time.Second)
		if sErr == nil && sendBtn != nil {
			if clickErr := sendBtn.Click(); clickErr == nil {
				sent = true
				break
			}
		}
	}

	if !sent {
		// Fallback: press Enter to send.
		err = page.KeyboardPress('\n')
		if err != nil {
			return fmt.Errorf("telegram: failed to send message: %w", err)
		}
	}

	time.Sleep(1 * time.Second)
	return nil
}
