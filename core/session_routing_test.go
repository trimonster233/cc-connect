package core

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const routingUUIDA = "11111111-1111-1111-1111-111111111111"
const routingUUIDB = "22222222-2222-2222-2222-222222222222"

type routingTestAgent struct {
	name, dir string
	known     map[string]AgentSessionInfo
	blocks    map[string]chan struct{}
}

func (a *routingTestAgent) Name() string          { return a.name }
func (a *routingTestAgent) GetWorkDir() string    { return a.dir }
func (a *routingTestAgent) SetWorkDir(dir string) { a.dir = dir }
func (a *routingTestAgent) Stop() error           { return nil }
func (a *routingTestAgent) ResolveSession(_ context.Context, id string) (*AgentSessionInfo, error) {
	info, ok := a.known[id]
	if !ok {
		return nil, nil
	}
	return &info, nil
}
func (a *routingTestAgent) ListSessions(context.Context) ([]AgentSessionInfo, error) {
	var result []AgentSessionInfo
	for _, info := range a.known {
		if info.WorkDir == a.dir {
			result = append(result, info)
		}
	}
	return result, nil
}
func (a *routingTestAgent) StartSession(_ context.Context, id string) (AgentSession, error) {
	if id == "" {
		id = routingUUIDA
	}
	return &routingTestSession{id: id, dir: a.dir, events: make(chan Event, 16), gate: a.blocks[id], stopped: make(chan struct{})}, nil
}

type routingTestSession struct {
	id, dir string
	events  chan Event
	gate    chan struct{}
	stopped chan struct{}
	once    sync.Once
}

func (s *routingTestSession) CurrentSessionID() string { return s.id }
func (s *routingTestSession) Events() <-chan Event     { return s.events }
func (s *routingTestSession) Alive() bool              { return true }
func (s *routingTestSession) Close() error {
	s.once.Do(func() {
		if s.stopped != nil {
			close(s.stopped)
		}
	})
	return nil
}
func (s *routingTestSession) RespondPermission(string, PermissionResult) error { return nil }
func (s *routingTestSession) Send(prompt, _ string, _ []ImageAttachment, _ []FileAttachment) error {
	go func() {
		if strings.HasPrefix(prompt, "backend-queued") {
			s.events <- Event{Type: EventQueued, SessionID: s.id}
		}
		if s.gate != nil && strings.HasPrefix(prompt, "held-") {
			select {
			case <-s.gate:
			case <-s.stopped:
				return
			}
		}
		s.events <- Event{Type: EventResult, Content: "backend=" + s.id + " directory=" + s.dir + " prompt=" + prompt, SessionID: s.id, Done: true}
	}()
	return nil
}

func TestSessionUUIDFromReply_RequiresFooterAndUsesLast(t *testing.T) {
	if id := SessionUUIDFromReply("quoted UUID " + routingUUIDA); id != "" {
		t.Fatalf("accepted prose: %q", id)
	}
	if id := SessionUUIDFromReply("UUID: " + routingUUIDA + "\nanswer\nUUID: " + routingUUIDB); id != routingUUIDB {
		t.Fatalf("got %q", id)
	}
}

func TestGetOrCreateReplySession_PreservesMainHistoryAndDeduplicates(t *testing.T) {
	sm := NewSessionManager(filepath.Join(t.TempDir(), "sessions.json"))
	main := sm.NewSession("chat", "main")
	main.SetAgentSessionID(routingUUIDA, "test")
	main.AddHistory("user", "main history")
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); sm.GetOrCreateReplySession("chat", routingUUIDB, "test", "side") }()
	}
	wg.Wait()
	if sm.GetOrCreateActive("chat") != main {
		t.Fatal("quoted reply changed main session")
	}
	if len(sm.ListSessions("chat")) != 2 {
		t.Fatal("concurrent replies created duplicate sessions")
	}
	if main.GetHistory(1)[0].Content != "main history" {
		t.Fatal("main history lost")
	}
}

func TestUUIDFooter_ShownWhenDisplayFooterDisabled(t *testing.T) {
	e := NewEngine("test", &stubAgent{}, nil, "", LangEnglish)
	e.SetSessionRoutingEnabled(true)
	e.SetReplyFooterEnabled(false)
	s := &routingTestSession{id: routingUUIDA}
	got := e.composeRichStatusFooter(false, time.Now(), e.agent, s, "")
	if got != "UUID: "+routingUUIDA {
		t.Fatalf("got %q", got)
	}
}

func TestPermissionAndQuestionCards_CarryTargetUUID(t *testing.T) {
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("routing", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetSessionRoutingEnabled(true)
	s := &routingTestSession{id: routingUUIDB}
	e.sendPermissionPrompt(p, "ctx", "permission", "tool", "input", s)
	e.sendAskQuestionPromptForSession(p, "ctx", []UserQuestion{{Question: "choose", Options: []UserQuestionOption{{Label: "option"}}}}, 0, s, "request-id")
	if len(p.sentCards) != 2 {
		t.Fatalf("cards: %d", len(p.sentCards))
	}
	for _, card := range p.sentCards {
		if !strings.Contains(card.RenderText(), "UUID: "+routingUUIDB) {
			t.Fatal("missing UUID in card")
		}
		for _, element := range card.Elements {
			if actions, ok := element.(CardActions); ok {
				for _, button := range actions.Buttons {
					if button.Extra["session_uuid"] != routingUUIDB {
						t.Fatal("callback lost target session")
					}
				}
			}
		}
	}
}

type routingSteerSession struct{ routingTestSession }

func (s *routingSteerSession) SteerUserInput(string) (bool, error) { return true, nil }

func TestAsyncUserInput_QuotedAnswerDoesNotWriteMainHistory(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("routing", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetSessionRoutingEnabled(true)
	main := e.sessions.NewSession("chat", "main")
	main.AddHistory("user", "original")
	side := e.sessions.GetOrCreateReplySession("chat", routingUUIDB, "test", "side")
	e.interactiveStates["side-key"] = &interactiveState{agentSession: &routingSteerSession{routingTestSession: routingTestSession{id: routingUUIDB}}, busySession: side, pendingUserInput: &pendingUserInput{id: "question", questions: []UserQuestion{{Question: "which"}}, answers: make([]string, 1)}}
	_, handled := e.handleAsyncUserInput(p, &Message{SessionKey: "chat", ReplyCtx: "ctx"}, "answer", "side-key", e.sessions)
	if !handled || e.sessions.GetOrCreateActive("chat") != main || len(main.GetHistory(10)) != 1 || len(side.GetHistory(10)) != 1 {
		t.Fatal("quoted answer changed main session/history")
	}
}

func TestSessionWorkDir_PersistsWithoutChangingProjectDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := NewProjectStateStore(path)
	s.SetWorkDirOverride("original")
	s.SetSessionWorkDir("user-a", "/a")
	s.SetSessionWorkDir("user-b", "/b")
	s.Save()
	loaded := NewProjectStateStore(path)
	if loaded.SessionWorkDir("user-a") != "/a" || loaded.SessionWorkDir("user-b") != "/b" || loaded.WorkDirOverride() != "original" {
		t.Fatal("routing state not preserved")
	}
}

// Test routing through the real Engine and SessionManager, mocking only the
// external process and platform. Assertions below are all platform-visible.
func newRoutingJourney(t *testing.T) (*Engine, *stubPlatformEngine, func(string, string), string, string) {
	t.Helper()
	dirA, dirB := t.TempDir(), t.TempDir()
	known := map[string]AgentSessionInfo{routingUUIDA: {ID: routingUUIDA, WorkDir: dirA, Summary: "A"}, routingUUIDB: {ID: routingUUIDB, WorkDir: dirB, Summary: "B"}}
	blocks := map[string]chan struct{}{}
	name := "routing-" + strings.ReplaceAll(t.Name(), "/", "-")
	RegisterAgent(name, func(opts map[string]any) (Agent, error) {
		dir, _ := opts["work_dir"].(string)
		return &routingTestAgent{name: name, dir: dir, known: known, blocks: blocks}, nil
	})
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("routing", &routingTestAgent{name: name, dir: dirA, known: known, blocks: blocks}, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	e.SetProjectStateStore(NewProjectStateStore(filepath.Join(t.TempDir(), "state.json")))
	e.SetSessionRoutingEnabled(true)
	e.SetAdminFrom("owner")
	t.Cleanup(func() { e.Stop() })
	send := func(content, quote string) {
		e.ReceiveMessage(p, &Message{SessionKey: "test:owner", Platform: "test", UserID: "owner", Content: content, ReplySessionID: quote, MessageID: time.Now().String(), ReplyCtx: "ctx"})
	}
	return e, p, send, dirA, dirB
}

func awaitRoutingReply(t *testing.T, p *stubPlatformEngine, needle string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range p.getSent() {
			if strings.Contains(s, needle) {
				return s
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no visible reply %q; sent %v", needle, p.getSent())
	return ""
}
