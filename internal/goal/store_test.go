package goal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "nested", "goaltracker.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestAddAndGet(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	y, err := s.Add(ctx, NewGoal{Statement: " hit the big number ", Why: "freedom", Period: "2026", Target: 70000, Unit: "£"})
	if err != nil {
		t.Fatal(err)
	}
	if y.Statement != "hit the big number" || y.Level != Year || y.Kind != Numeric || y.Target != 70000 || y.Unit != "£" || y.Current != 0 {
		t.Errorf("unexpected year goal: %+v", y)
	}

	q, err := s.Add(ctx, NewGoal{Statement: "finish the garden", Period: "2026-q3", ParentID: &y.ID})
	if err != nil {
		t.Fatal(err)
	}
	if q.Level != Quarter || q.Period != "2026-Q3" || q.Kind != YesNo || q.ParentID == nil || *q.ParentID != y.ID {
		t.Errorf("unexpected quarter goal: %+v", q)
	}

	if _, err := s.Get(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get missing = %v, want ErrNotFound", err)
	}
}

func TestAddValidation(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	bad := int64(42)
	cases := []NewGoal{
		{Statement: "", Period: "2026"},
		{Statement: "x", Period: "nope"},
		{Statement: "x", Period: "2026", Target: -1},
		{Statement: "x", Period: "2026", ParentID: &bad},
	}
	for _, c := range cases {
		if _, err := s.Add(ctx, c); err == nil {
			t.Errorf("Add(%+v) succeeded, want error", c)
		}
	}
}

func TestProgressHistoryAndCurrent(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	g, _ := s.Add(ctx, NewGoal{Statement: "revenue", Period: "2026", Target: 70000})

	if _, err := s.RecordProgress(ctx, g.ID, 20000, "march"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordProgress(ctx, g.ID, 35000, ""); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, g.ID)
	if got.Current != 35000 {
		t.Errorf("Current = %v want 35000", got.Current)
	}
	hist, err := s.History(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 || hist[0].Value != 20000 || hist[0].Note != "march" || hist[1].Value != 35000 {
		t.Errorf("unexpected history: %+v", hist)
	}

	yn, _ := s.Add(ctx, NewGoal{Statement: "launch", Period: "2026-Q1"})
	if _, err := s.RecordProgress(ctx, yn.ID, 1, ""); err == nil {
		t.Error("progress on yes/no goal succeeded, want error")
	}
}

func TestMarkAndList(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	y, _ := s.Add(ctx, NewGoal{Statement: "y", Period: "2026"})
	q, _ := s.Add(ctx, NewGoal{Statement: "q", Period: "2026-Q1", ParentID: &y.ID})
	_, _ = s.Add(ctx, NewGoal{Statement: "old", Period: "2025-12"})

	if err := s.Mark(ctx, q.ID, Hit); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, q.ID)
	if got.Outcome != Hit {
		t.Errorf("Outcome = %q want hit", got.Outcome)
	}
	if err := s.Mark(ctx, q.ID, Unmarked); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(ctx, q.ID)
	if got.Outcome != Unmarked {
		t.Errorf("Outcome after clear = %q want empty", got.Outcome)
	}
	if err := s.Mark(ctx, 999, Hit); !errors.Is(err, ErrNotFound) {
		t.Errorf("Mark missing = %v want ErrNotFound", err)
	}

	all, _ := s.List(ctx, Filter{})
	if len(all) != 3 {
		t.Errorf("List all = %d want 3", len(all))
	}
	this, _ := s.List(ctx, Filter{Year: "2026"})
	if len(this) != 2 {
		t.Errorf("List 2026 = %d want 2", len(this))
	}
	qs, _ := s.List(ctx, Filter{Level: Quarter})
	if len(qs) != 1 || qs[0].ID != q.ID {
		t.Errorf("List quarters = %+v", qs)
	}
}

func TestUpdateAndDelete(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	y, _ := s.Add(ctx, NewGoal{Statement: "y", Period: "2026"})
	q, _ := s.Add(ctx, NewGoal{Statement: "q", Period: "2026-Q1", ParentID: &y.ID})

	why := "because"
	target := 5000.0
	unit := "£"
	got, err := s.Update(ctx, q.ID, Edit{Why: &why, Target: &target, Unit: &unit})
	if err != nil {
		t.Fatal(err)
	}
	if got.Why != "because" || got.Kind != Numeric || got.Target != 5000 || got.Unit != "£" {
		t.Errorf("after update: %+v", got)
	}

	period := "2026-q2"
	got, err = s.Update(ctx, q.ID, Edit{Period: &period})
	if err != nil {
		t.Fatal(err)
	}
	if got.Period != "2026-Q2" || got.Level != Quarter {
		t.Errorf("period not updated: %+v", got)
	}
	bad := "2026-Q9"
	if _, err := s.Update(ctx, q.ID, Edit{Period: &bad}); err == nil {
		t.Error("bad period accepted")
	}

	var none *int64
	got, err = s.Update(ctx, q.ID, Edit{ParentID: &none})
	if err != nil {
		t.Fatal(err)
	}
	if got.ParentID != nil {
		t.Errorf("parent not cleared: %+v", got)
	}
	self := q.ID
	selfP := &self
	if _, err := s.Update(ctx, q.ID, Edit{ParentID: &selfP}); err == nil {
		t.Error("self-parent succeeded, want error")
	}

	pid := y.ID
	pidP := &pid
	if _, err := s.Update(ctx, q.ID, Edit{ParentID: &pidP}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, y.ID); err != nil {
		t.Fatal(err)
	}
	got, err = s.Get(ctx, q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ParentID != nil {
		t.Errorf("child kept dangling parent: %+v", got)
	}
	if err := s.Delete(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete missing = %v want ErrNotFound", err)
	}
}

func TestUpdateRejectsCycle(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	a, _ := s.Add(ctx, NewGoal{Statement: "a", Period: "2026"})
	b, _ := s.Add(ctx, NewGoal{Statement: "b", Period: "2026-Q1", ParentID: &a.ID})
	c, _ := s.Add(ctx, NewGoal{Statement: "c", Period: "2026-01", ParentID: &b.ID})

	// a -> c would close the loop a -> c -> b -> a.
	cid := &c.ID
	if _, err := s.Update(ctx, a.ID, Edit{ParentID: &cid}); err == nil {
		t.Fatal("cycle accepted, want error")
	}
	// Re-parenting a leaf to the root is still fine.
	aid := &a.ID
	if _, err := s.Update(ctx, c.ID, Edit{ParentID: &aid}); err != nil {
		t.Fatalf("legit reparent: %v", err)
	}
}

func TestListRejectsBadYear(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, _ = s.Add(ctx, NewGoal{Statement: "a", Period: "2026"})
	for _, y := range []string{"2", "20%", "abcd"} {
		if _, err := s.List(ctx, Filter{Year: y}); err == nil {
			t.Errorf("List year %q succeeded, want error", y)
		}
	}
}

func TestOpenIsOwnerOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	path := filepath.Join(dir, "goaltracker.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Force the WAL and shm files into existence.
	if _, err := s.Add(context.Background(), NewGoal{Statement: "x", Period: "2026"}); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Errorf("data dir mode = %o want 700", info.Mode().Perm())
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(path + suffix)
		if err != nil {
			t.Errorf("%s: %v", suffix, err)
			continue
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o want 600", path+suffix, info.Mode().Perm())
		}
	}
	// A database left loose by an earlier version is tightened on open.
	s.Close()
	os.Chmod(path, 0o644)
	os.Chmod(dir, 0o755)
	// Reopening an existing database must not fail or lose data.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if got, _ := s2.List(context.Background(), Filter{}); len(got) != 1 {
		t.Errorf("reopened database lost data: %d goals", len(got))
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("loose database not tightened: %o", info.Mode().Perm())
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Errorf("loose data dir not tightened: %o", info.Mode().Perm())
	}
}
