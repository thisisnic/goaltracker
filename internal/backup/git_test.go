package backup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisnic/lifeo/internal/goal"
)

// gitRepos makes a bare "remote" and a clone of it at dir, with an
// upstream set, so Push has somewhere to go.
// isolateGit keeps the developer's git config and identity out of the test.
func isolateGit(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
}

func gitRepos(t *testing.T, dir string) (remote string) {
	t.Helper()
	isolateGit(t)
	remote = filepath.Join(t.TempDir(), "remote.git")
	run := func(cwd string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = cwd
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	run(t.TempDir(), "init", "-q", "--bare", "-b", "main", remote)
	run(filepath.Dir(dir), "clone", "-q", remote, dir)
	run(dir, "commit", "-q", "--allow-empty", "-m", "init")
	run(dir, "push", "-q", "-u", "origin", "main")
	return remote
}

func remoteLog(t *testing.T, remote string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", remote, "log", "--format=%s", "main").Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestPushCommitsAndPushes(t *testing.T) {
	e := newEnv(t)
	remote := gitRepos(t, e.opts.Dir)
	ctx := context.Background()

	e.run(t)
	if err := Push(ctx, e.opts.Dir, now); err != nil {
		t.Fatal(err)
	}
	if log := remoteLog(t, remote); !strings.Contains(log, "lifeo backup 2026-09-16 12:00 UTC") {
		t.Errorf("remote log:\n%s", log)
	}

	// Nothing changed: no new commit, no error.
	if err := Push(ctx, e.opts.Dir, now); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(remoteLog(t, remote), "lifeo backup"); n != 1 {
		t.Errorf("unchanged backup produced %d commits", n)
	}

	// A change is committed and pushed.
	if _, err := e.store.Add(ctx, goal.NewGoal{Statement: "x", Period: "2026"}); err != nil {
		t.Fatal(err)
	}
	e.run(t)
	if err := Push(ctx, e.opts.Dir, now); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(remoteLog(t, remote), "lifeo backup"); n != 2 {
		t.Errorf("changed backup: %d commits on remote", n)
	}
}

func TestPushFailureIsRetried(t *testing.T) {
	e := newEnv(t)
	remote := gitRepos(t, e.opts.Dir)
	ctx := context.Background()
	e.run(t)

	// Break the remote so the push fails after the commit.
	if err := os.Rename(remote, remote+".gone"); err != nil {
		t.Fatal(err)
	}
	err := Push(ctx, e.opts.Dir, now)
	if !errors.Is(err, ErrPushFailed) {
		t.Fatalf("err = %v, want ErrPushFailed", err)
	}
	// The commit exists locally.
	out, _ := exec.Command("git", "-C", e.opts.Dir, "log", "--format=%s", "-1").Output()
	if !strings.Contains(string(out), "lifeo backup") {
		t.Errorf("commit not made locally: %s", out)
	}

	// Remote back: the next Push, with nothing new, carries the commit.
	if err := os.Rename(remote+".gone", remote); err != nil {
		t.Fatal(err)
	}
	if err := Push(ctx, e.opts.Dir, now); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(remoteLog(t, remote), "lifeo backup") {
		t.Error("earlier commit was not pushed on retry")
	}

	// A rejected push, because the remote moved on, says what to do.
	other := filepath.Join(t.TempDir(), "other")
	for _, args := range [][]string{
		{"clone", "-q", remote, other},
		{"-C", other, "commit", "-q", "--allow-empty", "-m", "from another machine"},
		{"-C", other, "push", "-q"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if _, err := e.store.Add(ctx, goal.NewGoal{Statement: "y", Period: "2026"}); err != nil {
		t.Fatal(err)
	}
	e.run(t)
	err = Push(ctx, e.opts.Dir, now)
	if !errors.Is(err, ErrPushFailed) || !strings.Contains(err.Error(), "git pull") {
		t.Errorf("rejected push: err = %v, want a pull hint", err)
	}
}

func TestPushSubfolderOfRepo(t *testing.T) {
	e := newEnv(t)
	parent := filepath.Join(t.TempDir(), "repo")
	remote := gitRepos(t, parent)
	e.opts.Dir = filepath.Join(parent, "nested")
	e.run(t)
	if err := Push(context.Background(), e.opts.Dir, now); err != nil {
		t.Fatalf("push from a subfolder of the repo: %v", err)
	}
	if !strings.Contains(remoteLog(t, remote), "lifeo backup") {
		t.Error("subfolder backup was not pushed")
	}
}

func TestPushFromFreshCloneSetsUpstream(t *testing.T) {
	e := newEnv(t)
	isolateGit(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	for _, args := range [][]string{
		{"init", "-q", "--bare", "-b", "main", remote},
		{"clone", "-q", remote, e.opts.Dir},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	ctx := context.Background()
	e.run(t)
	if err := Push(ctx, e.opts.Dir, now); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(remoteLog(t, remote), "lifeo backup") {
		t.Error("first push from a fresh clone did not reach the remote")
	}
	// Second time round the upstream is set and nothing is pending.
	if err := Push(ctx, e.opts.Dir, now); err != nil {
		t.Fatal(err)
	}
}

func TestPushNeedsRepo(t *testing.T) {
	e := newEnv(t)
	isolateGit(t)
	e.run(t)
	if err := Push(context.Background(), e.opts.Dir, now); err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("err = %v", err)
	}
}

func TestPushErrorHints(t *testing.T) {
	cases := map[string]string{
		"! [rejected] main -> main (non-fast-forward)":                        "git pull",
		"Updates were rejected because the remote contains work; fetch first": "git pull",
		"! [remote rejected] main -> main (pre-receive hook declined)":        "",
		"fatal: 'origin' does not appear to be a git repository":              "no origin remote",
	}
	for msg, hint := range cases {
		err := pushError(context.Background(), errors.New(msg))
		if !errors.Is(err, ErrPushFailed) {
			t.Errorf("%q: not ErrPushFailed", msg)
		}
		if hint == "" {
			if strings.Contains(err.Error(), "git pull") || strings.Contains(err.Error(), "no origin") {
				t.Errorf("%q: got a hint that does not apply: %v", msg, err)
			}
		} else if !strings.Contains(err.Error(), hint) {
			t.Errorf("%q: want hint %q, got %v", msg, hint, err)
		}
	}
}

func TestPushErrorTimeoutWrapsOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	err := pushError(ctx, errors.New("git push: killed"))
	if !errors.Is(err, ErrPushFailed) || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("err = %v", err)
	}
	if strings.Count(err.Error(), "push failed") != 1 {
		t.Errorf("ErrPushFailed appears more than once: %v", err)
	}
}

func TestPushCancelledContextIsNotSuccess(t *testing.T) {
	e := newEnv(t)
	gitRepos(t, e.opts.Dir)
	e.run(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Push(ctx, e.opts.Dir, now)
	if err == nil {
		t.Fatal("push with a cancelled context reported success")
	}
}
