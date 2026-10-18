package comm

import (
	"context"
	"fmt"
	"os"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/monoes/mono-agent/internal/fsconfine"
	"github.com/monoes/mono-agent/internal/workflow"
)

// TelegramNode sends messages and interacts with the Telegram Bot API.
// Type: "comm.telegram"
//
// Config fields:
//
//	"operation"  (string, required): "send_message" | "send_photo" | "get_updates"
//	"token"      (string, required): Bot API token
//	"chat_id"    (interface{}, required): chat ID (int64 or string username)
//	"text"       (string): message text (send_message, send_photo caption)
//	"photo_url"  (string): URL or local file path for photo (send_photo)
//	"parse_mode" (string): "HTML" (default) | "Markdown"
type TelegramNode struct{}

func (n *TelegramNode) Type() string { return "comm.telegram" }

func (n *TelegramNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	token, _ := config["bot_token"].(string)
	if token == "" {
		token, _ = config["token"].(string)
	}
	if token == "" {
		return nil, fmt.Errorf("comm.telegram: bot_token is required")
	}

	operation, _ := config["operation"].(string)
	if operation == "" {
		operation = "send_message"
	}

	// C-46: a local photo is a file sent away; confine it to the org workdir
	// before the first network call. Unconfined, photoPath is photo_url.
	photoPath, _ := config["photo_url"].(string)
	if operation == "send_photo" && photoPath != "" {
		var err error
		if photoPath, err = fsconfine.Path(ctx, photoPath); err != nil {
			return nil, fmt.Errorf("comm.telegram: send_photo: %w", err)
		}
	}

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("comm.telegram: create bot: %w", err)
	}

	parseMode := "HTML"
	if pm, ok := config["parse_mode"].(string); ok && pm != "" {
		if pm == "plain" {
			pm = ""
		}
		parseMode = pm
	}

	chatID, channelUsername, err := resolveTelegramChatID(config["chat_id"])
	if err != nil && operation != "get_updates" {
		return nil, fmt.Errorf("comm.telegram: %w", err)
	}

	switch operation {
	case "send_message":
		text := telegramText(config)
		msg := tgbotapi.NewMessage(chatID, text)
		msg.ChannelUsername = channelUsername
		msg.ParseMode = parseMode

		sent, err := bot.Send(msg)
		if err != nil {
			return nil, fmt.Errorf("comm.telegram: send_message: %w", err)
		}
		result := workflow.NewItem(map[string]interface{}{
			"message_id": sent.MessageID,
			"chat_id":    sent.Chat.ID,
			"chat_type":  sent.Chat.Type,
		})
		return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{result}}}, nil

	case "send_photo":
		photoURL, _ := config["photo_url"].(string)
		if photoURL == "" {
			return nil, fmt.Errorf("comm.telegram: photo_url is required for send_photo")
		}
		text := telegramText(config)

		var fileData tgbotapi.RequestFileData
		if _, err := os.Stat(photoPath); err == nil {
			// Local file.
			fileData = tgbotapi.FilePath(photoPath)
		} else {
			// Treat as URL.
			fileData = tgbotapi.FileURL(photoURL)
		}

		photo := tgbotapi.NewPhoto(chatID, fileData)
		photo.ChannelUsername = channelUsername
		photo.Caption = text
		photo.ParseMode = parseMode

		sent, err := bot.Send(photo)
		if err != nil {
			return nil, fmt.Errorf("comm.telegram: send_photo: %w", err)
		}
		result := workflow.NewItem(map[string]interface{}{
			"message_id": sent.MessageID,
			"chat_id":    sent.Chat.ID,
			"chat_type":  sent.Chat.Type,
		})
		return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{result}}}, nil

	case "get_updates":
		updates, err := bot.GetUpdates(tgbotapi.UpdateConfig{
			Offset:  0,
			Limit:   100,
			Timeout: 0,
		})
		if err != nil {
			return nil, fmt.Errorf("comm.telegram: get_updates: %w", err)
		}
		items := make([]workflow.Item, 0, len(updates))
		for _, u := range updates {
			item := map[string]interface{}{
				"update_id": u.UpdateID,
			}
			if u.Message != nil {
				item["message_id"] = u.Message.MessageID
				item["chat_id"] = u.Message.Chat.ID
				item["text"] = u.Message.Text
				if u.Message.From != nil {
					item["from"] = u.Message.From.UserName
				}
			}
			items = append(items, workflow.NewItem(item))
		}
		return []workflow.NodeOutput{{Handle: "main", Items: items}}, nil

	default:
		return nil, fmt.Errorf("comm.telegram: unsupported operation %q", operation)
	}
}

// telegramText reads the message body from config, accepting either the
// schema's "message" key or the doc comment's legacy "text" key.
func telegramText(config map[string]interface{}) string {
	if text, ok := config["message"].(string); ok {
		return text
	}
	text, _ := config["text"].(string)
	return text
}

// resolveTelegramChatID coerces the chat_id config value to either a numeric
// Telegram chat ID or a "@channel_username" string, as documented by the
// comm.telegram.json schema's chat_id help text.
func resolveTelegramChatID(v interface{}) (chatID int64, channelUsername string, err error) {
	if v == nil {
		return 0, "", fmt.Errorf("chat_id is required")
	}
	switch val := v.(type) {
	case int64:
		return val, "", nil
	case int:
		return int64(val), "", nil
	case float64:
		return int64(val), "", nil
	case string:
		if strings.HasPrefix(val, "@") {
			return 0, val, nil
		}
		var id int64
		if _, err := fmt.Sscanf(val, "%d", &id); err != nil {
			return 0, "", fmt.Errorf("chat_id %q is not a valid integer ID or @channel_username", val)
		}
		return id, "", nil
	}
	return 0, "", fmt.Errorf("chat_id must be an integer or string, got %T", v)
}
