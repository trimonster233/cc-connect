package codex

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnvironmentRegistry_OnlyEnabledEntriesAndRelativeScripts(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"dev", "off"} {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		enabled := "true"
		if name == "off" {
			enabled = "false"
		}
		body := "name = \"" + name + "\"\nenabled = " + enabled + "\nconnect_script = \"./connect.sh\"\nwork_dir = \"/workspace\"\n"
		if err := os.WriteFile(filepath.Join(dir, "environment.toml"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	a := &Agent{environmentDir: root}
	entries, err := a.readEnvironments()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries["dev"].ConnectScript != filepath.Join(root, "dev", "connect.sh") {
		t.Fatalf("unexpected entries: %#v", entries)
	}
	child, err := a.EnvironmentAgent("dev")
	if err != nil {
		t.Fatal(err)
	}
	if !child.(*Agent).isRemoteEnvironment() {
		t.Fatal("container environment uses local backend")
	}
	if _, err := a.EnvironmentAgent("../dev"); err == nil {
		t.Fatal("unregistered environment accepted")
	}
}
