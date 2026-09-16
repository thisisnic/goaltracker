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

var now = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

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

	res, err := Run(ctx, store, dir, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped || filepath.Base(res.Path) != FileName {
		t.Fatalf("first backup: %+v", res)
	}
	raw, _ := os.ReadFile(res.Path)
	if strings.Contains(string(raw), "secret goal") {
		t.Error("backup is not encrypted")
	}
	firstBackup := append([]byte{}, raw...)

	// Same content again: skipped, file untouched.
	res2, err := Run(ctx, store, dir, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if !res2.Skipped || res2.Path != res.Path {
		t.Errorf("unchanged database was not skipped: %+v", res2)
	}
	if again, _ := os.ReadFile(res.Path); string(again) != string(firstBackup) {
		t.Error("skipped backup rewrote the file")
	}

	// A change replaces the single file.
	if _, err := store.RecordProgress(ctx, g.ID, 5, ""); err != nil {
		t.Fatal(err)
	}
	res3, err := Run(ctx, store, dir, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if res3.Skipped || res3.Path != res.Path {
		t.Errorf("changed database: %+v", res3)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 2 { // lifeo.db.age and the marker, no temp files left
		t.Errorf("backup dir has %v", names)
	}

	// Keep the first backup's bytes as a separate file to restore from.
	old := filepath.Join(root, "old.db.age")
	os.WriteFile(old, firstBackup, 0o600)

	store.Close()
	kept, err := Restore(old, keyFile, dbPath, now)
	if err != nil {
		t.Fatal(err)
	}
	if kept != dbPath+".bak" {
		t.Errorf("kept = %q", kept)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Errorf("previous database not kept: %v", err)
	}
	restored, err := goal.Open(dbPath)
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

	// The .bak still has the progress that was recorded after the first
	// backup, and a second restore does not overwrite it.
	kept2, err := Restore(old, keyFile, dbPath, now)
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

func TestRunNoticesNewKeyAndMissingFile(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dir := filepath.Join(root, "backups")
	r1, _ := NewKey(filepath.Join(root, "a.txt"))
	r2, _ := NewKey(filepath.Join(root, "b.txt"))
	store, _ := goal.Open(filepath.Join(root, "lifeo.db"))
	defer store.Close()

	if res, _ := Run(ctx, store, dir, r1); res.Skipped {
		t.Fatal("first run skipped")
	}
	if res, _ := Run(ctx, store, dir, r2); res.Skipped {
		t.Error("new recipient was skipped; backup would be unreadable by the new key")
	}
	os.Remove(filepath.Join(dir, FileName))
	if res, _ := Run(ctx, store, dir, r2); res.Skipped {
		t.Error("deleted backup file was not rewritten")
	}
}

func TestRestoreKeepsWALWithBak(t *testing.T) {
	root := t.TempDir()
	keyFile := filepath.Join(root, "key.txt")
	recipient, _ := NewKey(keyFile)
	dbPath := filepath.Join(root, "lifeo.db")
	store, _ := goal.Open(dbPath)
	_, _ = store.Add(context.Background(), goal.NewGoal{Statement: "x", Period: "2026"})
	res, err := Run(context.Background(), store, filepath.Join(root, "b"), recipient)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	// Fake uncheckpointed WAL and shm files beside the live database.
	os.WriteFile(dbPath+"-wal", []byte("wal"), 0o600)
	os.WriteFile(dbPath+"-shm", []byte("shm"), 0o600)
	kept, err := Restore(res.Path, keyFile, dbPath, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(kept + suffix); err != nil {
			t.Errorf("%s not kept with the .bak: %v", suffix, err)
		}
		if _, err := os.Stat(dbPath + suffix); err == nil {
			t.Errorf("stale %s left beside the restored database", suffix)
		}
	}
}

func TestRunRejectsBadRecipient(t *testing.T) {
	store, _ := goal.Open(filepath.Join(t.TempDir(), "lifeo.db"))
	defer store.Close()
	if _, err := Run(context.Background(), store, t.TempDir(), "not-a-key"); err == nil {
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
	res, err := Run(ctx, store, filepath.Join(root, "backups"), recipient)
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
	junk := filepath.Join(root, "junk.db.age")
	rcpt, _ := parseRecipient(recipient)
	if err := writeEncrypted(junk, []byte("hello"), rcpt); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "lifeo.db")
	os.WriteFile(dbPath, []byte("keep me"), 0o600)
	if _, err := Restore(junk, keyFile, dbPath, now); err == nil {
		t.Fatal("restored a non-database")
	}
	if b, _ := os.ReadFile(dbPath); string(b) != "keep me" {
		t.Error("live database was touched by a failed restore")
	}
	if _, err := os.Stat(dbPath + ".restore-tmp"); err == nil {
		t.Error("temp file left behind")
	}
}
