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
	return f
}

func (f *fakeRelease) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/repos/"+Repo+"/releases/latest":
		rel := release{TagName: f.tag}
		for name := range f.archives {
			rel.Assets = append(rel.Assets, asset{Name: name, URL: f.server.URL + "/dl/" + name})
		}
		rel.Assets = append(rel.Assets, asset{Name: "checksums.txt", URL: f.server.URL + "/dl/checksums.txt"})
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
	f := newFakeRelease(t, "v0.2.0", []byte("new binary"))
	APIBase = f.server.URL
	exe := fakeExe(t)
	res, err := Update(context.Background(), Options{Current: "0.1.0", Executable: exe, OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Updated || res.Latest != "0.2.0" || res.Current != "0.1.0" || res.Path != exe {
		t.Errorf("result = %+v", res)
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "new binary" {
		t.Errorf("binary content = %q", got)
	}
	if info, _ := os.Stat(exe); info.Mode().Perm()&0o100 == 0 {
		t.Error("replaced binary is not executable")
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".goaltracker-update-*"))
	if len(leftovers) != 0 {
		t.Errorf("temp files left: %v", leftovers)
	}
}

func TestUpdateFromZipForWindows(t *testing.T) {
	f := newFakeRelease(t, "v0.2.0", []byte("win binary"))
	APIBase = f.server.URL
	exe := fakeExe(t)
	// Extraction path is what is under test; replace() behaves per the
	// host OS, which is fine for the file content check.
	res, err := Update(context.Background(), Options{Current: "0.1.0", Executable: exe, OS: "windows", Arch: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "win binary" || !res.Updated {
		t.Errorf("zip update: content=%q res=%+v", got, res)
	}
}

func TestUpdateAlreadyCurrentAndCheck(t *testing.T) {
	f := newFakeRelease(t, "v0.2.0", []byte("new binary"))
	APIBase = f.server.URL
	exe := fakeExe(t)

	res, err := Update(context.Background(), Options{Current: "v0.2.0", Executable: exe, OS: "linux", Arch: "amd64"})
	if err != nil || res.Updated {
		t.Errorf("already current: res=%+v err=%v", res, err)
	}
	res, err = Update(context.Background(), Options{Current: "0.1.0", Check: true, Executable: exe, OS: "linux", Arch: "amd64"})
	if err != nil || res.Updated || res.Latest != "0.2.0" {
		t.Errorf("check: res=%+v err=%v", res, err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old binary" {
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
	APIBase = f.server.URL
	exe := fakeExe(t)
	_, err := Update(context.Background(), Options{Current: "0.1.0", Executable: exe, OS: "linux", Arch: "amd64"})
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("err = %v, want checksum failure", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old binary" {
		t.Error("binary replaced despite bad checksum")
	}
}

func TestUpdateRefusesWithoutChecksums(t *testing.T) {
	f := newFakeRelease(t, "v0.2.0", []byte("new binary"))
	APIBase = f.server.URL
	// Drop the checksums asset from the listing by serving 404 for it.
	orig := f.server.Config.Handler
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dl/checksums.txt" {
			http.NotFound(w, r)
			return
		}
		orig.ServeHTTP(w, r)
	})
	exe := fakeExe(t)
	if _, err := Update(context.Background(), Options{Current: "0.1.0", Executable: exe, OS: "linux", Arch: "amd64"}); err == nil {
		t.Fatal("installed without checksums")
	}
}

func TestUpdateNoBuildForPlatform(t *testing.T) {
	f := newFakeRelease(t, "v0.2.0", []byte("new binary"))
	APIBase = f.server.URL
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
