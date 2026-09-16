package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisnic/lifeo/internal/goal"
)

type runner struct {
	t  *testing.T
	db string
}

func newRunner(t *testing.T) *runner {
	t.Helper()
	return &runner{t: t, db: filepath.Join(t.TempDir(), "lifeo.db")}
}

// run executes lifeo with args and returns stdout. It fails the test on error
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
			r.t.Fatalf("lifeo %v succeeded, want error", args)
		}
		return err.Error()
	}
	if err != nil {
		r.t.Fatalf("lifeo %v: %v\n%s", args, err, out.String())
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
