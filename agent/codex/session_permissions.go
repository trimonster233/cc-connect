package codex

import (
	"context"
	"fmt"
	"log/slog"
)

// SessionPermissionMode reads or updates the selected shared thread, never a
// new thread or the agent's default. The resume response is authoritative.
func (a *Agent) SessionPermissionMode(ctx context.Context, id, mode string) (string, error) {
	a.mu.Lock()
	shared := a.backend == "app_server" && isSharedAppServerURL(a.appServerURL)
	a.mu.Unlock()
	if !shared {
		return "", fmt.Errorf("codex: current-session permission updates require a shared app-server")
	}
	if mode != "" && mode != "suggest" && mode != "auto-edit" && mode != "full-auto" && mode != "yolo" {
		return "", fmt.Errorf("codex: invalid permission mode %q", mode)
	}
	connection, err := a.StartSession(ctx, id)
	if err != nil {
		return "", err
	}
	defer func() {
		if err := connection.Close(); err != nil {
			slog.Warn("codex: close permission connection", "error", err)
		}
	}()
	session, ok := connection.(*sharedAppServerAgentSession)
	if !ok {
		return "", fmt.Errorf("codex: shared session required")
	}
	params := map[string]any{"threadId": id, "excludeTurns": true}
	if mode != "" {
		var status struct {
			Thread struct {
				Status struct {
					Type string `json:"type"`
				} `json:"status"`
			} `json:"thread"`
		}
		if err := session.request("thread/read", map[string]any{"threadId": id, "includeTurns": false}, &status); err != nil {
			return "", err
		}
		if status.Thread.Status.Type == "active" {
			return "", fmt.Errorf("codex: session is busy; wait for the current turn before changing permissions")
		}
		approval, sandbox := appServerModeSettings(mode)
		policy := map[string]any{}
		switch sandbox {
		case "danger-full-access":
			policy["type"] = "dangerFullAccess"
		case "workspace-write":
			policy = map[string]any{"type": "workspaceWrite", "writableRoots": []string{}, "networkAccess": false, "excludeTmpdirEnvVar": false, "excludeSlashTmp": false}
		default:
			policy = map[string]any{"type": "readOnly", "networkAccess": false}
		}
		// Empty turn input replays prior work. Use an explicit administrative
		// message so the permission change cannot silently continue that task.
		var turn turnStartResponse
		if err := session.request("turn/start", map[string]any{"threadId": id, "approvalPolicy": approval, "sandboxPolicy": policy, "input": []map[string]any{{"type": "text", "text": "Permission configuration update only: mode=" + mode + ". Do not use tools or continue earlier tasks. Reply only with the selected mode.", "text_elements": []any{}}}}, &turn); err != nil {
			return "", fmt.Errorf("codex: apply session permissions: %w", err)
		}
	}

	var response struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Approval string `json:"approvalPolicy"`
		Sandbox  struct {
			Type string `json:"type"`
		} `json:"sandbox"`
	}
	if err := session.request("thread/resume", params, &response); err != nil {
		return "", fmt.Errorf("codex: update/read session permissions: %w", err)
	}
	if response.Thread.ID != id {
		return "", fmt.Errorf("codex: permission response changed thread identity")
	}
	actual := permissionModeFromPolicy(response.Approval, response.Sandbox.Type)
	if actual == "" {
		return "", fmt.Errorf("codex: effective permission policy is %s / %s", response.Approval, response.Sandbox.Type)
	}
	expected := mode
	if expected == "auto-edit" {
		expected = "full-auto"
	}
	if mode != "" && actual != expected {
		return "", fmt.Errorf("codex: server did not apply requested mode %s; actual mode is %s", mode, actual)
	}
	return actual, nil
}

func permissionModeFromPolicy(approval, sandbox string) string {
	if approval == "never" {
		switch sandbox {
		case "dangerFullAccess":
			return "yolo"
		case "workspaceWrite":
			return "full-auto"
		}
	}
	if approval == "on-request" && sandbox == "workspaceWrite" {
		return "workspace-ask"
	}
	if approval == "on-request" && sandbox == "readOnly" {
		return "suggest"
	}
	return ""
}
