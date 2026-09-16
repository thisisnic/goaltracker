package goal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store is the SQLite-backed goal store.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS goals (
	id         INTEGER PRIMARY KEY,
	statement  TEXT NOT NULL,
	why        TEXT NOT NULL DEFAULT '',
	level      TEXT NOT NULL CHECK (level IN ('year','quarter','month')),
	period     TEXT NOT NULL,
	parent_id  INTEGER REFERENCES goals(id) ON DELETE SET NULL,
	kind       TEXT NOT NULL CHECK (kind IN ('numeric','yesno')),
	target     REAL NOT NULL DEFAULT 0,
	unit       TEXT NOT NULL DEFAULT '',
	outcome    TEXT NOT NULL DEFAULT '' CHECK (outcome IN ('','hit','missed')),
	created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS progress (
	id          INTEGER PRIMARY KEY,
	goal_id     INTEGER NOT NULL REFERENCES goals(id) ON DELETE CASCADE,
	value       REAL NOT NULL,
	note        TEXT NOT NULL DEFAULT '',
	recorded_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS progress_goal ON progress(goal_id, recorded_at);
`

// Open opens or creates the database at path, creating parent directories.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// NewGoal is the input to Add.
type NewGoal struct {
	Statement string
	Why       string
	Period    string
	ParentID  *int64
	Target    float64 // > 0 makes the goal numeric
	Unit      string
}

// Add validates and inserts a goal.
func (s *Store) Add(ctx context.Context, in NewGoal) (Goal, error) {
	in.Statement = strings.TrimSpace(in.Statement)
	if in.Statement == "" {
		return Goal{}, errors.New("statement is required")
	}
	period, level, err := ParsePeriod(in.Period)
	if err != nil {
		return Goal{}, err
	}
	if in.Target < 0 {
		return Goal{}, errors.New("target must not be negative")
	}
	kind := YesNo
	if in.Target > 0 {
		kind = Numeric
	}
	if in.ParentID != nil {
		if _, err := s.Get(ctx, *in.ParentID); err != nil {
			return Goal{}, fmt.Errorf("parent %d: %w", *in.ParentID, err)
		}
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO goals (statement, why, level, period, parent_id, kind, target, unit, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Statement, strings.TrimSpace(in.Why), string(level), period, in.ParentID,
		string(kind), in.Target, strings.TrimSpace(in.Unit), now.Format(time.RFC3339))
	if err != nil {
		return Goal{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Goal{}, err
	}
	return s.Get(ctx, id)
}

const selectGoal = `
	SELECT g.id, g.statement, g.why, g.level, g.period, g.parent_id, g.kind, g.target, g.unit, g.outcome, g.created_at,
	       COALESCE((SELECT p.value FROM progress p WHERE p.goal_id = g.id ORDER BY p.recorded_at DESC, p.id DESC LIMIT 1), 0)
	FROM goals g`

func scanGoal(row interface{ Scan(...any) error }) (Goal, error) {
	var g Goal
	var parent sql.NullInt64
	var created string
	err := row.Scan(&g.ID, &g.Statement, &g.Why, &g.Level, &g.Period, &parent, &g.Kind,
		&g.Target, &g.Unit, &g.Outcome, &created, &g.Current)
	if err != nil {
		return Goal{}, err
	}
	if parent.Valid {
		g.ParentID = &parent.Int64
	}
	g.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return g, nil
}

// Get returns one goal by id.
func (s *Store) Get(ctx context.Context, id int64) (Goal, error) {
	g, err := scanGoal(s.db.QueryRowContext(ctx, selectGoal+" WHERE g.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Goal{}, ErrNotFound
	}
	return g, err
}

// Filter narrows List. Zero values mean no filtering.
type Filter struct {
	Year  string
	Level Level
}

// List returns goals ordered by period then id.
func (s *Store) List(ctx context.Context, f Filter) ([]Goal, error) {
	q := selectGoal
	var where []string
	var args []any
	if f.Year != "" {
		where = append(where, "g.period LIKE ?")
		args = append(args, f.Year+"%")
	}
	if f.Level != "" {
		where = append(where, "g.level = ?")
		args = append(args, string(f.Level))
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY g.period, g.id"
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Goal
	for rows.Next() {
		g, err := scanGoal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Edit holds optional changes; nil fields are left alone.
type Edit struct {
	Statement *string
	Why       *string
	Target    *float64
	Unit      *string
	ParentID  **int64 // outer nil = no change; inner nil = clear parent
}

// Update applies an Edit to a goal.
func (s *Store) Update(ctx context.Context, id int64, e Edit) (Goal, error) {
	g, err := s.Get(ctx, id)
	if err != nil {
		return Goal{}, err
	}
	if e.Statement != nil {
		v := strings.TrimSpace(*e.Statement)
		if v == "" {
			return Goal{}, errors.New("statement is required")
		}
		g.Statement = v
	}
	if e.Why != nil {
		g.Why = strings.TrimSpace(*e.Why)
	}
	if e.Target != nil {
		if *e.Target < 0 {
			return Goal{}, errors.New("target must not be negative")
		}
		g.Target = *e.Target
		g.Kind = YesNo
		if g.Target > 0 {
			g.Kind = Numeric
		}
	}
	if e.Unit != nil {
		g.Unit = strings.TrimSpace(*e.Unit)
	}
	if e.ParentID != nil {
		if *e.ParentID != nil {
			pid := **e.ParentID
			if pid == id {
				return Goal{}, errors.New("a goal cannot be its own parent")
			}
			if _, err := s.Get(ctx, pid); err != nil {
				return Goal{}, fmt.Errorf("parent %d: %w", pid, err)
			}
		}
		g.ParentID = *e.ParentID
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE goals SET statement = ?, why = ?, kind = ?, target = ?, unit = ?, parent_id = ? WHERE id = ?`,
		g.Statement, g.Why, string(g.Kind), g.Target, g.Unit, g.ParentID, id)
	if err != nil {
		return Goal{}, err
	}
	return s.Get(ctx, id)
}

// RecordProgress appends a running-total update to a numeric goal.
func (s *Store) RecordProgress(ctx context.Context, id int64, value float64, note string) (Progress, error) {
	g, err := s.Get(ctx, id)
	if err != nil {
		return Progress{}, err
	}
	if g.Kind != Numeric {
		return Progress{}, fmt.Errorf("goal %d is yes/no, not numeric; mark it hit or missed instead", id)
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO progress (goal_id, value, note, recorded_at) VALUES (?, ?, ?, ?)`,
		id, value, strings.TrimSpace(note), now.Format(time.RFC3339))
	if err != nil {
		return Progress{}, err
	}
	pid, err := res.LastInsertId()
	if err != nil {
		return Progress{}, err
	}
	return Progress{ID: pid, GoalID: id, Value: value, Note: strings.TrimSpace(note), RecordedAt: now}, nil
}

// History returns a goal's progress updates, oldest first.
func (s *Store) History(ctx context.Context, id int64) ([]Progress, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, goal_id, value, note, recorded_at FROM progress WHERE goal_id = ? ORDER BY recorded_at, id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Progress
	for rows.Next() {
		var p Progress
		var at string
		if err := rows.Scan(&p.ID, &p.GoalID, &p.Value, &p.Note, &at); err != nil {
			return nil, err
		}
		p.RecordedAt, _ = time.Parse(time.RFC3339, at)
		out = append(out, p)
	}
	return out, rows.Err()
}

// Mark sets a goal's outcome. Unmarked clears it.
func (s *Store) Mark(ctx context.Context, id int64, o Outcome) error {
	res, err := s.db.ExecContext(ctx, `UPDATE goals SET outcome = ? WHERE id = ?`, string(o), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a goal and its progress. Children keep existing but lose
// their parent link.
func (s *Store) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM goals WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
