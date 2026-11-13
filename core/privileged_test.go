package core

import (
	"context"
	"strings"
	"testing"
)

func TestIsPrivilegedCommandInvocation_StaticListUnchanged(t *testing.T) {
	for cmd := range privilegedCommands {
		if !isPrivilegedCommandInvocation(cmd, nil) {
			t.Errorf("%q should still be privileged even without args", cmd)
		}
		if !isPrivilegedCommandInvocation(cmd, []string{"whatever"}) {
			t.Errorf("%q should still be privileged with arbitrary args", cmd)
		}
	}
}

func TestIsPrivilegedCommandInvocation_CommandsSubcommandGate(t *testing.T) {
	if isPrivilegedCommandInvocation("commands", nil) {
		t.Fatal("/commands without subcommand must not be privileged")
	}
	for _, sub := range []string{"list", "add", "del", "delete", "rm", "remove"} {
		if isPrivilegedCommandInvocation("commands", []string{sub}) {
			t.Errorf("/commands %s must NOT require admin", sub)
		}
	}
	for _, sub := range []string{"addexec", "ADDEXEC", "addEx"} {
		if !isPrivilegedCommandInvocation("commands", []string{sub}) {
			t.Errorf("/commands %s must require admin", sub)
		}
	}
}

func TestIsPrivilegedCommandInvocation_CronSubcommandGate(t *testing.T) {
	if isPrivilegedCommandInvocation("cron", nil) {
		t.Fatal("/cron without subcommand must not be privileged")
	}
	for _, sub := range []string{"add", "list", "del", "enable", "disable", "mute", "unmute", "setup"} {
		if isPrivilegedCommandInvocation("cron", []string{sub}) {
			t.Errorf("/cron %s must NOT require admin", sub)
		}
	}
	for _, sub := range []string{"addexec", "ADDEXEC"} {
		if !isPrivilegedCommandInvocation("cron", []string{sub}) {
			t.Errorf("/cron %s must require admin", sub)
		}
	}
}

func TestIsPrivilegedCommandInvocation_UnknownCmdNotPrivileged(t *testing.T) {
	if isPrivilegedCommandInvocation("help", []string{"addexec"}) {
		t.Error("/help addexec is not a real privileged path; only commands/cron addexec are")
	}
}

func TestHandleCommand_CommandsAddexecBlocksNonAdmin(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetAdminFrom("") // no admins → addexec must be denied for everyone

	msg := &Message{UserID: "u", Platform: "test", ReplyCtx: "rctx"}
	handled := e.handleCommand(p, msg, "/commands addexec foo bar")
	if !handled {
		t.Fatal("addexec invocation must be intercepted, not forwarded to agent")
	}
	sent := p.getSent()
	if len(sent) == 0 || !strings.Contains(strings.ToLower(sent[0]), "admin") {
		t.Fatalf("expected admin-required reply; got %#v", sent)
	}
}

func TestHandleCommand_CommandsListNoAdmin(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetAdminFrom("")

	msg := &Message{UserID: "u", Platform: "test", ReplyCtx: "rctx"}
	handled := e.handleCommand(p, msg, "/commands list")
	if !handled {
		t.Fatal("/commands list must be handled by cc-connect")
	}
	for _, s := range p.getSent() {
		if strings.Contains(strings.ToLower(s), "admin") &&
			strings.Contains(strings.ToLower(s), "required") {
			t.Errorf("/commands list must NOT be admin-gated; got %q", s)
		}
	}
}

func TestHandleCommand_CronAddexecBlocksNonAdmin(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetAdminFrom("")

	msg := &Message{UserID: "u", Platform: "test", ReplyCtx: "rctx"}
	handled := e.handleCommand(p, msg, "/cron addexec daily echo hi")
	if !handled {
		t.Fatal("cron addexec must be intercepted")
	}
	sent := p.getSent()
	if len(sent) == 0 || !strings.Contains(strings.ToLower(sent[0]), "admin") {
		t.Fatalf("expected admin-required reply; got %#v", sent)
	}
}

func TestModeChangeRequiresAdmin(t *testing.T) {
	if isPrivilegedCommandInvocation("mode", nil) {
		t.Fatal("viewing modes must remain available")
	}
	for _, mode := range []string{"suggest", "full-auto", "yolo"} {
		if !isPrivilegedCommandInvocation("mode", []string{mode}) {
			t.Fatalf("mode %s must require admin", mode)
		}
		p := &stubPlatformEngine{n: "test"}
		e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
		e.SetAdminFrom("owner")
		e.handleCommand(p, &Message{UserID: "other", Platform: "test", ReplyCtx: "rctx"}, "/mode "+mode)
		sent := p.getSent()
		if len(sent) == 0 || !strings.Contains(strings.ToLower(sent[0]), "admin") {
			t.Fatalf("expected admin rejection: %#v", sent)
		}
	}
}

func TestModeCardAction_PreservesSelectedSessionAndHistory(t *testing.T) {
	agent := &stubModelModeAgent{}
	e := NewEngine("test", agent, nil, "", LangEnglish)
	key := "test:owner"
	session := e.sessions.GetOrCreateActive(key)
	session.SetAgentSessionID("01a1d329-4c38-7a00-b363-0d16a8745138", "test")
	session.AddHistory("user", "keep history")
	e.executeCardAction("/mode", "yolo", key)
	if got := session.GetAgentSessionID(); got != "01a1d329-4c38-7a00-b363-0d16a8745138" {
		t.Fatalf("mode card discarded selected UUID: %q", got)
	}
	if session.HistoryLen() != 1 {
		t.Fatal("mode card discarded history")
	}
}

type permissionModeTestAgent struct {
	stubModelModeAgent
	id, target, actual string
}

func (a *permissionModeTestAgent) SessionPermissionMode(_ context.Context, id, mode string) (string, error) {
	a.id = id
	a.target = mode
	return a.actual, nil
}

func TestModeCommand_UpdatesSelectedUUIDInsteadOfDefault(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	a := &permissionModeTestAgent{actual: "full-auto"}
	e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
	e.SetAdminFrom("owner")
	key := "test:owner"
	id := "01a1d329-4c38-7a00-b363-0d16a8745138"
	e.sessions.GetOrCreateActive(key).SetAgentSessionID(id, "test")
	e.cmdMode(p, &Message{SessionKey: key, UserID: "owner"}, []string{"full-auto"})
	if a.id != id || a.target != "full-auto" {
		t.Fatalf("updated wrong target: %q %q", a.id, a.target)
	}
	if e.sessions.GetOrCreateActive(key).GetAgentSessionID() != id {
		t.Fatal("UUID changed")
	}
}
