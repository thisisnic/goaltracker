package tui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/thisisnic/lifeo/internal/goal"
)

// goalForm collects the fields for adding or editing a goal.
type goalForm struct {
	form   *huh.Form
	editID int64 // 0 when adding

	statement string
	period    string
	why       string
	target    string
	unit      string
	parent    int64 // 0 means none
}

// newGoalForm builds the form. For an edit, existing is the goal being
// changed and its fields are prefilled. candidates are the goals offered as
// parents; the goal being edited and its descendants are excluded.
func newGoalForm(existing *goal.Goal, candidates []goal.Row, defaultPeriod string) *goalForm {
	f := &goalForm{period: defaultPeriod}
	if existing != nil {
		f.editID = existing.ID
		f.statement = existing.Statement
		f.period = existing.Period
		f.why = existing.Why
		f.unit = existing.Unit
		if existing.Target > 0 {
			f.target = goal.FormatNumber(existing.Target)
		}
		if existing.ParentID != nil {
			f.parent = *existing.ParentID
		}
	}

	opts := []huh.Option[int64]{huh.NewOption("none", int64(0))}
	for _, r := range candidates {
		g := r.Goal
		label := fmt.Sprintf("#%d %s %s", g.ID, g.Period, g.Statement)
		opts = append(opts, huh.NewOption(label, g.ID))
	}

	title := "New goal"
	if existing != nil {
		title = fmt.Sprintf("Edit goal #%d", existing.ID)
	}

	f.form = huh.NewForm(
		huh.NewGroup(
			huh.NewInput().Title("Goal").Value(&f.statement).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return errors.New("say what the goal is")
					}
					return nil
				}),
			huh.NewInput().Title("Period").Description("2026, 2026-Q3 or 2026-09").Value(&f.period).
				Validate(func(s string) error {
					_, _, err := goal.ParsePeriod(s)
					return err
				}),
			huh.NewInput().Title("Why").Value(&f.why),
			huh.NewInput().Title("Target").Description("a number for a numeric goal, blank for yes/no").Value(&f.target).
				Validate(func(s string) error {
					_, err := parseTarget(s)
					return err
				}),
			huh.NewInput().Title("Unit").Description("e.g. £").Value(&f.unit),
			huh.NewSelect[int64]().Title("Under").Options(opts...).Value(&f.parent),
		).Title(title),
	).WithShowHelp(true)
	return f
}

func parseTarget(s string) (float64, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	if s == "" {
		return 0, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0, errors.New("target must be a number of zero or more")
	}
	return v, nil
}

func (f *goalForm) Init() tea.Cmd { return f.form.Init() }

// Update feeds a message to the form and reports whether it has finished.
func (f *goalForm) Update(msg tea.Msg) (done bool, submitted bool, cmd tea.Cmd) {
	m, cmd := f.form.Update(msg)
	if fm, ok := m.(*huh.Form); ok {
		f.form = fm
	}
	switch f.form.State {
	case huh.StateCompleted:
		return true, true, cmd
	case huh.StateAborted:
		return true, false, cmd
	}
	return false, false, cmd
}

func (f *goalForm) View() string { return f.form.View() }

// apply writes the form's values to the store.
func (f *goalForm) apply(m *model) (goal.Goal, error) {
	target, err := parseTarget(f.target)
	if err != nil {
		return goal.Goal{}, err
	}
	var parent *int64
	if f.parent > 0 {
		p := f.parent
		parent = &p
	}
	if f.editID == 0 {
		return m.store.Add(m.ctx, goal.NewGoal{
			Statement: f.statement, Why: f.why, Period: f.period,
			Target: target, Unit: f.unit, ParentID: parent,
		})
	}
	return m.store.Update(m.ctx, f.editID, goal.Edit{
		Statement: &f.statement, Why: &f.why, Period: &f.period,
		Target: &target, Unit: &f.unit, ParentID: &parent,
	})
}

// parentCandidates returns rows that may be chosen as a parent when editing
// id: everything except id itself and its descendants. For an add, id is 0.
func parentCandidates(rows []goal.Row, id int64) []goal.Row {
	if id == 0 {
		return rows
	}
	var out []goal.Row
	skipBelow := -1 // depth of the excluded subtree's root, or -1
	for _, r := range rows {
		if skipBelow >= 0 && r.Depth > skipBelow {
			continue
		}
		skipBelow = -1
		if r.Goal.ID == id {
			skipBelow = r.Depth
			continue
		}
		out = append(out, r)
	}
	return out
}
