package transport

import (
	"encoding/binary"
	"fmt"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// Wire format for a batched frame (one Yandex/transport message can now carry
// many tunnel packets):
//
//	[0]   version byte (batchFormatVersion)
//	[1]   flags (bit0 = payload is zstd-compressed)
//	[2:]  payload: a sequence of [2-byte big-endian length][packet] records,
//	      optionally zstd-compressed as a whole.
const (
	batchFormatVersion = 0x02
	batchFlagZstd      = 0x01

	// maxPacketSize is the largest record a batch may carry: an IPv4 packet
	// cannot be longer, and the 2-byte length prefix cannot say more.
	maxPacketSize = 0xFFFF
	// maxFrameBytes caps one encoded batch frame. The document transports
	// prefix each frame with a 2-byte length and cap it at 65535, and the
	// encrypted layer adds encryptedOverhead below the codec, so the batch
	// itself must stay under 65535 - overhead.
	encryptedOverhead = 64
	maxFrameBytes     = 0xFFFF - encryptedOverhead
	// maxBatchRecords and maxBatchDecoded bound what one frame from the
	// shared document can make us allocate. Senders stay far below both
	// (64 packets / 8 KB by default).
	maxBatchRecords = 1024
	maxBatchDecoded = 4 << 20
)

var (
	zstdEnc *zstd.Encoder
	zstdDec *zstd.Decoder
)

// scratchPool holds reusable byte-slice scratch buffers for the frame/
// compress/decompress steps below. Every one of these buffers is fully
// copied into its caller's own (separately allocated) return value before
// the function that borrowed it returns, so it's always safe to recycle at
// that point -- this only cuts the number of fresh heap allocations on the
// hottest path in the tunnel (every batch, both directions), not any
// lifetime.
var scratchPool = sync.Pool{
	New: func() any { return make([]byte, 0, 4096) },
}

func getScratch() []byte  { return scratchPool.Get().([]byte)[:0] }
func putScratch(b []byte) { scratchPool.Put(b[:0]) }

func init() {
	var err error
	// SpeedDefault (~level 3): far better ratio than LZ4 at a CPU cost that is
	// irrelevant next to the Yandex channel's latency. Single-shot EncodeAll /
	// DecodeAll are safe for concurrent use on a shared instance.
	zstdEnc, err = zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.SpeedDefault),
		zstd.WithEncoderConcurrency(1),
	)
	if err != nil {
		panic(fmt.Sprintf("zstd encoder init: %v", err))
	}
	zstdDec, err = zstd.NewReader(nil,
		zstd.WithDecoderConcurrency(1),
		// Bound the damage from a malformed/hostile frame injected into the
		// shared document: cap decompressed memory.
		zstd.WithDecoderMaxMemory(maxBatchDecoded),
	)
	if err != nil {
		panic(fmt.Sprintf("zstd decoder init: %v", err))
	}
}

// frameBatch concatenates packets into length-prefixed records, appending
// onto dst (typically a pooled scratch buffer; pass nil for a fresh one).
func frameBatch(pkts [][]byte, dst []byte) []byte {
	var lenbuf [2]byte
	for _, p := range pkts {
		binary.BigEndian.PutUint16(lenbuf[:], uint16(len(p)))
		dst = append(dst, lenbuf[:]...)
		dst = append(dst, p...)
	}
	return dst
}

// encodeBatch serializes packets into a single wire frame, compressing the
// whole batch with zstd only when that actually shrinks it.
func encodeBatch(pkts [][]byte) []byte {
	framed := frameBatch(pkts, getScratch())
	defer putScratch(framed)

	compressed := zstdEnc.EncodeAll(framed, getScratch())
	defer putScratch(compressed)

	if len(compressed) < len(framed) {
		out := make([]byte, 2, 2+len(compressed))
		out[0] = batchFormatVersion
		out[1] = batchFlagZstd
		return append(out, compressed...)
	}
	out := make([]byte, 2, 2+len(framed))
	out[0] = batchFormatVersion
	out[1] = 0
	return append(out, framed...)
}

// decodeBatch reverses encodeBatch, returning the original packets.
func decodeBatch(data []byte) ([][]byte, error) {
	if len(data) < 2 {
		return nil, fmt.Errorf("batch frame too short: %d bytes", len(data))
	}
	if data[0] != batchFormatVersion {
		return nil, fmt.Errorf("unknown batch version 0x%02x", data[0])
	}
	flags := data[1]
	payload := data[2:]

	framed := payload
	if flags&batchFlagZstd != 0 {
		buf := getScratch()
		var err error
		framed, err = zstdDec.DecodeAll(payload, buf)
		if err != nil {
			putScratch(buf)
			return nil, fmt.Errorf("zstd decode: %w", err)
		}
		defer putScratch(framed)
	}

	var pkts [][]byte
	for len(framed) > 0 {
		if len(framed) < 2 {
			return nil, fmt.Errorf("truncated length prefix")
		}
		n := int(binary.BigEndian.Uint16(framed[:2]))
		framed = framed[2:]
		if n == 0 {
			return nil, fmt.Errorf("empty packet record")
		}
		if len(pkts) == maxBatchRecords {
			return nil, fmt.Errorf("batch has more than %d packets", maxBatchRecords)
		}
		if len(framed) < n {
			return nil, fmt.Errorf("truncated packet: need %d, have %d", n, len(framed))
		}
		pkt := make([]byte, n)
		copy(pkt, framed[:n])
		pkts = append(pkts, pkt)
		framed = framed[n:]
	}
	return pkts, nil
}
