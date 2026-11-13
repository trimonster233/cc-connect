package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/chenhg5/cc-connect/core"
)

func (a *Agent) ResolveSession(ctx context.Context, id string) (*core.AgentSessionInfo, error) {
	if !core.IsSessionUUID(id) {
		return nil, nil
	}
	paths, err := filepath.Glob(filepath.Join(a.configHome(), "projects", "*", id+".jsonl"))
	if err != nil {
		return nil, fmt.Errorf("claudecode: resolve session files: %w", err)
	}
	var result *core.AgentSessionInfo
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cwd, err := sessionOriginalCWD(path)
		if err != nil {
			return nil, err
		}
		if cwd == "" {
			continue
		}
		if result != nil && filepath.Clean(result.WorkDir) != filepath.Clean(cwd) {
			return nil, fmt.Errorf("claudecode: UUID has conflicting working directories")
		}
		stat, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("claudecode: stat session: %w", err)
		}
		summary, count := scanSessionMeta(path)
		result = &core.AgentSessionInfo{ID: id, WorkDir: cwd, Summary: summary, MessageCount: count, ModifiedAt: stat.ModTime()}
	}
	return result, nil
}

func sessionOriginalCWD(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("claudecode: open session: %w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), maxSessionJSONLLineSize)
	for scanner.Scan() {
		var row struct {
			Type      string `json:"type"`
			CWD       string `json:"cwd"`
			Sidechain bool   `json:"isSidechain"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return "", fmt.Errorf("claudecode: read session metadata: %w", err)
		}
		if row.Sidechain {
			return "", nil
		}
		if (row.Type == "user" || row.Type == "assistant") && row.CWD != "" {
			return row.CWD, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("claudecode: scan session metadata: %w", err)
	}
	return "", nil
}
