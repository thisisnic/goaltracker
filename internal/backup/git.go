package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Push commits the backup file in dir and pushes to the repo's upstream.
// It is a no-op when nothing changed. Push needs credentials that work
// without a prompt, such as an SSH key. The returned error wraps
// ErrPushFailed when the commit succeeded but the push did not, so the
// caller can treat that as a warning: the backup is safe on disk and the
// next push will carry it.
func Push(ctx context.Context, dir string, now time.Time) error {
	// Before anything is committed, a cancelled context is just that: not
	// a push failure, which would wrongly claim a commit was made.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("backup git cancelled: %w", err)
	}
	if out, err := git(ctx, dir, "rev-parse", "--is-inside-work-tree"); err != nil || strings.TrimSpace(out) != "true" {
		if cerr := ctx.Err(); cerr != nil {
			return fmt.Errorf("backup git cancelled: %w", cerr)
		}
		return fmt.Errorf("%s is not a git repository; run git init there or set git = false", dir)
	}
	if _, err := git(ctx, dir, "add", "--", FileName); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return fmt.Errorf("backup git cancelled: %w", cerr)
		}
		return err
	}
	// Anything staged?
	if _, err := git(ctx, dir, "diff", "--cached", "--quiet", "--", FileName); err == nil {
		// Nothing new to commit, but an earlier push may have failed.
		return push(ctx, dir)
	}
	msg := "lifeo backup " + now.UTC().Format("2006-01-02 15:04 UTC")
	if _, err := git(ctx, dir, "-c", "commit.gpgsign=false", "commit", "-q", "-m", msg, "--", FileName); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			// git may have written the commit and then been killed. If
			// nothing is staged any more, it landed, and this is a push
			// failure rather than a cancellation.
			if committed(dir) {
				return fmt.Errorf("%w: committed, but the run was cancelled before pushing: %v", ErrPushFailed, cerr)
			}
			return fmt.Errorf("backup git cancelled: %w", cerr)
		}
		return err
	}
	return push(ctx, dir)
}

// committed reports whether the backup file has nothing staged, meaning
// the commit that was in flight completed. It uses its own short context
// because the caller's may already be done.
func committed(dir string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := git(ctx, dir, "diff", "--cached", "--quiet", "--", FileName)
	return err == nil
}

// ErrPushFailed marks a push that failed after the commit succeeded.
var ErrPushFailed = errors.New("push failed")

// pushTimeout bounds the network step so a hung remote cannot block the
// TUI from exiting for long.
const pushTimeout = 60 * time.Second

func push(ctx context.Context, dir string) error {
	ctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()
	if _, err := git(ctx, dir, "rev-parse", "--verify", "-q", "HEAD"); err != nil {
		if ctx.Err() != nil {
			return pushError(ctx, err)
		}
		return nil // nothing committed yet, nothing to push
	}
	_, upstreamErr := git(ctx, dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if upstreamErr == nil {
		// Skip the network when there is nothing to push.
		if out, err := git(ctx, dir, "rev-list", "--count", "@{upstream}..HEAD"); err == nil && strings.TrimSpace(out) == "0" {
			return nil
		}
		if _, err := git(ctx, dir, "push", "-q"); err != nil {
			return pushError(ctx, err)
		}
		return nil
	}
	if ctx.Err() != nil {
		return pushError(ctx, upstreamErr)
	}
	// First push of a fresh clone: set the upstream as we go.
	branch, err := git(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return pushError(ctx, err)
	}
	if _, err := git(ctx, dir, "push", "-q", "-u", "origin", strings.TrimSpace(branch)); err != nil {
		return pushError(ctx, err)
	}
	return nil
}

// pushError wraps a failed push once in ErrPushFailed, adding a hint for
// the causes that will not fix themselves on retry. ctx is the push's own
// context, so a deadline on it means the remote did not answer in time.
func pushError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: timed out waiting for the remote: %v", ErrPushFailed, err)
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "non-fast-forward") || strings.Contains(msg, "fetch first"):
		return fmt.Errorf("%w: the remote has newer commits, perhaps a backup from another machine; run git pull in the backup folder and pick which lifeo.db.age to keep: %v", ErrPushFailed, err)
	case strings.Contains(msg, "does not appear to be a git repository") || strings.Contains(msg, "No such remote") || strings.Contains(msg, "'origin' does not appear"):
		return fmt.Errorf("%w: the backup folder has no origin remote: %v", ErrPushFailed, err)
	}
	return fmt.Errorf("%w: %v", ErrPushFailed, err)
}

// git runs a git command in dir with no way to prompt: git's own prompts
// are off, ssh's askpass is off, and on Unix the process runs detached from
// the terminal in its own session so ssh cannot open /dev/tty. The user's
// own ssh configuration is left alone. If the context ends, the process
// group is asked to stop (SIGTERM, then SIGKILL on Unix) and Run gives up
// waiting on any child that still holds the output pipes after WaitDelay,
// which is longer than the Unix kill grace.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "SSH_ASKPASS_REQUIRE=never")
	cmd.WaitDelay = 5 * time.Second
	defer detach(cmd)()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(errb.String())
		if detail == "" {
			detail = err.Error()
		}
		return out.String(), fmt.Errorf("git %s: %s", args[0], detail)
	}
	return out.String(), nil
}
