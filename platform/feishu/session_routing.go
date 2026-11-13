package feishu

import "github.com/chenhg5/cc-connect/core"

func (p *Platform) SetSessionRoutingEnabled(enabled bool) { p.sessionRouting = enabled }

func quotedSessionID(chain []chainMessage, appID string) string {
	// Only our own bot's footer is authoritative. User text, forwarded text,
	// and replies from other applications must never select a backend UUID.
	for i := len(chain) - 1; i >= 0; i-- {
		msg := chain[i]
		if msg.senderType == "app" && appID != "" && msg.senderID == appID {
			if id := core.SessionUUIDFromReply(msg.text); id != "" {
				return id
			}
		}
	}
	return ""
}
