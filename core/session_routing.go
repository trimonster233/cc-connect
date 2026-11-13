package core

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var sessionUUIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var sessionUUIDFooterPattern = regexp.MustCompile(`(?m)^UUID: ([0-9a-fA-F-]{36})\s*$`)

func IsSessionUUID(id string) bool { return sessionUUIDPattern.MatchString(id) }

// SessionUUIDFromReply returns the final footer, never an arbitrary UUID in prose.
// Platforms must additionally verify that the message was sent by this bot.
func SessionUUIDFromReply(text string) string {
	matches := sessionUUIDFooterPattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return ""
	}
	id := matches[len(matches)-1][1]
	if !IsSessionUUID(id) {
		return ""
	}
	return strings.ToLower(id)
}

func (e *Engine) SetSessionRoutingEnabled(enabled bool) {
	e.sessionRouting = enabled
	for _, p := range e.platforms {
		if routing, ok := p.(SessionReplyRoutingEnabler); ok {
			routing.SetSessionRoutingEnabled(enabled)
		}
	}
}

func sessionRoutingKey(base string, session *Session) string {
	return base + ":session:" + session.ID
}

func (e *Engine) uuidFooter(session AgentSession) string {
	if !e.sessionRouting || session == nil {
		return ""
	}
	id := session.CurrentSessionID()
	if !IsSessionUUID(id) {
		return ""
	}
	return e.i18n.Tf(MsgSessionUUID, id)
}

func joinSessionFooter(footer, uuid string) string {
	if uuid == "" {
		return footer
	}
	if footer == "" {
		return uuid
	}
	return footer + "\n" + uuid
}

func withSessionUUID(extra map[string]string, id string) map[string]string {
	if id != "" {
		extra["session_uuid"] = id
	}
	return extra
}

func (e *Engine) resolveGlobalSession(id string) (Agent, *SessionManager, *AgentSessionInfo, error) {
	resolver, ok := e.agent.(GlobalSessionResolver)
	if !ok {
		return nil, nil, nil, fmt.Errorf("%s", e.i18n.T(MsgGlobalSessionUnsupported))
	}
	if !IsSessionUUID(id) {
		return nil, nil, nil, fmt.Errorf("%s", e.i18n.Tf(MsgSwitchNoMatch, id))
	}
	info, err := resolver.ResolveSession(e.ctx, strings.ToLower(id))
	if err != nil {
		return nil, nil, nil, err
	}
	if info == nil || !strings.EqualFold(info.ID, id) || !filepath.IsAbs(info.WorkDir) {
		return nil, nil, nil, fmt.Errorf("%s", e.i18n.Tf(MsgSwitchNoMatch, id))
	}
	dir, err := filepath.EvalSymlinks(info.WorkDir)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolve session directory: %w", err)
	}
	stat, err := os.Stat(dir)
	if err != nil || !stat.IsDir() {
		return nil, nil, nil, fmt.Errorf("%s", e.i18n.Tf(MsgDirInvalidPath, dir))
	}
	if e.multiWorkspace {
		base, err := filepath.EvalSymlinks(e.baseDir)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("resolve workspace base: %w", err)
		}
		rel, err := filepath.Rel(base, dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, nil, nil, fmt.Errorf("%s", e.i18n.Tf(MsgDirInvalidPath, dir))
		}
	}
	info.WorkDir = dir
	// Keep sessions already in the configured directory on their original
	// manager so their busy locks and bridge history survive global switching.
	if wd, ok := e.agent.(interface{ GetWorkDir() string }); ok {
		if current, err := filepath.EvalSymlinks(wd.GetWorkDir()); err == nil && current == dir {
			return e.agent, e.sessions, info, nil
		}
	}
	agent, sessions, err := e.getOrCreateWorkspaceAgent(dir)
	return agent, sessions, info, err
}

func (e *Engine) routingBaseKey(sessionKey, workDir string, sessions *SessionManager) string {
	if sessions == e.sessions {
		return sessionKey
	}
	return workDir + ":" + sessionKey
}

func (e *Engine) routingWorkspace(workDir string) (Agent, *SessionManager, error) {
	if e.sessionRouting {
		if wd, ok := e.agent.(interface{ GetWorkDir() string }); ok {
			if current, err := filepath.EvalSymlinks(wd.GetWorkDir()); err == nil && current == workDir {
				return e.agent, e.sessions, nil
			}
		}
	}
	return e.getOrCreateWorkspaceAgent(workDir)
}

func (e *Engine) commandContextWithWorkspace(p Platform, msg *Message) (Agent, *SessionManager, string, string, error) {
	agent, sessions, key, dir, err := e.commandContextWithWorkspaceBase(p, msg)
	if err == nil && e.sessionRouting {
		key = sessionRoutingKey(e.routingBaseKey(msg.SessionKey, dir, sessions), sessions.GetOrCreateActive(msg.SessionKey))
	}
	return agent, sessions, key, dir, err
}

func (e *Engine) switchGlobalSession(p Platform, msg *Message, id string) {
	if !e.isAdmin(msg.UserID) {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgAdminRequired, "/switch UUID"))
		return
	}
	agent, sessions, info, err := e.resolveGlobalSession(id)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return
	}
	// Explicit switching changes only the default route. A running turn on
	// another session may finish normally; its immutable key keeps it isolated.
	sessions.SwitchToAgentSession(msg.SessionKey, info.ID, agent.Name(), info.Summary)
	e.bindSendWorkDir(msg.SessionKey, info.WorkDir)
	if e.projectState != nil {
		e.projectState.SetSessionWorkDir(msg.SessionKey, info.WorkDir)
		e.projectState.Save()
	}
	e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgGlobalSessionSwitched, info.ID, info.WorkDir))
}

type replySessionRoute struct {
	agent    Agent
	sessions *SessionManager
	session  *Session
	key      string
	workDir  string
}

// Resolve both quoted messages and commands through the same identity and
// workspace checks. Never fall back to the main session on a failed quote.
func (e *Engine) resolveReplySession(p Platform, msg *Message) *replySessionRoute {
	if !e.isAdmin(msg.UserID) {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgAdminRequired, e.i18n.T(MsgQuotedSessionReply)))
		return nil
	}
	if msg.ReplySessionID == "" {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgQuotedSessionUnknown))
		return nil
	}
	agent, sessions, info, err := e.resolveGlobalSession(msg.ReplySessionID)
	if err != nil {
		// Never silently send a failed quote resolution to the main session.
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return nil
	}
	session := sessions.GetOrCreateReplySession(msg.SessionKey, info.ID, agent.Name(), info.Summary)
	key := sessionRoutingKey(e.routingBaseKey(msg.SessionKey, info.WorkDir, sessions), session)
	return &replySessionRoute{agent: agent, sessions: sessions, session: session, key: key, workDir: info.WorkDir}
}

func (e *Engine) handleSessionReply(p Platform, msg *Message) {
	route := e.resolveReplySession(p, msg)
	if route == nil {
		return
	}
	agent, sessions, session, key := route.agent, route.sessions, route.session, route.key
	content, handled := e.handleAsyncUserInput(p, msg, strings.TrimSpace(msg.Content), key, sessions)
	if handled {
		return
	}
	if e.handlePendingPermission(p, msg, content, key) {
		return
	}
	msg.Content = content
	if e.discardStaleUserMessageIfNeeded(key, msg) {
		return
	}
	e.ensureInteractiveStateForQueueing(key, p, msg.ReplyCtx)
	gen, locked := session.TryLock()
	if !locked {
		if e.queueMessageForBusySession(p, msg, key) {
			if next, ok := session.TryLock(); ok {
				go e.drainOrphanedQueue(session, sessions, key, agent, route.workDir, next)
			}
		} else {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgPreviousProcessing))
		}
		return
	}
	session.TouchUserActivity()
	sessions.UpdateUserMeta(msg.SessionKey, msg.UserName, msg.ChatName)
	e.noteUserMessageAccepted(key, msg.UserMessageTimeMs)
	runMessageAccepted(msg)
	go e.processInteractiveMessageWith(p, msg, session, agent, sessions, key, route.workDir, msg.SessionKey, gen)
}

// Directory selection uses a separate manager instead of mutating a cached
// workspace agent. Otherwise a later UUID lookup could resume in the wrong cwd.
func (e *Engine) routingDirApply(agent Agent, userKey string, args []string) (string, string) {
	getter, ok := agent.(interface{ GetWorkDir() string })
	if !ok {
		return e.i18n.T(MsgDirNotSupported), ""
	}
	query := strings.TrimSpace(strings.Join(args, " "))
	reset := strings.EqualFold(query, "reset")
	if reset {
		query = e.baseWorkDir
		if query == "" {
			query = e.currentSendWorkDir()
		}
	} else if index, err := strconv.Atoi(query); err == nil && index > 0 {
		if e.dirHistory == nil {
			return e.i18n.T(MsgDirNoHistory), ""
		}
		query = e.dirHistory.Get(e.name, index)
		if query == "" {
			return e.i18n.Tf(MsgDirInvalidIndex, index), ""
		}
	} else if query == "-" {
		if e.dirHistory == nil {
			return e.i18n.T(MsgDirNoHistory), ""
		}
		query = e.dirHistory.Previous(e.name)
		if query == "" {
			return e.i18n.T(MsgDirNoPrevious), ""
		}
	}
	dir, err := normalizeSendWorkDir(query, getter.GetWorkDir())
	if err != nil {
		return e.i18n.Tf(MsgError, err), ""
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return e.i18n.Tf(MsgDirInvalidPath, query), ""
	}
	stat, err := os.Stat(dir)
	if err != nil || !stat.IsDir() {
		return e.i18n.Tf(MsgDirInvalidPath, dir), ""
	}
	if e.multiWorkspace {
		base, err := filepath.EvalSymlinks(e.baseDir)
		if err != nil {
			return e.i18n.Tf(MsgError, err), ""
		}
		rel, err := filepath.Rel(base, dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return e.i18n.Tf(MsgDirInvalidPath, dir), ""
		}
	}
	_, sessions, err := e.routingWorkspace(dir)
	if err != nil {
		return e.i18n.Tf(MsgError, err), ""
	}
	sessions.NewSession(userKey, "default")
	e.bindSendWorkDir(userKey, dir)
	if e.projectState != nil {
		e.projectState.SetSessionWorkDir(userKey, dir)
		e.projectState.Save()
	}
	if e.dirHistory != nil {
		e.dirHistory.Add(e.name, dir)
	}
	if reset {
		return "", e.i18n.Tf(MsgDirReset, dir)
	}
	return "", e.i18n.Tf(MsgDirChanged, dir)
}
