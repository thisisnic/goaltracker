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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRelease serves a GitHub-shaped latest release with one archive per
// platform and a checksums file, all built from binary.
type fakeRelease struct {
	tag      string
	binary   []byte
	archives map[string][]byte // name -> bytes
	sums     string
	omitSums bool // leave checksums.txt out of the asset listing
	server   *httptest.Server
}

func newFakeRelease(t *testing.T, tag string, binary []byte) *fakeRelease {
	t.Helper()
	f := &fakeRelease{tag: tag, binary: binary, archives: map[string][]byte{}}
	version := strings.TrimPrefix(tag, "v")
	var sums strings.Builder
	for _, p := range []struct{ os, arch string }{{"linux", "amd64"}, {"darwin", "arm64"}, {"windows", "amd64"}} {
		name := archiveName(version, p.os, p.arch)
		var data []byte
		if p.os == "windows" {
			data = zipWith(t, "goaltracker.exe", binary)
		} else {
			data = tarGzWith(t, "goaltracker", binary)
		}
		f.archives[name] = data
		sum := sha256.Sum256(data)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	f.sums = sums.String()
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	old := APIBase
	APIBase = f.server.URL
	t.Cleanup(func() { APIBase = old })
	return f
}

func (f *fakeRelease) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/repos/"+Repo+"/releases/latest":
		rel := release{TagName: f.tag}
		for name := range f.archives {
			rel.Assets = append(rel.Assets, asset{Name: name, URL: f.server.URL + "/dl/" + name})
		}
		if !f.omitSums {
			rel.Assets = append(rel.Assets, asset{Name: "checksums.txt", URL: f.server.URL + "/dl/checksums.txt"})
		}
		json.NewEncoder(w).Encode(rel)
	case r.URL.Path == "/dl/checksums.txt":
		w.Write([]byte(f.sums))
	case strings.HasPrefix(r.URL.Path, "/dl/"):
		data, ok := f.archives[strings.TrimPrefix(r.URL.Path, "/dl/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
	default:
		http.NotFound(w, r)
	}
}

func tarGzWith(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		body []byte
	}{{"README.md", []byte("readme")}, {name, content}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write(f.body)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func zipWith(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range []struct {
		name string
		body []byte
	}{{"README.md", []byte("readme")}, {name, content}} {
		w, err := zw.Create(f.name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(f.body)
	}
	zw.Close()
	return buf.Bytes()
}

func readExe(t *testing.T, exe string) string {
	t.Helper()
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func fakeExe(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "bin", "goaltracker")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

func TestUpdateInstallsLatest(t *testing.T) {
	newFakeRelease(t, "v0.2.0", []byte("new binary"))
	exe := fakeExe(t)
	res, err := Update(context.Background(), Options{Current: "0.1.0", Executable: exe, OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Updated || res.Latest != "0.2.0" || res.Current != "0.1.0" || res.Path != exe {
		t.Errorf("result = %+v", res)
	}
	if got := readExe(t, exe); got != "new binary" {
		t.Errorf("binary content = %q", got)
	}
	info, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Error("replaced binary is not executable")
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".goaltracker-update-*"))
	if len(leftovers) != 0 {
		t.Errorf("temp files left: %v", leftovers)
	}
}

func TestUpdateFromZipForWindows(t *testing.T) {
	newFakeRelease(t, "v0.2.0", []byte("win binary"))
	exe := fakeExe(t)
	// Extraction path is what is under test; replace() behaves per the
	// host OS, which is fine for the file content check.
	res, err := Update(context.Background(), Options{Current: "0.1.0", Executable: exe, OS: "windows", Arch: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if got := readExe(t, exe); got != "win binary" || !res.Updated {
		t.Errorf("zip update: content=%q res=%+v", got, res)
	}
}

func TestUpdateAlreadyCurrentAndCheck(t *testing.T) {
	newFakeRelease(t, "v0.2.0", []byte("new binary"))
	exe := fakeExe(t)

	res, err := Update(context.Background(), Options{Current: "v0.2.0", Executable: exe, OS: "linux", Arch: "amd64"})
	if err != nil || res.Updated {
		t.Errorf("already current: res=%+v err=%v", res, err)
	}
	res, err = Update(context.Background(), Options{Current: "0.1.0", Check: true, Executable: exe, OS: "linux", Arch: "amd64"})
	if err != nil || res.Updated || res.Latest != "0.2.0" {
		t.Errorf("check: res=%+v err=%v", res, err)
	}
	if got := readExe(t, exe); got != "old binary" {
		t.Error("check or no-op modified the binary")
	}
	res, err = Update(context.Background(), Options{Current: "0.2.0", Force: true, Executable: exe, OS: "linux", Arch: "amd64"})
	if err != nil || !res.Updated {
		t.Errorf("force: res=%+v err=%v", res, err)
	}
}

func TestUpdateRefusesBadChecksum(t *testing.T) {
	f := newFakeRelease(t, "v0.2.0", []byte("new binary"))
	// Tamper with the archive after the checksums were computed.
	name := archiveName("0.2.0", "linux", "amd64")
	f.archives[name] = append(f.archives[name], 0)
	exe := fakeExe(t)
	_, err := Update(context.Background(), Options{Current: "0.1.0", Executable: exe, OS: "linux", Arch: "amd64"})
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("err = %v, want checksum failure", err)
	}
	if got := readExe(t, exe); got != "old binary" {
		t.Error("binary replaced despite bad checksum")
	}
}

func TestUpdateRefusesWithoutChecksums(t *testing.T) {
	f := newFakeRelease(t, "v0.2.0", []byte("new binary"))
	f.omitSums = true // set before any request is made
	exe := fakeExe(t)
	_, err := Update(context.Background(), Options{Current: "0.1.0", Executable: exe, OS: "linux", Arch: "amd64"})
	if err == nil || !strings.Contains(err.Error(), "checksums.txt") {
		t.Fatalf("err = %v, want a checksums refusal", err)
	}
	if got := readExe(t, exe); got != "old binary" {
		t.Error("binary replaced without checksums")
	}
}

func TestUpdateNeverDowngrades(t *testing.T) {
	newFakeRelease(t, "v0.2.0", []byte("new binary"))
	exe := fakeExe(t)
	cases := []struct {
		current string
		state   State
		wantErr error
	}{
		{"0.3.0", Newer, nil},
		{"v0.2.1-0.20260910120000-abcdef123456", Newer, nil},                 // go install @main pseudo-version after a tag
		{"v0.0.0-20260910120000-abcdef123456", Unknown, ErrNotRelease},       // checkout that can see no tags
		{"v0.0.0-20260910120000-abcdef123456+dirty", Unknown, ErrNotRelease}, // the same with local changes
		{"0.2.0+dirty", Current, nil},                                        // checkout build at the tag
		{"dev", Unknown, ErrNotRelease},
		{"(devel)", Unknown, ErrNotRelease},
	}
	for _, c := range cases {
		res, err := Update(context.Background(), Options{Current: c.current, Executable: exe, OS: "linux", Arch: "amd64"})
		if res.State != c.state {
			t.Errorf("%q: state = %v want %v", c.current, res.State, c.state)
		}
		if !errors.Is(err, c.wantErr) {
			t.Errorf("%q: err = %v want %v", c.current, err, c.wantErr)
		}
		if res.Updated || readExe(t, exe) != "old binary" {
			t.Errorf("%q: binary was replaced", c.current)
		}
		// --check reports the same state without touching anything.
		if chk, err := Update(context.Background(), Options{Current: c.current, Check: true, Executable: exe, OS: "linux", Arch: "amd64"}); err != nil || chk.State != c.state {
			t.Errorf("%q: check state=%v err=%v", c.current, chk.State, err)
		}
	}
	// --force installs regardless.
	res, err := Update(context.Background(), Options{Current: "dev", Force: true, Executable: exe, OS: "linux", Arch: "amd64"})
	if err != nil || !res.Updated || readExe(t, exe) != "new binary" {
		t.Errorf("force from dev: res=%+v err=%v", res, err)
	}
}

func TestUpdateNoBuildForPlatform(t *testing.T) {
	newFakeRelease(t, "v0.2.0", []byte("new binary"))
	exe := fakeExe(t)
	_, err := Update(context.Background(), Options{Current: "0.1.0", Executable: exe, OS: "plan9", Arch: "mips"})
	if err == nil || !strings.Contains(err.Error(), "no build for plan9/mips") {
		t.Fatalf("err = %v", err)
	}
}

func TestChecksumFor(t *testing.T) {
	sums := []byte("ABC  a.tar.gz\ndef  b.zip\n")
	if got, _ := checksumFor(sums, "a.tar.gz"); got != "abc" {
		t.Errorf("got %q", got)
	}
	if _, err := checksumFor(sums, "c"); err == nil {
		t.Error("missing entry accepted")
	}
}
