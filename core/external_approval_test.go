package core

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExternalApprovalResponse_AdminAndSessionIsolation(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetAdminFrom("owner")
	pending := &externalApproval{sessionKey: "chat", decision: make(chan string, 1)}
	e.externalApprovals.Store("id", pending)
	for _, msg := range []*Message{{UserID: "other", SessionKey: "chat"}, {UserID: "owner", SessionKey: "other-chat"}} {
		e.handleExternalApprovalResponse(p, msg, "/approval id allow")
		select {
		case <-pending.decision:
			t.Fatal("unauthorized decision accepted")
		default:
		}
	}
	e.handleExternalApprovalResponse(p, &Message{UserID: "owner", SessionKey: "chat"}, "/approval id deny")
	e.handleExternalApprovalResponse(p, &Message{UserID: "owner", SessionKey: "chat"}, "/approval id allow")
	if got := <-pending.decision; got != "deny" {
		t.Fatalf("decision=%s", got)
	}
	select {
	case <-pending.decision:
		t.Fatal("duplicate decision accepted")
	default:
	}
	if !e.handleExternalApprovalResponse(p, &Message{UserID: "owner", SessionKey: "chat"}, "/approval expired allow") {
		t.Fatal("stale approval forwarded to agent")
	}
}

func TestExternalApprovalAPI_RequiresConfiguredAdmin(t *testing.T) {
	e := NewEngine("test", &stubAgent{}, nil, "", LangEnglish)
	s := &APIServer{engines: map[string]*Engine{"test": e}}
	r := httptest.NewRequest(http.MethodPost, "/approval/request", bytes.NewBufferString(`{"project":"test","session_key":"chat","session_id":"01a1d329-4c38-7a00-b363-0d16a8745138","tool_name":"Bash"}`))
	w := httptest.NewRecorder()
	s.handleExternalApproval(w, r)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "admin_from") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
