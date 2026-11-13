package codex

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/chenhg5/cc-connect/core"
)

type environmentConfig struct {
	Name          string `toml:"name"`
	Enabled       *bool  `toml:"enabled"`
	Container     string `toml:"container"`
	WorkDir       string `toml:"work_dir"`
	StartScript   string `toml:"start_script"`
	ConnectScript string `toml:"connect_script"`
}

func (a *Agent) readEnvironments() (map[string]environmentConfig, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(home, ".config", "cc-connect", "environments")
	if a.environmentDir != "" {
		root = a.environmentDir
	}
	paths, err := filepath.Glob(filepath.Join(root, "*", "environment.toml"))
	if err != nil {
		return nil, err
	}
	result := map[string]environmentConfig{}
	for _, path := range paths {
		var config environmentConfig
		if _, err := toml.DecodeFile(path, &config); err != nil {
			return nil, err
		}
		if config.Enabled != nil && !*config.Enabled {
			continue
		}
		if !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(config.Name) || config.Name == "local" {
			return nil, fmt.Errorf("codex: invalid environment name")
		}
		if _, exists := result[config.Name]; exists {
			return nil, fmt.Errorf("codex: duplicate environment %s", config.Name)
		}
		if config.ConnectScript == "" {
			return nil, fmt.Errorf("codex: environment %s requires connect_script", config.Name)
		}
		if !filepath.IsAbs(config.ConnectScript) {
			config.ConnectScript = filepath.Join(filepath.Dir(path), config.ConnectScript)
		}
		if config.StartScript != "" && !filepath.IsAbs(config.StartScript) {
			config.StartScript = filepath.Join(filepath.Dir(path), config.StartScript)
		}
		result[config.Name] = config
	}
	return result, nil
}
func (a *Agent) EnvironmentNames() ([]string, error) {
	configs, err := a.readEnvironments()
	if err != nil {
		return nil, err
	}
	var names []string
	for name := range configs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
func (a *Agent) EnvironmentAgent(name string) (core.Agent, error) {
	configs, err := a.readEnvironments()
	if err != nil {
		return nil, err
	}
	config, ok := configs[name]
	if !ok {
		return nil, fmt.Errorf("codex: unknown environment %s", name)
	}
	opts := a.WorkspaceAgentOptions()
	opts["backend"] = "app_server"
	opts["app_server_url"] = "script://" + config.ConnectScript
	opts["work_dir"] = config.WorkDir
	delete(opts, "codex_home")
	child, err := New(opts)
	if err != nil {
		return nil, err
	}
	label := config.Name
	if config.Container != "" && config.Container != label {
		label += " (" + config.Container + ")"
	}
	child.(*Agent).environmentName = label
	return child, nil
}

func (a *Agent) StartEnvironment(ctx context.Context, name string) error {
	configs, err := a.readEnvironments()
	if err != nil {
		return err
	}
	config, ok := configs[name]
	if !ok {
		return fmt.Errorf("codex: environment not configured")
	}
	if config.StartScript == "" {
		return fmt.Errorf("codex: start_script is not configured; interactive shell scripts cannot start a protocol connection")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, config.StartScript).Run(); err != nil {
		return fmt.Errorf("codex: start environment %s: %w", name, err)
	}
	return nil
}

func (a *Agent) ExecutionEnvironment() string {
	if a.environmentName == "" {
		return "local"
	}
	return a.environmentName
}
