//go:build unix

package backup

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// killGrace is how long the process group gets to exit after SIGTERM
// before it is killed outright. It must be shorter than the WaitDelay set
// in git(), so the kill lands before Run stops waiting.
const killGrace = 2 * time.Second

// detach runs cmd in its own session, away from the controlling terminal,
// and arranges for the whole process group to be stopped on cancel so a
// hung ssh child does not outlive git. The group gets SIGTERM first, which
// git handles by removing its lock files, then SIGKILL if it lingers. The
// returned function must be called once the command has finished. After a
// cancel it replaces the pending delayed SIGKILL with an immediate one, so
// a child that ignored SIGTERM cannot outlive git, and so the kill can
// never fire later against a process group that has since been given the
// same id. If the group is already gone the command finished on its own,
// which is not an error.
func detach(cmd *exec.Cmd) (done func()) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	var escalate *time.Timer
	var pgid int
	cmd.Cancel = func() error {
		pgid = -cmd.Process.Pid
		err := syscall.Kill(pgid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		if err != nil {
			return err
		}
		escalate = time.AfterFunc(killGrace, func() { syscall.Kill(pgid, syscall.SIGKILL) })
		return nil
	}
	return func() {
		if escalate != nil && escalate.Stop() {
			// Git has exited but the grace period had not run out. Any
			// member still alive keeps the group id reserved, so this
			// cannot hit anyone else; if none is, it is a no-op.
			syscall.Kill(pgid, syscall.SIGKILL)
		}
	}
}
