package utils

import (
	"fmt"
	"io"
	"log"
	"os"
	"runtime/debug"
	"sync"
	"sync/atomic"
)

var (
	// debugLog is non-nil whenever debug output is on; Debugf reads it
	// lock-free, so toggling debug at runtime (mobile bridge) is race-free.
	debugLog atomic.Pointer[log.Logger]

	outputMu sync.Mutex
	output   io.Writer = os.Stderr
)

// SetOutput redirects all debug and standard log output to w.
// Used by the mobile bridge to pipe logs into the app UI.
func SetOutput(w io.Writer) {
	outputMu.Lock()
	defer outputMu.Unlock()
	output = w
	log.SetOutput(w)
	if l := debugLog.Load(); l != nil {
		l.SetOutput(w)
	}
}

func EnableDebug() {
	outputMu.Lock()
	defer outputMu.Unlock()
	log.SetOutput(output)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)
	debugLog.Store(log.New(output, "", log.LstdFlags|log.Lmicroseconds))
}

func Debugf(format string, args ...interface{}) {
	if l := debugLog.Load(); l != nil {
		l.Output(2, fmt.Sprintf(format, args...))
	}
}

// SetDebug toggles verbose logging at runtime (off = Debugf becomes a no-op).
func SetDebug(on bool) {
	if on {
		EnableDebug()
		return
	}
	debugLog.Store(nil)
}

func IsVerbose() bool {
	return debugLog.Load() != nil
}

// SafeGo runs fn in a new goroutine, recovering from any panic so a crash in
// one worker cannot take down the whole process (critical when this code runs
// embedded as a library inside a mobile app). The panic is always logged: a
// silently dead worker looks exactly like a dead channel.
func SafeGo(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[PANIC] recovered in %s: %v\n%s", name, r, debug.Stack())
			}
		}()
		fn()
	}()
}
