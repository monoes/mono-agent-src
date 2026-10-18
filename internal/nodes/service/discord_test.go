package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// withDiscordServer starts an httptest server, points discordBaseURL at it
// for the duration of the test, and restores the original value on cleanup.
func withDiscordServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	orig := discordBaseURL
	discordBaseURL = srv.URL
	t.Cleanup(func() { discordBaseURL = orig })
	return srv
}

func discordExecute(t *testing.T, config map[string]interface{}) []workflow.NodeOutput {
	t.Helper()
	n := &DiscordNode{}
	out, err := n.Execute(context.Background(), workflow.NodeInput{}, config)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	return out
}

func discordMainItems(out []workflow.NodeOutput) []workflow.Item {
	for _, o := range out {
		if o.Handle == "main" {
			return o.Items
		}
	}
	return nil
}

func TestDiscordNode_Type(t *testing.T) {
	n := &DiscordNode{}
	if got := n.Type(); got != "service.discord" {
		t.Errorf("Type() = %q, want service.discord", got)
	}
}

func TestDiscordNode_RequiresBotToken(t *testing.T) {
	n := &DiscordNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation": "list_channels",
	})
	if err == nil {
		t.Fatal("expected error when bot_token is missing")
	}
}

func TestDiscordNode_UnknownOperation(t *testing.T) {
	n := &DiscordNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation": "bogus",
		"bot_token": "test-token",
	})
	if err == nil {
		t.Fatal("expected error for unknown operation")
	}
}

func TestDiscordNode_SendMessage(t *testing.T) {
	var seenAuth string
	var gotPath string
	var gotBody map[string]interface{}

	withDiscordServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/channels/123" {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"type": 0, "guild_id": "guild"})
			return
		}
		seenAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":      "111",
			"content": "Hello World",
		})
	})

	out := discordExecute(t, map[string]interface{}{
		"operation":  "send_message",
		"bot_token":  "test-token",
		"channel_id": "123",
		"text":       "Hello World",
	})

	if seenAuth != "Bot test-token" {
		t.Errorf("Authorization header = %q, want %q", seenAuth, "Bot test-token")
	}
	if gotPath != "/channels/123/messages" {
		t.Errorf("path = %q, want /channels/123/messages", gotPath)
	}
	if gotBody["content"] != "Hello World" {
		t.Errorf("content = %v, want Hello World", gotBody["content"])
	}

	items := discordMainItems(out)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].JSON["guild_id"] != "guild" || items[0].JSON["channel_type"] != float64(0) {
		t.Fatalf("missing destination metadata: %+v", items[0].JSON)
	}
	if items[0].JSON["id"] != "111" {
		t.Errorf("id = %v, want 111", items[0].JSON["id"])
	}
}

func TestDiscordNode_SendMessageRequiresChannelIDAndText(t *testing.T) {
	n := &DiscordNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation": "send_message",
		"bot_token": "test-token",
	})
	if err == nil {
		t.Fatal("expected error when channel_id/text are missing")
	}
}

func TestDiscordNode_ListMessages(t *testing.T) {
	var gotPath, gotQuery string
	withDiscordServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{
			{"id": "1", "content": "Msg 1"},
			{"id": "2", "content": "Msg 2"},
		})
	})

	out := discordExecute(t, map[string]interface{}{
		"operation":  "list_messages",
		"bot_token":  "test-token",
		"channel_id": "123",
		"limit":      float64(10),
	})

	if gotPath != "/channels/123/messages" {
		t.Errorf("path = %q, want /channels/123/messages", gotPath)
	}
	if gotQuery != "limit=10" {
		t.Errorf("query = %q, want limit=10", gotQuery)
	}
	items := discordMainItems(out)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].JSON["content"] != "Msg 1" {
		t.Errorf("content = %v, want Msg 1", items[0].JSON["content"])
	}
}

func TestDiscordNode_ListMessagesDefaultsLimit(t *testing.T) {
	var gotQuery string
	withDiscordServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{})
	})

	discordExecute(t, map[string]interface{}{
		"operation":  "list_messages",
		"bot_token":  "test-token",
		"channel_id": "123",
	})

	if gotQuery != "limit=50" {
		t.Errorf("query = %q, want limit=50", gotQuery)
	}
}

func TestDiscordNode_ListMessagesRequiresChannelID(t *testing.T) {
	n := &DiscordNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation": "list_messages",
		"bot_token": "test-token",
	})
	if err == nil {
		t.Fatal("expected error when channel_id is missing")
	}
}

func TestDiscordNode_AddReaction(t *testing.T) {
	var gotPath string
	var gotMethod string
	var gotBodyBytes []byte

	withDiscordServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		gotBodyBytes = buf[:n]
		w.WriteHeader(http.StatusNoContent)
	})

	out := discordExecute(t, map[string]interface{}{
		"operation":  "add_reaction",
		"bot_token":  "test-token",
		"channel_id": "123",
		"message_id": "456",
		"emoji":      "👍",
	})

	if gotMethod != http.MethodPut {
		t.Errorf("expected PUT, got %s", gotMethod)
	}
	wantPath := "/channels/123/messages/456/reactions/👍/@me"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	if len(gotBodyBytes) != 0 {
		t.Errorf("expected empty request body, got %q", gotBodyBytes)
	}

	// 204 No Content should not produce an error and should yield an empty
	// (but non-nil) result item rather than a JSON parse error.
	items := discordMainItems(out)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if len(items[0].JSON) != 0 {
		t.Errorf("expected empty JSON object for 204 response, got %v", items[0].JSON)
	}
}

func TestDiscordNode_AddReactionRequiresFields(t *testing.T) {
	cases := []map[string]interface{}{
		{"operation": "add_reaction", "bot_token": "test-token", "message_id": "456", "emoji": "👍"},
		{"operation": "add_reaction", "bot_token": "test-token", "channel_id": "123", "emoji": "👍"},
		{"operation": "add_reaction", "bot_token": "test-token", "channel_id": "123", "message_id": "456"},
	}
	for _, c := range cases {
		n := &DiscordNode{}
		_, err := n.Execute(context.Background(), workflow.NodeInput{}, c)
		if err == nil {
			t.Errorf("expected error for config %v", c)
		}
	}
}

func TestDiscordNode_ListChannels(t *testing.T) {
	var gotPath string
	withDiscordServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{
			{"id": "1", "name": "general"},
			{"id": "2", "name": "random"},
		})
	})

	out := discordExecute(t, map[string]interface{}{
		"operation": "list_channels",
		"bot_token": "test-token",
		"guild_id":  "999",
	})

	if gotPath != "/guilds/999/channels" {
		t.Errorf("path = %q, want /guilds/999/channels", gotPath)
	}
	items := discordMainItems(out)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].JSON["name"] != "general" {
		t.Errorf("name = %v, want general", items[0].JSON["name"])
	}
}

func TestDiscordNode_ListChannelsRequiresGuildID(t *testing.T) {
	n := &DiscordNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation": "list_channels",
		"bot_token": "test-token",
	})
	if err == nil {
		t.Fatal("expected error when guild_id is missing")
	}
}

func TestDiscordNode_ErrorResponsePropagates(t *testing.T) {
	withDiscordServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"401: Unauthorized"}`))
	})

	n := &DiscordNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":  "list_messages",
		"bot_token":  "bad-token",
		"channel_id": "123",
	})
	if err == nil {
		t.Fatal("expected error on 401 response")
	}
}
