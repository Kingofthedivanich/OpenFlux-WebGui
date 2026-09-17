package transport

import (
	"fmt"
	"log"
	"sync/atomic"
	"time"
)

// rateLog prints a warning at most once per interval, so errors caused by a
// misconfigured peer (wrong codec, wrong key) are visible without --debug
// but cannot flood the log.
type rateLog struct {
	interval   time.Duration
	last       atomic.Int64
	suppressed atomic.Uint64
}

func newRateLog(interval time.Duration) *rateLog {
	return &rateLog{interval: interval}
}

func (r *rateLog) Printf(format string, args ...interface{}) {
	now := time.Now().UnixNano()
	last := r.last.Load()
	if now-last < int64(r.interval) || !r.last.CompareAndSwap(last, now) {
		r.suppressed.Add(1)
		return
	}
	msg := fmt.Sprintf(format, args...)
	if n := r.suppressed.Swap(0); n > 0 {
		msg += fmt.Sprintf(" (+%d similar suppressed)", n)
	}
	log.Print(msg)
}
