//go:build !windows

package exitmgr

import (
	"os"
	"syscall"
	"time"
)

// stopProcess asks the core to stop (SIGTERM: a Telemost carrier leaves its
// room cleanly) and kills it if it is still there after grace.
func stopProcess(p *os.Process, waited <-chan error, grace time.Duration) error {
	_ = p.Signal(syscall.SIGTERM)
	select {
	case err := <-waited:
		return err
	case <-time.After(grace):
		_ = p.Kill()
		return <-waited
	}
}
