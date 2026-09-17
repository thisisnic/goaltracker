// Package tui is the terminal UI for goaltracker, built on Bubble Tea v2.
package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/thisisnic/goaltracker/internal/goal"
)

// Options adjust how the TUI starts.
type Options struct {
	// Private hides the why, amounts and history notes until toggled off.
	Private bool
}

// Run opens the goals view and blocks until the user quits.
func Run(ctx context.Context, store *goal.Store, opts Options) error {
	m := newModel(ctx, store)
	m.private = opts.Private
	if err := m.reload(); err != nil {
		return err
	}
	_, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	return err
}

type mode int

const (
	modeBrowse mode = iota
	modeProgress
	modeConfirmDelete
	modeForm
)

// editor is a form shown in place of the two panes: the goal form or the
// notes editor. apply writes its result to the store.
type editor interface {
	Init() tea.Cmd
	Update(tea.Msg) (done bool, submitted bool, cmd tea.Cmd)
	View() string
	apply(*model) (goal.Goal, error)
	filtering() bool
	resize(width, height int)
	help() string
}

type model struct {
	ctx   context.Context
	store *goal.Store

	rows   []goal.Row
	hist   []goal.Progress // history for the selected goal
	cursor int
	width  int
	height int

	mode    mode
	private bool // hide the why, amounts and notes from onlookers
	form    editor
	input   textinput.Model
	status  string
	err     error
}

func newModel(ctx context.Context, store *goal.Store) *model {
	in := textinput.New()
	in.Prompt = "new total: "
	in.SetWidth(30)
	return &model{ctx: ctx, store: store, input: in, width: 100, height: 30}
}

func (m *model) reload() error {
	goals, err := m.store.List(m.ctx, goal.Filter{})
	if err != nil {
		return err
	}
	m.rows = goal.Flatten(goal.Tree(goals))
	if m.cursor >= len(m.rows) {
		m.cursor = max(0, len(m.rows)-1)
	}
	return m.loadHistory()
}

func (m *model) loadHistory() error {
	m.hist = nil
	if len(m.rows) == 0 {
		return nil
	}
	g := m.rows[m.cursor].Goal
	if g.Kind != goal.Numeric {
		return nil
	}
	h, err := m.store.History(m.ctx, g.ID)
	if err != nil {
		return err
	}
	m.hist = h
	return nil
}

func (m *model) selected() (goal.Goal, bool) {
	if len(m.rows) == 0 {
		return goal.Goal{}, false
	}
	return m.rows[m.cursor].Goal, true
}

func (m *model) Init() tea.Cmd { return nil }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.mode == modeForm && m.form != nil {
			m.form.resize(m.formSize())
		}
		return m, nil
	case tea.KeyPressMsg:
		switch m.mode {
		case modeProgress:
			return m.updateProgress(msg)
		case modeConfirmDelete:
			return m.updateConfirm(msg)
		case modeForm:
			return m.updateForm(msg)
		}
		return m.updateBrowse(msg)
	}
	if m.mode == modeForm {
		return m.updateForm(msg)
	}
	return m, nil
}

// footer renders the status and help lines wrapped to the terminal width.
func (m *model) footer() string {
	wrap := lipgloss.NewStyle().Width(max(10, m.width))
	return wrap.Render(m.viewStatus()) + "\n" + wrap.Render(dimStyle.Render(m.helpLine()))
}

// bodyHeight is the height of the main panes: everything but the title and
// the footer, which can wrap on narrow terminals.
func (m *model) bodyHeight() int {
	return max(5, m.height-1-lipgloss.Height(m.footer()))
}

// formSize is the content area inside the form's pane.
func (m *model) formSize() (int, int) { return m.width - 4, m.bodyHeight() - 2 }

func (m *model) openForm(existing *goal.Goal) tea.Cmd {
	var id int64
	if existing != nil {
		id = existing.ID
	}
	return m.openEditor(newGoalForm(existing, parentCandidates(m.rows, id), time.Now().Format("2006"), 0, 0))
}

// openEditor shows an editor in place of the panes. It is sized once it is
// in place, since formSize measures the editor's own help line.
func (m *model) openEditor(e editor) tea.Cmd {
	m.mode = modeForm
	m.form = e
	e.resize(m.formSize())
	return e.Init()
}

func (m *model) updateForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "esc" && !m.form.filtering() {
		m.mode = modeBrowse
		m.form = nil
		m.status = "cancelled"
		return m, nil
	}
	done, submitted, cmd := m.form.Update(msg)
	if !done {
		return m, cmd
	}
	m.mode = modeBrowse
	if !submitted {
		m.status = "cancelled"
		m.form = nil
		return m, nil
	}
	g, err := m.form.apply(m)
	if err != nil {
		m.err = err
		m.form = nil
		return m, nil
	}
	m.form = nil
	if err := m.reload(); err != nil {
		m.err = err
		return m, nil
	}
	for i, r := range m.rows {
		if r.Goal.ID == g.ID {
			m.cursor = i
			break
		}
	}
	m.err = m.loadHistory()
	m.status = fmt.Sprintf("saved #%d", g.ID)
	return m, nil
}

func (m *model) updateBrowse(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	m.status, m.err = "", nil
	switch msg.String() {
	case "q", "ctrl+c", "esc":
		return m, tea.Quit
	case "j", "down":
		if m.cursor < len(m.rows)-1 {
			m.cursor++
			m.err = m.loadHistory()
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
			m.err = m.loadHistory()
		}
	case "g", "home":
		m.cursor = 0
		m.err = m.loadHistory()
	case "G", "end":
		m.cursor = max(0, len(m.rows)-1)
		m.err = m.loadHistory()
	case "r":
		m.err = m.reload()
		m.status = "reloaded"
	case "x":
		m.private = !m.private
		if m.private {
			m.status = "private: why, notes and amounts hidden"
		} else {
			m.status = "private off"
		}
	case "h":
		m.mark(goal.Hit)
	case "m":
		m.mark(goal.Missed)
	case "c":
		m.mark(goal.Unmarked)
	case "p":
		g, ok := m.selected()
		if !ok {
			return m, nil
		}
		if g.Kind != goal.Numeric {
			m.status = "yes/no goal: use h (hit) or m (missed) instead"
			return m, nil
		}
		m.mode = modeProgress
		m.input.SetValue("")
		return m, m.input.Focus()
	case "d":
		if _, ok := m.selected(); ok {
			m.mode = modeConfirmDelete
		}
	case "a":
		return m, m.openForm(nil)
	case "n":
		if m.private {
			m.status = "notes are hidden in private mode: press x to leave it first"
			return m, nil
		}
		if g, ok := m.selected(); ok {
			return m, m.openEditor(newNotesForm(g, 0, 0))
		}
	case "e":
		if m.private {
			m.status = "editing shows the why and target: press x to leave private mode first"
			return m, nil
		}
		if g, ok := m.selected(); ok {
			return m, m.openForm(&g)
		}
	}
	return m, nil
}

func (m *model) mark(o goal.Outcome) {
	g, ok := m.selected()
	if !ok {
		return
	}
	if err := m.store.Mark(m.ctx, g.ID, o); err != nil {
		m.err = err
		return
	}
	m.err = m.reload()
	if o == goal.Unmarked {
		m.status = fmt.Sprintf("cleared outcome on #%d", g.ID)
	} else {
		m.status = fmt.Sprintf("marked #%d %s", g.ID, o)
	}
}

func (m *model) updateProgress(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.mode = modeBrowse
		m.input.Blur()
		return m, nil
	case "enter":
		raw := strings.ReplaceAll(strings.TrimSpace(m.input.Value()), ",", "")
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			m.err = fmt.Errorf("%q is not a number", m.input.Value())
			return m, nil
		}
		g, _ := m.selected()
		if _, err := m.store.RecordProgress(m.ctx, g.ID, v, ""); err != nil {
			m.err = err
			return m, nil
		}
		m.mode = modeBrowse
		m.input.Blur()
		m.err = m.reload()
		if m.private {
			m.status = fmt.Sprintf("recorded new total on #%d", g.ID)
		} else {
			m.status = fmt.Sprintf("recorded %s on #%d", g.Amount(v), g.ID)
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *model) updateConfirm(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		g, _ := m.selected()
		m.mode = modeBrowse
		if err := m.store.Delete(m.ctx, g.ID); err != nil {
			m.err = err
			return m, nil
		}
		m.status = fmt.Sprintf("deleted #%d", g.ID)
		m.err = m.reload()
	default:
		m.mode = modeBrowse
		m.status = "kept"
	}
	return m, nil
}

// hidden stands in for a value in private mode.
const hidden = "••••"

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	selectedStyle = lipgloss.NewStyle().Reverse(true)
	hitStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	missedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	labelStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	yearStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	stretchStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	errStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	paneStyle     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8")).Padding(0, 1)
)

func (m *model) View() tea.View {
	listW := m.width * 55 / 100
	detailW := m.width - listW
	bodyH := m.bodyHeight()

	// paneStyle's Width and Height include its border and padding, so the
	// content area is 4 narrower (border 2 + padding 2) and 2 shorter.
	left := paneStyle.Width(listW).Height(bodyH).Render(m.viewList(listW-4, bodyH-2))
	right := paneStyle.Width(detailW).Height(bodyH).Render(clipLines(m.viewDetail(detailW-4, bodyH-2), bodyH-2))

	var b strings.Builder
	b.WriteString(titleStyle.Render("goaltracker · goals"))
	if m.private {
		b.WriteString(dimStyle.Render(" · private"))
	}
	b.WriteString("\n")
	if m.mode == modeForm && m.form != nil {
		b.WriteString(paneStyle.Width(m.width).Height(bodyH).Render(clipLines(m.form.View(), bodyH-2)))
	} else {
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, left, right))
	}
	b.WriteString("\n")
	b.WriteString(m.footer())

	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}

// clipLines keeps the first h lines of s, ending with a marker when
// anything was cut, so the pane never grows past its height.
func clipLines(s string, h int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= h {
		return s
	}
	if h < 1 {
		return ""
	}
	return strings.Join(lines[:h-1], "\n") + "\n" + dimStyle.Render("…")
}

func (m *model) viewList(w, h int) string {
	if len(m.rows) == 0 {
		return dimStyle.Render("no goals yet\n\nadd one from the shell:\n  goaltracker goal add \"...\" --period 2026")
	}
	// Render every row, with a header where a top-level goal starts a new
	// year, then show the window of h lines that holds the cursor. Children
	// stay under their parent's year, whatever their own period says.
	var lines []string
	cursorLine, year := 0, ""
	for i, r := range m.rows {
		if r.Depth == 0 && r.Goal.Year() != year {
			year = r.Goal.Year()
			lines = append(lines, yearStyle.Render(year))
		}
		if i == m.cursor {
			cursorLine = len(lines)
		}
		lines = append(lines, m.viewRow(r, i == m.cursor, w))
	}
	start := 0
	if cursorLine >= h {
		start = cursorLine - h + 1
	}
	return strings.Join(lines[start:min(len(lines), start+h)], "\n")
}

// viewRow renders one goal as a line of width w. A done goal is dimmed as a
// whole; a missed one keeps its red cross.
func (m *model) viewRow(r goal.Row, selected bool, w int) string {
	g := r.Goal
	// Build the row as plain text first so width and truncation are
	// measured without escape codes, then style it.
	mark := "  "
	switch g.Outcome {
	case goal.Hit:
		mark = "✓ "
	case goal.Missed:
		mark = "✗ "
	}
	right := ""
	if g.Kind == goal.Numeric {
		right = fmt.Sprintf("%3.0f%%", g.Percent())
	}
	indent := strings.Repeat("  ", r.Depth)
	left := fmt.Sprintf("%s%s%-8s %s", indent, mark, g.Period, g.Statement)
	line := fit(left, right, w)
	switch {
	case selected:
		return selectedStyle.Render(line)
	case g.Done():
		return dimStyle.Render(line)
	case g.Outcome == goal.Missed:
		return strings.Replace(line, "✗", missedStyle.Render("✗"), 1)
	}
	return line
}

// fit pads or truncates left so that right sits flush at width w. Both the
// measurement and the cut use display width, so wide characters count as
// two columns.
func fit(left, right string, w int) string {
	avail := max(0, w-lipgloss.Width(right))
	left = ansi.Truncate(left, avail, "…")
	pad := max(0, avail-lipgloss.Width(left))
	return left + strings.Repeat(" ", pad) + right
}

// viewDetail renders the selected goal into a w by h area. The header,
// progress, outcome and (in progress mode) the input are always shown; the
// why and history share the remaining lines, with history keeping its most
// recent entries.
func (m *model) viewDetail(w, h int) string {
	g, ok := m.selected()
	if !ok {
		return ""
	}
	wrap := lipgloss.NewStyle().Width(w)
	cut := func(line string) string { return ansi.Truncate(line, w, "…") }
	amount := func(v float64) string {
		if m.private {
			return hidden
		}
		return g.Amount(v)
	}

	var meta []string
	meta = append(meta, cut(fmt.Sprintf("%s %s (%s)   %s #%d", labelStyle.Render("period"), g.Period, g.Level, labelStyle.Render("id"), g.ID)))
	if g.ParentID != nil {
		meta = append(meta, cut(fmt.Sprintf("%s #%d", labelStyle.Render("under "), *g.ParentID)))
	}
	statement := strings.Split(wrap.Bold(true).Render(g.Statement), "\n")

	var prog []string
	prog = append(prog, "")
	switch g.Kind {
	case goal.Numeric:
		prog = append(prog, cut(fmt.Sprintf("%s %s / %s", labelStyle.Render("progress"), amount(g.Current), amount(g.Target))))
		// The stretch is always listed, but the bar only carries on past
		// the target towards it once the target is reached.
		if g.Stretch > 0 {
			prog = append(prog, cut(labelStyle.Render("stretch ")+" "+amount(g.Stretch)))
		}
		if g.Stretch > 0 && g.Percent() >= 100 {
			prog = append(prog, stretchBar(g, w))
		} else {
			prog = append(prog, bar(g.Percent(), w))
		}
	default:
		prog = append(prog, labelStyle.Render("progress")+" yes/no")
	}
	switch g.Outcome {
	case goal.Hit:
		prog = append(prog, labelStyle.Render("outcome ")+" "+hitStyle.Render("hit"))
	case goal.Missed:
		prog = append(prog, labelStyle.Render("outcome ")+" "+missedStyle.Render("missed"))
	}

	var input []string
	if m.mode == modeProgress {
		input = append(input, "", cut(m.input.View()))
	}

	// A long statement gives way to the progress line and input rather
	// than pushing them out of the pane.
	stmtMax := h - len(meta) - len(prog) - len(input)
	if len(statement) > stmtMax {
		statement = clipPlain(statement, max(1, stmtMax))
	}
	head := append(statement, meta...)

	// Space left for why and history after the fixed parts.
	free := h - len(head) - len(prog) - len(input)

	// The why and the notes are one block of text for the space-sharing
	// below: both give way from the bottom when history needs room.
	var why []string
	if g.Why != "" && free > 0 {
		why = append(why, "", labelStyle.Render("why"))
		if m.private {
			why = append(why, dimStyle.Render(hidden))
		} else {
			why = append(why, strings.Split(wrap.Render(g.Why), "\n")...)
		}
	}
	if g.Notes != "" && free > 0 {
		why = append(why, "", labelStyle.Render("notes"))
		if m.private {
			why = append(why, dimStyle.Render(hidden))
		} else {
			why = append(why, strings.Split(wrap.Render(g.Notes), "\n")...)
		}
	}
	var hist []string
	if len(m.hist) > 0 && free > 0 {
		hist = append(hist, "", labelStyle.Render("history"))
		for _, p := range m.hist {
			line := fmt.Sprintf("  %s  %s", p.RecordedAt.Local().Format("2006-01-02"), amount(p.Value))
			if p.Note != "" && !m.private {
				line += "  " + dimStyle.Render(p.Note)
			}
			hist = append(hist, cut(line))
		}
	}
	// History keeps at least its heading and last two entries when there
	// is a why competing for space; the why gets the rest, then history
	// takes whatever the why leaves.
	histMin := min(len(hist), 4)
	if len(why) > free-histMin {
		why = clipTail(why, max(0, free-histMin))
	}
	if len(hist) > free-len(why) {
		hist = clipHead(hist, max(0, free-len(why)))
	}

	var all []string
	all = append(all, head...)
	all = append(all, why...)
	all = append(all, prog...)
	all = append(all, hist...)
	all = append(all, input...)
	if len(all) > h {
		// Very short pane: lose the spacer lines, then the top, so the
		// progress line and input are the last things to go.
		all = dropBlank(all)
	}
	if len(all) > h {
		all = all[len(all)-max(0, h):]
	}
	return strings.Join(all, "\n")
}

func dropBlank(lines []string) []string {
	out := lines[:0:0]
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// clipPlain keeps the first n lines, marking the cut on the last one.
func clipPlain(lines []string, n int) []string {
	if len(lines) <= n {
		return lines
	}
	if n < 1 {
		return nil
	}
	out := append([]string{}, lines[:n]...)
	out[n-1] = dimStyle.Render("…")
	return out
}

// clipTail keeps the first n lines of a labelled section, marking the cut
// on the last one. Fewer than three lines (spacer, label, one line) is not
// worth showing, so the section is dropped.
func clipTail(lines []string, n int) []string {
	if len(lines) <= n {
		return lines
	}
	if n < 3 {
		return nil
	}
	return clipPlain(lines, n)
}

// clipHead keeps a heading line plus the last n-2 entries, with a marker
// saying how many earlier entries were dropped.
func clipHead(lines []string, n int) []string {
	if len(lines) <= n {
		return lines
	}
	if n < 3 {
		return nil
	}
	// lines[0] is a blank spacer, lines[1] the heading.
	keep := n - 3
	dropped := len(lines) - 2 - keep
	out := []string{lines[0], lines[1], dimStyle.Render(fmt.Sprintf("  … %d earlier", dropped))}
	return append(out, lines[len(lines)-keep:]...)
}

func bar(pct float64, w int) string {
	if w < 10 {
		return ""
	}
	filled := int(pct / 100 * float64(w))
	return hitStyle.Render(strings.Repeat("█", filled)) + dimStyle.Render(strings.Repeat("░", w-filled))
}

// stretchBar draws the whole way to the stretch target: the run up to the
// main target in the hit colour, the part beyond it in the stretch colour.
func stretchBar(g goal.Goal, w int) string {
	if w < 10 {
		return ""
	}
	target := int(g.Target / g.Stretch * float64(w))
	filled := int(g.StretchPercent() / 100 * float64(w))
	beyond := max(0, filled-target)
	return hitStyle.Render(strings.Repeat("█", min(filled, target))) +
		stretchStyle.Render(strings.Repeat("█", beyond)) +
		dimStyle.Render(strings.Repeat("░", w-filled))
}

func (m *model) viewStatus() string {
	switch {
	case m.err != nil:
		return errStyle.Render("error: " + m.err.Error())
	case m.mode == modeConfirmDelete:
		g, _ := m.selected()
		return errStyle.Render(fmt.Sprintf("delete #%d %q and its history? y/N", g.ID, g.Statement))
	case m.mode == modeProgress:
		return "type the new running total, enter to save, esc to cancel"
	}
	return m.status
}

func (m *model) helpLine() string {
	if m.mode == modeForm && m.form != nil {
		return m.form.help()
	}
	return "a add · e edit · n notes · p progress · h hit · m missed · c clear · d delete · x private · j/k move · q quit"
}
