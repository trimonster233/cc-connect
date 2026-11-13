package codex

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

const sharedTestThread = "11111111-1111-1111-1111-111111111111"

type sharedTestServer struct {
	t                *testing.T
	server           *httptest.Server
	adds             atomic.Int32
	interrupts       atomic.Int32
	deletes          atomic.Int32
	unsubscribes     atomic.Int32
	foreignResponses atomic.Int32
	connections      atomic.Int32
	mu               sync.Mutex
	clientID         string
	resumeParams     []map[string]any
	reconnect        bool
	keepQueued       bool
}

func newSharedTestServer(t *testing.T, reconnect, keepQueued bool) *sharedTestServer {
	f := &sharedTestServer{t: t, reconnect: reconnect, keepQueued: keepQueued}
	upgrader := websocket.Upgrader{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		connection := f.connections.Add(1)
		reply := func(id any, value any) {
			if err := c.WriteJSON(map[string]any{"id": id, "result": value}); err != nil && !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				return
			}
		}
		notify := func(method string, params any) { _ = c.WriteJSON(map[string]any{"method": method, "params": params}) }
		for {
			var req struct {
				ID     any            `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if c.ReadJSON(&req) != nil {
				return
			}
			switch req.Method {
			case "initialize":
				reply(req.ID, map[string]any{})
			case "initialized":
			case "thread/resume":
				f.mu.Lock()
				f.resumeParams = append(f.resumeParams, req.Params)
				f.mu.Unlock()
				reply(req.ID, map[string]any{"cwd": "/tmp", "model": "test-model", "thread": map[string]any{"id": sharedTestThread, "status": map[string]any{"type": "active"}}})
			case "account/rateLimits/read":
				reply(req.ID, map[string]any{})
			case "thread/queue/add":
				f.adds.Add(1)
				client, _ := req.Params["clientUserMessageId"].(string)
				f.mu.Lock()
				f.clientID = client
				f.mu.Unlock()
				reply(req.ID, map[string]any{"queuedSubmission": map[string]any{"id": "our-queue", "clientUserMessageId": client}})
				if f.reconnect && connection == 1 {
					_ = c.Close()
					return
				}
				if f.keepQueued {
					continue
				}
				// An existing terminal turn must not be reported as the reply to
				// the newly queued platform message, even on the same thread.
				notify("turn/started", map[string]any{"threadId": sharedTestThread, "turn": map[string]any{"id": "terminal-turn"}})
				notify("item/completed", map[string]any{"threadId": sharedTestThread, "turnId": "terminal-turn", "item": map[string]any{"id": "foreign-text", "type": "agentMessage", "text": "FOREIGN TERMINAL ANSWER"}})
				_ = c.WriteJSON(map[string]any{"id": 999, "method": "item/tool/call", "params": map[string]any{"threadId": sharedTestThread, "turnId": "terminal-turn", "tool": "existing-tool"}})
				notify("turn/completed", map[string]any{"threadId": sharedTestThread, "turn": map[string]any{"id": "terminal-turn", "status": "completed"}})
				notify("turn/started", map[string]any{"threadId": sharedTestThread, "turn": map[string]any{"id": "our-turn", "items": []any{}}})
				notify("item/started", map[string]any{"threadId": sharedTestThread, "turnId": "our-turn", "item": map[string]any{"id": "our-user", "type": "userMessage", "clientId": client}})
				answer := map[string]any{"threadId": sharedTestThread, "turnId": "our-turn", "item": map[string]any{"id": "our-text", "type": "agentMessage", "text": "OUR SHARED ANSWER"}}
				notify("item/completed", answer)
				notify("item/completed", answer)
				notify("turn/completed", map[string]any{"threadId": sharedTestThread, "turn": map[string]any{"id": "our-turn", "status": "completed"}})
			case "thread/queue/list":
				var entries []any
				if f.keepQueued {
					f.mu.Lock()
					client := f.clientID
					f.mu.Unlock()
					entries = append(entries, map[string]any{"id": "our-queue", "clientUserMessageId": client})
				}
				reply(req.ID, map[string]any{"data": entries})
			case "thread/read":
				reply(req.ID, map[string]any{"thread": map[string]any{"id": sharedTestThread, "status": map[string]any{"type": "active"}}})
			case "thread/turns/list":
				f.mu.Lock()
				client := f.clientID
				f.mu.Unlock()
				reply(req.ID, map[string]any{"data": []any{map[string]any{"id": "our-turn", "status": "completed", "items": []any{
					map[string]any{"id": "our-user", "type": "userMessage", "clientId": client}, map[string]any{"id": "our-text", "type": "agentMessage", "text": "OUR SHARED ANSWER"},
				}}}})
			case "thread/queue/delete":
				f.deletes.Add(1)
				reply(req.ID, map[string]any{})
			case "turn/interrupt":
				f.interrupts.Add(1)
				reply(req.ID, map[string]any{})
			case "thread/unsubscribe":
				f.unsubscribes.Add(1)
				reply(req.ID, map[string]any{})
			case "":
				f.foreignResponses.Add(1)
			default:
				t.Errorf("unexpected shared RPC %s", req.Method)
				reply(req.ID, map[string]any{})
			}
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *sharedTestServer) attach(t *testing.T) *appServerSession {
	t.Helper()
	s, err := newAppServerSession(context.Background(), "/must-not-be-spawned", nil, "ws"+strings.TrimPrefix(f.server.URL, "http"), "/tmp", "", "", "full-auto", sharedTestThread, "", "", nil, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func collectSharedResult(t *testing.T, s *appServerSession) []core.Event {
	t.Helper()
	timer := time.NewTimer(12 * time.Second)
	defer timer.Stop()
	var events []core.Event
	for {
		select {
		case event := <-s.Events():
			events = append(events, event)
			if event.Type == core.EventError {
				t.Fatalf("shared session error: %v", event.Error)
			}
			if event.Type == core.EventResult {
				return events
			}
		case <-timer.C:
			t.Fatalf("missing shared result; events: %+v", events)
		}
	}
}

func TestSharedAppServer_QueuesWithoutSecondWriterAndIgnoresForeignTurn(t *testing.T) {
	f := newSharedTestServer(t, false, false)
	s := f.attach(t)
	if s.cmd != nil {
		t.Fatal("shared attach spawned a new server")
	}
	if err := s.Send("continue shared thread", "message-1", nil, nil); err != nil {
		t.Fatal(err)
	}
	events := collectSharedResult(t, s)
	texts := 0
	for _, event := range events {
		if event.Type == core.EventText {
			texts++
			if event.Content != "OUR SHARED ANSWER" {
				t.Fatalf("foreign or duplicate reply: %+v", event)
			}
		}
	}
	if texts != 1 {
		t.Fatalf("text events: %d", texts)
	}
	if f.adds.Load() != 1 {
		t.Fatal("queued more than once")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, params := range f.resumeParams {
		if len(params) != 2 || params["threadId"] != sharedTestThread || params["excludeTurns"] != true {
			t.Fatalf("attach changed original session settings: %+v", params)
		}
	}
	if f.foreignResponses.Load() != 0 {
		t.Fatal("bridge answered another client's tool call")
	}
}

func TestSharedAppServer_ReconnectRecoversAcceptedInputWithoutResubmitting(t *testing.T) {
	f := newSharedTestServer(t, true, false)
	s := f.attach(t)
	if err := s.Send("run exactly once", "message-reconnect", nil, nil); err != nil {
		t.Fatal(err)
	}
	events := collectSharedResult(t, s)
	texts := 0
	for _, e := range events {
		if e.Type == core.EventText && e.Content == "OUR SHARED ANSWER" {
			texts++
		}
	}
	if texts != 1 || f.adds.Load() != 1 || f.connections.Load() < 2 {
		t.Fatalf("texts=%d adds=%d connections=%d", texts, f.adds.Load(), f.connections.Load())
	}
}

func TestSharedAppServer_StopOnlyDeletesOwnQueueAndCloseLeavesDaemonRunning(t *testing.T) {
	f := newSharedTestServer(t, false, true)
	s := f.attach(t)
	if err := s.Send("queued behind terminal task", "message-stop", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := (&sharedAppServerAgentSession{s}).CancelTurn(); err != nil {
		t.Fatal(err)
	}
	if f.deletes.Load() != 1 || f.interrupts.Load() != 0 {
		t.Fatal("stop interrupted original terminal turn")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if f.unsubscribes.Load() != 0 {
		t.Fatal("disconnect unloaded the shared thread")
	}
}

func TestSharedClientMessageID_IsStableForRedelivery(t *testing.T) {
	a := sharedClientMessageID(sharedTestThread, "message")
	if a != sharedClientMessageID(sharedTestThread, "message") || a == sharedClientMessageID(sharedTestThread, "other") {
		t.Fatal("unstable or colliding deduplication key")
	}
}

func TestSharedAppServer_BridgeMarkerOnlyCoversOwnedTurn(t *testing.T) {
	dir := t.TempDir()
	s := &appServerSession{events: make(chan core.Event, 8), shared: &sharedAppServerTransport{clientID: "our-request", seenItems: map[string]bool{}}, extraEnv: []string{"CC_DATA_DIR=" + dir}}
	s.threadID.Store(sharedTestThread)
	s.handleNotification("item/started", notificationProbe(t, map[string]any{"threadId": sharedTestThread, "turnId": "our-turn", "item": map[string]any{"type": "userMessage", "clientId": "our-request"}}))
	marker := filepath.Join(dir, "bridge-turns", sharedTestThread+".json")
	if _, err := os.Stat(marker); err != nil {
		t.Fatal(err)
	}
	s.handleNotification("turn/completed", notificationProbe(t, map[string]any{"threadId": sharedTestThread, "turn": map[string]any{"id": "foreign-turn", "status": "completed"}}))
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("foreign turn removed marker")
	}
	s.handleNotification("turn/completed", notificationProbe(t, map[string]any{"threadId": sharedTestThread, "turn": map[string]any{"id": "our-turn", "status": "completed"}}))
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("owned turn marker was not removed")
	}
}

type sharedJourneyPlatform struct {
	mu   sync.Mutex
	sent []string
}

func (p *sharedJourneyPlatform) Name() string                    { return "shared-test" }
func (p *sharedJourneyPlatform) Start(core.MessageHandler) error { return nil }
func (p *sharedJourneyPlatform) Stop() error                     { return nil }
func (p *sharedJourneyPlatform) Reply(_ context.Context, _ any, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, text)
	return nil
}
func (p *sharedJourneyPlatform) Send(ctx context.Context, reply any, text string) error {
	return p.Reply(ctx, reply, text)
}

func TestSharedAppServer_EngineKeepsUUIDAndDoesNotReplyWithForeignTurn(t *testing.T) {
	f := newSharedTestServer(t, false, false)
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "2026", "11", "12")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	meta := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"cwd":"/tmp","source":"cli"}}`+"\n", sharedTestThread)
	if err := os.WriteFile(filepath.Join(dir, "rollout-"+sharedTestThread+".jsonl"), []byte(meta), 0600); err != nil {
		t.Fatal(err)
	}
	agent, err := New(map[string]any{"work_dir": "/tmp", "backend": "app_server", "app_server_url": "ws" + strings.TrimPrefix(f.server.URL, "http"), "cmd": "/must-not-spawn", "codex_home": home})
	if err != nil {
		t.Fatal(err)
	}
	p := &sharedJourneyPlatform{}
	e := core.NewEngine("shared-journey", agent, []core.Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), core.LangEnglish)
	e.SetSessionRoutingEnabled(true)
	e.SetAdminFrom("owner")
	t.Cleanup(func() { e.Stop() })
	send := func(text string) {
		e.ReceiveMessage(p, &core.Message{SessionKey: "shared-test:owner", Platform: p.Name(), UserID: "owner", Content: text, ReplyCtx: "ctx", MessageID: text})
	}
	waitForReplies := func(count int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			p.mu.Lock()
			answers := 0
			for _, text := range p.sent {
				if strings.Contains(text, "FOREIGN TERMINAL ANSWER") {
					p.mu.Unlock()
					t.Fatal("foreign turn leaked to platform")
				}
				if strings.Contains(text, "OUR SHARED ANSWER") && strings.Contains(text, "UUID: "+sharedTestThread) {
					answers++
				}
			}
			p.mu.Unlock()
			if answers >= count {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("missing platform-visible shared reply")
	}
	// Three user actions: switch to an existing UUID, send a message while
	// another client owns the thread, then continue the same main session.
	send("/switch " + sharedTestThread)
	send("first shared request")
	waitForReplies(1)
	send("second shared request")
	waitForReplies(2)
	if f.adds.Load() != 2 {
		t.Fatalf("submissions=%d, want 2", f.adds.Load())
	}
}
