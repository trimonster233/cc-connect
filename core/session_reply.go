package core

import (
	"fmt"
	"strings"
)

// SessionReply is the unformatted final result shared by interactive turns and
// the local /send API. Collectors supply facts; the engine alone renders them.
type SessionReply struct {
	Environment string             `json:"environment,omitempty"`
	Content     string             `json:"content"`
	Question    string             `json:"question,omitempty"`
	WorkDir     string             `json:"work_dir,omitempty"`
	Model       string             `json:"model,omitempty"`
	SessionID   string             `json:"session_id"`
	Effort      string             `json:"effort,omitempty"`
	Usage       *SessionReplyUsage `json:"usage,omitempty"`
}

// SessionReplyUsage carries reported token facts across the local sync API.
// Both collectors use the latest inference's usage, not lifetime thread totals.
type SessionReplyUsage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
	UsedTokens       int `json:"used_tokens"`
	ContextWindow    int `json:"context_window"`
}

func sessionReplyStatusLine(reply SessionReply) string {
	var parts []string
	if reply.Model != "" {
		parts = append(parts, reply.Model)
	}
	if reply.Effort != "" {
		parts = append(parts, "effort:"+reply.Effort)
	}
	if usage := reply.Usage; usage != nil {
		parts = append(parts, "out "+formatStatusTokenCount(usage.OutputTokens))
		parts = append(parts, fmt.Sprintf("in %s cw %s cr %s", formatStatusTokenCount(usage.InputTokens), formatStatusTokenCount(usage.CacheWriteTokens), formatStatusTokenCount(usage.CacheReadTokens)))
		used := usage.UsedTokens
		if used <= 0 {
			used = usage.InputTokens + usage.OutputTokens
		}
		if usage.ContextWindow > 0 && used > 0 {
			pct := min(100, used*100/usage.ContextWindow)
			parts = append(parts, fmt.Sprintf("ctx %d%%", pct))
		}
	}
	return strings.Join(parts, " · ")
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
	strippedUUID := false
	for len(lines) > 0 {
		last := strings.TrimSpace(lines[len(lines)-1])
		if last != "" && strings.Trim(last, "*") != "UUID: "+reply.SessionID {
			break
		}
		if last != "" {
			strippedUUID = true
		}
		lines = lines[:len(lines)-1]
	}
	// Older replies may be repeated verbatim by the model or an old collector.
	// Remove only an exact legacy footer for this same UUID and metadata;
	// leave ordinary prose, code examples and other sessions untouched.
	if strippedUUID && len(lines) >= 2 && reply.WorkDir != "" && reply.Model != "" &&
		strings.TrimSpace(lines[len(lines)-2]) == "目录："+reply.WorkDir &&
		strings.TrimSpace(lines[len(lines)-1]) == "模型："+reply.Model {
		lines = lines[:len(lines)-2]
	}
	body := strings.TrimRight(strings.Join(lines, "\n"), "\r\n \t")
	if question := questionExcerpt(reply.Question); question != "" {
		body = "> " + e.i18n.Tf(MsgReplyQuestion, question) + "\n\n" + body
	}
	var footer []string
	if status := sessionReplyStatusLine(reply); status != "" {
		footer = append(footer, status)
	}
	if reply.WorkDir != "" {
		footer = append(footer, compactReplyFooterPath(reply.WorkDir))
	}
	if reply.Environment != "" {
		label := reply.Environment
		if label == "local" {
			label = e.i18n.T(MsgEnvironmentLocal)
		}
		footer = append(footer, e.i18n.Tf(MsgEnvironmentFooter, label))
	}
	if IsSessionUUID(reply.SessionID) {
		footer = append(footer, e.i18n.Tf(MsgSessionUUID, reply.SessionID))
	}
	return body, strings.Join(footer, "\n")
}

func sessionReplyMetadata(agent Agent, session AgentSession, workspaceDir string) SessionReply {
	reply := SessionReply{WorkDir: workspaceDir, Model: replyFooterModel(session, agent), Effort: replyFooterReasoningEffort(session, agent)}
	reply.Environment = "local"
	if source, ok := agent.(ExecutionEnvironment); ok {
		reply.Environment = source.ExecutionEnvironment()
	}
	if usage := replyFooterSessionContextUsage(session); usage != nil {
		used := usage.UsedTokens
		if used <= 0 {
			used = usage.TotalTokens
		}
		reply.Usage = &SessionReplyUsage{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, CacheWriteTokens: usage.CacheCreationInputTokens, CacheReadTokens: usage.CachedInputTokens, UsedTokens: used, ContextWindow: usage.ContextWindow}
	}
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
	if reply.Environment == "" {
		reply.Environment = "local"
	}
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

// SessionEnvironmentFromReply reads the last environment footer, never guesses
// from a working directory. Callers must verify the quoted bot's identity.
func SessionEnvironmentFromReply(text string) string {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(strings.Trim(lines[i], "*"))
		for _, prefix := range []string{"Environment:", "环境：", "環境：", "Entorno:"} {
			if !strings.HasPrefix(line, prefix) {
				continue
			}
			value := strings.TrimSpace(strings.TrimPrefix(line, prefix))
			switch value {
			case "Local", "本机", "本機", "ローカル":
				return "local"
			}
			fields := strings.Fields(value)
			if len(fields) == 0 {
				return ""
			}
			name := fields[0]
			for _, r := range name {
				if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
					return ""
				}
			}
			return name
		}
	}
	return ""
}
