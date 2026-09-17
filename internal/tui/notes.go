package tui

import (
	"fmt"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/thisisnic/goaltracker/internal/goal"
)

// notesForm edits a goal's notes as one block of text. Enter starts a new
// line and ctrl+s saves, unlike the goal form where enter moves on.
type notesForm struct {
	form   *huh.Form
	text   *huh.Text
	id     int64
	notes  string
	width  int
	height int
}

func newNotesForm(g goal.Goal, width, height int) *notesForm {
	f := &notesForm{id: g.ID, notes: g.Notes}
	km := huh.NewDefaultKeyMap()
	km.Text.NewLine = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "new line"))
	km.Text.Next = key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "save"))
	km.Text.Submit = key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "save"))
	f.text = huh.NewText().Title(fmt.Sprintf("Notes for #%d %s", g.ID, g.Statement)).Value(&f.notes)
	f.form = huh.NewForm(huh.NewGroup(f.text)).WithKeyMap(km).WithShowHelp(false)
	f.resize(width, height)
	return f
}

// resize fits the form to the space given and lets the text area use most
// of it, leaving room for the title and a spare line.
func (f *notesForm) resize(width, height int) {
	f.width, f.height = max(20, width), max(10, height)
	f.text.Lines(max(3, f.height-4))
	f.form = f.form.WithWidth(f.width).WithHeight(f.height)
}

func (f *notesForm) Init() tea.Cmd { return f.form.Init() }

func (f *notesForm) Update(msg tea.Msg) (done bool, submitted bool, cmd tea.Cmd) {
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

func (f *notesForm) View() string    { return f.form.View() }
func (f *notesForm) filtering() bool { return false }
func (f *notesForm) help() string {
	return "enter new line · ctrl+s save · ctrl+e open $EDITOR · esc cancel"
}

func (f *notesForm) apply(m *model) (goal.Goal, error) {
	return m.store.Update(m.ctx, f.id, goal.Edit{Notes: &f.notes})
}
