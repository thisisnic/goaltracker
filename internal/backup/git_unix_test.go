//go:build unix

package backup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	time.Sleep(200 * time.Millisecond) // let the kill land
	if err := syscall.Kill(pid, 0); err == nil {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("fake ssh (pid %d) survived the push timeout", pid)
	}
}
