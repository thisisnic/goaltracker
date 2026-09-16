//go:build unix

package backup

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// detach runs cmd in its own session, away from the controlling terminal,
// and arranges for the whole process group to be killed on cancel so a
// hung ssh child does not outlive git. If the group is already gone the
// command finished on its own, which is not an error.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
