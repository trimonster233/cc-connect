package codex

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Opt in with a separately created, disposable thread. Never submits input to
// the optional observer thread (which may be the user's running terminal).
func TestSharedAppServerLive_IsolatedThread(t *testing.T) {
	endpoint, id := os.Getenv("CC_CONNECT_SHARED_LIVE_URL"), os.Getenv("CC_CONNECT_SHARED_LIVE_THREAD")
	if endpoint == "" || id == "" {
		t.Skip("requires an isolated shared-server test thread")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	attach := func(thread string) *appServerSession {
		s, err := newAppServerSession(ctx, "/must-not-spawn", nil, endpoint, "/tmp", "", "", "suggest", thread, "", "", nil, "", "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		if s.cmd != nil {
			t.Fatal("spawned a second writer")
		}
		return s
	}
	if observer := os.Getenv("CC_CONNECT_SHARED_LIVE_OBSERVER"); observer != "" {
		s := attach(observer)
		if s.CurrentSessionID() != observer {
			t.Fatal("observer attachment changed UUID")
		}
		select {
		case event := <-s.Events():
			t.Fatalf("readonly observer emitted a foreign event: %+v", event)
		default:
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	s := attach(id)
	if err := s.Send("这是独立的飞书共享连接验证。只回复 SHARED_BRIDGE_OK，不调用任何工具。", fmt.Sprintf("live-probe-%d", time.Now().UnixNano()), nil, nil); err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for {
		select {
		case event := <-s.Events():
			if event.Type == core.EventError {
				t.Fatal(event.Error)
			}
			if event.Type == core.EventText {
				text.WriteString(event.Content)
			}
			if event.Type == core.EventResult {
				if !strings.Contains(text.String(), "SHARED_BRIDGE_OK") {
					t.Fatalf("unexpected reply: %q", text.String())
				}
				t.Log("shared adapter returned", text.String())
				return
			}
		case <-ctx.Done():
			t.Fatal("shared adapter probe timed out")
		}
	}
}
