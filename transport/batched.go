package transport

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"openflux/utils"
)

// Defaults for the coalescing layer. Tunable at runtime via env vars so the
// batch size can be matched to the channel's per-message limits without a
// rebuild (OPENFLUX_BATCH_BYTES / OPENFLUX_BATCH_COUNT / OPENFLUX_BATCH_LINGER_MS).
const (
	defaultMaxBatchBytes = 8192
	defaultMaxBatchCount = 64
	defaultLingerMs      = 5
	batchQueueDepth      = 4096
)

// BatchedTransport replaces the old per-packet CompressedTransport. It queues
// outgoing tunnel packets, coalesces bursts into a single framed+zstd batch per
// inner transport message, and splits batches back into packets on receive.
//
// This is the symmetric layer: client and exit node must both use it (they do,
// because main.go wraps both the same way).
type BatchedTransport struct {
	Transport

	queue         chan []byte
	lingerMs      int
	maxBatchBytes int
	maxBatchCount int

	running atomic.Bool

	mu     sync.RWMutex
	userCb func([]byte)

	// sendErrors counts batches dropped because the wrapped transport's
	// Send failed (e.g. mid-reconnect). flushLoop used to ignore this
	// return value entirely, so a transport hiccup silently dropped
	// whole batches with no signal anywhere.
	sendErrors atomic.Uint64
}

// SendErrors returns the number of flushed batches lost to a Send error on
// the wrapped transport since the transport started.
func (b *BatchedTransport) SendErrors() uint64 { return b.sendErrors.Load() }

// packetBufPool holds the per-packet copy buffers Send makes. Every one is
// released back here from sendBatch, right after encodeBatch (via
// frameBatch) has copied its contents into its own separate output buffer
// -- by then nothing still references the original, so it's always safe to
// reuse. 1500 matches a typical Ethernet MTU; larger packets just grow the
// buffer on Send, same as any other append-style reuse.
var packetBufPool = sync.Pool{
	New: func() any { return make([]byte, 0, 1500) },
}

// sendBatch flushes one coalesced batch, records/logs a Send failure
// instead of discarding it silently, and returns every packet buffer in
// the batch to packetBufPool.
func (b *BatchedTransport) sendBatch(batch [][]byte) {
	if err := b.Transport.Send(encodeBatch(batch)); err != nil {
		b.sendErrors.Add(1)
		utils.Debugf("[BATCH] flush send error, dropped %d packets: %v", len(batch), err)
	}
	for _, p := range batch {
		packetBufPool.Put(p[:0])
	}
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func NewBatchedTransport(inner Transport) *BatchedTransport {
	return &BatchedTransport{
		Transport:     inner,
		queue:         make(chan []byte, batchQueueDepth),
		lingerMs:      envInt("OPENFLUX_BATCH_LINGER_MS", defaultLingerMs),
		maxBatchBytes: envInt("OPENFLUX_BATCH_BYTES", defaultMaxBatchBytes),
		maxBatchCount: envInt("OPENFLUX_BATCH_COUNT", defaultMaxBatchCount),
	}
}

func (b *BatchedTransport) Start() error {
	if err := b.Transport.Start(); err != nil {
		return err
	}
	b.running.Store(true)
	go b.flushLoop()
	return nil
}

func (b *BatchedTransport) Stop() error {
	b.running.Store(false)
	return b.Transport.Stop()
}

// Send copies the packet (the caller's buffer is reused by gVisor) and enqueues
// it for batching. A full queue drops the packet; the tunnel's TCP will
// retransmit, same as the old "write queue full" behavior.
func (b *BatchedTransport) Send(data []byte) error {
	p := packetBufPool.Get().([]byte)
	if cap(p) < len(data) {
		p = make([]byte, len(data))
	} else {
		p = p[:len(data)]
	}
	copy(p, data)
	select {
	case b.queue <- p:
		return nil
	default:
		packetBufPool.Put(p[:0])
		return fmt.Errorf("batch queue full")
	}
}

func (b *BatchedTransport) Receive(callback func([]byte)) {
	b.mu.Lock()
	b.userCb = callback
	b.mu.Unlock()

	b.Transport.Receive(func(data []byte) {
		pkts, err := decodeBatch(data)
		if err != nil {
			utils.Debugf("[BATCH] decode error (%d bytes): %v", len(data), err)
			return
		}
		b.mu.RLock()
		cb := b.userCb
		b.mu.RUnlock()
		if cb == nil {
			return
		}
		for _, p := range pkts {
			cb(p)
		}
	})
}

func (b *BatchedTransport) flushLoop() {
	for b.running.Load() {
		first, ok := <-b.queue
		if !ok {
			return
		}
		batch := [][]byte{first}
		size := 2 + len(first)

		// Phase 1: absorb everything already queued (burst coalescing). This
		// alone collapses a window's worth of segments into one message.
	drainNow:
		for size < b.maxBatchBytes && len(batch) < b.maxBatchCount {
			select {
			case p, ok := <-b.queue:
				if !ok {
					b.sendBatch(batch)
					return
				}
				batch = append(batch, p)
				size += 2 + len(p)
			default:
				break drainNow
			}
		}

		// Phase 2: brief linger to catch stragglers arriving just after the
		// burst. Negligible next to the channel RTT, but it fills batches
		// during steady bulk transfer.
		if b.lingerMs > 0 && size < b.maxBatchBytes && len(batch) < b.maxBatchCount {
			timer := time.NewTimer(time.Duration(b.lingerMs) * time.Millisecond)
		linger:
			for size < b.maxBatchBytes && len(batch) < b.maxBatchCount {
				select {
				case p, ok := <-b.queue:
					if !ok {
						timer.Stop()
						b.sendBatch(batch)
						return
					}
					batch = append(batch, p)
					size += 2 + len(p)
				case <-timer.C:
					break linger
				}
			}
			timer.Stop()
		}

		b.sendBatch(batch)
	}
}
