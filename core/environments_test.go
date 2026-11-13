package core

import (
	"path/filepath"
	"testing"
)

type environmentTestAgent struct {
	stubAgent
	remote Agent
}

func (a *environmentTestAgent) EnvironmentNames() ([]string, error)    { return []string{"dev"}, nil }
func (a *environmentTestAgent) EnvironmentAgent(string) (Agent, error) { return a.remote, nil }

func TestDockerCommand_ReturnsSelectableCard(t *testing.T) {
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	a := &environmentTestAgent{remote: &stubAgent{}}
	e := NewEngine("test", a, []Platform{p}, "", LangChinese)
	e.sessions = NewSessionManager(filepath.Join(t.TempDir(), "sessions.json"))
	e.SetAdminFrom("owner")
	msg := &Message{UserID: "owner", SessionKey: "test:owner", ReplyCtx: "ctx"}
	e.handleEnvironmentCommand(p, msg, "/docker")
	if len(p.repliedCards) != 1 {
		t.Fatalf("expected card, got %d", len(p.repliedCards))
	}
	card := p.repliedCards[0]
	if card.Header.Title != e.i18n.T(MsgEnvironmentTitle) {
		t.Fatal(card.Header.Title)
	}
	found := false
	for _, element := range card.Elements {
		if row, ok := element.(CardListItem); ok && row.BtnValue == "cmd:/docker dev" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing selectable environment")
	}
	e.handleEnvironmentCommand(p, msg, "/docker dev")
	card = p.repliedCards[len(p.repliedCards)-1]
	for _, element := range card.Elements {
		if row, ok := element.(CardListItem); ok && row.BtnValue == "cmd:/docker dev" {
			if row.BtnType != "primary" {
				t.Fatal("selected environment not highlighted")
			}
		}
	}
}

type crossEnvironmentAgent struct {
	*routingTestAgent
	remote Agent
}

func (a *crossEnvironmentAgent) EnvironmentNames() ([]string, error)    { return []string{"dev"}, nil }
func (a *crossEnvironmentAgent) EnvironmentAgent(string) (Agent, error) { return a.remote, nil }

func TestQuotedEnvironment_DisambiguatesCopiedUUID(t *testing.T) {
	dir := t.TempDir()
	local := &routingTestAgent{name: "routing", dir: dir, known: map[string]AgentSessionInfo{routingUUIDA: {ID: routingUUIDA, WorkDir: dir}}}
	remote := &routingTestAgent{name: "routing", dir: "/container", known: map[string]AgentSessionInfo{routingUUIDA: {ID: routingUUIDA, WorkDir: "/container"}}}
	a := &crossEnvironmentAgent{routingTestAgent: local, remote: remote}
	e := NewEngine("test", a, nil, filepath.Join(dir, "sessions.json"), LangEnglish)
	got, _, _, err := e.resolveQuotedEnvironment("dev", routingUUIDA)
	if err != nil || got != remote {
		t.Fatalf("wrong origin: %v", err)
	}
	if _, _, _, err := e.resolveSessionAcrossEnvironments(routingUUIDA); err == nil {
		t.Fatal("ambiguous UUID silently routed")
	}
}
