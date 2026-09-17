package transport

import (
	"bytes"
	"fmt"
	"io"
	"time"

	"github.com/pierrec/lz4/v4"
)

const (
	MinCompressSize   = 200
	CompressionMarker = 0x1F
	// maxDecompressedSize bounds legacy-codec decompression: one frame holds
	// one IP packet, so a hostile LZ4 block cannot make us allocate more.
	maxDecompressedSize = maxPacketSize
)

var legacyDecodeWarn = newRateLog(10 * time.Second)

type CompressedTransport struct {
	Transport
}

func NewCompressedTransport(inner Transport) Transport {
	return &CompressedTransport{Transport: inner}
}

func (c *CompressedTransport) Send(data []byte) error {
	compressed := compress(data)
	return c.Transport.Send(compressed)
}

func (c *CompressedTransport) Receive(callback func([]byte)) {
	c.Transport.Receive(func(data []byte) {
		decompressed, err := decompress(data)
		if err != nil {
			// Never hand undecoded bytes up as a packet.
			legacyDecodeWarn.Printf("[LZ4] dropped undecodable frame (%d bytes): %v - "+
				"does the peer use the same --codec and encryption settings?", len(data), err)
			return
		}
		callback(decompressed)
	})
}

func compress(data []byte) []byte {
	if len(data) <= MinCompressSize {
		out := make([]byte, 1, len(data)+1)
		out[0] = 0x00
		out = append(out, data...)
		return out
	}

	var buf bytes.Buffer
	buf.WriteByte(CompressionMarker)

	w := lz4.NewWriter(&buf)
	w.Write(data)
	w.Close()

	if buf.Len() >= len(data)+1 {
		out := make([]byte, 1, len(data)+1)
		out[0] = 0x00
		out = append(out, data...)
		return out
	}

	return buf.Bytes()
}

func decompress(data []byte) ([]byte, error) {
	if len(data) < 1 {
		return data, nil
	}

	switch data[0] {
	case 0x00:
		return data[1:], nil
	case CompressionMarker:
	default:
		return nil, fmt.Errorf("unknown legacy frame marker 0x%02x", data[0])
	}

	r := lz4.NewReader(bytes.NewReader(data[1:]))
	out, err := io.ReadAll(io.LimitReader(r, maxDecompressedSize+1))
	if err != nil {
		return nil, err
	}
	if len(out) > maxDecompressedSize {
		return nil, fmt.Errorf("decompressed size exceeds %d byte limit", maxDecompressedSize)
	}
	return out, nil
}
