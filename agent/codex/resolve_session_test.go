package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveSession_GlobalUUIDRestoresOriginalDirectory(t *testing.T) {
	const id = "11111111-1111-1111-1111-111111111111"
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "2026", "11", "12")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-date-"+id+".jsonl")
	content := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"cwd":"/other/project","source":"cli"}}`+"\n", id)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	a := &Agent{workDir: "/current/project", codexHome: home}
	info, err := a.ResolveSession(context.Background(), id)
	if err != nil || info == nil || info.WorkDir != "/other/project" {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	if a.workDir != "/current/project" {
		t.Fatal("lookup mutated agent directory")
	}
	// Internal subagents must never be exposed as user-selectable sessions.
	content = fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"cwd":"/other/project","source":{"subagent":{"thread_spawn":{}}}}}`+"\n", id)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	info, err = a.ResolveSession(context.Background(), id)
	if err != nil || info != nil {
		t.Fatalf("subagent resolved: %+v %v", info, err)
	}
}

func TestResolveSession_RejectsUUIDTraversal(t *testing.T) {
	a := &Agent{codexHome: t.TempDir()}
	info, err := a.ResolveSession(context.Background(), "../../secret")
	if info != nil || err != nil {
		t.Fatalf("info=%+v err=%v", info, err)
	}
}
