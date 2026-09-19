package utils

import (
	"io"
	"sync"
	"testing"
)

// Toggling debug while other goroutines log must be race-free and never
// dereference a nil logger (the mobile bridge flips it at runtime).
func TestDebugToggleConcurrent(t *testing.T) {
	SetOutput(io.Discard)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 2000; j++ {
				Debugf("msg %d", j)
			}
		}()
	}
	for j := 0; j < 200; j++ {
		SetDebug(j%2 == 0)
	}
	wg.Wait()
	SetDebug(false)
}
