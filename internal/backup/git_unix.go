//go:build unix

package backup

import (
	"os/exec"
	"syscall"
)

// detach runs cmd in its own session, away from the controlling terminal,
// and arranges for the whole process group to be killed on cancel so a
// hung ssh child does not outlive git.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
