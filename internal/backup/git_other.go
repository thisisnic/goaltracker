//go:build !unix

package backup

import "os/exec"

// detach is a no-op where process groups are not available; WaitDelay
// still stops Run from blocking on a child that holds the pipes.
func detach(cmd *exec.Cmd) {}
