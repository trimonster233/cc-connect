package codex

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func (a *Agent) remoteSessions(ctx context.Context) ([]core.AgentSessionInfo, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &appServerSession{url: a.appServerURL, ctx: ctx, cancel: cancel, events: make(chan core.Event, 128), pending: make(map[int64]chan rpcResponseEnvelope), pendingApprovals: make(map[string]chan core.PermissionResult)}
	s.alive.Store(true)
	if err := s.connect(); err != nil {
		return nil, err
	}
	defer func() {
		if err := s.Close(); err != nil {
			slog.Warn("codex: close remote list connection", "error", err)
		}
	}()
	if err := s.initialize(); err != nil {
		return nil, err
	}
	s.shared.ready.Store(true)
	var result []core.AgentSessionInfo
	cursor := ""
	for page := 0; page < 100; page++ {
		var response struct {
			Data []struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				Preview string `json:"preview"`
				CWD     string `json:"cwd"`
				Updated int64  `json:"updatedAt"`
			} `json:"data"`
			Next *string `json:"nextCursor"`
		}
		params := map[string]any{"limit": 100}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if err := s.request("thread/list", params, &response); err != nil {
			return nil, fmt.Errorf("codex: list remote sessions: %w", err)
		}
		for _, thread := range response.Data {
			summary := thread.Name
			if summary == "" {
				summary = thread.Preview
			}
			result = append(result, core.AgentSessionInfo{ID: thread.ID, WorkDir: thread.CWD, Summary: summary, ModifiedAt: time.Unix(thread.Updated, 0)})
		}
		if response.Next == nil || *response.Next == "" {
			return result, nil
		}
		cursor = *response.Next
	}
	return nil, fmt.Errorf("codex: remote session list exceeds page limit")
}

func (a *Agent) isRemoteEnvironment() bool { return strings.HasPrefix(a.appServerURL, "script://") }
