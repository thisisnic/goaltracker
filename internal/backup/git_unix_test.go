//go:build unix

package backup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPushGivesUpOnHungRemote(t *testing.T) {
	e := newEnv(t)
	gitRepos(t, e.opts.Dir)
	e.run(t)

	// A "remote" over ssh where ssh is a script that records its PID and
	// sleeps forever. The push must come back well before WaitDelay alone
	// would let it, and the sleeping ssh must be gone, which only the
	// process-group kill achieves.
	pidFile := filepath.Join(t.TempDir(), "ssh.pid")
	fake := filepath.Join(t.TempDir(), "ssh")
	script := "#!/bin/sh\necho $$ > " + pidFile + "\nsleep 60\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", fake)
	if out, err := exec.Command("git", "-C", e.opts.Dir, "remote", "set-url", "origin", "git@example.invalid:nobody/nothing.git").CombinedOutput(); err != nil {
		t.Fatalf("set-url: %v\n%s", err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	err := Push(ctx, e.opts.Dir, now)
	took := time.Since(start)
	if !errors.Is(err, ErrPushFailed) || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timed-out ErrPushFailed", err)
	}
	if took > 5*time.Second {
		t.Errorf("push took %s; the hung ssh was not cut off promptly", took)
	}

	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("fake ssh never ran: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 1 {
		t.Fatalf("bad fake ssh pid %q: %v", raw, err)
	}
	dead := false
	t.Cleanup(func() {
		if !dead {
			syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	// Gone means reaped, or a zombie waiting for an init that does not
	// reap promptly; either way it is no longer running.
	gone := func() bool {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		if runtime.GOOS != "linux" {
			return false // no /proc to tell a zombie from a live process
		}
		stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return true
		}
		// State is the field after the parenthesised command name.
		if i := strings.LastIndexByte(string(stat), ')'); i >= 0 && i+2 < len(stat) {
			return stat[i+2] == 'Z'
		}
		return false
	}
	deadline := time.Now().Add(3 * time.Second)
	for !gone() {
		if time.Now().After(deadline) {
			t.Fatalf("fake ssh (pid %d) survived the push timeout", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
	dead = true // the pid may be reused from here; never kill it again
}

func TestPushExpiringDuringCommitIsCancellation(t *testing.T) {
	e := newEnv(t)
	gitRepos(t, e.opts.Dir)
	e.run(t)
	// A pre-commit hook that outlives the context, so the deadline hits
	// during git commit rather than before it.
	hook := filepath.Join(e.opts.Dir, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := Push(ctx, e.opts.Dir, now)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrPushFailed) {
		t.Errorf("Push expiring during commit: %v", err)
	}
	// Nothing was committed, so the file is still staged.
	if out, err := exec.Command("git", "-C", e.opts.Dir, "diff", "--cached", "--name-only").Output(); err != nil || !strings.Contains(string(out), FileName) {
		t.Errorf("staged files after the cancelled commit: %q (%v)", out, err)
	}
	// A later run, with time to spare, commits and pushes it.
	os.Remove(hook)
	if err := Push(context.Background(), e.opts.Dir, now); err != nil {
		t.Errorf("retry after cancellation: %v", err)
	}
}

func TestPushExpiringAfterCommitIsPushFailure(t *testing.T) {
	e := newEnv(t)
	remote := gitRepos(t, e.opts.Dir)
	e.run(t)
	before, _ := exec.Command("git", "-C", e.opts.Dir, "rev-parse", "HEAD").Output()
	// A post-commit hook runs after the commit is written and the index
	// lock released, so the deadline hits with the commit already landed.
	hook := filepath.Join(e.opts.Dir, ".git", "hooks", "post-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := Push(ctx, e.opts.Dir, now)
	if !errors.Is(err, ErrPushFailed) || !strings.Contains(err.Error(), "committed, but") {
		t.Errorf("Push expiring after commit: %v", err)
	}
	after, _ := exec.Command("git", "-C", e.opts.Dir, "rev-parse", "HEAD").Output()
	if string(after) == string(before) {
		t.Error("HEAD did not move; the commit did not land")
	}
	os.Remove(hook)
	if err := Push(context.Background(), e.opts.Dir, now); err != nil {
		t.Errorf("retry: %v", err)
	}
	if !strings.Contains(remoteLog(t, remote), "lifeo backup") {
		t.Error("the landed commit was not pushed on retry")
	}
}

func TestCancelKillsChildThatIgnoresTerm(t *testing.T) {
	e := newEnv(t)
	gitRepos(t, e.opts.Dir)
	e.run(t)
	// A pre-commit hook that starts a background child which ignores
	// TERM, keeps git's pipes closed, records its PID and sleeps, then
	// the hook itself sleeps so the deadline cancels git mid-commit.
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	hook := filepath.Join(e.opts.Dir, ".git", "hooks", "pre-commit")
	// exec makes the recorded PID the very process that ignores TERM; an
	// ignored signal disposition survives exec.
	script := "#!/bin/sh\n" +
		"sh -c 'trap \"\" TERM; echo $$ > " + pidFile + "; exec sleep 60' >/dev/null 2>&1 </dev/null &\n" +
		"sleep 5\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := Push(ctx, e.opts.Dir, now); err == nil {
		t.Fatal("cancelled push succeeded")
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("background child never started: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 1 {
		t.Fatalf("bad child pid %q: %v", raw, err)
	}
	dead := false
	t.Cleanup(func() {
		if !dead {
			syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	// The child must die from the immediate kill, well inside killGrace.
	deadline := time.Now().Add(time.Second)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			dead = true // the pid may be reused from here; never kill it again
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("child %d that ignored TERM outlived git", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if took := time.Since(start); took > killGrace {
		t.Errorf("child died only after %s, so the delayed kill did it, not the immediate one", took)
	}
}
