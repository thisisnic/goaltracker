// Package update replaces the running binary with the latest GitHub
// release, after checking the download against the release's checksums.
package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

// Repo is the GitHub repository releases are fetched from.
const Repo = "thisisnic/goaltracker"

// APIBase is the GitHub API root. Tests point it at a local server.
var APIBase = "https://api.github.com"

// maxDownload bounds how much is read from the network for one asset.
const maxDownload = 200 << 20

// Result says what Update did.
type Result struct {
	Current string // version running before
	Latest  string // latest release tag, without the leading v
	State   State  // how Current compares with Latest
	Updated bool   // false when already current or when only checking
	Path    string // the binary that was replaced
}

// State is how the running version compares with the latest release.
type State int

const (
	// Older means the latest release is newer than the running build.
	Older State = iota
	// Current means the running build is the latest release.
	Current
	// Newer means the running build is ahead of the latest release, for
	// example a build from main.
	Newer
	// Unknown means the running version is not a release version, such
	// as a dev build, so the two cannot be compared.
	Unknown
)

// ErrNotRelease is returned when the running build's version cannot be
// compared with a release and Force was not given.
var ErrNotRelease = errors.New("not a release build")

// compare works out State from two version strings. Build metadata such
// as +dirty is ignored, as semver says it should be.
func compare(current, latest string) State {
	c, l := "v"+strings.TrimPrefix(current, "v"), "v"+strings.TrimPrefix(latest, "v")
	if !semver.IsValid(c) || !semver.IsValid(l) {
		return Unknown
	}
	switch semver.Compare(c, l) {
	case -1:
		return Older
	case 1:
		return Newer
	}
	return Current
}

// Options adjust Update.
type Options struct {
	Current string // the running version, as reported by version.String()
	Check   bool   // only report whether an update exists
	Force   bool   // install even when the versions match
	// Executable overrides the binary to replace; tests use it.
	Executable string
	// OS and Arch override runtime.GOOS and runtime.GOARCH; tests use them.
	OS, Arch string
}

type release struct {
	TagName string  `json:"tag_name"`
	Assets  []asset `json:"assets"`
}

type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// Update fetches the latest release and, unless Check is set or it is
// already running, installs it over the current executable.
func Update(ctx context.Context, o Options) (Result, error) {
	if o.OS == "" {
		o.OS = runtime.GOOS
	}
	if o.Arch == "" {
		o.Arch = runtime.GOARCH
	}
	client := &http.Client{Timeout: 2 * time.Minute}

	rel, err := latest(ctx, client)
	if err != nil {
		return Result{}, err
	}
	res := Result{
		Current: strings.TrimPrefix(o.Current, "v"),
		Latest:  strings.TrimPrefix(rel.TagName, "v"),
	}
	res.State = compare(res.Current, res.Latest)
	if o.Check {
		return res, nil
	}
	if !o.Force {
		switch res.State {
		case Current, Newer:
			return res, nil
		case Unknown:
			return res, fmt.Errorf("%w: running %q, which is not a release version; pass --force to install %s", ErrNotRelease, res.Current, res.Latest)
		}
	}

	exe := o.Executable
	if exe == "" {
		exe, err = os.Executable()
		if err != nil {
			return res, err
		}
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
	}
	res.Path = exe

	name := archiveName(res.Latest, o.OS, o.Arch)
	archiveURL, sumsURL := "", ""
	for _, a := range rel.Assets {
		switch a.Name {
		case name:
			archiveURL = a.URL
		case "checksums.txt":
			sumsURL = a.URL
		}
	}
	if archiveURL == "" {
		return res, fmt.Errorf("release %s has no build for %s/%s (looked for %s)", rel.TagName, o.OS, o.Arch, name)
	}
	if sumsURL == "" {
		return res, fmt.Errorf("release %s has no checksums.txt; refusing to install unverified", rel.TagName)
	}

	sums, err := fetch(ctx, client, sumsURL)
	if err != nil {
		return res, fmt.Errorf("checksums: %w", err)
	}
	want, err := checksumFor(sums, name)
	if err != nil {
		return res, err
	}
	archive, err := fetch(ctx, client, archiveURL)
	if err != nil {
		return res, fmt.Errorf("download: %w", err)
	}
	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); got != want {
		return res, fmt.Errorf("%s does not match its published checksum; not installing", name)
	}

	bin, err := extractBinary(archive, name, binaryName(o.OS))
	if err != nil {
		return res, err
	}
	if err := replace(exe, bin); err != nil {
		return res, err
	}
	res.Updated = true
	return res, nil
}

func latest(ctx context.Context, client *http.Client) (release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, APIBase+"/repos/"+Repo+"/releases/latest", nil)
	if err != nil {
		return release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "goaltracker-update")
	resp, err := client.Do(req)
	if err != nil {
		return release{}, fmt.Errorf("fetch latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return release{}, fmt.Errorf("fetch latest release: GitHub answered %s", resp.Status)
	}
	var rel release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return release{}, fmt.Errorf("fetch latest release: %w", err)
	}
	if rel.TagName == "" {
		return release{}, errors.New("fetch latest release: no tag in response")
	}
	return rel, nil
}

func fetch(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "goaltracker-update")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxDownload))
}

func archiveName(version, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return "goaltracker_" + version + "_" + goos + "_" + goarch + ext
}

func binaryName(goos string) string {
	if goos == "windows" {
		return "goaltracker.exe"
	}
	return "goaltracker"
}

// checksumFor finds name's SHA-256 in a goreleaser checksums.txt, whose
// lines are "<hex>  <file>".
func checksumFor(sums []byte, name string) (string, error) {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("checksums.txt has no entry for %s", name)
}

// extractBinary pulls the named binary out of a tar.gz or zip archive.
func extractBinary(archive []byte, archiveName, binary string) ([]byte, error) {
	if strings.HasSuffix(archiveName, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", archiveName, err)
		}
		for _, f := range zr.File {
			if filepath.Base(f.Name) == binary && !f.FileInfo().IsDir() {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(io.LimitReader(rc, maxDownload))
			}
		}
		return nil, fmt.Errorf("%s does not contain %s", archiveName, binary)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", archiveName, err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s does not contain %s", archiveName, binary)
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", archiveName, err)
		}
		if hdr.Typeflag == tar.TypeReg && filepath.Base(hdr.Name) == binary {
			return io.ReadAll(io.LimitReader(tr, maxDownload))
		}
	}
}

// replace writes bin beside exe and renames it into place, keeping the
// mode of the old binary. On Windows the running file cannot be replaced
// directly, so it is moved aside first.
func replace(exe string, bin []byte) error {
	mode := os.FileMode(0o755)
	if info, err := os.Stat(exe); err == nil {
		mode = info.Mode().Perm() | 0o700
	}
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".goaltracker-update-*")
	if err != nil {
		return fmt.Errorf("cannot write next to %s: %w", exe, err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { os.Remove(tmpPath) }
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if runtime.GOOS == "windows" {
		old := exe + ".old"
		os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			cleanup()
			return err
		}
		if err := os.Rename(tmpPath, exe); err != nil {
			os.Rename(old, exe)
			cleanup()
			return err
		}
		return nil
	}
	if err := os.Rename(tmpPath, exe); err != nil {
		cleanup()
		return fmt.Errorf("replace %s: %w", exe, err)
	}
	return nil
}
