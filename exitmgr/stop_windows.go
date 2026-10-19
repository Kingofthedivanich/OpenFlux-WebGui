package exitmgr

import (
	"os"
	"time"
)

// stopProcess kills the core: Windows has no signal a console child can be
// sent that it would handle.
func stopProcess(p *os.Process, waited <-chan error, grace time.Duration) error {
	_ = p.Kill()
	return <-waited
}
