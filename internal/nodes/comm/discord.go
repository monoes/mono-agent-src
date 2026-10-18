package comm

import (
	"context"
	"fmt"
	"os"

	"github.com/bwmarrin/discordgo"

	"github.com/monoes/mono-agent/internal/workflow"
)

// DiscordNode interacts with the Discord API using a bot token.
// Type: "comm.discord"
//
// Config fields:
//
//	"operation"         (string, required): "send_message" | "send_embed" | "get_channels"
//	"token"             (string, required): Bot token (with or without "Bot " prefix)
//	"channel_id"        (string, required for send_message/send_embed): Discord channel ID
//	"content"           (string): message text (send_message)
//	"embed_title"       (string): embed title (send_embed)
//	"embed_description" (string): embed description (send_embed)
//	"embed_color"       (int): embed color as decimal integer (send_embed)
//	"guild_id"          (string): guild/server ID (get_channels)
type DiscordNode struct{}

func (n *DiscordNode) Type() string { return "comm.discord" }

func (n *DiscordNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	token, _ := config["bot_token"].(string)
	if token == "" {
		token, _ = config["token"].(string)
	}
	if token == "" {
		return nil, fmt.Errorf("comm.discord: bot_token is required")
	}

	operation, _ := config["operation"].(string)
	if operation == "" {
		operation = "send_message"
	}

	// Ensure the "Bot " prefix is present.
	if len(token) < 4 || token[:4] != "Bot " {
		token = "Bot " + token
	}

	dg, err := discordgo.New(token)
	if err != nil {
		return nil, fmt.Errorf("comm.discord: create session: %w", err)
	}

	channelID, _ := config["channel_id"].(string)

	switch operation {
	case "send_message":
		if channelID == "" {
			return nil, fmt.Errorf("comm.discord: channel_id is required for send_message")
		}
		content, _ := config["content"].(string)

		msg, err := dg.ChannelMessageSend(channelID, content)
		if err != nil {
			return nil, fmt.Errorf("comm.discord: send_message: %w", err)
		}
		result := workflow.NewItem(map[string]interface{}{
			"message_id": msg.ID,
			"channel_id": msg.ChannelID,
			"guild_id":   msg.GuildID,
		})
		// A message response does not always include its guild. Channel type
		// distinguishes shared destinations from private direct messages.
		if ch, lookupErr := dg.Channel(channelID, discordgo.WithContext(ctx)); lookupErr == nil {
			result.JSON["channel_type"] = int(ch.Type)
			result.JSON["guild_id"] = ch.GuildID
		} else if msg.GuildID == "" {
			fmt.Fprintln(os.Stderr, "Warning: publication history skipped because Discord channel visibility could not be verified; the message was sent successfully.")
		}
		return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{result}}}, nil

	case "send_embed":
		if channelID == "" {
			return nil, fmt.Errorf("comm.discord: channel_id is required for send_embed")
		}
		embedTitle, _ := config["embed_title"].(string)
		embedDesc, _ := config["embed_description"].(string)
		embedColor := 0
		if c, ok := config["embed_color"]; ok {
			switch v := c.(type) {
			case int:
				embedColor = v
			case float64:
				embedColor = int(v)
			}
		}

		embed := &discordgo.MessageEmbed{
			Title:       embedTitle,
			Description: embedDesc,
			Color:       embedColor,
		}

		msg, err := dg.ChannelMessageSendEmbed(channelID, embed)
		if err != nil {
			return nil, fmt.Errorf("comm.discord: send_embed: %w", err)
		}
		result := workflow.NewItem(map[string]interface{}{
			"message_id": msg.ID,
			"channel_id": msg.ChannelID,
			"guild_id":   msg.GuildID,
		})
		// A message response does not always include its guild. Channel type
		// distinguishes shared destinations from private direct messages.
		if ch, lookupErr := dg.Channel(channelID, discordgo.WithContext(ctx)); lookupErr == nil {
			result.JSON["channel_type"] = int(ch.Type)
			result.JSON["guild_id"] = ch.GuildID
		} else if msg.GuildID == "" {
			fmt.Fprintln(os.Stderr, "Warning: publication history skipped because Discord channel visibility could not be verified; the message was sent successfully.")
		}
		return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{result}}}, nil

	case "get_channels":
		guildID, _ := config["guild_id"].(string)
		if guildID == "" {
			return nil, fmt.Errorf("comm.discord: guild_id is required for get_channels")
		}
		channels, err := dg.GuildChannels(guildID)
		if err != nil {
			return nil, fmt.Errorf("comm.discord: get_channels: %w", err)
		}
		items := make([]workflow.Item, 0, len(channels))
		for _, ch := range channels {
			items = append(items, workflow.NewItem(map[string]interface{}{
				"id":       ch.ID,
				"name":     ch.Name,
				"type":     ch.Type,
				"guild_id": ch.GuildID,
			}))
		}
		return []workflow.NodeOutput{{Handle: "main", Items: items}}, nil

	default:
		return nil, fmt.Errorf("comm.discord: unsupported operation %q", operation)
	}
}
