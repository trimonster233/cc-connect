package core

import (
	"fmt"
	"strings"
)

// SessionReply is the unformatted final result shared by interactive turns and
// the local /send API. Collectors supply facts; the engine alone renders them.
type SessionReply struct {
	Content   string `json:"content"`
	Question  string `json:"question,omitempty"`
	WorkDir   string `json:"work_dir,omitempty"`
	Model     string `json:"model,omitempty"`
	SessionID string `json:"session_id"`
}

func questionExcerpt(text string) string {
	if strings.HasPrefix(text, "--- Reply chain (") {
		if _, request, ok := strings.Cut(text, "\n---\n\n"); ok {
			text = request
		}
	} else if strings.HasPrefix(text, "[Quoted message from ") {
		if index := strings.LastIndex(text, "\n\n\n"); index >= 0 {
			text = text[index+3:]
		}
	}
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) > 180 {
		text = string(runes[:180]) + "…"
	}
	return strings.NewReplacer("\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "<", "\\<", ">", "\\>").Replace(text)
}

func (e *Engine) formatSessionReply(reply SessionReply) (string, string) {
	lines := strings.Split(strings.TrimRight(reply.Content, "\r\n \t"), "\n")
	for len(lines) > 0 {
		last := strings.TrimSpace(lines[len(lines)-1])
		if last != "" && strings.Trim(last, "*") != "UUID: "+reply.SessionID {
			break
		}
		lines = lines[:len(lines)-1]
	}
	body := strings.Join(lines, "\n")
	if question := questionExcerpt(reply.Question); question != "" {
		body = "> " + e.i18n.Tf(MsgReplyQuestion, question) + "\n\n" + body
	}
	var footer []string
	if reply.WorkDir != "" {
		footer = append(footer, e.i18n.Tf(MsgReplyDirectory, reply.WorkDir))
	}
	if reply.Model != "" {
		footer = append(footer, e.i18n.Tf(MsgReplyModel, reply.Model))
	}
	if IsSessionUUID(reply.SessionID) {
		footer = append(footer, e.i18n.Tf(MsgSessionUUID, reply.SessionID))
	}
	return body, strings.Join(footer, "\n")
}

func sessionReplyMetadata(agent Agent, session AgentSession, workspaceDir string) SessionReply {
	reply := SessionReply{WorkDir: workspaceDir, Model: replyFooterModel(session, agent)}
	if session != nil {
		reply.SessionID = session.CurrentSessionID()
		if getter, ok := session.(interface{ GetWorkDir() string }); ok && reply.WorkDir == "" {
			reply.WorkDir = getter.GetWorkDir()
		}
	}
	if getter, ok := agent.(interface{ GetWorkDir() string }); ok && reply.WorkDir == "" {
		reply.WorkDir = getter.GetWorkDir()
	}
	return reply
}

// SendSessionReply renders an externally collected result exactly like an
// interactive final reply. It never switches or creates the main conversation.
func (e *Engine) SendSessionReply(sessionKey string, reply SessionReply) error {
	if sessionKey == "" || !IsSessionUUID(reply.SessionID) || strings.TrimSpace(reply.Content) == "" {
		return fmt.Errorf("session reply requires session_key, content, and a valid session_id")
	}
	_, p, replyCtx, err := e.resolveOutboundSessionTarget(sessionKey, false)
	if err != nil {
		return err
	}
	body, footer := e.formatSessionReply(reply)
	if rich, ok := p.(RichCardSupporter); ok {
		if err := e.waitOutgoing(p); err != nil {
			return err
		}
		card := rich.BuildRichCard(CardStatusDone, "", nil, body, false, footer)
		return p.Send(e.ctx, replyCtx, card)
	}
	send := func(p Platform, ctx any, content string) error { return e.sendWithError(p, ctx, content) }
	if !sendChunksWithStatusFooter(e.ctx, p, replyCtx, body, footer, send) {
		return fmt.Errorf("send formatted session reply failed")
	}
	return nil
}
