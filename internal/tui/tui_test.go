package tui

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/thisisnic/goaltracker/internal/goal"
)

func setup(t *testing.T) (*model, *goal.Store) {
	t.Helper()
	store, err := goal.Open(filepath.Join(t.TempDir(), "goaltracker.db"))
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

// send delivers msg to the model and then runs any command it returns,
// feeding the resulting messages back in, as the Bubble Tea runtime would.
// Batch and sequence messages are slices of commands and are unpacked. A
// budget caps how many commands run per delivery, since cursor blinks
// return batches that would otherwise fan out forever.
func send(m *model, msg tea.Msg, budget *int) {
	if msg == nil || *budget <= 0 {
		return
	}
	if rv := reflect.ValueOf(msg); rv.Kind() == reflect.Slice {
		// Run the batch concurrently so timer commands that never return
		// in time cost one wait, not one per command.
		var cmds []tea.Cmd
		for i := 0; i < rv.Len() && *budget > 0; i++ {
			if c, ok := rv.Index(i).Interface().(tea.Cmd); ok && c != nil {
				*budget--
				cmds = append(cmds, c)
			}
		}
		for _, out := range runCmds(cmds) {
			send(m, out, budget)
		}
		return
	}
	_, cmd := m.Update(msg)
	if cmd != nil && *budget > 0 {
		*budget--
		send(m, runCmd(cmd), budget)
	}
}

func deliver(m *model, msg tea.Msg) {
	budget := 16
	send(m, msg, &budget)
}

// cmdWait is how long a command gets to return before its message is
// dropped. Field moves return at once; cursor blinks and other timers do not.
const cmdWait = 100 * time.Millisecond

func runCmd(c tea.Cmd) tea.Msg {
	return runCmds([]tea.Cmd{c})[0]
}

// runCmds runs commands concurrently and returns their messages in order,
// with nil for any that did not finish within cmdWait.
func runCmds(cmds []tea.Cmd) []tea.Msg {
	out := make([]tea.Msg, len(cmds))
	chans := make([]chan tea.Msg, len(cmds))
	for i, c := range cmds {
		chans[i] = make(chan tea.Msg, 1)
		go func(c tea.Cmd, ch chan tea.Msg) { ch <- c() }(c, chans[i])
	}
	deadline := time.After(cmdWait)
	timedOut := false
	for i, ch := range chans {
		if timedOut {
			// Past the deadline: take anything already finished, don't wait.
			select {
			case out[i] = <-ch:
			default:
			}
			continue
		}
		select {
		case out[i] = <-ch:
		case <-deadline:
			timedOut = true
			select {
			case out[i] = <-ch:
			default:
			}
		}
	}
	return out
}

func TestRunCmdsKeepsResultsAfterTimer(t *testing.T) {
	timer := func() tea.Msg { time.Sleep(cmdWait * 3); return "late" }
	quick := func() tea.Msg { return "quick" }
	got := runCmds([]tea.Cmd{timer, quick, quick})
	if got[0] != nil || got[1] != "quick" || got[2] != "quick" {
		t.Errorf("runCmds = %v, want [nil quick quick]", got)
	}
}

func press(m *model, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "backspace":
			msg = tea.KeyPressMsg{Code: tea.KeyBackspace}
		case "shift+tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
		default:
			r := []rune(k)[0]
			msg = tea.KeyPressMsg{Code: r, Text: k}
		}
		deliver(m, msg)
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

func TestFit(t *testing.T) {
	cases := []struct {
		left, right string
		w           int
	}{
		{"short", "50%", 20},
		{"a long statement that will not fit in the pane", "50%", 20},
		{"日本語の目標をここに書く", "24%", 14}, // wide runes
		{"no right side", "", 8},
	}
	for _, c := range cases {
		got := fit(c.left, c.right, c.w)
		if lipgloss.Width(got) != c.w {
			t.Errorf("fit(%q,%q,%d) width = %d want %d: %q", c.left, c.right, c.w, lipgloss.Width(got), c.w, got)
		}
		if !strings.HasSuffix(got, c.right) {
			t.Errorf("fit(%q,%q,%d) = %q, right side not flush", c.left, c.right, c.w, got)
		}
	}
}

func TestDeleteFailureStaysVisible(t *testing.T) {
	m, store := setup(t)
	press(m, "j")
	// Remove the selected goal behind the TUI's back so its delete fails.
	if err := store.Delete(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	press(m, "d", "y")
	if m.err == nil {
		t.Error("failed delete reported no error")
	}
	if m.mode != modeBrowse {
		t.Errorf("mode = %v want browse", m.mode)
	}
	if !strings.Contains(m.View().Content, "error:") {
		t.Error("error not shown in status line")
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

// typeText sends each rune of s as a key press.
func typeText(m *model, s string) {
	for _, r := range s {
		deliver(m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func TestAddViaForm(t *testing.T) {
	m, store := setup(t)
	press(m, "a")
	if m.mode != modeForm || m.form == nil {
		t.Fatalf("a did not open the form: mode=%v", m.mode)
	}
	typeText(m, "write the book")
	press(m, "enter") // goal -> period (prefilled with this year)
	press(m, "enter") // period -> why
	typeText(m, "because")
	press(m, "enter") // why -> target
	typeText(m, "12,000")
	press(m, "enter") // target -> stretch
	press(m, "enter") // stretch -> unit
	typeText(m, "£")
	press(m, "enter") // unit -> under
	press(m, "enter") // submit
	if m.mode != modeBrowse {
		t.Fatalf("form did not close: mode=%v err=%v", m.mode, m.err)
	}
	if m.err != nil {
		t.Fatal(m.err)
	}
	goals, _ := store.List(context.Background(), goal.Filter{})
	if len(goals) != 3 {
		t.Fatalf("got %d goals want 3", len(goals))
	}
	var g goal.Goal
	for _, c := range goals {
		if c.Statement == "write the book" {
			g = c
		}
	}
	if g.ID == 0 || g.Why != "because" || g.Target != 12000 || g.Unit != "£" || g.Level != goal.Year {
		t.Errorf("saved goal: %+v", g)
	}
	if sel, _ := m.selected(); sel.ID != g.ID {
		t.Errorf("cursor not on the new goal: %d", sel.ID)
	}
}

func TestFormValidationBlocksSubmit(t *testing.T) {
	m, store := setup(t)
	press(m, "a")
	press(m, "enter") // empty statement must not advance
	typeText(m, "x")
	press(m, "enter")
	typeText(m, "-bad") // period becomes 2026-bad
	press(m, "enter", "enter", "enter", "enter", "enter")
	if m.mode != modeForm {
		t.Fatalf("form submitted with a bad period (err=%v)", m.err)
	}
	press(m, "esc")
	if m.mode != modeBrowse || m.form != nil {
		t.Errorf("esc did not cancel: mode=%v", m.mode)
	}
	goals, _ := store.List(context.Background(), goal.Filter{})
	if len(goals) != 2 {
		t.Errorf("cancelled form saved a goal: %d goals", len(goals))
	}
}

func TestEditViaForm(t *testing.T) {
	m, store := setup(t)
	press(m, "j", "e")
	if m.form == nil || m.form.editID != 2 || m.form.statement != "finish the garden" || m.form.period != "2026-Q3" || m.form.parent != 1 {
		t.Fatalf("edit form not prefilled: %+v", m.form)
	}
	typeText(m, " in september")
	press(m, "enter", "enter", "enter", "enter", "enter", "enter", "enter")
	if m.mode != modeBrowse || m.err != nil {
		t.Fatalf("edit did not save: mode=%v err=%v", m.mode, m.err)
	}
	g, _ := store.Get(context.Background(), 2)
	if g.Statement != "finish the garden in september" || g.ParentID == nil || *g.ParentID != 1 {
		t.Errorf("after edit: %+v", g)
	}
}

func TestEditKeepsExactTarget(t *testing.T) {
	m, store := setup(t)
	ctx := context.Background()
	tiny, _ := store.Add(ctx, goal.NewGoal{Statement: "tiny", Period: "2026-01", Target: 0.004})
	frac, _ := store.Add(ctx, goal.NewGoal{Statement: "frac", Period: "2026-02", Target: 1.2345})
	if err := m.reload(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{tiny.ID, frac.ID} {
		for i, r := range m.rows {
			if r.Goal.ID == id {
				m.cursor = i
			}
		}
		before, _ := store.Get(ctx, id)
		press(m, "e")
		typeText(m, "!")
		press(m, "enter", "enter", "enter", "enter", "enter", "enter", "enter")
		if m.mode != modeBrowse || m.err != nil {
			t.Fatalf("edit of #%d did not save: mode=%v err=%v", id, m.mode, m.err)
		}
		after, _ := store.Get(ctx, id)
		if after.Statement != before.Statement+"!" {
			t.Errorf("#%d statement = %q", id, after.Statement)
		}
		if after.Target != before.Target || after.Kind != goal.Numeric {
			t.Errorf("#%d target changed by an unrelated edit: %v -> %v (%s)", id, before.Target, after.Target, after.Kind)
		}
	}
}

func TestEscClearsFilterBeforeClosingForm(t *testing.T) {
	m, _ := setup(t)
	press(m, "a")
	typeText(m, "x")
	press(m, "enter", "enter", "enter", "enter", "enter", "enter") // focus lands on Under
	if m.form.filtering() {
		t.Fatal("filter open before / was pressed")
	}
	press(m, "/")
	typeText(m, "ear")
	if !m.form.filtering() {
		t.Fatal("/ did not open the parent filter")
	}
	press(m, "esc")
	if m.mode != modeForm || m.form == nil {
		t.Fatal("esc while filtering closed the whole form")
	}
	if m.form.filtering() {
		t.Error("esc did not clear the filter")
	}
	press(m, "esc")
	if m.mode != modeBrowse || m.form != nil || m.status != "cancelled" {
		t.Errorf("second esc did not cancel: mode=%v status=%q", m.mode, m.status)
	}
}

func TestLongWhyDoesNotPushHelpOffScreen(t *testing.T) {
	m, store := setup(t)
	why := strings.Repeat("this is a very long reason that goes on and on. ", 40)
	if _, err := store.Update(context.Background(), 1, goal.Edit{Why: &why}); err != nil {
		t.Fatal(err)
	}
	if err := m.reload(); err != nil {
		t.Fatal(err)
	}
	deliver(m, tea.WindowSizeMsg{Width: 100, Height: 24})
	view := m.View().Content
	if h := lipgloss.Height(view); h > 24 {
		t.Errorf("view is %d lines tall for a 24-line terminal", h)
	}
	if !strings.Contains(view, "q quit") {
		t.Error("help line not visible")
	}
	if !strings.Contains(view, "…") {
		t.Error("no marker that the detail pane was clipped")
	}
}

func TestProgressInputVisibleWithLongWhy(t *testing.T) {
	m, store := setup(t)
	ctx := context.Background()
	why := strings.Repeat("a long reason that wraps across many lines of the pane. ", 40)
	if _, err := store.Update(ctx, 1, goal.Edit{Why: &why}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		if _, err := store.RecordProgress(ctx, 1, float64(i*1000), "a note that is quite long and would wrap in a narrow pane"); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.reload(); err != nil {
		t.Fatal(err)
	}
	deliver(m, tea.WindowSizeMsg{Width: 70, Height: 22})
	press(m, "p")
	view := m.View().Content
	if h := lipgloss.Height(view); h > 22 {
		t.Errorf("view is %d lines tall for a 22-line terminal", h)
	}
	for _, want := range []string{"new total:", "progress", "history", "earlier", "q quit"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
	if w := lipgloss.Width(view); w > 70 {
		t.Errorf("view is %d wide for a 70-column terminal", w)
	}
}

func TestPrivateModeHidesSensitiveText(t *testing.T) {
	m, store := setup(t)
	ctx := context.Background()
	// The statement is the user's own words and is never redacted, so use
	// one without the number in it here.
	plain := "earn the revenue target"
	if _, err := store.Update(ctx, 1, goal.Edit{Statement: &plain}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordProgress(ctx, 1, 35000, "secret client paid"); err != nil {
		t.Fatal(err)
	}
	if err := m.reload(); err != nil {
		t.Fatal(err)
	}
	visible := ansi.Strip(m.View().Content)
	for _, want := range []string{"for good reasons", "£35,000", "£70,000", "secret client paid"} {
		if !strings.Contains(visible, want) {
			t.Errorf("normal view missing %q", want)
		}
	}

	press(m, "x")
	if !m.private {
		t.Fatal("x did not turn private mode on")
	}
	private := ansi.Strip(m.View().Content)
	for _, leak := range []string{"for good reasons", "35,000", "70,000", "secret client paid"} {
		if strings.Contains(private, leak) {
			t.Errorf("private view shows %q", leak)
		}
	}
	for _, want := range []string{"private", "why", "history", "50%", hidden} {
		if !strings.Contains(private, want) {
			t.Errorf("private view missing %q", want)
		}
	}

	// Recording progress while private must not echo the amount.
	press(m, "p")
	typeText(m, "40000")
	press(m, "enter")
	if strings.Contains(ansi.Strip(m.View().Content), "40,000") {
		t.Error("status line leaked the new amount in private mode")
	}

	// Editing would show the why and target, so it is refused while private.
	press(m, "e")
	if m.mode != modeBrowse || m.form != nil {
		t.Fatal("edit form opened in private mode")
	}
	if !strings.Contains(m.status, "press x") {
		t.Errorf("no hint about leaving private mode: %q", m.status)
	}
	if strings.Contains(ansi.Strip(m.View().Content), "for good reasons") {
		t.Error("why leaked after refused edit")
	}

	press(m, "x")
	if m.private || !strings.Contains(ansi.Strip(m.View().Content), "for good reasons") {
		t.Error("x did not turn private mode off")
	}
	press(m, "e")
	if m.mode != modeForm {
		t.Error("edit still refused after leaving private mode")
	}
}

func TestClipHelpers(t *testing.T) {
	sec := []string{"", "label", "l1", "l2", "l3", "l4"}
	for n, want := range map[int]int{0: 0, 1: 0, 2: 0, 3: 3, 4: 4, 6: 6, 9: 6} {
		if got := len(clipTail(sec, n)); got != want {
			t.Errorf("clipTail n=%d kept %d lines want %d", n, got, want)
		}
		if got := len(clipHead(sec, n)); got != want {
			t.Errorf("clipHead n=%d kept %d lines want %d", n, got, want)
		}
	}
	if got := clipHead(sec, 4); got[len(got)-1] != "l4" || !strings.Contains(got[2], "3 earlier") {
		t.Errorf("clipHead keeps the wrong lines: %q", got)
	}
	if got := clipTail(sec, 3); !strings.Contains(got[2], "…") || got[1] != "label" {
		t.Errorf("clipTail keeps the wrong lines: %q", got)
	}
}

func TestLongStatementKeepsInputVisible(t *testing.T) {
	m, store := setup(t)
	ctx := context.Background()
	long := strings.Repeat("a very long goal statement ", 12)
	if _, err := store.Update(ctx, 1, goal.Edit{Statement: &long}); err != nil {
		t.Fatal(err)
	}
	if err := m.reload(); err != nil {
		t.Fatal(err)
	}
	deliver(m, tea.WindowSizeMsg{Width: 60, Height: 12})
	press(m, "p")
	view := m.View().Content
	if h := lipgloss.Height(view); h > 12 {
		t.Errorf("view is %d lines tall for a 12-line terminal", h)
	}
	for _, want := range []string{"new total:", "progress £0 / £70,000"} {
		if !strings.Contains(ansi.Strip(view), want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

func TestNarrowDeletePromptStaysOnScreen(t *testing.T) {
	m, store := setup(t)
	ctx := context.Background()
	long := strings.Repeat("a very long goal statement ", 6)
	if _, err := store.Update(ctx, 1, goal.Edit{Statement: &long}); err != nil {
		t.Fatal(err)
	}
	if err := m.reload(); err != nil {
		t.Fatal(err)
	}
	deliver(m, tea.WindowSizeMsg{Width: 50, Height: 16})
	press(m, "d")
	view := m.View().Content
	if h := lipgloss.Height(view); h > 16 {
		t.Errorf("view is %d lines tall for a 16-line terminal", h)
	}
	if w := lipgloss.Width(view); w > 50 {
		t.Errorf("view is %d wide for a 50-column terminal", w)
	}
	if !strings.Contains(view, "y/N") {
		t.Error("delete prompt not shown")
	}
	press(m, "n")
}

func TestFormOpensAtRightSizeOnNarrowTerminal(t *testing.T) {
	m, _ := setup(t)
	deliver(m, tea.WindowSizeMsg{Width: 50, Height: 30})
	press(m, "a")
	// The browse help line wraps at this width but the form's does not, so
	// the form must be sized with the form's footer, i.e. after the mode
	// switch. Sending the size again yields the same numbers.
	wantW, wantH := m.formSize()
	if m.form.width != wantW || m.form.height != wantH {
		t.Errorf("form opened at %dx%d, want %dx%d", m.form.width, m.form.height, wantW, wantH)
	}
	deliver(m, tea.WindowSizeMsg{Width: 50, Height: 30})
	if m.form.width != wantW || m.form.height != wantH {
		t.Errorf("form resized to %dx%d on an unchanged window, want %dx%d", m.form.width, m.form.height, wantW, wantH)
	}
	if h := lipgloss.Height(m.View().Content); h > 30 {
		t.Errorf("form view is %d lines tall for a 30-line terminal", h)
	}
}

func TestFormResizes(t *testing.T) {
	m, _ := setup(t)
	press(m, "a")
	deliver(m, tea.WindowSizeMsg{Width: 60, Height: 20})
	if m.mode != modeForm || m.form == nil {
		t.Fatal("resize closed the form")
	}
	if w := lipgloss.Width(m.View().Content); w > 60 {
		t.Errorf("form view is %d wide after resize to 60", w)
	}
}

func TestParentCandidates(t *testing.T) {
	id := func(n int64) *int64 { return &n }
	rows := goal.Flatten(goal.Tree([]goal.Goal{
		{ID: 1},
		{ID: 2, ParentID: id(1)},
		{ID: 3, ParentID: id(2)},
		{ID: 4, ParentID: id(1)},
		{ID: 5},
	}))
	got := parentCandidates(rows, 2)
	var ids []int64
	for _, r := range got {
		ids = append(ids, r.Goal.ID)
	}
	want := []int64{1, 4, 5}
	if len(ids) != len(want) {
		t.Fatalf("candidates = %v want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("candidates = %v want %v", ids, want)
		}
	}
	if len(parentCandidates(rows, 0)) != 5 {
		t.Error("add should offer every goal")
	}
}

func TestYearSectionsAndDoneRows(t *testing.T) {
	m, store := setup(t)
	ctx := context.Background()
	if _, err := store.Add(ctx, goal.NewGoal{Statement: "next year's thing", Period: "2027"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordProgress(ctx, 1, 70000, ""); err != nil {
		t.Fatal(err)
	}
	if err := m.reload(); err != nil {
		t.Fatal(err)
	}
	press(m, "j") // move off the done goal so it is not drawn selected
	view := ansi.Strip(m.viewList(50, 20))
	// Each year heads its own section, in order, with the child under its
	// parent's year.
	order := []string{"2026\n", "hit the big number", "finish the garden", "2027\n", "next year's thing"}
	last := -1
	for _, want := range order {
		i := strings.Index(view, want)
		if i < 0 {
			t.Fatalf("list missing %q\n%s", want, view)
		}
		if i < last {
			t.Errorf("%q out of order\n%s", want, view)
		}
		last = i
	}
	// The goal at 100% is dimmed as a whole row; the open one is not.
	if got := m.viewRow(m.rows[0], false, 40); got != dimStyle.Render(ansi.Strip(got)) {
		t.Errorf("done row not dimmed: %q", got)
	}
	if got := m.viewRow(m.rows[1], false, 40); got != ansi.Strip(got) {
		t.Errorf("open row styled: %q", got)
	}
}

func TestStretchBarOnlyAtTarget(t *testing.T) {
	m, store := setup(t)
	ctx := context.Background()
	six := 90000.0
	if _, err := store.Update(ctx, 1, goal.Edit{Stretch: &six}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordProgress(ctx, 1, 35000, ""); err != nil {
		t.Fatal(err)
	}
	if err := m.reload(); err != nil {
		t.Fatal(err)
	}
	// Below the target the stretch is listed but the bar stops at the target.
	raw := m.View().Content
	if view := ansi.Strip(raw); !strings.Contains(view, "stretch  £90,000") {
		t.Errorf("stretch missing below the target:\n%s", view)
	}
	if strings.Contains(raw, stretchStyle.Render("█")[:5]) {
		t.Errorf("bar ran past the target before it was reached")
	}
	if _, err := store.RecordProgress(ctx, 1, 75000, ""); err != nil {
		t.Fatal(err)
	}
	if err := m.reload(); err != nil {
		t.Fatal(err)
	}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "progress £75,000 / £70,000") || !strings.Contains(view, "stretch  £90,000") {
		t.Errorf("stretch missing at 100%%:\n%s", view)
	}
	// The bar runs to the stretch: some of it filled, some still empty.
	raw = m.View().Content
	if !strings.Contains(raw, stretchStyle.Render("█")[:5]) || !strings.Contains(ansi.Strip(raw), "█░") {
		t.Errorf("bar does not carry on past the target:\n%s", view)
	}
	// Private mode hides the stretch amount too.
	press(m, "x")
	if view := ansi.Strip(m.View().Content); strings.Contains(view, "90,000") {
		t.Errorf("private mode leaked the stretch:\n%s", view)
	}
}

func TestFormSavesStretch(t *testing.T) {
	m, store := setup(t)
	press(m, "e")
	f := m.form
	f.stretch = "80000"
	if _, err := f.apply(m); err != nil {
		t.Fatal(err)
	}
	g, _ := store.Get(context.Background(), 1)
	if g.Stretch != 80000 {
		t.Errorf("Stretch = %v want 80000", g.Stretch)
	}
	press(m, "esc")
	if err := m.reload(); err != nil {
		t.Fatal(err)
	}
	press(m, "e")
	if m.form.stretch != "80000" {
		t.Errorf("edit form stretch = %q", m.form.stretch)
	}
	// Blanking the target in the form turns the goal yes/no and drops the
	// stretch with it; the prefilled Stretch field must not block the way.
	press(m, "enter", "enter", "enter") // to target
	press(m, "backspace", "backspace", "backspace", "backspace", "backspace")
	if m.form.target != "" {
		t.Fatalf("target not cleared: %q", m.form.target)
	}
	press(m, "enter", "enter", "enter", "enter") // stretch, unit, under, submit
	if m.mode != modeBrowse || m.err != nil {
		t.Fatalf("blank-target edit did not save: mode=%v err=%v", m.mode, m.err)
	}
	g, _ = store.Get(context.Background(), 1)
	if g.Kind != goal.YesNo || g.Stretch != 0 {
		t.Errorf("after blanking target: kind=%s stretch=%v", g.Kind, g.Stretch)
	}
}

func TestStretchFieldValidatesAgainstTarget(t *testing.T) {
	m, _ := setup(t)
	press(m, "a")
	typeText(m, "x")
	press(m, "enter", "enter", "enter") // to target
	typeText(m, "100")
	press(m, "enter") // to stretch
	typeText(m, "50")
	press(m, "enter")
	if err := m.form.form.GetFocusedField().Error(); err == nil || !strings.Contains(err.Error(), "beyond the target") {
		t.Fatalf("stretch below target accepted: %v", err)
	}
	press(m, "backspace", "backspace")
	typeText(m, "150")
	press(m, "enter") // to unit
	if err := m.form.form.GetFocusedField().Error(); err != nil {
		t.Fatalf("valid stretch rejected: %v", err)
	}
	// Raising the target past the stretch is not caught on the target
	// field, so both can be raised in turn without getting stuck.
	press(m, "shift+tab", "shift+tab") // unit -> stretch -> target
	typeText(m, "0")                   // 1000
	press(m, "enter")                  // to stretch
	if err := m.form.form.GetFocusedField().Error(); err != nil {
		t.Fatalf("target field blocked: %v", err)
	}
	typeText(m, "0")                    // 1500
	press(m, "enter", "enter", "enter") // unit, under, submit
	if m.mode != modeBrowse || m.err != nil {
		t.Fatalf("raising both did not save: mode=%v err=%v", m.mode, m.err)
	}
	g, _ := m.selected()
	if g.Target != 1000 || g.Stretch != 1500 {
		t.Errorf("saved target=%v stretch=%v", g.Target, g.Stretch)
	}
}
