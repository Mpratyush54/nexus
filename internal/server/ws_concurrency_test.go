package server

// WebSocket lifecycle safety tests (nexus issue #101).
//
//   - Concurrent-write mutex: wsWriteLoop data frames and wsReadLoop
//     control replies serialize on Client.wmu (see ws.go); here we prove
//     the hub side never panics under concurrent broadcast + remove.
//   - Channel-close discipline: sends hold RLock across safeSend so Remove
//     cannot close mid-send; duplicate IDs retire the old client instead
//     of leaking it.
//   - RSV bits: frames with RSV1-3 set are rejected per RFC 6455 §5.2.

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

// A duplicate client ID gracefully retires the previous client: the old
// Send channel is closed exactly once, the new client is registered, and no
// goroutine is left behind.
func TestHubDuplicateIDRetiresOld(t *testing.T) {
	h := NewHub()
	old := &Client{ID: "dup", UserID: "u1"}
	h.Add(old)
	if n := h.ClientCount(); n != 1 {
		t.Fatalf("count = %d, want 1", n)
	}
	fresh := &Client{ID: "dup", UserID: "u2"}
	h.Add(fresh)
	if n := h.ClientCount(); n != 1 {
		t.Fatalf("count after dup = %d, want 1", n)
	}
	if got := h.Get("dup"); got != fresh {
		t.Fatal("duplicate Add must replace the client")
	}
	select {
	case _, ok := <-old.Send:
		if ok {
			t.Fatal("old channel must be closed, not delivering")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("old Send channel was not closed on duplicate Add")
	}
	// The retired client accepts no further sends (no panic, no delivery).
	old.safeSend([]byte(`{}`))
}

// Concurrent broadcast, reply, heartbeat, add, and remove must never panic
// (send-on-closed-channel) and must terminate.
func TestHubConcurrentBroadcastRemove(t *testing.T) {
	h := NewHub()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				c := &Client{ID: fmt.Sprintf("race-%d-%d", w, i), UserID: "u"}
				h.Add(c)
				h.Heartbeat(c.ID)
				h.BroadcastToProject("proj1", WSMessage{Type: WSMsgEvent, EventType: "R"})
				h.reply(c.ID, WSMessage{Type: WSMsgError, Message: "x"})
				h.Subscribe(c.ID, "proj1", "")
				h.Remove(c.ID)
				select {
				case <-stop:
					return
				default:
				}
			}
		}(w)
	}
	bcast := make(chan struct{})
	go func() {
		defer close(bcast)
		for i := 0; i < 200; i++ {
			h.Broadcast(WSMessage{Type: WSMsgEvent, EventType: "G"}, "proj1", "")
		}
	}()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		close(stop)
		t.Fatal("concurrent hub ops did not terminate")
	}
	<-bcast
}

// Frames with RSV bits set violate RFC 6455 §5.2 and must be rejected.
func TestWSReadFrameRejectsRSVBits(t *testing.T) {
	raw := encodeMaskedFrame(true, wsOpText, []byte("hi"))
	raw[0] |= 0x40 // set RSV1
	rw := bufio.NewReadWriter(bufio.NewReader(bytes.NewReader(raw)), bufio.NewWriter(io.Discard))
	if _, _, _, err := wsReadFrame(rw); err == nil {
		t.Fatal("RSV1 frame must be rejected")
	}
}

// Fragmented control frames violate RFC 6455 §5.5: the read loop drops the
// connection instead of buffering them into reassembly.
func TestWSFragmentedControlDrops(t *testing.T) {
	h := NewHub()
	c := newHubClient(h, "ctrl", "", "")
	testConn, done := startWSReadLoop(h, c)
	defer func() { _ = testConn.Close() }()

	writeClientFrame(t, testConn, false, wsOpPing, []byte("x"))
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("read loop must exit on fragmented control frame")
	}
}
