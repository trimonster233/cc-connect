package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type replySyncTestPlatform struct{ *stubPlatformEngine }

func (p *replySyncTestPlatform) ReconstructReplyCtx(string) (any, error) { return "ctx", nil }

func TestFormatSessionReply_OneQuestionAndIdentityFooter(t *testing.T) {
	e := NewEngine("test", &stubAgent{}, nil, "", LangChinese)
	reply := SessionReply{Content: "正文\n\nUUID: " + routingUUIDA + "\n\nUUID: " + routingUUIDA, Question: "--- Reply chain (2 messages) ---\nold content\n---\n\n当前问题\n第二行", WorkDir: "/workspace/project", Model: "actual-model", SessionID: routingUUIDA}
	body, footer := e.formatSessionReply(reply)
	if body != "> 问题：当前问题 第二行\n\n正文" || footer != "actual-model\n/workspace/project\nUUID: "+routingUUIDA {
		t.Fatalf("body=%q footer=%q", body, footer)
	}
	for _, lang := range []Language{LangEnglish, LangChinese, LangTraditionalChinese, LangJapanese, LangSpanish} {
		e.i18n.SetLang(lang)
		body, footer = e.formatSessionReply(reply)
		if !strings.Contains(body, "当前问题") || !strings.Contains(footer, "actual-model") || !strings.Contains(footer, "/workspace/project") {
			t.Fatalf("missing localized identity in %s: %s %s", lang, body, footer)
		}
	}
}

func TestFormatSessionReply_PreservesRequestedUsageFooter(t *testing.T) {
	e := NewEngine("test", &stubAgent{}, nil, "", LangChinese)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	reply := SessionReply{Content: "answer", Model: "gpt-6.1-sol", Effort: "medium", WorkDir: filepath.Join(home, "project", "cc-connect"), SessionID: routingUUIDA,
		Usage: &SessionReplyUsage{InputTokens: 193100, OutputTokens: 272, CacheReadTokens: 189200, UsedTokens: 193372, ContextWindow: 470000}}
	_, footer := e.formatSessionReply(reply)
	want := "gpt-6.1-sol · effort:medium · out 272 · in 193.1k cw 0 cr 189.2k · ctx 41%\n~/project/cc-connect\nUUID: " + routingUUIDA
	if footer != want {
		t.Fatalf("got %q; want %q", footer, want)
	}
	reply.Usage = nil
	_, footer = e.formatSessionReply(reply)
	if strings.Contains(footer, "out ") || strings.Contains(footer, "ctx ") {
		t.Fatal("invented missing usage: " + footer)
	}
}

type statusReplyTestSession struct{ routingTestSession }

func (s *statusReplyTestSession) GetReasoningEffort() string { return "medium" }
func (s *statusReplyTestSession) GetContextUsage() *ContextUsage {
	return &ContextUsage{InputTokens: 193100, OutputTokens: 272, CachedInputTokens: 189200, UsedTokens: 193372, ContextWindow: 470000}
}

func TestSessionReplyMetadata_UsesReportedEffortAndLastInferenceUsage(t *testing.T) {
	s := &statusReplyTestSession{routingTestSession: routingTestSession{id: routingUUIDA}}
	reply := sessionReplyMetadata(&routingTestAgent{}, s, "/project")
	if reply.Effort != "medium" || reply.Usage == nil || reply.Usage.InputTokens != 193100 || reply.Usage.OutputTokens != 272 || reply.Usage.CacheReadTokens != 189200 || reply.Usage.ContextWindow != 470000 {
		t.Fatalf("missing reported metadata: %+v", reply)
	}
}

func TestQuestionExcerpt_StripsQuoteAndLimitsUnicode(t *testing.T) {
	if got := questionExcerpt("[Quoted message from Bot]:\nold\n\n\nnew [question]"); got != `new \[question\]` {
		t.Fatal(got)
	}
	if got := questionExcerpt(strings.Repeat("文", 200)); got != strings.Repeat("文", 180)+"…" {
		t.Fatal(got)
	}
}

func TestHandleSend_SessionReplyRejectsIncompleteOrMixedPayload(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	api := &APIServer{engines: map[string]*Engine{"test": e}}
	for _, req := range []SendRequest{
		{Project: "test", SessionKey: "chat", SessionReply: &SessionReply{Content: "reply", SessionID: "invalid"}},
		{Project: "test", SessionReply: &SessionReply{Content: "reply", SessionID: routingUUIDA}},
		{Project: "test", SessionKey: "chat", Message: "mixed", SessionReply: &SessionReply{Content: "reply", SessionID: routingUUIDA}},
	} {
		b, _ := json.Marshal(req)
		rec := httptest.NewRecorder()
		api.handleSend(rec, httptest.NewRequest(http.MethodPost, "/send", bytes.NewReader(b)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	if len(p.getSent()) != 0 {
		t.Fatal("invalid reply was delivered")
	}
}
