package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisnic/lifeo/internal/goal"
)

func TestBackupRestoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "data", "lifeo.db")
	keyFile := filepath.Join(root, "cfg", "key.txt")
	dir := filepath.Join(root, "backups")

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

	store, err := goal.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	g, err := store.Add(ctx, goal.NewGoal{Statement: "secret goal", Period: "2026", Target: 10})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	res, err := Run(ctx, store, dir, recipient, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped || filepath.Base(res.Path) != "lifeo-20260916-120000.db.age" {
		t.Fatalf("first backup: %+v", res)
	}
	raw, _ := os.ReadFile(res.Path)
	if strings.Contains(string(raw), "secret goal") {
		t.Error("snapshot is not encrypted")
	}

	// Same content again: skipped.
	res2, err := Run(ctx, store, dir, recipient, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !res2.Skipped {
		t.Errorf("unchanged database produced a new snapshot: %+v", res2)
	}

	// A change produces a new file.
	if _, err := store.RecordProgress(ctx, g.ID, 5, ""); err != nil {
		t.Fatal(err)
	}
	res3, err := Run(ctx, store, dir, recipient, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if res3.Skipped {
		t.Error("changed database was skipped")
	}
	files, _ := List(dir)
	if len(files) != 2 {
		t.Errorf("List = %v want 2 files", files)
	}

	// Restore the first snapshot over the live database.
	store.Close()
	bak, err := Restore(res.Path, keyFile, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if bak != dbPath+".bak" {
		t.Errorf("bak = %q", bak)
	}
	if _, err := os.Stat(bak); err != nil {
		t.Errorf("previous database not kept: %v", err)
	}
	restored, err := goal.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	got, err := restored.Get(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Statement != "secret goal" || got.Current != 0 {
		t.Errorf("restored goal = %+v, want the pre-progress state", got)
	}
}

func TestRunRejectsBadRecipient(t *testing.T) {
	store, _ := goal.Open(filepath.Join(t.TempDir(), "lifeo.db"))
	defer store.Close()
	if _, err := Run(context.Background(), store, t.TempDir(), "not-a-key", time.Now()); err == nil {
		t.Error("bad recipient accepted")
	}
}

func TestDecryptWrongKey(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	recipient, _ := NewKey(filepath.Join(root, "a.txt"))
	_, _ = NewKey(filepath.Join(root, "b.txt"))
	store, _ := goal.Open(filepath.Join(root, "lifeo.db"))
	defer store.Close()
	res, err := Run(ctx, store, filepath.Join(root, "backups"), recipient, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt(res.Path, filepath.Join(root, "b.txt")); err == nil {
		t.Error("decrypted with the wrong key")
	}
	if _, err := Decrypt(res.Path, filepath.Join(root, "missing.txt")); err == nil {
		t.Error("decrypted with no key")
	}
}

func TestRestoreRefusesNonDatabase(t *testing.T) {
	root := t.TempDir()
	keyFile := filepath.Join(root, "key.txt")
	recipient, _ := NewKey(keyFile)
	// Encrypt something that is not SQLite.
	junk := filepath.Join(root, "junk.db.age")
	rcpt, _ := parseRecipient(recipient)
	if err := writeEncrypted(junk, []byte("hello"), rcpt); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "lifeo.db")
	os.WriteFile(dbPath, []byte("keep me"), 0o600)
	if _, err := Restore(junk, keyFile, dbPath); err == nil {
		t.Fatal("restored a non-database")
	}
	if b, _ := os.ReadFile(dbPath); string(b) != "keep me" {
		t.Error("live database was touched by a failed restore")
	}
}
