package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisnic/goaltracker/internal/goal"
)

type runner struct {
	t  *testing.T
	db string
}

func newRunner(t *testing.T) *runner {
	t.Helper()
	// Keep markers and any default paths out of the developer's real
	// config and data directories.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	return &runner{t: t, db: filepath.Join(t.TempDir(), "goaltracker.db")}
}

// run executes goaltracker with args and returns stdout. It fails the test on error
// unless wantErr is true, in which case it returns the error text.
func (r *runner) run(stdin string, wantErr bool, args ...string) string {
	r.t.Helper()
	root := New()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{"--db", r.db}, args...))
	err := root.Execute()
	if wantErr {
		if err == nil {
			r.t.Fatalf("goaltracker %v succeeded, want error", args)
		}
		return err.Error()
	}
	if err != nil {
		r.t.Fatalf("goaltracker %v: %v\n%s", args, err, out.String())
	}
	return out.String()
}

func TestAddListShow(t *testing.T) {
	r := newRunner(t)
	out := r.run("", false, "goal", "add", "hit the big number", "--period", "2026", "--target", "70000", "--unit", "£", "--why", "for good reasons")
	if !strings.Contains(out, "added goal 1") {
		t.Errorf("add output: %q", out)
	}
	r.run("", false, "goal", "add", "launch", "--period", "2026-q3", "--parent", "1")
	r.run("", false, "goal", "progress", "1", "35,000", "--note", "june")
	r.run("", false, "goal", "mark", "2", "hit")

	out = r.run("", false, "goal", "list")
	for _, want := range []string{"hit the big number", "£35,000 / £70,000 (50%)", "  launch", "hit"} {
		if !strings.Contains(out, want) {
			t.Errorf("list missing %q:\n%s", want, out)
		}
	}

	var goals []goal.Goal
	if err := json.Unmarshal([]byte(r.run("", false, "goal", "list", "--json", "--level", "quarter")), &goals); err != nil {
		t.Fatal(err)
	}
	if len(goals) != 1 || goals[0].Period != "2026-Q3" || goals[0].Outcome != goal.Hit {
		t.Errorf("list --json: %+v", goals)
	}

	var detail goalDetail
	if err := json.Unmarshal([]byte(r.run("", false, "goal", "show", "1", "--json")), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Current != 35000 || len(detail.History) != 1 || detail.History[0].Note != "june" {
		t.Errorf("show --json: %+v", detail)
	}
	out = r.run("", false, "goal", "show", "1")
	if !strings.Contains(out, "why:      for good reasons") || !strings.Contains(out, "history:") {
		t.Errorf("show output:\n%s", out)
	}
}

func TestEmptyJSONIsArray(t *testing.T) {
	r := newRunner(t)
	if got := strings.TrimSpace(r.run("", false, "goal", "list", "--json")); got != "[]" {
		t.Errorf("empty list --json = %q want []", got)
	}
	r.run("", false, "goal", "add", "x", "--period", "2026")
	out := r.run("", false, "goal", "show", "1", "--json")
	if !strings.Contains(out, `"history": []`) {
		t.Errorf("show --json without history: %s", out)
	}
}

func TestEditFlags(t *testing.T) {
	r := newRunner(t)
	r.run("", false, "goal", "add", "y", "--period", "2026")
	r.run("", false, "goal", "add", "q", "--period", "2026-Q1", "--parent", "1")

	if msg := r.run("", true, "goal", "edit", "2"); !strings.Contains(msg, "nothing to change") {
		t.Errorf("edit with no flags: %q", msg)
	}
	// show decodes into a fresh struct each time: parent_id is omitempty, so
	// reusing one would keep a stale pointer.
	show := func() goal.Goal {
		var g goal.Goal
		if err := json.Unmarshal([]byte(r.run("", false, "goal", "show", "2", "--json")), &g); err != nil {
			t.Fatal(err)
		}
		return g
	}
	r.run("", false, "goal", "edit", "2", "--target", "5000", "--unit", "£", "--why", "because")
	if g := show(); g.Kind != goal.Numeric || g.Target != 5000 || g.Unit != "£" || g.Why != "because" {
		t.Errorf("after edit: %+v", g)
	}

	r.run("", false, "goal", "edit", "2", "--no-parent")
	if g := show(); g.ParentID != nil {
		t.Errorf("--no-parent left parent %v", *g.ParentID)
	}
	r.run("", false, "goal", "edit", "2", "--parent", "1")
	if g := show(); g.ParentID == nil || *g.ParentID != 1 {
		t.Errorf("--parent 1 not applied: %+v", g)
	}
	// Making the root a child of its own child must fail.
	r.run("", true, "goal", "edit", "1", "--parent", "2")
}

func TestBadInputs(t *testing.T) {
	r := newRunner(t)
	r.run("", false, "goal", "add", "yn", "--period", "2026")
	cases := [][]string{
		{"goal", "add", "x", "--period", "2026-Q7"},
		{"goal", "add", "x", "--period", "2026", "--parent", "0"},
		{"goal", "add", "x", "--period", "2026", "--parent", "-1"},
		{"goal", "list", "--year", "20"},
		{"goal", "list", "--level", "decade"},
		{"goal", "progress", "1", "5"},
		{"goal", "progress", "1", "five"},
		{"goal", "mark", "1", "sorta"},
		{"goal", "mark", "99", "hit"},
		{"goal", "show", "abc"},
		{"goal", "delete", "99", "-y"},
	}
	for _, c := range cases {
		r.run("", true, c...)
	}
}

func TestDeleteConfirmation(t *testing.T) {
	r := newRunner(t)
	r.run("", false, "goal", "add", "x", "--period", "2026")

	if out := r.run("n\n", false, "goal", "delete", "1"); !strings.Contains(out, "kept") {
		t.Errorf("declined delete: %q", out)
	}
	if out := r.run("", false, "goal", "list", "--json"); !strings.Contains(out, `"id": 1`) {
		t.Error("goal was deleted after declining")
	}
	if out := r.run("y\n", false, "goal", "delete", "1"); !strings.Contains(out, "deleted goal 1") {
		t.Errorf("confirmed delete: %q", out)
	}
	if got := strings.TrimSpace(r.run("", false, "goal", "list", "--json")); got != "[]" {
		t.Errorf("goal still present after delete: %s", got)
	}
}

func TestEnvFailClosed(t *testing.T) {
	t.Setenv("GOALTRACKER_PRIVATE", "") // so the test restores it afterwards
	os.Unsetenv("GOALTRACKER_PRIVATE")
	if envFailClosed("GOALTRACKER_PRIVATE") {
		t.Error("unset variable turned private mode on")
	}
	for val, want := range map[string]bool{
		"": false, "0": false, "false": false, "no": false, "off": false, " No ": false,
		"1": true, "true": true, "TRUE": true, "yes": true, "on": true, "y": true,
		"sure": true, // anything unrecognised fails closed
		"  ":   true, // set, even if only spaces
	} {
		t.Setenv("GOALTRACKER_PRIVATE", val)
		if got := envFailClosed("GOALTRACKER_PRIVATE"); got != want {
			t.Errorf("GOALTRACKER_PRIVATE=%q -> %v want %v", val, got, want)
		}
	}
}

func TestKeyBackupRestore(t *testing.T) {
	r := newRunner(t)
	root := t.TempDir()
	keyFile := filepath.Join(root, "key.txt")
	cfgPath := filepath.Join(root, "config.toml")
	dir := filepath.Join(root, "data-repo")

	out := r.run("", false, "key", "new", "--out", keyFile)
	var recipient string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "public key: ") {
			recipient = strings.TrimPrefix(line, "public key: ")
		}
	}
	if !strings.HasPrefix(recipient, "age1") {
		t.Fatalf("no public key in output:\n%s", out)
	}
	r.run("", true, "key", "new", "--out", keyFile) // refuses to overwrite

	// Without config, backup explains what to do.
	if msg := r.run("", true, "--config", cfgPath, "backup"); !strings.Contains(msg, "goaltracker key new") {
		t.Errorf("unconfigured backup error: %q", msg)
	}

	cfg := "[backup]\ndir = \"" + dir + "\"\nrecipient = \"" + recipient + "\"\nidentity_file = \"" + keyFile + "\"\non_quit = true\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	r.run("", false, "goal", "add", "keep this", "--period", "2026")
	out = r.run("", false, "--config", cfgPath, "backup")
	if !strings.Contains(out, "backup: wrote ") {
		t.Fatalf("backup output: %q", out)
	}
	snapshot := strings.TrimSpace(strings.TrimPrefix(out, "backup: wrote "))
	if snapshot != filepath.Join(dir, "goaltracker.db.age") {
		t.Errorf("backup went to %q", snapshot)
	}
	if out := r.run("", false, "--config", cfgPath, "backup"); !strings.Contains(out, "no changes") {
		t.Errorf("second backup: %q", out)
	}

	r.run("", false, "goal", "delete", "1", "-y")
	if got := strings.TrimSpace(r.run("", false, "goal", "list", "--json")); got != "[]" {
		t.Fatal("goal not deleted")
	}

	if out := r.run("n\n", false, "--config", cfgPath, "restore", snapshot); !strings.Contains(out, "kept") {
		t.Errorf("declined restore: %q", out)
	}
	// No FILE argument: restore from the configured folder.
	out = r.run("", false, "--config", cfgPath, "restore", "-y")
	if !strings.Contains(out, "restored") || !strings.Contains(out, snapshot) || !strings.Contains(out, ".bak") {
		t.Errorf("restore output: %q", out)
	}
	if out := r.run("", false, "goal", "list"); !strings.Contains(out, "keep this") {
		t.Errorf("goal not back after restore:\n%s", out)
	}
	// Restore without any key configured or given fails clearly.
	if msg := r.run("", true, "--config", filepath.Join(root, "none.toml"), "restore", snapshot, "-y"); !strings.Contains(msg, "no private key") {
		t.Errorf("restore without key: %q", msg)
	}
	// A key but no folder and no FILE also fails clearly.
	if msg := r.run("", true, "--config", filepath.Join(root, "none.toml"), "restore", "--identity", keyFile, "-y"); !strings.Contains(msg, "no backup file") {
		t.Errorf("restore without file: %q", msg)
	}
}

func TestVersion(t *testing.T) {
	r := newRunner(t)
	if out := r.run("", false, "version"); !strings.HasPrefix(out, "goaltracker ") || strings.TrimSpace(out) == "goaltracker" {
		t.Errorf("version output: %q", out)
	}
	if out := r.run("", false, "--version"); !strings.HasPrefix(out, "goaltracker ") {
		t.Errorf("--version output: %q", out)
	}
}
