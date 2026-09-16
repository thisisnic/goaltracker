// Package backup writes and restores an encrypted copy of the database.
//
// The backup is a consistent copy of the SQLite file, encrypted with age to
// the user's public key and written as a single file, lifeo.db.age, in a
// folder of their choosing, typically a private git repo. Each backup
// replaces the previous file; git holds the history. Restoring needs the
// matching private key.
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

// FileName is the encrypted backup's name inside the backup folder.
const FileName = "lifeo.db.age"

// markerFile records the content hash and recipient of the last backup so
// an unchanged database does not produce a new file, and a change of key or
// a deleted backup file is noticed.
const markerFile = ".last-backup"

// sqliteMagic starts every SQLite database file.
const sqliteMagic = "SQLite format 3\x00"

// Result says what a backup did.
type Result struct {
	Path    string // the file written, or the existing one when skipped
	Skipped bool   // true when the database was unchanged since the last backup
}

// Run writes an encrypted copy of store to dir/FileName, replacing any
// previous one. It returns Skipped when the database content, the
// recipient and the backup file are all unchanged since the last run.
func Run(ctx context.Context, store *goal.Store, dir, recipient string) (Result, error) {
	rcpt, err := parseRecipient(recipient)
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
	stamp := hex.EncodeToString(sum[:]) + " " + rcpt.String()
	out := filepath.Join(dir, FileName)
	marker := filepath.Join(dir, markerFile)
	if last, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(last)) == stamp {
		if _, err := os.Stat(out); err == nil {
			return Result{Path: out, Skipped: true}, nil
		}
	}

	if err := writeEncrypted(out, plain, rcpt); err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(marker, []byte(stamp+"\n"), 0o600); err != nil {
		return Result{}, err
	}
	return Result{Path: out}, nil
}

// writeEncrypted encrypts plain to a temporary file beside path and renames
// it into place, so a failure part way leaves no partial file and a reader
// never sees a half-written one.
func writeEncrypted(path string, plain []byte, rcpt age.Recipient) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".lifeo-backup-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if err = f.Chmod(0o600); err != nil {
		return err
	}
	w, err := age.Encrypt(f, rcpt)
	if err != nil {
		return err
	}
	if _, err = w.Write(plain); err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Decrypt reads an encrypted backup using the identities in identityFile
// and checks the result is a SQLite database.
func Decrypt(backupFile, identityFile string) ([]byte, error) {
	idf, err := os.Open(identityFile)
	if err != nil {
		return nil, fmt.Errorf("identity file: %w", err)
	}
	defer idf.Close()
	ids, err := age.ParseIdentities(idf)
	if err != nil {
		return nil, fmt.Errorf("identity file %s: %w", identityFile, err)
	}
	f, err := os.Open(backupFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r, err := age.Decrypt(f, ids...)
	if err != nil {
		return nil, fmt.Errorf("decrypt %s: %w", backupFile, err)
	}
	plain, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("decrypt %s: %w", backupFile, err)
	}
	if !bytes.HasPrefix(plain, []byte(sqliteMagic)) {
		return nil, errors.New("decrypted file is not a SQLite database")
	}
	return plain, nil
}

// Restore replaces the database at dbPath with the decrypted backup. The
// current database, if any, is kept beside it as dbPath + ".bak", or a
// timestamped .bak when one already exists, together with its WAL files so
// nothing uncheckpointed is lost. If the swap fails the current database is
// put back. The database must not be open in another process.
func Restore(backupFile, identityFile, dbPath string, now time.Time) (kept string, err error) {
	plain, err := Decrypt(backupFile, identityFile)
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
		kept = dbPath + ".bak"
		if _, err := os.Stat(kept); err == nil {
			kept = dbPath + "." + now.UTC().Format("20060102-150405") + ".bak"
		}
		if _, err := os.Stat(kept); err == nil {
			os.Remove(tmp)
			return "", fmt.Errorf("%s already exists; move it aside first", kept)
		}
		if err := os.Rename(dbPath, kept); err != nil {
			os.Remove(tmp)
			return "", err
		}
		// SQLite names the WAL and shm files after the database, so moving
		// them with the same suffix keeps them usable with the .bak.
		for _, suffix := range []string{"-wal", "-shm"} {
			if _, err := os.Stat(dbPath + suffix); err == nil {
				if err := os.Rename(dbPath+suffix, kept+suffix); err != nil {
					rollback(dbPath, kept)
					os.Remove(tmp)
					return "", err
				}
			}
		}
	}
	if err := os.Rename(tmp, dbPath); err != nil {
		if kept != "" {
			rollback(dbPath, kept)
		}
		os.Remove(tmp)
		return "", err
	}
	return kept, nil
}

// rollback moves a kept database and its WAL files back to dbPath.
func rollback(dbPath, kept string) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(kept + suffix); err == nil {
			os.Rename(kept+suffix, dbPath+suffix)
		}
	}
}

// NewKey generates an age keypair, writes the private key to identityFile
// (which must not exist) with owner-only permissions, and returns the
// public key to put in the config. A failed write leaves no file behind.
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
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(identityFile)
		}
	}()
	if _, err = fmt.Fprintf(f, "# created: %s\n# public key: %s\n%s\n",
		time.Now().UTC().Format(time.RFC3339), id.Recipient(), id); err != nil {
		return "", fmt.Errorf("write key: %w", err)
	}
	if err = f.Close(); err != nil {
		return "", fmt.Errorf("write key: %w", err)
	}
	return id.Recipient().String(), nil
}

func parseRecipient(s string) (*age.X25519Recipient, error) {
	return age.ParseX25519Recipient(strings.TrimSpace(s))
}
