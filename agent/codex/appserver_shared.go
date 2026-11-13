package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

// Connection ownership is deliberately separate from thread ownership: closing
// this transport must never kill or unload the user's shared daemon/thread.
type sharedAppServerTransport struct {
	notificationMu sync.Mutex // serialize replay and live notifications
	markerMu       sync.Mutex
	markerTurn     string          // markerMu; avoid duplicate global terminal notifications
	conn           *websocket.Conn // protected by appServerSession.procMu
	ready          atomic.Bool
	reconnecting   atomic.Bool
	// The following fields are protected by appServerSession.stateMu.
	clientID  string
	queueID   string
	ownedTurn string
	seenItems map[string]bool
}

type sharedAppServerAgentSession struct{ *appServerSession }

func isSharedAppServerURL(raw string) bool {
	return strings.HasPrefix(raw, "unix://") || strings.HasPrefix(raw, "ws://") || strings.HasPrefix(raw, "wss://")
}

func (s *appServerSession) dialSharedAppServer() (*websocket.Conn, error) {
	endpoint := s.url
	dialer := websocket.Dialer{HandshakeTimeout: 8 * time.Second}
	if strings.HasPrefix(endpoint, "unix://") {
		path := strings.TrimPrefix(endpoint, "unix://")
		if path == "" {
			path = filepath.Join(resolveCodexHomeDir(s.codexHome), "app-server-control", "app-server-control.sock")
		}
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("codex app-server: Unix socket path must be absolute")
		}
		dialer.NetDialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		}
		endpoint = "ws://localhost/"
	} else {
		u, err := url.Parse(endpoint)
		if err != nil || u.Host == "" || u.User != nil {
			return nil, fmt.Errorf("codex app-server: invalid shared endpoint")
		}
	}
	conn, _, err := dialer.DialContext(s.ctx, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("codex app-server: connect shared endpoint: %w", err)
	}
	conn.SetReadLimit(maxCodexJSONLLineSize)
	return conn, nil
}

func (s *appServerSession) connectSharedAppServer() error {
	conn, err := s.dialSharedAppServer()
	if err != nil {
		return err
	}
	s.shared = &sharedAppServerTransport{conn: conn}
	s.wg.Add(1)
	go s.readSharedAppServer(conn)
	slog.Info("codex: connected to shared app-server", "transport", "websocket", "work_dir", s.workDir)
	return nil
}

func (s *appServerSession) writeSharedJSON(data []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.procMu.Lock()
	conn := s.shared.conn
	s.procMu.Unlock()
	if conn == nil {
		return fmt.Errorf("codex shared app-server: connection unavailable")
	}
	if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return fmt.Errorf("codex shared app-server write: %w", err)
	}
	return nil
}

func (s *appServerSession) readSharedAppServer(conn *websocket.Conn) {
	defer s.wg.Done()
	defer conn.Close()
	for {
		kind, data, err := conn.ReadMessage()
		if err != nil {
			s.procMu.Lock()
			current := s.shared.conn == conn
			if current {
				s.shared.conn = nil
			}
			s.procMu.Unlock()
			if !current {
				return
			}
			if s.ctx.Err() == nil && s.shared.ready.Load() {
				s.shared.ready.Store(false)
				s.rejectPending(fmt.Errorf("shared app-server disconnected: %w", err))
				s.scheduleSharedReconnect()
			} else if s.ctx.Err() == nil {
				s.rejectPending(fmt.Errorf("shared app-server disconnected during handshake: %w", err))
			}
			return
		}
		if kind != websocket.TextMessage {
			continue
		}
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(data, &probe); err != nil {
			slog.Warn("codex shared app-server: invalid JSON frame", "error", err)
			continue
		}
		_, hasID := probe["id"]
		_, hasMethod := probe["method"]
		switch {
		case hasID && !hasMethod:
			var response rpcResponseEnvelope
			if json.Unmarshal(data, &response) == nil {
				s.handleResponse(response)
			}
		case hasID && hasMethod:
			s.handleServerRequest(probe)
		default:
			var event rpcNotificationEnvelope
			if json.Unmarshal(data, &event) == nil {
				s.handleNotification(event.Method, event.Params)
			}
		}
	}
}

func (s *appServerSession) scheduleSharedReconnect() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			if s.ctx.Err() != nil || s.shared.ready.Load() {
				return
			}
			if s.shared.reconnecting.CompareAndSwap(false, true) {
				s.wg.Add(1)
				go s.reconnectSharedAppServer()
				return
			}
			select {
			case <-s.ctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()
}

func (s *appServerSession) reconnectSharedAppServer() {
	defer s.wg.Done()
	defer s.shared.reconnecting.Store(false)
	var last error
	for attempt := 0; attempt < 5; attempt++ {
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(time.Duration(attempt+1) * time.Second):
		}
		conn, err := s.dialSharedAppServer()
		if err != nil {
			last = err
			continue
		}
		s.procMu.Lock()
		s.shared.conn = conn
		s.procMu.Unlock()
		s.wg.Add(1)
		go s.readSharedAppServer(conn)
		err = s.initialize()
		if err == nil {
			err = s.ensureThread(s.CurrentSessionID())
		}
		if err != nil {
			last = err
			_ = conn.Close()
			continue
		}
		s.shared.ready.Store(true)
		// Never re-submit input on reconnect. Recover the existing queue/turn
		// by clientUserMessageId; the daemon already owns the durable request.
		if err := s.recoverSharedSubmission(); err != nil {
			slog.Warn("codex shared app-server: recovery pending", "error", err)
		}
		if !s.shared.ready.Load() {
			last = fmt.Errorf("connection lost during recovery")
			continue
		}
		slog.Info("codex shared app-server: reconnected", "thread_id", s.CurrentSessionID())
		return
	}
	if last == nil {
		last = fmt.Errorf("connection unavailable")
	}
	s.alive.Store(false)
	s.clearSharedTurnMarker()
	s.stateMu.Lock()
	waiting := s.shared.clientID != ""
	s.stateMu.Unlock()
	s.rejectPending(last)
	if waiting {
		s.emitError(fmt.Errorf("codex shared app-server: reconnect failed; queued input was not resubmitted: %w", last))
	}
}

func sharedClientMessageID(thread, message string) string {
	if message == "" {
		message = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	sum := sha256.Sum256([]byte(thread + "\x00" + message))
	return "cc-connect-" + hex.EncodeToString(sum[:16])
}

func (s *appServerSession) submitSharedInput(input []map[string]any, messageID string) error {
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for !s.shared.ready.Load() {
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("shared app-server is reconnecting")
		case <-time.After(100 * time.Millisecond):
		}
	}
	clientID := sharedClientMessageID(s.CurrentSessionID(), messageID)
	var before struct {
		Thread struct {
			Status struct {
				Type string `json:"type"`
			} `json:"status"`
		} `json:"thread"`
	}
	if err := s.requestWithTimeout("thread/read", map[string]any{"threadId": s.CurrentSessionID(), "includeTurns": false}, &before, 5*time.Second); err != nil {
		return fmt.Errorf("codex shared app-server: read thread before enqueue: %w", err)
	}
	s.stateMu.Lock()
	if s.shared.clientID != "" {
		s.stateMu.Unlock()
		return fmt.Errorf("codex shared app-server: previous input is still pending")
	}
	s.shared.clientID = clientID
	s.shared.queueID = ""
	s.shared.ownedTurn = ""
	s.shared.seenItems = make(map[string]bool)
	s.currentTurn = ""
	s.pendingMsgs = nil
	s.stateMu.Unlock()
	var response struct {
		Submission struct {
			ID string `json:"id"`
		} `json:"queuedSubmission"`
	}
	err := s.request("thread/queue/add", map[string]any{"threadId": s.CurrentSessionID(), "clientUserMessageId": clientID, "input": input}, &response)
	if err != nil && !s.shared.ready.Load() && s.ctx.Err() == nil {
		// The write may have been accepted before the transport was lost.
		// Let recovery inspect history/queue; replaying could run tools twice.
		s.wg.Add(1)
		go s.monitorSharedSubmission(clientID)
		return nil
	}
	if err != nil {
		s.stateMu.Lock()
		s.shared.clientID = ""
		s.stateMu.Unlock()
		return fmt.Errorf("codex shared app-server enqueue: %w", err)
	}
	s.stateMu.Lock()
	queued := s.shared.clientID == clientID && s.shared.ownedTurn == ""
	if s.shared.clientID == clientID {
		s.shared.queueID = response.Submission.ID
	}
	s.stateMu.Unlock()
	if queued && before.Thread.Status.Type == "active" {
		s.emit(core.Event{Type: core.EventQueued, SessionID: s.CurrentSessionID()})
	}
	s.wg.Add(1)
	go s.monitorSharedSubmission(clientID)
	return nil
}

func (s *appServerSession) monitorSharedSubmission(clientID string) {
	defer s.wg.Done()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
		s.stateMu.Lock()
		pending := s.shared.clientID == clientID
		s.stateMu.Unlock()
		if !pending {
			return
		}
		if !s.shared.ready.Load() {
			continue
		}
		if err := s.recoverSharedSubmission(); err != nil {
			slog.Debug("codex shared app-server: queue check failed", "error", err)
		}
	}
}

func (s *appServerSession) ownsSharedServerRequest(method string, raw json.RawMessage) bool {
	// The originating rich client implements its registered dynamic tools.
	// A secondary bridge must not answer them with "unsupported tool".
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval", "item/tool/requestUserInput":
	default:
		return false
	}
	var ids struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
	}
	if json.Unmarshal(raw, &ids) != nil || ids.ThreadID != s.CurrentSessionID() {
		return false
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.shared.ownedTurn != "" && ids.TurnID == s.shared.ownedTurn
}

func (s *appServerSession) syncSharedTurnMarker() {
	dir := getenvFromList(s.extraEnv, "CC_DATA_DIR")
	if dir == "" || !core.IsSessionUUID(s.CurrentSessionID()) {
		return
	}
	s.shared.markerMu.Lock()
	defer s.shared.markerMu.Unlock()
	s.stateMu.Lock()
	turn, client := s.shared.ownedTurn, s.shared.clientID
	s.stateMu.Unlock()
	if turn == s.shared.markerTurn {
		return
	}
	path := filepath.Join(dir, "bridge-turns", s.CurrentSessionID()+".json")
	if turn == "" {
		s.removeSharedTurnMarker(path)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		slog.Warn("codex: create bridge-turn marker directory", "error", err)
		return
	}
	data, err := json.Marshal(map[string]any{"turn_id": turn, "client_id": client, "started_at": time.Now().Unix()})
	if err != nil {
		slog.Warn("codex: encode bridge-turn marker", "error", err)
		return
	}
	if err := core.AtomicWriteFile(path, data, 0600); err != nil {
		slog.Warn("codex: save bridge-turn marker", "error", err)
		return
	}
	s.shared.markerTurn = turn
}

// Caller holds markerMu. Never remove another attachment's newer marker.
func (s *appServerSession) removeSharedTurnMarker(path string) {
	if s.shared.markerTurn == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err == nil {
		var marker struct {
			TurnID string `json:"turn_id"`
		}
		if json.Unmarshal(data, &marker) == nil && marker.TurnID == s.shared.markerTurn {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				slog.Warn("codex: remove bridge-turn marker", "error", err)
			}
		}
	} else if !os.IsNotExist(err) {
		slog.Warn("codex: read bridge-turn marker", "error", err)
	}
	s.shared.markerTurn = ""
}

func (s *appServerSession) clearSharedTurnMarker() {
	if s.shared == nil {
		return
	}
	s.shared.markerMu.Lock()
	defer s.shared.markerMu.Unlock()
	dir := getenvFromList(s.extraEnv, "CC_DATA_DIR")
	if dir != "" {
		s.removeSharedTurnMarker(filepath.Join(dir, "bridge-turns", s.CurrentSessionID()+".json"))
	}
}

func (s *appServerSession) acceptSharedNotification(method string, raw json.RawMessage) bool {
	if method == "account/rateLimits/updated" {
		return true
	}
	var n struct {
		ThreadID string         `json:"threadId"`
		TurnID   string         `json:"turnId"`
		Item     map[string]any `json:"item"`
		Turn     struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if json.Unmarshal(raw, &n) != nil || n.ThreadID != s.CurrentSessionID() {
		return false
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.shared.clientID == "" {
		return false
	}
	if (method == "item/started" || method == "item/completed") && n.Item["type"] == "userMessage" && n.Item["clientId"] == s.shared.clientID {
		if s.shared.ownedTurn == "" {
			s.shared.ownedTurn = n.TurnID
			s.currentTurn = n.TurnID
			s.pendingMsgs = nil
		}
	}
	turnID := n.TurnID
	if method == "turn/completed" || method == "turn/started" {
		turnID = n.Turn.ID
	}
	if s.shared.ownedTurn == "" || turnID == "" || turnID != s.shared.ownedTurn {
		return false
	}
	if method == "item/completed" {
		id, _ := n.Item["id"].(string)
		if id != "" {
			if s.shared.seenItems[id] {
				return false
			}
			s.shared.seenItems[id] = true
		}
	}
	if method == "turn/completed" {
		s.shared.clientID = ""
		s.shared.queueID = ""
		s.shared.ownedTurn = ""
	}
	return true
}

type sharedQueueEntry struct {
	ID       string `json:"id"`
	ClientID string `json:"clientUserMessageId"`
}
type sharedStoredTurn struct {
	ID     string           `json:"id"`
	Status string           `json:"status"`
	Items  []map[string]any `json:"items"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Recovery is read-only except for explicitly starting our own head-of-queue
// item when the daemon is idle (including a queue paused after an interruption).
func (s *appServerSession) recoverSharedSubmission() error {
	s.stateMu.Lock()
	clientID, queueID := s.shared.clientID, s.shared.queueID
	s.stateMu.Unlock()
	if clientID == "" {
		return nil
	}
	var queue struct {
		Data []sharedQueueEntry `json:"data"`
	}
	if err := s.requestWithTimeout("thread/queue/list", map[string]any{"threadId": s.CurrentSessionID(), "limit": 100}, &queue, 5*time.Second); err != nil {
		return err
	}
	for index, entry := range queue.Data {
		if entry.ClientID != clientID {
			continue
		}
		s.stateMu.Lock()
		if s.shared.clientID == clientID {
			s.shared.queueID = entry.ID
		}
		s.stateMu.Unlock()
		if index != 0 {
			return nil
		}
		var status struct {
			Thread struct {
				Status struct {
					Type string `json:"type"`
				} `json:"status"`
			} `json:"thread"`
		}
		if err := s.requestWithTimeout("thread/read", map[string]any{"threadId": s.CurrentSessionID(), "includeTurns": false}, &status, 5*time.Second); err != nil {
			return err
		}
		if status.Thread.Status.Type == "idle" {
			var started any
			// This method is atomic in the server: a competing terminal turn
			// either wins first or our queued item starts exactly once.
			return s.requestWithTimeout("thread/queue/start", map[string]any{"threadId": s.CurrentSessionID(), "queuedSubmissionId": entry.ID}, &started, 5*time.Second)
		}
		return nil
	}
	var turns struct {
		Data []sharedStoredTurn `json:"data"`
	}
	if err := s.requestWithTimeout("thread/turns/list", map[string]any{"threadId": s.CurrentSessionID(), "limit": 16, "sortDirection": "desc", "itemsView": "summary"}, &turns, 8*time.Second); err != nil {
		return err
	}
	for _, turn := range turns.Data {
		for _, item := range turn.Items {
			if item["type"] != "userMessage" || item["clientId"] != clientID {
				continue
			}
			for _, entry := range turn.Items {
				if entry["type"] != "userMessage" && entry["type"] != "agentMessage" {
					continue
				}
				if turn.Status == "inProgress" && entry["type"] != "userMessage" {
					continue
				}
				payload, _ := json.Marshal(map[string]any{"threadId": s.CurrentSessionID(), "turnId": turn.ID, "item": entry})
				s.handleNotification("item/completed", payload)
			}
			if turn.Status != "inProgress" {
				payload, _ := json.Marshal(map[string]any{"threadId": s.CurrentSessionID(), "turn": turn})
				s.handleNotification("turn/completed", payload)
			}
			return nil
		}
	}
	if queueID == "" {
		return fmt.Errorf("shared input acknowledgement is uncertain; input was not resubmitted")
	}
	return nil
}

// Explicit /stop cancels only the bridge's own queued input or active turn.
// Ordinary Close simply disconnects and leaves the shared daemon running.
func (s *sharedAppServerAgentSession) CancelTurn() error {
	s.stateMu.Lock()
	queueID, turnID := s.shared.queueID, s.shared.ownedTurn
	s.stateMu.Unlock()
	if turnID != "" {
		if err := s.request("turn/interrupt", map[string]any{"threadId": s.CurrentSessionID(), "turnId": turnID}, nil); err != nil {
			return err
		}
	} else if queueID != "" {
		if err := s.request("thread/queue/delete", map[string]any{"threadId": s.CurrentSessionID(), "queuedSubmissionId": queueID}, nil); err != nil {
			return err
		}
	}
	s.stateMu.Lock()
	s.shared.clientID = ""
	s.shared.queueID = ""
	s.shared.ownedTurn = ""
	s.currentTurn = ""
	s.pendingMsgs = nil
	s.turnRevision++
	s.stateMu.Unlock()
	s.clearSharedTurnMarker()
	s.emit(core.Event{Type: core.EventResult, SessionID: s.CurrentSessionID(), Done: true})
	return nil
}

// Compile-time check: a shared attachment doesn't own any child process.
var _ core.AgentSessionCanceller = (*sharedAppServerAgentSession)(nil)
