package l3

import (
	"sync"
	"sync/atomic"
	"time"
)

const (
	ctTimeoutEstablished = 60 * time.Minute
	ctTimeoutClosing     = 15 * time.Second
	// ctTimeoutUnreplied bounds flows that never saw a packet back from the
	// internet (SYN to a black hole, scans): they must not hold a slot for
	// the established timeout, or a SYN burst fills the table for an hour.
	ctTimeoutUnreplied = 60 * time.Second
	ctSweepInterval      = 30 * time.Second

	// ctShardCount splits the conntrack table so sweep() only ever holds one
	// shard's lock at a time. Before sharding, sweep held a single global
	// lock across a full-table scan, blocking every Insert/Touch/Exists call
	// on the packet-forwarding hot path for the whole scan duration.
	ctShardCount = 32

	// ctMaxEntries caps total tracked flows so a flood of SYNs from a
	// malicious client (one entry per spoofed 4-tuple, held for the idle
	// timeout) cannot exhaust memory. Past the cap, new flows are refused;
	// their return packets are then dropped (noct) instead of the exit
	// running out of memory. ~200k entries is a few tens of MB.
	ctMaxEntries = 200000
)

type ctEntry struct {
	lastSeen time.Time
	dying    bool
	replied  bool // a packet from the internet side has been seen
}

type ctShard struct {
	mu      sync.RWMutex
	entries map[flowKey]*ctEntry
}

type conntrack struct {
	shards  [ctShardCount]*ctShard
	count   atomic.Int64
	stop    chan struct{}
	stopped sync.Once
}

func newConntrack() *conntrack {
	ct := &conntrack{
		stop: make(chan struct{}),
	}
	for i := range ct.shards {
		ct.shards[i] = &ctShard{entries: make(map[flowKey]*ctEntry, 64)}
	}
	go ct.sweepLoop()
	return ct
}

// shardIndex picks a shard for k. It doesn't need to be cryptographically
// strong, just spread real traffic evenly -- in particular, many flows from
// one client to the same destination:port differ only in the ephemeral
// source port, so the finalizer below must mix that into the low bits that
// %ctShardCount reads (a plain XOR of the raw fields does not: srcPort's
// bits alone would land above bit 15 and never move the shard index).
func shardIndex(k flowKey) uint32 {
	h := k.srcIP*2654435761 ^ k.dstIP*40503 ^ uint32(k.srcPort)<<16 ^ uint32(k.dstPort) ^ uint32(k.proto)
	h ^= h >> 16
	h *= 0x85ebca6b
	h ^= h >> 13
	return h % ctShardCount
}

func (c *conntrack) shardFor(k flowKey) *ctShard {
	return c.shards[shardIndex(k)]
}

// Insert records or refreshes the flow for an outbound packet. syn marks a
// TCP SYN, which starts a new connection: it clears any leftover dying flag
// from a previous connection that reused the same 4-tuple (otherwise the new
// flow would inherit the 15s closing timeout and be swept mid-transfer).
func (c *conntrack) Insert(k flowKey, syn bool) {
	s := c.shardFor(k)
	now := time.Now()
	s.mu.Lock()
	if e, ok := s.entries[k]; ok {
		e.lastSeen = now
		if syn {
			e.dying = false
		}
		s.mu.Unlock()
		return
	}
	if c.count.Load() >= ctMaxEntries {
		s.mu.Unlock()
		return // table full: refuse the new flow rather than exhaust memory
	}
	s.entries[k] = &ctEntry{lastSeen: now}
	c.count.Add(1)
	s.mu.Unlock()
}

func (c *conntrack) Touch(k flowKey, dying bool) {
	s := c.shardFor(k)
	s.mu.Lock()
	if e, ok := s.entries[k]; ok {
		e.lastSeen = time.Now()
		if dying {
			e.dying = true
		}
	}
	s.mu.Unlock()
}

// TouchReplied is Touch for packets arriving from the internet side; it also
// marks the flow as replied so it graduates from the short unreplied timeout.
func (c *conntrack) TouchReplied(k flowKey, dying bool) {
	s := c.shardFor(k)
	s.mu.Lock()
	if e, ok := s.entries[k]; ok {
		e.lastSeen = time.Now()
		e.replied = true
		if dying {
			e.dying = true
		}
	}
	s.mu.Unlock()
}

func (c *conntrack) Exists(k flowKey) bool {
	s := c.shardFor(k)
	s.mu.RLock()
	_, ok := s.entries[k]
	s.mu.RUnlock()
	return ok
}

// get is a test/introspection helper; production code has no need to read
// an entry back out (Exists/Touch cover every real use).
func (c *conntrack) get(k flowKey) (ctEntry, bool) {
	s := c.shardFor(k)
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entries[k]
	if !ok {
		return ctEntry{}, false
	}
	return *e, true
}

func (c *conntrack) Close() {
	c.stopped.Do(func() {
		close(c.stop)
	})
}

func (c *conntrack) sweepLoop() {
	t := time.NewTicker(ctSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-t.C:
			c.sweep()
		}
	}
}

// sweep expires stale entries one shard at a time, so it never holds more
// than one shard's lock at once -- packet handling on every other shard
// keeps flowing while a sweep is in progress.
func (c *conntrack) sweep() {
	now := time.Now()
	for _, s := range c.shards {
		s.mu.Lock()
		for k, e := range s.entries {
			timeout := ctTimeoutEstablished
			if !e.replied {
				timeout = ctTimeoutUnreplied
			}
			if e.dying {
				timeout = ctTimeoutClosing
			}
			if now.Sub(e.lastSeen) > timeout {
				delete(s.entries, k)
				c.count.Add(-1)
			}
		}
		s.mu.Unlock()
	}
}
