package mailru

import (
	"testing"
	"time"

	"openflux/internal/transport"
)

// Stop must be idempotent and must unblock the writer/keepalive goroutines
// (they used to hang on the queue / never see the running flag flip).
func TestStopIdempotentAndUnblocksLoops(t *testing.T) {
	tr := NewMailruDocsTransport("AbCdEfGh1/IjKlMnOp2", transport.DefaultConfig())
	_ = tr.BaseTransport.Start()

	done := make(chan struct{})
	go func() { tr.writerLoop(); done <- struct{}{} }()
	go func() { tr.keepAliveLoop(); done <- struct{}{} }()

	if err := tr.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := tr.Stop(); err != nil { // second Stop must not panic
		t.Fatalf("second Stop: %v", err)
	}

	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("a transport loop did not exit after Stop")
		}
	}
}
