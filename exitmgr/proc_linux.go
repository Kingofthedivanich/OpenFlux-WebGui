package exitmgr

import (
	"os/exec"
	"syscall"
)

// setProcAttr has the kernel stop a channel's core when the panel dies, so
// a crashed or killed panel leaves no orphan exits holding the documents.
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}
