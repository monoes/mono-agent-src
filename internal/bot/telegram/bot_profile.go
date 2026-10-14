package telegram

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/browser"
)

// GetProfileData scrapes profile information from the currently open Telegram
// Web chat or profile view.
func (b *TelegramBot) GetProfileData(ctx context.Context, page browser.PageInterface) (map[string]interface{}, error) {
	data := make(map[string]interface{})

	err := page.WaitLoad()
	if err != nil {
		return data, fmt.Errorf("telegram: page did not finish loading: %w", err)
	}
	time.Sleep(2 * time.Second)

	pageURL := func() string { u, _ := page.GetURL(); return u }()
	data["username"] = b.ExtractUsername(pageURL)
	data["profile_url"] = pageURL

	// Try to open the profile panel by clicking the chat header.
	headerSelectors := []string{
		"div.chat-info",
		"div.chat-info-container",
		"div.person",
	}
	for _, sel := range headerSelectors {
		el, findErr := page.Element(sel, 3*time.Second)
		if findErr == nil && el != nil {
			_ = el.Click()
			break
		}
	}
	time.Sleep(2 * time.Second)

	// Display name.
	nameSelectors := []string{
		"div.profile-name span.peer-title",
		"div.chat-info span.peer-title",
		"div.sidebar-right span.peer-title",
		"div.profile-content span.peer-title",
	}
	for _, sel := range nameSelectors {
		el, findErr := page.Element(sel, 3*time.Second)
		if findErr == nil && el != nil {
			text, tErr := el.Text()
			if tErr == nil && strings.TrimSpace(text) != "" {
				data["full_name"] = strings.TrimSpace(text)
				break
			}
		}
	}

	// Username (from the profile panel).
	usernameSelectors := []string{
		"div.profile-row div.row-title:has-text('@')",
		"div.sidebar-right span:has-text('@')",
		"div.profile-content span:has-text('@')",
	}
	for _, sel := range usernameSelectors {
		el, findErr := page.Element(sel, 3*time.Second)
		if findErr == nil && el != nil {
			text, tErr := el.Text()
			if tErr == nil && strings.TrimSpace(text) != "" {
				clean := strings.TrimPrefix(strings.TrimSpace(text), "@")
				if clean != "" {
					data["telegram_username"] = clean
				}
				break
			}
		}
	}

	// Phone number.
	phoneSelectors := []string{
		"div.profile-row div.row-title[dir='auto']",
		"div.profile-row span[class*='phone']",
	}
	for _, sel := range phoneSelectors {
		el, findErr := page.Element(sel, 3*time.Second)
		if findErr == nil && el != nil {
			text, tErr := el.Text()
			if tErr == nil && strings.TrimSpace(text) != "" {
				trimmed := strings.TrimSpace(text)
				// Simple heuristic: if it starts with + or contains only digits and spaces,
				// it is likely a phone number.
				if strings.HasPrefix(trimmed, "+") || isPhoneNumber(trimmed) {
					data["phone"] = trimmed
					break
				}
			}
		}
	}

	// Bio / description.
	bioSelectors := []string{
		"div.profile-row.profile-row-bio div.row-title",
		"div.profile-row div.row-subtitle:first-child + div.row-title",
	}
	for _, sel := range bioSelectors {
		el, findErr := page.Element(sel, 3*time.Second)
		if findErr == nil && el != nil {
			text, tErr := el.Text()
			if tErr == nil && strings.TrimSpace(text) != "" {
				data["bio"] = strings.TrimSpace(text)
				break
			}
		}
	}

	// Profile picture URL.
	imgSelectors := []string{
		"div.profile-content img.avatar-photo",
		"div.chat-info img.avatar-photo",
		"div.sidebar-right img.avatar-photo",
		"img.avatar-photo",
	}
	for _, sel := range imgSelectors {
		el, findErr := page.Element(sel, 3*time.Second)
		if findErr == nil && el != nil {
			src, aErr := el.Attribute("src")
			if aErr == nil && src != nil && *src != "" {
				data["profile_picture_url"] = *src
				break
			}
		}
	}

	// Online status / last seen.
	statusSelectors := []string{
		"div.profile-subtitle",
		"div.chat-info-container span.info",
		"div.sidebar-right span.info",
	}
	for _, sel := range statusSelectors {
		el, findErr := page.Element(sel, 2*time.Second)
		if findErr == nil && el != nil {
			text, tErr := el.Text()
			if tErr == nil && strings.TrimSpace(text) != "" {
				data["status"] = strings.TrimSpace(text)
				break
			}
		}
	}

	// Member count (for groups/channels).
	memberSelectors := []string{
		"div.profile-subtitle span.online",
		"div.chat-info span.members",
	}
	for _, sel := range memberSelectors {
		el, findErr := page.Element(sel, 2*time.Second)
		if findErr == nil && el != nil {
			text, tErr := el.Text()
			if tErr == nil && strings.TrimSpace(text) != "" {
				data["member_count"] = strings.TrimSpace(text)
				break
			}
		}
	}

	return data, nil
}

// isPhoneNumber performs a simple heuristic check to determine if a string
// looks like a phone number (digits, spaces, dashes, parentheses, plus sign).
func isPhoneNumber(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r >= '0' && r <= '9' {
			continue
		}
		if r == ' ' || r == '-' || r == '(' || r == ')' || r == '+' {
			continue
		}
		return false
	}
	return true
}
