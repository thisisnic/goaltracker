package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingIsEmpty(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil || c.Backup.Configured() {
		t.Fatalf("Load missing = %+v, %v", c, err)
	}
}

func TestLoadExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte(Example("age1abc", "~/.config/lifeo/key.txt")), 0o600)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Backup.Dir != filepath.Join(home, "lifeo-data") || c.Backup.IdentityFile != filepath.Join(home, ".config/lifeo/key.txt") {
		t.Errorf("paths not expanded: %+v", c.Backup)
	}
	if c.Backup.Recipient != "age1abc" || !c.Backup.OnQuit || c.Backup.Git || !c.Backup.Configured() {
		t.Errorf("fields: %+v", c.Backup)
	}
}

func TestLoadBadTOML(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte("[backup\ndir = 1"), 0o600)
	if _, err := Load(p); err == nil {
		t.Error("bad TOML accepted")
	}
}

func TestDirHonoursXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x")
	if Dir() != "/x/lifeo" || Path() != "/x/lifeo/config.toml" {
		t.Errorf("Dir=%s Path=%s", Dir(), Path())
	}
}
