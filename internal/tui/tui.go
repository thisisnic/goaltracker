// Package tui is the terminal UI for lifeo, built on Bubble Tea v2.
package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/thisisnic/lifeo/internal/goal"
)

// Run opens the goals view and blocks until the user quits.
func Run(ctx context.Context, store *goal.Store) error {
	m := newModel(ctx, store)
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
)

type model struct {
	ctx   context.Context
	store *goal.Store

	rows   []goal.Row
	hist   []goal.Progress // history for the selected goal
	cursor int
	width  int
	height int

	mode   mode
	input  textinput.Model
	status string
	err    error
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
		return m, nil
	case tea.KeyPressMsg:
		switch m.mode {
		case modeProgress:
			return m.updateProgress(msg)
		case modeConfirmDelete:
			return m.updateConfirm(msg)
		}
		return m.updateBrowse(msg)
	}
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
		m.status = fmt.Sprintf("recorded %s on #%d", g.Amount(v), g.ID)
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

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	selectedStyle = lipgloss.NewStyle().Reverse(true)
	hitStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	missedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	labelStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	errStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	paneStyle     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8")).Padding(0, 1)
)

func (m *model) View() tea.View {
	listW := m.width * 55 / 100
	detailW := m.width - listW
	bodyH := max(5, m.height-3) // title + status + help

	// paneStyle's Width and Height include its border and padding, so the
	// content area is 4 narrower (border 2 + padding 2) and 2 shorter.
	left := paneStyle.Width(listW).Height(bodyH).Render(m.viewList(listW-4, bodyH-2))
	right := paneStyle.Width(detailW).Height(bodyH).Render(m.viewDetail(detailW - 4))

	var b strings.Builder
	b.WriteString(titleStyle.Render("lifeo · goals"))
	b.WriteString("\n")
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, left, right))
	b.WriteString("\n")
	b.WriteString(m.viewStatus())
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(m.helpLine()))

	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}

func (m *model) viewList(w, h int) string {
	if len(m.rows) == 0 {
		return dimStyle.Render("no goals yet\n\nadd one from the shell:\n  lifeo goal add \"...\" --period 2026")
	}
	// keep the cursor in view
	start := 0
	if m.cursor >= h {
		start = m.cursor - h + 1
	}
	var lines []string
	for i := start; i < len(m.rows) && i < start+h; i++ {
		r := m.rows[i]
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
		case i == m.cursor:
			line = selectedStyle.Render(line)
		case g.Outcome == goal.Hit:
			line = strings.Replace(line, "✓", hitStyle.Render("✓"), 1)
		case g.Outcome == goal.Missed:
			line = strings.Replace(line, "✗", missedStyle.Render("✗"), 1)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
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

func (m *model) viewDetail(w int) string {
	g, ok := m.selected()
	if !ok {
		return ""
	}
	wrap := lipgloss.NewStyle().Width(w)
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", wrap.Bold(true).Render(g.Statement))
	fmt.Fprintf(&b, "%s %s (%s)   %s #%d\n", labelStyle.Render("period"), g.Period, g.Level, labelStyle.Render("id"), g.ID)
	if g.ParentID != nil {
		fmt.Fprintf(&b, "%s #%d\n", labelStyle.Render("under "), *g.ParentID)
	}
	if g.Why != "" {
		fmt.Fprintf(&b, "\n%s\n%s\n", labelStyle.Render("why"), wrap.Render(g.Why))
	}
	b.WriteString("\n")
	switch g.Kind {
	case goal.Numeric:
		fmt.Fprintf(&b, "%s %s / %s\n%s\n", labelStyle.Render("progress"),
			g.Amount(g.Current), g.Amount(g.Target), bar(g.Percent(), w))
	default:
		fmt.Fprintf(&b, "%s yes/no\n", labelStyle.Render("progress"))
	}
	switch g.Outcome {
	case goal.Hit:
		fmt.Fprintf(&b, "%s %s\n", labelStyle.Render("outcome "), hitStyle.Render("hit"))
	case goal.Missed:
		fmt.Fprintf(&b, "%s %s\n", labelStyle.Render("outcome "), missedStyle.Render("missed"))
	}
	if len(m.hist) > 0 {
		fmt.Fprintf(&b, "\n%s\n", labelStyle.Render("history"))
		for _, p := range m.hist {
			line := fmt.Sprintf("  %s  %s", p.RecordedAt.Local().Format("2006-01-02"), g.Amount(p.Value))
			if p.Note != "" {
				line += "  " + dimStyle.Render(p.Note)
			}
			b.WriteString(line + "\n")
		}
	}
	if m.mode == modeProgress {
		fmt.Fprintf(&b, "\n%s\n", m.input.View())
	}
	return b.String()
}

func bar(pct float64, w int) string {
	if w < 10 {
		return ""
	}
	filled := int(pct / 100 * float64(w))
	return hitStyle.Render(strings.Repeat("█", filled)) + dimStyle.Render(strings.Repeat("░", w-filled))
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
	return "j/k move · p progress · h hit · m missed · c clear · d delete · r reload · q quit"
}
