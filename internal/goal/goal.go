// Package goal holds the goal model and its SQLite store.
//
// A goal lives at one of three levels: year, quarter or month. The level is
// implied by the period it is set for, so callers only supply the period.
// Goals are either numeric, with a target and a hand-updated running total, or
// yes/no. At the end of a period a goal is marked hit or missed.
package goal

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Level is the horizon a goal is set for.
type Level string

const (
	Year    Level = "year"
	Quarter Level = "quarter"
	Month   Level = "month"
)

// Kind says how a goal is measured.
type Kind string

const (
	Numeric Kind = "numeric"
	YesNo   Kind = "yesno"
)

// Outcome is set at the end of a period. Empty means not yet marked.
type Outcome string

const (
	Unmarked Outcome = ""
	Hit      Outcome = "hit"
	Missed   Outcome = "missed"
)

// ErrNotFound is returned when a goal or progress row does not exist.
var ErrNotFound = errors.New("not found")

// Goal is a single goal at any level.
type Goal struct {
	ID        int64     `json:"id"`
	Statement string    `json:"statement"`
	Why       string    `json:"why,omitempty"`
	Level     Level     `json:"level"`
	Period    string    `json:"period"`
	ParentID  *int64    `json:"parent_id,omitempty"`
	Kind      Kind      `json:"kind"`
	Target    float64   `json:"target,omitempty"`
	Unit      string    `json:"unit,omitempty"`
	Current   float64   `json:"current,omitempty"`
	Outcome   Outcome   `json:"outcome,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Progress is one hand-entered update to a numeric goal's running total.
type Progress struct {
	ID         int64     `json:"id"`
	GoalID     int64     `json:"goal_id"`
	Value      float64   `json:"value"`
	Note       string    `json:"note,omitempty"`
	RecordedAt time.Time `json:"recorded_at"`
}

var (
	yearRe    = regexp.MustCompile(`^\d{4}$`)
	quarterRe = regexp.MustCompile(`^(\d{4})-[Qq]([1-4])$`)
	monthRe   = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)
)

// ParsePeriod normalises a period string and returns its level.
// Accepted forms: "2026", "2026-Q3", "2026-09".
func ParsePeriod(s string) (period string, level Level, err error) {
	s = strings.TrimSpace(s)
	switch {
	case yearRe.MatchString(s):
		return s, Year, nil
	case quarterRe.MatchString(s):
		m := quarterRe.FindStringSubmatch(s)
		return m[1] + "-Q" + m[2], Quarter, nil
	case monthRe.MatchString(s):
		return s, Month, nil
	}
	return "", "", fmt.Errorf("period %q: want YYYY, YYYY-Qn or YYYY-MM", s)
}

// YearOf returns the four-digit year a period belongs to.
func YearOf(period string) string {
	if len(period) >= 4 {
		return period[:4]
	}
	return period
}

// ParseOutcome accepts hit, missed, or clear (meaning unmarked).
func ParseOutcome(s string) (Outcome, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "hit":
		return Hit, nil
	case "missed", "miss":
		return Missed, nil
	case "clear", "unmark", "":
		return Unmarked, nil
	}
	return "", fmt.Errorf("outcome %q: want hit, missed or clear", s)
}

// Year is the calendar year the goal's period falls in.
func (g Goal) Year() string {
	if len(g.Period) < 4 {
		return g.Period
	}
	return g.Period[:4]
}

// Done reports whether the goal is finished: marked hit, or a numeric goal
// whose running total has reached its target. The owner rarely marks goals,
// so reaching 100% counts on its own.
func (g Goal) Done() bool {
	return g.Outcome == Hit || (g.Kind == Numeric && g.Percent() >= 100)
}

// Percent is how far a numeric goal is towards its target, capped at 100.
// It returns 0 for yes/no goals or a zero target.
func (g Goal) Percent() float64 {
	if g.Kind != Numeric || g.Target == 0 {
		return 0
	}
	p := g.Current / g.Target * 100
	if p > 100 {
		p = 100
	}
	if p < 0 {
		p = 0
	}
	return p
}

// Amount formats a value with the goal's unit. Currency symbols go in
// front, as in "£35,000". Units that start with a letter go after with a
// space, as in "6 kg". Other symbols go straight after, as in "50%" or "20°C".
func (g Goal) Amount(v float64) string {
	n := FormatNumber(v)
	if g.Unit == "" {
		return n
	}
	r := []rune(g.Unit)[0]
	switch {
	case unicode.Is(unicode.Sc, r):
		return g.Unit + n
	case unicode.IsLetter(r):
		return n + " " + g.Unit
	}
	return n + g.Unit
}

// FormatNumber renders a float with thousands separators and no trailing
// zeros: 70000 -> "70,000", 1234.5 -> "1,234.5".
func FormatNumber(v float64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	s := fmt.Sprintf("%.2f", v)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	intPart, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	for i, r := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	out := b.String()
	if frac != "" {
		out += "." + frac
	}
	if neg {
		out = "-" + out
	}
	return out
}

// Node is a goal with its children, used to render the year > quarter > month
// tree. Goals whose parent is missing from the input are treated as roots.
type Node struct {
	Goal     Goal
	Children []*Node
}

// Tree arranges goals into parent/child nodes. Roots are sorted by period;
// ties keep their order, with goals rescued from a loop after the other
// roots. Children keep their input order. Goals that would be unreachable
// from any root, for instance because their parent links form a loop, become
// roots too so nothing is hidden.
func Tree(goals []Goal) []*Node {
	byID := make(map[int64]*Node, len(goals))
	nodes := make([]*Node, 0, len(goals))
	for _, g := range goals {
		n := &Node{Goal: g}
		byID[g.ID] = n
		nodes = append(nodes, n)
	}
	var roots []*Node
	for _, n := range nodes {
		if n.Goal.ParentID != nil {
			if p, ok := byID[*n.Goal.ParentID]; ok {
				p.Children = append(p.Children, n)
				continue
			}
		}
		roots = append(roots, n)
	}
	visited := map[int64]bool{}
	var mark func(n *Node)
	mark = func(n *Node) {
		if visited[n.Goal.ID] {
			return
		}
		visited[n.Goal.ID] = true
		for _, c := range n.Children {
			mark(c)
		}
	}
	for _, r := range roots {
		mark(r)
	}
	for _, n := range nodes {
		if !visited[n.Goal.ID] {
			roots = append(roots, n)
			mark(n)
		}
	}
	// Goals rescued from a loop were appended last; put every root back in
	// period order so a list grouped by year stays in one piece.
	sort.SliceStable(roots, func(i, j int) bool { return roots[i].Goal.Period < roots[j].Goal.Period })
	return roots
}

// Flatten walks a tree depth-first, returning each goal with its depth. Each
// goal appears once even if parent links loop.
func Flatten(roots []*Node) []Row {
	var out []Row
	visited := map[int64]bool{}
	var walk func(n *Node, depth int)
	walk = func(n *Node, depth int) {
		if visited[n.Goal.ID] {
			return
		}
		visited[n.Goal.ID] = true
		out = append(out, Row{Goal: n.Goal, Depth: depth})
		for _, c := range n.Children {
			walk(c, depth+1)
		}
	}
	for _, r := range roots {
		walk(r, 0)
	}
	return out
}

// Row is a goal at a depth in the flattened tree.
type Row struct {
	Goal  Goal
	Depth int
}
