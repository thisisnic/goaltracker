// Package backup writes and restores encrypted snapshots of the database.
//
// A snapshot is a consistent copy of the SQLite file, encrypted with age to
// the user's public key and written to a folder of their choosing, typically
// a private git repo. Restoring needs the matching private key.
package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/thisisnic/lifeo/internal/goal"
)

// Suffix is the file extension of an encrypted snapshot.
const Suffix = ".db.age"

// markerFile records the hash of the last snapshot so an unchanged database
// does not produce a new file every time the TUI exits.
const markerFile = ".last-snapshot"

// sqliteMagic starts every SQLite database file.
const sqliteMagic = "SQLite format 3\x00"

// Result says what a backup did.
type Result struct {
	Path    string // the file written, empty if skipped
	Skipped bool   // true when the database was unchanged since the last snapshot
}

// Run writes an encrypted snapshot of store into dir. It returns Skipped
// when the database content matches the previous snapshot.
func Run(ctx context.Context, store *goal.Store, dir, recipient string, now time.Time) (Result, error) {
	rcpt, err := age.ParseX25519Recipient(strings.TrimSpace(recipient))
	if err != nil {
		return Result{}, fmt.Errorf("backup recipient: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Result{}, err
	}

	tmp, err := os.MkdirTemp("", "lifeo-snapshot-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(tmp)
	snap := filepath.Join(tmp, "snapshot.db")
	if err := store.SnapshotTo(ctx, snap); err != nil {
		return Result{}, err
	}
	plain, err := os.ReadFile(snap)
	if err != nil {
		return Result{}, err
	}

	sum := sha256.Sum256(plain)
	hash := hex.EncodeToString(sum[:])
	marker := filepath.Join(dir, markerFile)
	if last, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(last)) == hash {
		return Result{Skipped: true}, nil
	}

	name := "lifeo-" + now.UTC().Format("20060102-150405") + Suffix
	out := filepath.Join(dir, name)
	if err := writeEncrypted(out, plain, rcpt); err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(marker, []byte(hash+"\n"), 0o600); err != nil {
		return Result{}, err
	}
	return Result{Path: out}, nil
}

func writeEncrypted(path string, plain []byte, rcpt age.Recipient) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	w, err := age.Encrypt(f, rcpt)
	if err != nil {
		f.Close()
		return err
	}
	if _, err := w.Write(plain); err != nil {
		f.Close()
		return err
	}
	if err := w.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Decrypt reads an encrypted snapshot using the identities in identityFile
// and checks the result is a SQLite database.
func Decrypt(snapshot, identityFile string) ([]byte, error) {
	idf, err := os.Open(identityFile)
	if err != nil {
		return nil, fmt.Errorf("identity file: %w", err)
	}
	defer idf.Close()
	ids, err := age.ParseIdentities(idf)
	if err != nil {
		return nil, fmt.Errorf("identity file %s: %w", identityFile, err)
	}
	f, err := os.Open(snapshot)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r, err := age.Decrypt(f, ids...)
	if err != nil {
		return nil, fmt.Errorf("decrypt %s: %w", snapshot, err)
	}
	plain, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("decrypt %s: %w", snapshot, err)
	}
	if !bytes.HasPrefix(plain, []byte(sqliteMagic)) {
		return nil, errors.New("decrypted file is not a SQLite database")
	}
	return plain, nil
}

// Restore replaces the database at dbPath with the decrypted snapshot. The
// current database, if any, is kept beside it as dbPath + ".bak". The
// database must not be open in another process while this runs.
func Restore(snapshot, identityFile, dbPath string) (backup string, err error) {
	plain, err := Decrypt(snapshot, identityFile)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return "", err
	}
	tmp := dbPath + ".restore-tmp"
	if err := os.WriteFile(tmp, plain, 0o600); err != nil {
		return "", err
	}
	if _, err := os.Stat(dbPath); err == nil {
		backup = dbPath + ".bak"
		if err := os.Rename(dbPath, backup); err != nil {
			os.Remove(tmp)
			return "", err
		}
	}
	// Stale WAL and shm files would be applied to the restored database.
	os.Remove(dbPath + "-wal")
	os.Remove(dbPath + "-shm")
	if err := os.Rename(tmp, dbPath); err != nil {
		return backup, err
	}
	return backup, nil
}

// NewKey generates an age keypair, writes the private key to identityFile
// (which must not exist) with owner-only permissions, and returns the
// public key to put in the config.
func NewKey(identityFile string) (recipient string, err error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(identityFile), 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(identityFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("write key: %w", err)
	}
	fmt.Fprintf(f, "# created: %s\n# public key: %s\n%s\n",
		time.Now().UTC().Format(time.RFC3339), id.Recipient(), id)
	if err := f.Close(); err != nil {
		return "", err
	}
	return id.Recipient().String(), nil
}

// List returns the snapshot files in dir, oldest first.
func List(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), Suffix) {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out, nil
}

func parseRecipient(s string) (age.Recipient, error) {
	return age.ParseX25519Recipient(strings.TrimSpace(s))
}
