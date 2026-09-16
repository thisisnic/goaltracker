package goal

import "testing"

func TestParsePeriod(t *testing.T) {
	cases := []struct {
		in     string
		period string
		level  Level
		ok     bool
	}{
		{"2026", "2026", Year, true},
		{"2026-Q3", "2026-Q3", Quarter, true},
		{"2026-q1", "2026-Q1", Quarter, true},
		{"2026-09", "2026-09", Month, true},
		{" 2026-12 ", "2026-12", Month, true},
		{"2026-Q5", "", "", false},
		{"2026-13", "", "", false},
		{"26", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		period, level, err := ParsePeriod(c.in)
		if (err == nil) != c.ok {
			t.Errorf("ParsePeriod(%q) err=%v, want ok=%v", c.in, err, c.ok)
			continue
		}
		if period != c.period || level != c.level {
			t.Errorf("ParsePeriod(%q) = %q,%q want %q,%q", c.in, period, level, c.period, c.level)
		}
	}
}

func TestFormatNumber(t *testing.T) {
	cases := map[float64]string{
		0:        "0",
		70000:    "70,000",
		1234.5:   "1,234.5",
		999:      "999",
		1000000:  "1,000,000",
		-2500.25: "-2,500.25",
	}
	for in, want := range cases {
		if got := FormatNumber(in); got != want {
			t.Errorf("FormatNumber(%v) = %q want %q", in, got, want)
		}
	}
}

func TestAmount(t *testing.T) {
	cases := []struct {
		unit string
		v    float64
		want string
	}{
		{"£", 35000, "£35,000"},
		{"$", 12.5, "$12.5"},
		{"kg", 6, "6 kg"},
		{"sessions", 12, "12 sessions"},
		{"%", 50, "50%"},
		{"°C", 20, "20°C"},
		{"€", 99, "€99"},
		{"", 7, "7"},
	}
	for _, c := range cases {
		if got := (Goal{Unit: c.unit}).Amount(c.v); got != c.want {
			t.Errorf("Amount(%q, %v) = %q want %q", c.unit, c.v, got, c.want)
		}
	}
}

func TestPercent(t *testing.T) {
	g := Goal{Kind: Numeric, Target: 70000, Current: 35000}
	if p := g.Percent(); p != 50 {
		t.Errorf("Percent = %v want 50", p)
	}
	g.Current = 90000
	if p := g.Percent(); p != 100 {
		t.Errorf("Percent over target = %v want 100", p)
	}
	if p := (Goal{Kind: YesNo}).Percent(); p != 0 {
		t.Errorf("yesno Percent = %v want 0", p)
	}
}

func TestTreeAndFlatten(t *testing.T) {
	id := func(n int64) *int64 { return &n }
	goals := []Goal{
		{ID: 1, Statement: "year"},
		{ID: 2, Statement: "q under year", ParentID: id(1)},
		{ID: 3, Statement: "month under q", ParentID: id(2)},
		{ID: 4, Statement: "orphan month", ParentID: id(99)},
		{ID: 5, Statement: "loose quarter"},
	}
	rows := Flatten(Tree(goals))
	want := []struct {
		id    int64
		depth int
	}{{1, 0}, {2, 1}, {3, 2}, {4, 0}, {5, 0}}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows want %d", len(rows), len(want))
	}
	for i, w := range want {
		if rows[i].Goal.ID != w.id || rows[i].Depth != w.depth {
			t.Errorf("row %d = id %d depth %d, want id %d depth %d", i, rows[i].Goal.ID, rows[i].Depth, w.id, w.depth)
		}
	}
}

func TestTreeShowsLoopedGoals(t *testing.T) {
	id := func(n int64) *int64 { return &n }
	goals := []Goal{
		{ID: 1, Statement: "root"},
		{ID: 2, ParentID: id(3)},
		{ID: 3, ParentID: id(2)},
	}
	rows := Flatten(Tree(goals))
	if len(rows) != 3 {
		t.Fatalf("got %d rows want 3: %+v", len(rows), rows)
	}
	seen := map[int64]bool{}
	for _, r := range rows {
		seen[r.Goal.ID] = true
	}
	for _, want := range []int64{1, 2, 3} {
		if !seen[want] {
			t.Errorf("goal %d missing from tree", want)
		}
	}
}
