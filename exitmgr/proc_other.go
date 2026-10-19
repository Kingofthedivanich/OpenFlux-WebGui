//go:build !linux

package exitmgr

import "os/exec"

func setProcAttr(cmd *exec.Cmd) {}
