package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type engineEnvironment struct {
	agent    Agent
	sessions *SessionManager
}
type environmentState struct {
	loadErr   error
	mu        sync.Mutex
	selected  map[string]string
	instances map[string]engineEnvironment
	loaded    bool
}

func (e *Engine) environmentPath() string { return e.sessions.storePath + ".environments.json" }
func (e *Engine) loadEnvironmentStateLocked() {
	if e.environments.loaded {
		return
	}
	e.environments.loaded = true
	e.environments.selected = map[string]string{}
	e.environments.instances = map[string]engineEnvironment{}
	data, err := os.ReadFile(e.environmentPath())
	if err != nil {
		if !os.IsNotExist(err) {
			e.environments.loadErr = fmt.Errorf("read environment selection: %w", err)
		}
		return
	}
	if err := json.Unmarshal(data, &e.environments.selected); err != nil {
		e.environments.loadErr = fmt.Errorf("decode environment selection: %w", err)
	}

}
func (e *Engine) selectedEnvironment(key string) (Agent, *SessionManager, error) {
	e.environments.mu.Lock()
	defer e.environments.mu.Unlock()
	e.loadEnvironmentStateLocked()
	if e.environments.loadErr != nil {
		return nil, nil, e.environments.loadErr
	}
	name := e.environments.selected[key]
	if name == "" || name == "local" {
		return nil, nil, nil
	}
	return e.environmentByNameLocked(name)
}
func (e *Engine) environmentByName(name string) (Agent, *SessionManager, error) {
	e.environments.mu.Lock()
	defer e.environments.mu.Unlock()
	e.loadEnvironmentStateLocked()
	if name == "local" {
		return e.agent, e.sessions, nil
	}
	return e.environmentByNameLocked(name)
}
func (e *Engine) environmentByNameLocked(name string) (Agent, *SessionManager, error) {
	provider, ok := e.agent.(EnvironmentProvider)
	if !ok {
		return nil, nil, fmt.Errorf("environment provider unavailable")
	}
	names, err := provider.EnvironmentNames()
	if err != nil {
		return nil, nil, err
	}
	allowed := false
	for _, registered := range names {
		if registered == name {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, nil, fmt.Errorf("environment %q is disabled or not registered", name)
	}
	if instance, ok := e.environments.instances[name]; ok {
		return instance.agent, instance.sessions, nil
	}

	agent, err := provider.EnvironmentAgent(name)
	if err != nil {
		return nil, nil, err
	}
	store := filepath.Join(filepath.Dir(e.sessions.storePath), "environment-"+name+".json")
	sessions := NewSessionManager(store)
	e.environments.instances[name] = engineEnvironment{agent, sessions}
	return agent, sessions, nil
}
func (e *Engine) handleEnvironmentCommand(p Platform, msg *Message, content string) bool {
	fields := strings.Fields(content)
	if len(fields) == 0 || fields[0] != "/docker" {
		return false
	}
	if !e.isAdmin(msg.UserID) {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgAdminRequired, "/docker"))
		return true
	}
	provider, ok := e.agent.(EnvironmentProvider)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgModeNotSupported))
		return true
	}
	names, err := provider.EnvironmentNames()
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return true
	}
	if len(fields) == 1 {
		e.replyWithCard(p, msg.ReplyCtx, e.renderEnvironmentCard(msg.SessionKey, names))
		return true
	}
	if len(fields) == 3 && fields[1] == "start" {
		starter, ok := e.agent.(EnvironmentStarter)
		if !ok {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgModeNotSupported))
			return true
		}
		if err := starter.StartEnvironment(e.ctx, fields[2]); err != nil {
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		} else {
			e.reply(p, msg.ReplyCtx, fields[2])
		}
		return true
	}
	name := fields[1]
	valid := name == "local"
	for _, n := range names {
		valid = valid || n == name
	}
	if !valid {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, fmt.Errorf("unknown environment %q", name)))
		return true
	}
	e.environments.mu.Lock()
	e.loadEnvironmentStateLocked()
	if e.environments.selected == nil {
		e.environments.selected = map[string]string{}
	}
	previous := e.environments.selected[msg.SessionKey]
	e.environments.selected[msg.SessionKey] = name
	data, err := json.Marshal(e.environments.selected)
	if err == nil {
		err = saveEnvironmentSelection(e.environmentPath(), data)
	}
	if err != nil {
		e.environments.selected[msg.SessionKey] = previous
	} else {
		e.environments.loadErr = nil
	}
	e.environments.mu.Unlock()
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return true
	}
	if _, _, err = e.selectedEnvironment(msg.SessionKey); err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return true
	}
	e.replyWithCard(p, msg.ReplyCtx, e.renderEnvironmentCard(msg.SessionKey, names))
	return true
}

func saveEnvironmentSelection(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".environments-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func (e *Engine) renderEnvironmentCard(key string, names []string) *Card {
	e.environments.mu.Lock()
	e.loadEnvironmentStateLocked()
	current := e.environments.selected[key]
	e.environments.mu.Unlock()
	if current == "" {
		current = "local"
	}
	card := NewCard().Title(e.i18n.T(MsgEnvironmentTitle), "blue")
	elements := []CardElement{}
	for _, name := range append([]string{"local"}, names...) {
		label := name
		if name == "local" {
			label = e.i18n.T(MsgEnvironmentLocal) + " (local)"
		}
		button := e.i18n.T(MsgEnvironmentSelect)
		style := "default"
		if name == current {
			label = "▶ " + label
			button = e.i18n.T(MsgEnvironmentCurrent)
			style = "primary"
		}
		elements = append(elements, CardListItem{Text: label, BtnText: button, BtnType: style, BtnValue: "cmd:/docker " + name})
	}
	result := card.Build()
	result.Elements = append(result.Elements, elements...)
	return result
}
