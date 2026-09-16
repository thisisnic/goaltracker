// Package backup writes and restores an encrypted copy of the database.
//
// The backup is a consistent copy of the SQLite file, encrypted with age to
// the user's public key and written as a single file, goaltracker.db.age, in a
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

	"github.com/thisisnic/goaltracker/internal/goal"
)

// FileName is the encrypted backup's name inside the backup folder.
const FileName = "goaltracker.db.age"

// tmpPattern names the temporary file a backup is written to before being
// renamed into place. It lives in the backup folder so the rename is atomic.
const tmpPattern = ".goaltracker-backup-*.tmp"

// sqliteMagic starts every SQLite database file.
const sqliteMagic = "SQLite format 3\x00"

// Options say where a backup goes and how to tell it is unchanged.
type Options struct {
	// Dir is the backup folder.
	Dir string
	// Recipient is the age public key to encrypt to.
	Recipient string
	// Marker is a file, kept outside Dir, recording what the last backup
	// contained so an unchanged database is not rewritten. Empty disables
	// skipping.
	Marker string
}

// Result says what a backup did.
type Result struct {
	Path    string // the file written, or the existing one when skipped
	Skipped bool   // true when the database was unchanged since the last backup
}

// Run writes an encrypted copy of store to Dir/FileName, replacing any
// previous one. It returns Skipped when the database content and recipient
// match the marker and the backup file on disk is still the one the marker
// describes.
func Run(ctx context.Context, store *goal.Store, o Options) (Result, error) {
	rcpt, err := parseRecipient(o.Recipient)
	if err != nil {
		return Result{}, fmt.Errorf("backup recipient: %w", err)
	}
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		return Result{}, err
	}
	removeStaleTemps(o.Dir)

	tmp, err := os.MkdirTemp("", "goaltracker-snapshot-")
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

	out := filepath.Join(o.Dir, FileName)
	plainHash := hashOf(plain)
	if o.Marker != "" {
		if m, err := readMarker(o.Marker); err == nil && m.plain == plainHash && m.recipient == rcpt.String() {
			if cur, err := os.ReadFile(out); err == nil && hashOf(cur) == m.cipher {
				return Result{Path: out, Skipped: true}, nil
			}
		}
	}

	cipherHash, err := writeEncrypted(out, plain, rcpt)
	if err != nil {
		return Result{}, err
	}
	if o.Marker != "" {
		m := marker{plain: plainHash, recipient: rcpt.String(), cipher: cipherHash}
		if err := m.write(o.Marker); err != nil {
			return Result{}, err
		}
	}
	return Result{Path: out}, nil
}

// marker is what the last backup contained: a hash of the database, the
// recipient it was encrypted to, and a hash of the encrypted file.
type marker struct {
	plain, recipient, cipher string
}

func readMarker(path string) (marker, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return marker{}, err
	}
	f := strings.Fields(string(b))
	if len(f) != 3 {
		return marker{}, errors.New("malformed marker")
	}
	return marker{plain: f[0], recipient: f[1], cipher: f[2]}, nil
}

func (m marker) write(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(m.plain+" "+m.recipient+" "+m.cipher+"\n"), 0o600)
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// staleAfter is how old a temp file must be before it is treated as left
// behind by a killed run rather than in use by a concurrent one.
const staleAfter = 10 * time.Minute

// removeStaleTemps deletes old temporary files left by a backup that was
// killed part way, so they never end up committed to the data repo. Recent
// ones may belong to another backup still running and are left alone.
func removeStaleTemps(dir string) {
	matches, _ := filepath.Glob(filepath.Join(dir, tmpPattern))
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil && time.Since(info.ModTime()) > staleAfter {
			os.Remove(m)
		}
	}
}

// MarkerPath is where the skip marker for a given database and backup
// folder goes inside stateDir, so different databases or folders sharing a
// config directory do not overwrite each other's marker.
func MarkerPath(stateDir, dbPath, dir string) string {
	h := hashOf([]byte(canonical(dbPath) + "\x00" + canonical(dir)))
	return filepath.Join(stateDir, "last-backup-"+h[:12])
}

// canonical makes a path absolute and follows symlinks, so the same file
// reached by different spellings shares one marker. When the path does not
// exist yet, the nearest existing parent is resolved and the rest appended,
// so a marker made before the folder is created matches later ones. A
// dangling symlink counts as not existing and is not followed; once its
// target appears the marker changes, which costs one extra backup and
// nothing else.
func canonical(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	p = filepath.Clean(p)
	var rest []string
	for dir := p; ; dir = filepath.Dir(dir) {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			parts := append([]string{real}, rest...)
			return filepath.Join(parts...)
		}
		if parent := filepath.Dir(dir); parent == dir {
			return p
		}
		rest = append([]string{filepath.Base(dir)}, rest...)
	}
}

// writeEncrypted encrypts plain to a temporary file beside path and renames
// it into place, so a failure part way leaves no partial file and a reader
// never sees a half-written one. It returns the hash of the encrypted file.
func writeEncrypted(path string, plain []byte, rcpt age.Recipient) (cipherHash string, err error) {
	f, err := os.CreateTemp(filepath.Dir(path), tmpPattern)
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if err = f.Chmod(0o600); err != nil {
		return "", err
	}
	h := sha256.New()
	w, err := age.Encrypt(io.MultiWriter(f, h), rcpt)
	if err != nil {
		return "", err
	}
	if _, err = w.Write(plain); err != nil {
		return "", err
	}
	if err = w.Close(); err != nil {
		return "", err
	}
	if err = f.Sync(); err != nil {
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	if err = os.Rename(tmp, path); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
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

// sidecars are the files SQLite keeps beside a database in WAL mode. They
// are named after the database, so moving them with the same suffix keeps
// them paired with it.
var sidecars = []string{"-wal", "-shm"}

// rename is os.Rename, swapped out by tests to make a step of Restore fail.
var rename = os.Rename

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

	if !exists(dbPath) {
		// No live database. A WAL file without one is abnormal: it may hold
		// commits from a database that a failed restore left elsewhere, and
		// SQLite would apply it to the restored file. Refuse rather than
		// delete or pair it. A lone shm file is only a rebuildable index,
		// so it is simply removed.
		if exists(dbPath + "-wal") {
			os.Remove(tmp)
			return "", fmt.Errorf("%s-wal exists but %s does not; a previous restore may have failed. Put the database back beside it, or move the file away, before restoring", dbPath, dbPath)
		}
		if err := os.Remove(dbPath + "-shm"); err != nil && !os.IsNotExist(err) {
			os.Remove(tmp)
			return "", fmt.Errorf("remove stale %s-shm: %w", dbPath, err)
		}
	} else {
		kept = dbPath + ".bak"
		if anyExists(kept, sidecars) {
			kept = dbPath + "." + now.UTC().Format("20060102-150405") + ".bak"
		}
		if anyExists(kept, sidecars) {
			os.Remove(tmp)
			return "", fmt.Errorf("%s already exists; move it aside first", kept)
		}
		if err := rename(dbPath, kept); err != nil {
			os.Remove(tmp)
			return "", err
		}
		for _, s := range sidecars {
			if exists(dbPath + s) {
				if err := rename(dbPath+s, kept+s); err != nil {
					os.Remove(tmp)
					return undo(dbPath, kept, err)
				}
			}
		}
	}
	if err := rename(tmp, dbPath); err != nil {
		os.Remove(tmp)
		if kept != "" {
			return undo(dbPath, kept, err)
		}
		return "", err
	}
	return kept, nil
}

// undo moves a kept database and its WAL files back to dbPath after a
// failed restore. The WAL files go first. If the database itself cannot go
// back, the WAL files are gathered beside it at kept so the data stays
// together, and the error names every file that is really there.
func undo(dbPath, kept string, cause error) (string, error) {
	for _, s := range sidecars {
		if !exists(kept + s) {
			continue
		}
		if err := rename(kept+s, dbPath+s); err != nil {
			return kept, regroup(dbPath, kept, fmt.Errorf("%w; and could not put the database back: %v", cause, err))
		}
	}
	if err := rename(kept, dbPath); err != nil {
		return kept, regroup(dbPath, kept, fmt.Errorf("%w; and could not put the database back: %v", cause, err))
	}
	return "", cause
}

// regroup gathers any WAL files still at dbPath beside the database at
// kept, then lists where every remaining file is.
func regroup(dbPath, kept string, cause error) error {
	for _, s := range sidecars {
		if exists(dbPath + s) {
			rename(dbPath+s, kept+s)
		}
	}
	var locations []string
	for _, s := range append([]string{""}, sidecars...) {
		for _, base := range []string{kept, dbPath} {
			if exists(base + s) {
				locations = append(locations, base+s)
			}
		}
	}
	return fmt.Errorf("%w. Your data is at %s", cause, strings.Join(locations, " and "))
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// anyExists reports whether path or any of path+suffix exists.
func anyExists(path string, suffixes []string) bool {
	if exists(path) {
		return true
	}
	for _, s := range suffixes {
		if exists(path + s) {
			return true
		}
	}
	return false
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
