package transport

import (
	"bytes"
	"crypto/rand"
	"testing"
)

// round-trips a set of packets through encode/decode and asserts equality.
func assertBatchRoundTrip(t *testing.T, pkts [][]byte) {
	t.Helper()
	wire := encodeBatch(pkts)
	got, err := decodeBatch(wire)
	if err != nil {
		t.Fatalf("decodeBatch error: %v", err)
	}
	if len(got) != len(pkts) {
		t.Fatalf("packet count = %d, want %d", len(got), len(pkts))
	}
	for i := range pkts {
		if !bytes.Equal(got[i], pkts[i]) {
			t.Fatalf("packet %d mismatch:\n got=%v\nwant=%v", i, got[i], pkts[i])
		}
	}
}

func TestBatchRoundTripSinglePacket(t *testing.T) {
	assertBatchRoundTrip(t, [][]byte{[]byte("hello world")})
}

func TestBatchRoundTripManyPackets(t *testing.T) {
	pkts := [][]byte{
		[]byte("first"),
		{0x00, 0x01, 0x02, 0xfe, 0xff},
		[]byte("a much longer third packet with some repetition repetition repetition"),
		[]byte("last"),
	}
	assertBatchRoundTrip(t, pkts)
}

func TestBatchRoundTripBinaryMTUSized(t *testing.T) {
	// Realistic full-MTU TCP payloads with arbitrary binary content.
	mk := func(fill byte) []byte {
		b := make([]byte, 1460)
		for i := range b {
			b[i] = fill ^ byte(i)
		}
		return b
	}
	assertBatchRoundTrip(t, [][]byte{mk(0x11), mk(0x22), mk(0x33)})
}

func TestBatchRoundTripEmptyList(t *testing.T) {
	got, err := decodeBatch(encodeBatch(nil))
	if err != nil {
		t.Fatalf("decodeBatch error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 packets, got %d", len(got))
	}
}

// A compressible batch must actually use the compressed path and shrink,
// proving zstd is wired in (not just a raw passthrough).
func TestBatchCompressesRepetitiveData(t *testing.T) {
	pkt := bytes.Repeat([]byte("ABCDEFGH"), 512) // 4096 bytes, highly compressible
	pkts := [][]byte{pkt, pkt, pkt}
	wire := encodeBatch(pkts)

	rawFramedLen := 0
	for _, p := range pkts {
		rawFramedLen += 2 + len(p)
	}
	if len(wire) >= rawFramedLen {
		t.Fatalf("expected compression to shrink %d framed bytes, got wire len %d", rawFramedLen, len(wire))
	}
	assertBatchRoundTrip(t, pkts)
}

// Incompressible data must fall back to the uncompressed path and still
// round-trip (zstd would otherwise inflate it).
func TestBatchIncompressibleFallsBackAndRoundTrips(t *testing.T) {
	pkt := make([]byte, 1200)
	if _, err := rand.Read(pkt); err != nil {
		t.Fatalf("rand: %v", err)
	}
	assertBatchRoundTrip(t, [][]byte{pkt})
}

func TestDecodeBatchRejectsUnknownVersion(t *testing.T) {
	if _, err := decodeBatch([]byte{0xFF, 0x00}); err == nil {
		t.Fatal("expected error for unknown version byte")
	}
}

func TestDecodeBatchRejectsTruncatedLengthPrefix(t *testing.T) {
	// valid header, flags=0 (uncompressed), then a length prefix claiming 10
	// bytes but only 2 present.
	bad := []byte{batchFormatVersion, 0x00, 0x00, 0x0A, 0x01, 0x02}
	if _, err := decodeBatch(bad); err == nil {
		t.Fatal("expected error for truncated length-prefixed packet")
	}
}

func TestDecodeBatchRejectsShortFrame(t *testing.T) {
	if _, err := decodeBatch([]byte{batchFormatVersion}); err == nil {
		t.Fatal("expected error for frame shorter than header")
	}
}

// A tiny zstd frame of zeros used to expand into millions of empty records
// (hundreds of MB of allocations per document message).
func TestDecodeBatchRejectsEmptyRecordBomb(t *testing.T) {
	zeros := make([]byte, maxBatchDecoded)
	frame := append([]byte{batchFormatVersion, batchFlagZstd}, zstdEnc.EncodeAll(zeros, nil)...)
	if len(frame) > 4096 {
		t.Fatalf("test frame unexpectedly large: %d bytes", len(frame))
	}
	if _, err := decodeBatch(frame); err == nil {
		t.Fatal("decodeBatch accepted a frame of empty records")
	}
}

func TestDecodeBatchRejectsTooManyRecords(t *testing.T) {
	pkts := make([][]byte, maxBatchRecords+1)
	for i := range pkts {
		pkts[i] = []byte{1}
	}
	if _, err := decodeBatch(encodeBatch(pkts)); err == nil {
		t.Fatal("decodeBatch accepted more than maxBatchRecords packets")
	}
}

func TestDecodeBatchRejectsOversizedOutput(t *testing.T) {
	big := make([]byte, maxPacketSize)
	pkts := make([][]byte, maxBatchDecoded/maxPacketSize+2)
	for i := range pkts {
		pkts[i] = big
	}
	if _, err := decodeBatch(encodeBatch(pkts)); err == nil {
		t.Fatal("decodeBatch accepted a frame above maxBatchDecoded")
	}
}

func TestBatchedSendRejectsBadSizes(t *testing.T) {
	bt := NewBatchedTransport(&fakeTransport{})
	bt.running.Store(true)
	if err := bt.Send(nil); err == nil {
		t.Fatal("Send accepted an empty packet")
	}
	if err := bt.Send(make([]byte, maxPacketSize+1)); err == nil {
		t.Fatal("Send accepted a packet longer than the length prefix allows")
	}
}

func TestLegacyDecompressRejectsUnknownMarkerAndBomb(t *testing.T) {
	if _, err := decompress([]byte{0x45, 1, 2, 3}); err == nil {
		t.Fatal("decompress passed a frame with an unknown marker")
	}
	bomb := compress(make([]byte, 1<<20))
	if bomb[0] != CompressionMarker {
		t.Fatal("expected an LZ4 frame")
	}
	if _, err := decompress(bomb); err == nil {
		t.Fatal("decompress accepted output larger than one packet")
	}
}
