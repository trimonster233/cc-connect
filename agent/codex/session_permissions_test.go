package codex

import "testing"

func TestPermissionModeFromPolicy_RequiresActualSandboxAndApproval(t *testing.T) {
	for _, tt := range []struct{ approval, sandbox, want string }{{"never", "dangerFullAccess", "yolo"}, {"never", "workspaceWrite", "full-auto"}, {"on-request", "readOnly", "suggest"}, {"on-request", "workspaceWrite", "workspace-ask"}, {"on-request", "dangerFullAccess", ""}} {
		if got := permissionModeFromPolicy(tt.approval, tt.sandbox); got != tt.want {
			t.Errorf("%s/%s=%s want %s", tt.approval, tt.sandbox, got, tt.want)
		}
	}
}
