package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveSession_GlobalUUIDUsesTranscriptDirectory(t *testing.T) {
	const id = "11111111-1111-1111-1111-111111111111"
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	dir := filepath.Join(home, "projects", "-different-project")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","cwd":"/different_project","message":{"content":"hello"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a := &Agent{workDir: "/current", configEnv: []string{"CLAUDE_CONFIG_DIR=" + home}}
	info, err := a.ResolveSession(context.Background(), id)
	if err != nil || info == nil || info.WorkDir != "/different_project" {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	if a.workDir != "/current" {
		t.Fatal("lookup mutated current directory")
	}
	if err := os.WriteFile(path, []byte(`{"type":"user","cwd":"/different_project","isSidechain":true}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err = a.ResolveSession(context.Background(), id)
	if err != nil || info != nil {
		t.Fatalf("sidechain resolved: %+v %v", info, err)
	}
}
