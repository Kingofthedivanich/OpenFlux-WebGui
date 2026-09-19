package l3

import (
	"encoding/binary"
	"testing"
)

func TestIPChecksumCoversOptions(t *testing.T) {
	// IHL=6 (24-byte header: 20 + 4 bytes of options), TCP.
	pkt := make([]byte, 24+20)
	pkt[0] = 0x46 // version 4, IHL 6
	pkt[9] = 6    // TCP
	binary.BigEndian.PutUint16(pkt[2:4], uint16(len(pkt)))
	copy(pkt[12:16], []byte{10, 0, 0, 1})
	copy(pkt[16:20], []byte{10, 0, 0, 2})
	pkt[20], pkt[21], pkt[22], pkt[23] = 0x01, 0x01, 0x01, 0x00 // option bytes
	fixChecksums(pkt)
	if got := onesComplementSum(pkt[:24]); got != 0 {
		t.Fatalf("IP checksum over full header not valid: verify sum = 0x%04x", got)
	}
}

func TestIsFragmented(t *testing.T) {
	pkt := make([]byte, 40)
	pkt[0] = 0x45
	if isFragmented(pkt) {
		t.Fatal("unfragmented packet flagged")
	}
	pkt[6] = 0x20 // MF set
	if !isFragmented(pkt) {
		t.Fatal("MF fragment not detected")
	}
	pkt[6], pkt[7] = 0x00, 0x25 // non-zero offset
	if !isFragmented(pkt) {
		t.Fatal("offset fragment not detected")
	}
}
