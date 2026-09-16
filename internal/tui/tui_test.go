package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/thisisnic/lifeo/internal/goal"
)

func setup(t *testing.T) (*model, *goal.Store) {
	t.Helper()
	store, err := goal.Open(filepath.Join(t.TempDir(), "lifeo.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	y, _ := store.Add(ctx, goal.NewGoal{Statement: "hit the big number", Period: "2026", Target: 70000, Unit: "£", Why: "for good reasons"})
	_, _ = store.Add(ctx, goal.NewGoal{Statement: "finish the garden", Period: "2026-Q3", ParentID: &y.ID})
	m := newModel(ctx, store)
	if err := m.reload(); err != nil {
		t.Fatal(err)
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m, store
}

func press(m *model, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		default:
			r := []rune(k)[0]
			msg = tea.KeyPressMsg{Code: r, Text: k}
		}
		m.Update(msg)
	}
}

func TestNavigateAndView(t *testing.T) {
	m, _ := setup(t)
	if g, _ := m.selected(); g.ID != 1 {
		t.Fatalf("initial selection %d want 1", g.ID)
	}
	press(m, "j")
	if g, _ := m.selected(); g.ID != 2 {
		t.Fatalf("after j selection %d want 2", g.ID)
	}
	press(m, "j") // clamps at the end
	if g, _ := m.selected(); g.ID != 2 {
		t.Fatalf("cursor ran past the end")
	}
	press(m, "k")
	view := m.View().Content
	for _, want := range []string{"hit the big number", "finish the garden", "for good reasons", "£0 / £70,000"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q", want)
		}
	}
}

func TestProgressFlow(t *testing.T) {
	m, store := setup(t)
	press(m, "p")
	if m.mode != modeProgress {
		t.Fatalf("mode after p = %v want progress", m.mode)
	}
	press(m, "3", "5", ",", "0", "0", "0", "enter")
	if m.mode != modeBrowse {
		t.Fatalf("mode after enter = %v want browse (err=%v)", m.mode, m.err)
	}
	g, _ := store.Get(context.Background(), 1)
	if g.Current != 35000 {
		t.Errorf("Current = %v want 35000", g.Current)
	}
	if len(m.hist) != 1 {
		t.Errorf("history not reloaded: %+v", m.hist)
	}

	// A bad number keeps the input open and reports the error.
	press(m, "p", "x", "enter")
	if m.mode != modeProgress || m.err == nil {
		t.Errorf("bad input: mode=%v err=%v", m.mode, m.err)
	}
	press(m, "esc")
	if m.mode != modeBrowse {
		t.Errorf("esc did not cancel")
	}

	// Yes/no goals refuse progress entry.
	press(m, "j", "p")
	if m.mode != modeBrowse || !strings.Contains(m.status, "yes/no") {
		t.Errorf("progress on yes/no goal: mode=%v status=%q", m.mode, m.status)
	}
}

func TestMarkAndDeleteFlow(t *testing.T) {
	m, store := setup(t)
	ctx := context.Background()
	press(m, "j", "h")
	if g, _ := store.Get(ctx, 2); g.Outcome != goal.Hit {
		t.Errorf("h did not mark hit: %+v", g)
	}
	press(m, "m")
	if g, _ := store.Get(ctx, 2); g.Outcome != goal.Missed {
		t.Errorf("m did not mark missed: %+v", g)
	}
	press(m, "c")
	if g, _ := store.Get(ctx, 2); g.Outcome != goal.Unmarked {
		t.Errorf("c did not clear: %+v", g)
	}

	press(m, "d", "n")
	if m.mode != modeBrowse || len(m.rows) != 2 {
		t.Errorf("declined delete: mode=%v rows=%d", m.mode, len(m.rows))
	}
	press(m, "d", "y")
	if len(m.rows) != 1 || m.err != nil {
		t.Errorf("confirmed delete: rows=%d err=%v", len(m.rows), m.err)
	}
	if _, err := store.Get(ctx, 2); err == nil {
		t.Error("goal 2 still exists after delete")
	}
	// Deleting the last goal leaves an empty, error-free view.
	press(m, "d", "y")
	if len(m.rows) != 0 || m.err != nil {
		t.Errorf("after deleting all: rows=%d err=%v", len(m.rows), m.err)
	}
	if !strings.Contains(m.View().Content, "no goals yet") {
		t.Error("empty state not shown")
	}
}
