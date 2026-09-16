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
// before it is killed outright.
const killGrace = 2 * time.Second

// detach runs cmd in its own session, away from the controlling terminal,
// and arranges for the whole process group to be stopped on cancel so a
// hung ssh child does not outlive git. The group gets SIGTERM first, which
// git handles by removing its lock files, then SIGKILL if it lingers. If
// the group is already gone the command finished on its own, which is not
// an error.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		pgid := -cmd.Process.Pid
		err := syscall.Kill(pgid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		if err != nil {
			return err
		}
		go func() {
			time.Sleep(killGrace)
			syscall.Kill(pgid, syscall.SIGKILL)
		}()
		return nil
	}
}
