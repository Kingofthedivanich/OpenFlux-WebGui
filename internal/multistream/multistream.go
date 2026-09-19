package multistream

import (
	"fmt"
	"log"
	"strings"
	"time"

	"openflux/internal/transport"
)

// StatusLoop logs one line per interval with each document's state:
// UP (connected, peer heard from recently), NOPEER (connected, peer silent)
// or DOWN.
func StatusLoop(ms *transport.MultiStreamTransport, urls []string, every time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for range tick.C {
		streams := ms.Streams()
		parts := make([]string, 0, len(streams))
		up, alive := 0, 0
		for i, s := range streams {
			st := s.Stats()
			state := "DOWN"
			if st.Connected {
				up++
				state = "NOPEER"
				if ms.PeerAlive(i) {
					alive++
					state = "UP"
				}
			}
			peer := "never"
			if !st.LastRecv.IsZero() {
				peer = time.Since(st.LastRecv).Round(time.Second).String()
			}
			parts = append(parts, fmt.Sprintf("s%d[%s]=%s(peer=%s,rx=%d,tx=%d,rc=%d)",
				i, docLabel(urls[i]), state, peer, st.PacketsRecv, st.PacketsSent, st.Reconnects))
		}
		log.Printf("[MULTI] connected=%d/%d peer=%d/%d %s",
			up, len(streams), alive, len(streams), strings.Join(parts, " "))
	}
}

// docLabel shortens a document URL to its last path segment for logs.
func docLabel(u string) string {
	u = strings.TrimRight(u, "/")
	if i := strings.LastIndex(u, "/"); i >= 0 {
		u = u[i+1:]
	}
	if len(u) > 8 {
		u = u[:8]
	}
	return u
}
