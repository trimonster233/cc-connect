package core

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// External approvals exist only while their originating local hook is connected.
type externalApproval struct {
	sessionKey string
	decision   chan string
	once       sync.Once
}

func (s *APIServer) handleExternalApproval(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", 405)
		return
	}
	var req struct {
		Project    string          `json:"project"`
		SessionKey string          `json:"session_key"`
		SessionID  string          `json:"session_id"`
		Tool       string          `json:"tool_name"`
		Input      json.RawMessage `json:"tool_input"`
		CWD        string          `json:"cwd"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&req); err != nil || !IsSessionUUID(req.SessionID) || req.SessionKey == "" || req.Tool == "" {
		http.Error(w, "invalid approval", 400)
		return
	}
	s.mu.RLock()
	e := s.engines[req.Project]
	s.mu.RUnlock()
	if e == nil {
		http.Error(w, "unknown project", 404)
		return
	}
	e.userRolesMu.RLock()
	configured := e.adminFrom != ""
	e.userRolesMu.RUnlock()
	if !configured {
		http.Error(w, "admin_from required", 403)
		return
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		http.Error(w, "generate approval ID", 500)
		return
	}
	id := hex.EncodeToString(nonce[:])
	pending := &externalApproval{sessionKey: req.SessionKey, decision: make(chan string, 1)}
	e.externalApprovals.Store(id, pending)
	defer e.externalApprovals.Delete(id)
	message := fmt.Sprintf("%s\n%s\n%s\nUUID: %s\n\n/approval %s allow\n/approval %s deny", req.Tool, string(req.Input), req.CWD, req.SessionID, id, id)
	_, platform, replyCtx, err := e.resolveOutboundSessionTarget(req.SessionKey, false)
	if err == nil {
		if sender, ok := platform.(CardSender); ok {
			card := &Card{Elements: []CardElement{CardMarkdown{Content: message}, CardActions{Buttons: []CardButton{Btn(e.i18n.T(MsgPermissionAllowed), "primary", "cmd:/approval "+id+" allow"), Btn(e.i18n.T(MsgPermissionDenied), "danger", "cmd:/approval "+id+" deny")}}}}
			err = sender.SendCard(r.Context(), replyCtx, card)
		} else {
			err = e.SendToSession(req.SessionKey, message)
		}
	}
	if err != nil {
		slog.Warn("external approval: send failed", "error", err)
		http.Error(w, "send approval failed", 502)
		return
	}
	timer := time.NewTimer(4 * time.Minute)
	defer timer.Stop()
	select {
	case decision := <-pending.decision:
		apiJSON(w, 200, map[string]string{"behavior": decision})
	case <-r.Context().Done():
		return
	case <-timer.C:
		apiJSON(w, 200, map[string]string{"behavior": "deny"})
	}
}

func (e *Engine) handleExternalApprovalResponse(p Platform, msg *Message, content string) bool {
	fields := strings.Fields(content)
	if len(fields) == 0 || fields[0] != "/approval" {
		return false
	}
	if !e.isAdmin(msg.UserID) {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgAdminRequired, "/approval"))
		return true
	}
	if len(fields) != 3 || (fields[2] != "allow" && fields[2] != "deny") {
		return true
	}
	value, ok := e.externalApprovals.Load(fields[1])
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgPermissionDenied))
		return true
	}
	pending := value.(*externalApproval)
	if pending.sessionKey != msg.SessionKey {
		return true
	}
	pending.once.Do(func() {
		pending.decision <- fields[2]
		key := MsgPermissionDenied
		if fields[2] == "allow" {
			key = MsgPermissionAllowed
		}
		e.reply(p, msg.ReplyCtx, e.i18n.T(key))
	})
	return true
}
