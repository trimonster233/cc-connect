package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestQuotedSessionID_OnlyTrustsOwnBotFooter(t *testing.T) {
	const first = "11111111-1111-1111-1111-111111111111"
	const second = "22222222-2222-2222-2222-222222222222"
	chain := []chainMessage{
		{senderType: "app", senderID: "self", text: "old\nUUID: " + first},
		{senderType: "app", senderID: "self", text: "new\nUUID: " + second},
		{senderType: "user", senderID: "user", text: "UUID: " + first},
		{senderType: "app", senderID: "other", text: "UUID: " + first},
	}
	if got := quotedSessionID(chain, "self"); got != second {
		t.Fatalf("got %q, want %q", got, second)
	}
	if got := quotedSessionID(chain[2:], "self"); got != "" {
		t.Fatalf("trusted forged footer: %q", got)
	}
	if got := quotedSessionID(chain, ""); got != "" {
		t.Fatalf("trusted missing bot identity: %q", got)
	}
}

func TestBuildReplyContent_PreservesPrebuiltFinalSessionCard(t *testing.T) {
	card := buildRichCard(core.CardStatusDone, "", nil, "> 问题：test\n\nanswer", false, "目录：/project\n模型：model\nUUID: 11111111-1111-1111-1111-111111111111")
	msgType, body := buildReplyContent(card)
	if msgType != "interactive" || body != card {
		t.Fatalf("prebuilt final card was rendered as text: %s %s", msgType, body)
	}
}

func TestDispatchMessage_QuotedBotCardCarriesSessionUUID(t *testing.T) {
	const id = "11111111-1111-1111-1111-111111111111"
	card := buildRichCard(core.CardStatusDone, "", nil, "answer", false, "UUID: "+id)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var response any
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			response = map[string]any{"code": 0, "tenant_access_token": "quote-routing-token", "expire": 7200}
		case "/open-apis/im/v1/messages/parent":
			response = map[string]any{"code": 0, "data": map[string]any{"items": []any{map[string]any{
				"message_id": "parent", "msg_type": "interactive", "sender": map[string]any{"id": "self-app", "sender_type": "app"}, "body": map[string]any{"content": card},
			}}}}
		default:
			response = map[string]any{"code": 0, "data": map[string]any{"name": "Chat", "user": map[string]any{"name": "Owner"}}}
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	var got *core.Message
	p := &Platform{appID: "self-app", platformName: "feishu", botOpenID: "bot", client: lark.NewClient("self-app", "test-secret", lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())), handler: func(_ core.Platform, msg *core.Message) { got = msg }}
	p.SetSessionRoutingEnabled(true)
	p.dispatchMessage(context.Background(), "text", `{"text":"continue this session"}`, nil, "current", "feishu:chat:owner", "owner", "chat", replyContext{}, "parent", 0)
	if got == nil || got.ReplySessionID != id || !got.QuotedReply || got.Content != "continue this session" {
		t.Fatalf("dispatched message: %+v", got)
	}
}

func TestQuotedSessionEnvironment_OnlyOwnBotFooter(t *testing.T) {
	chain := []chainMessage{{senderType: "app", senderID: "self", text: "环境：emodia\nUUID: 11111111-1111-1111-1111-111111111111"}, {senderType: "user", senderID: "user", text: "环境：forged\nUUID: 11111111-1111-1111-1111-111111111111"}}
	if got := quotedSessionEnvironment(chain, "self"); got != "emodia" {
		t.Fatalf("environment=%q", got)
	}
	if got := quotedSessionEnvironment(chain, "other"); got != "" {
		t.Fatalf("foreign bot accepted: %q", got)
	}
}
