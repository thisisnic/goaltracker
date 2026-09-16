package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisnic/lifeo/internal/goal"
)

var now = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

// env is a backup setup in a temp dir: a key, a database and options
// pointing at a backup folder with the marker kept outside it.
type env struct {
	root, dbPath, keyFile string
	opts                  Options
	store                 *goal.Store
}

func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	e := &env{
		root:    root,
		dbPath:  filepath.Join(root, "data", "lifeo.db"),
		keyFile: filepath.Join(root, "cfg", "key.txt"),
	}
	recipient, err := NewKey(e.keyFile)
	if err != nil {
		t.Fatal(err)
	}
	e.opts = Options{
		Dir:       filepath.Join(root, "repo"),
		Recipient: recipient,
		Marker:    filepath.Join(root, "cfg", "last-backup"),
	}
	e.store, err = goal.Open(e.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.store.Close() })
	return e
}

func (e *env) run(t *testing.T) Result {
	t.Helper()
	res, err := Run(context.Background(), e.store, e.opts)
	if err != nil {
		t.Fatal(err)
	}
	if !exists(res.Path) {
		t.Fatalf("backup reported %s but it does not exist", res.Path)
	}
	return res
}

func TestNewKey(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "cfg", "key.txt")
	recipient, err := NewKey(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(recipient, "age1") {
		t.Fatalf("recipient = %q", recipient)
	}
	if info, _ := os.Stat(keyFile); info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %o want 600", info.Mode().Perm())
	}
	if _, err := NewKey(keyFile); err == nil {
		t.Error("NewKey overwrote an existing key file")
	}
}

func TestBackupRestoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	g, err := e.store.Add(ctx, goal.NewGoal{Statement: "secret goal", Period: "2026", Target: 10})
	if err != nil {
		t.Fatal(err)
	}

	res := e.run(t)
	if res.Skipped || filepath.Base(res.Path) != FileName {
		t.Fatalf("first backup: %+v", res)
	}
	raw, _ := os.ReadFile(res.Path)
	if strings.Contains(string(raw), "secret goal") {
		t.Error("backup is not encrypted")
	}
	first := append([]byte{}, raw...)

	// Same content again: skipped, file untouched.
	res2 := e.run(t)
	if !res2.Skipped || res2.Path != res.Path {
		t.Errorf("unchanged database was not skipped: %+v", res2)
	}
	if again, _ := os.ReadFile(res.Path); string(again) != string(first) {
		t.Error("skipped backup rewrote the file")
	}

	// A change replaces the single file, and nothing else is in the repo.
	if _, err := e.store.RecordProgress(ctx, g.ID, 5, ""); err != nil {
		t.Fatal(err)
	}
	if res3 := e.run(t); res3.Skipped || res3.Path != res.Path {
		t.Errorf("changed database: %+v", res3)
	}
	entries, _ := os.ReadDir(e.opts.Dir)
	if len(entries) != 1 || entries[0].Name() != FileName {
		var names []string
		for _, en := range entries {
			names = append(names, en.Name())
		}
		t.Errorf("repo folder has %v, want only %s", names, FileName)
	}

	// Restore the first backup over the live database.
	old := filepath.Join(e.root, "old.db.age")
	os.WriteFile(old, first, 0o600)
	e.store.Close()
	kept, err := Restore(old, e.keyFile, e.dbPath, now)
	if err != nil {
		t.Fatal(err)
	}
	if kept != e.dbPath+".bak" {
		t.Errorf("kept = %q", kept)
	}
	restored, err := goal.Open(e.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := restored.Get(ctx, g.ID)
	restored.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got.Statement != "secret goal" || got.Current != 0 {
		t.Errorf("restored goal = %+v, want the pre-progress state", got)
	}

	// A second restore keeps to a new name, and the first .bak still holds
	// the progress recorded after the first backup.
	kept2, err := Restore(old, e.keyFile, e.dbPath, now)
	if err != nil {
		t.Fatal(err)
	}
	if kept2 == kept || !strings.Contains(kept2, "20260916-120000") {
		t.Errorf("second restore kept to %q, first was %q", kept2, kept)
	}
	bak, err := goal.Open(kept)
	if err != nil {
		t.Fatal(err)
	}
	defer bak.Close()
	if b, _ := bak.Get(ctx, g.ID); b.Current != 5 {
		t.Errorf("first .bak lost data: %+v", b)
	}
}

func TestRunNoticesNewKeyMissingOrSwappedFile(t *testing.T) {
	e := newEnv(t)
	if res := e.run(t); res.Skipped {
		t.Fatal("first run skipped")
	}

	// A new recipient must produce a new file the new key can open.
	r2, err := NewKey(filepath.Join(e.root, "b.txt"))
	if err != nil {
		t.Fatal(err)
	}
	e.opts.Recipient = r2
	if res := e.run(t); res.Skipped {
		t.Error("new recipient was skipped; backup would be unreadable by the new key")
	}

	// A deleted backup file is rewritten.
	os.Remove(filepath.Join(e.opts.Dir, FileName))
	if res := e.run(t); res.Skipped {
		t.Error("deleted backup file was not rewritten")
	}

	// A file swapped in from git history is not trusted either.
	if err := os.WriteFile(filepath.Join(e.opts.Dir, FileName), []byte("something else"), 0o600); err != nil {
		t.Fatal(err)
	}
	if res := e.run(t); res.Skipped {
		t.Error("swapped backup file was not rewritten")
	}

	// Stale temp files from a killed run are cleaned up.
	stale := filepath.Join(e.opts.Dir, ".lifeo-backup-123.tmp")
	os.WriteFile(stale, []byte("partial"), 0o600)
	e.run(t)
	if exists(stale) {
		t.Error("stale temp file left in the repo folder")
	}

	// Without a marker every run writes.
	e.opts.Marker = ""
	if res := e.run(t); res.Skipped {
		t.Error("run without marker was skipped")
	}
}

func TestRestoreKeepsWALWithBak(t *testing.T) {
	e := newEnv(t)
	if _, err := e.store.Add(context.Background(), goal.NewGoal{Statement: "x", Period: "2026"}); err != nil {
		t.Fatal(err)
	}
	res := e.run(t)
	e.store.Close()

	// Fake uncheckpointed WAL and shm files beside the live database.
	os.WriteFile(e.dbPath+"-wal", []byte("wal"), 0o600)
	os.WriteFile(e.dbPath+"-shm", []byte("shm"), 0o600)
	kept, err := Restore(res.Path, e.keyFile, e.dbPath, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sidecars {
		if !exists(kept + s) {
			t.Errorf("%s not kept with the .bak", s)
		}
		if exists(e.dbPath + s) {
			t.Errorf("stale %s left beside the restored database", s)
		}
	}

	// Leftover .bak sidecars count as the name being taken.
	os.Remove(kept)
	kept2, err := Restore(res.Path, e.keyFile, e.dbPath, now)
	if err != nil {
		t.Fatal(err)
	}
	if kept2 == kept {
		t.Errorf("restore reused %s although its WAL files were still there", kept)
	}

	// With no live database, stale sidecars are removed, not paired.
	os.Remove(e.dbPath)
	os.WriteFile(e.dbPath+"-wal", []byte("wal"), 0o600)
	kept3, err := Restore(res.Path, e.keyFile, e.dbPath, now)
	if err != nil {
		t.Fatal(err)
	}
	if kept3 != "" || exists(e.dbPath+"-wal") {
		t.Errorf("no-live-db restore: kept=%q wal exists=%v", kept3, exists(e.dbPath+"-wal"))
	}
}

func TestRestoreUndoOnFailure(t *testing.T) {
	e := newEnv(t)
	if _, err := e.store.Add(context.Background(), goal.NewGoal{Statement: "keep", Period: "2026"}); err != nil {
		t.Fatal(err)
	}
	res := e.run(t)
	e.store.Close()
	os.WriteFile(e.dbPath+"-wal", []byte("wal"), 0o600)
	before, _ := os.ReadFile(e.dbPath)

	// Fail the final swap only: the temp file moving into place.
	tmp := e.dbPath + ".restore-tmp"
	rename = func(from, to string) error {
		if from == tmp {
			return errors.New("disk on fire")
		}
		return os.Rename(from, to)
	}
	t.Cleanup(func() { rename = os.Rename })

	kept, err := Restore(res.Path, e.keyFile, e.dbPath, now)
	if err == nil || !strings.Contains(err.Error(), "disk on fire") {
		t.Fatalf("err = %v", err)
	}
	if kept != "" {
		t.Errorf("kept = %q after a rolled-back restore", kept)
	}
	after, _ := os.ReadFile(e.dbPath)
	if string(after) != string(before) {
		t.Error("the original database was not put back")
	}
	if !exists(e.dbPath + "-wal") {
		t.Error("the WAL was not put back")
	}
	for _, leftover := range []string{e.dbPath + ".bak", e.dbPath + ".bak-wal", e.dbPath + ".restore-tmp"} {
		if exists(leftover) {
			t.Errorf("%s left behind after undo", leftover)
		}
	}

	// If the undo itself fails, the error says where the data is.
	rename = func(from, to string) error {
		if from == tmp {
			return errors.New("disk on fire")
		}
		if strings.HasPrefix(from, e.dbPath+".bak") {
			return errors.New("still on fire")
		}
		return os.Rename(from, to)
	}
	kept, err = Restore(res.Path, e.keyFile, e.dbPath, now)
	if err == nil || !strings.Contains(err.Error(), "Your data is at") {
		t.Fatalf("err = %v", err)
	}
	if kept == "" || !exists(kept) {
		t.Errorf("kept = %q, want the path holding the data", kept)
	}
}

func TestRunRejectsBadRecipient(t *testing.T) {
	e := newEnv(t)
	e.opts.Recipient = "not-a-key"
	if _, err := Run(context.Background(), e.store, e.opts); err == nil {
		t.Error("bad recipient accepted")
	}
}

func TestDecryptWrongKey(t *testing.T) {
	e := newEnv(t)
	res := e.run(t)
	other := filepath.Join(e.root, "b.txt")
	if _, err := NewKey(other); err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt(res.Path, other); err == nil {
		t.Error("decrypted with the wrong key")
	}
	if _, err := Decrypt(res.Path, filepath.Join(e.root, "missing.txt")); err == nil {
		t.Error("decrypted with no key")
	}
}

func TestRestoreRefusesNonDatabase(t *testing.T) {
	e := newEnv(t)
	junk := filepath.Join(e.root, "junk.db.age")
	rcpt, err := parseRecipient(e.opts.Recipient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeEncrypted(junk, []byte("hello"), rcpt); err != nil {
		t.Fatal(err)
	}
	e.store.Close()
	os.WriteFile(e.dbPath, []byte("keep me"), 0o600)
	if _, err := Restore(junk, e.keyFile, e.dbPath, now); err == nil {
		t.Fatal("restored a non-database")
	}
	if b, _ := os.ReadFile(e.dbPath); string(b) != "keep me" {
		t.Error("live database was touched by a failed restore")
	}
	if exists(e.dbPath + ".restore-tmp") {
		t.Error("temp file left behind")
	}
}
