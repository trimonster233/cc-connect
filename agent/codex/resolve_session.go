package codex

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

func (a *Agent) ResolveSession(ctx context.Context, id string) (*core.AgentSessionInfo, error) {
	if a.isRemoteEnvironment() {
		sessions, err := a.remoteSessions(ctx)
		if err != nil {
			return nil, err
		}
		for _, info := range sessions {
			if strings.EqualFold(info.ID, id) {
				return &info, nil
			}
		}
		return nil, nil
	}
	if !core.IsSessionUUID(id) {
		return nil, nil
	}
	a.mu.RLock()
	home := resolveCodexHomeDir(a.codexHome)
	a.mu.RUnlock()
	var result *core.AgentSessionInfo
	err := filepath.WalkDir(filepath.Join(home, "sessions"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), strings.ToLower(id)+".jsonl") {
			return nil
		}
		info := parseCodexSessionFile(path, "")
		if info == nil || !strings.EqualFold(info.ID, id) {
			return nil
		}
		if result != nil && filepath.Clean(result.WorkDir) != filepath.Clean(info.WorkDir) {
			return fmt.Errorf("codex: UUID has conflicting working directories")
		}
		if result == nil || info.ModifiedAt.After(result.ModifiedAt) {
			result = info
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("codex: resolve session: %w", err)
	}
	if result != nil {
		if title := loadCodexSessionTitles(home)[id]; title != "" {
			result.Summary = title
		}
	}
	return result, nil
}
