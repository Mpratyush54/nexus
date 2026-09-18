package steering

// Boundedness + deadlock-safety tests (nexus issue #113).
//
//   - Prompt queues cap at MaxPromptQueue (overflow yields ErrQueueFull).
//   - Run histories cap at MaxRunHistory (oldest dropped).
//   - RequestInterrupt signals the bridge WITHOUT holding the manager lock:
//     a blocked bridge never wedges Gate/State/Steer.
//   - Register evicts finished runs at MaxRuns instead of growing forever.

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestPromptQueueBounded(t *testing.T) {
	m := NewInterruptManager()
	if err := m.Register("r", nil); err != nil {
		t.Fatal(err)
	}
	if err := m.RequestInterrupt("r", "alice", ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxPromptQueue; i++ {
		if err := m.Steer("r", "alice", "prompt"); err != nil {
			t.Fatalf("steer %d: %v", i, err)
		}
	}
	if d := m.QueueDepth("r"); d != MaxPromptQueue {
		t.Fatalf("depth = %d, want %d", d, MaxPromptQueue)
	}
	if err := m.Steer("r", "alice", "one too many"); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("overflow err = %v, want ErrQueueFull", err)
	}
	// Draining frees capacity.
	if _, ok := m.TakeNextPrompt("r"); !ok {
		t.Fatal("TakeNextPrompt must succeed")
	}
	if err := m.Steer("r", "alice", "fits again"); err != nil {
		t.Fatalf("steer after drain: %v", err)
	}
}

func TestRunHistoryBounded(t *testing.T) {
	m := NewInterruptManager()
	if err := m.Register("r", nil); err != nil {
		t.Fatal(err)
	}
	// Each interrupt/resume cycle appends history; loop past the cap.
	for i := 0; i < MaxRunHistory+50; i++ {
		_ = m.RequestInterrupt("r", "alice", "")
		_ = m.Resume("r", "alice")
	}
	if n := len(m.Events("r")); n > MaxRunHistory {
		t.Fatalf("history = %d, want <= %d", n, MaxRunHistory)
	}
	// Newest events survive (Resumed is last in every cycle).
	evs := m.Events("r")
	if evs[len(evs)-1].EventType != EventResumed {
		t.Fatalf("newest event = %q, want %q", evs[len(evs)-1].EventType, EventResumed)
	}
}

// A bridge that blocks must not wedge the manager: Gate/State answer while
// the signal is in flight, because Signal runs outside the lock.
func TestSignalOutsideLock(t *testing.T) {
	release := make(chan struct{})
	blocking := FuncBridge{Fn: func(string) error {
		select {
		case <-release:
			return nil
		case <-time.After(10 * time.Second):
			return errors.New("bridge stuck")
		}
	}}
	m := NewInterruptManager()
	if err := m.Register("r", blocking); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- m.RequestInterrupt("r", "alice", "") }()
	// While the bridge is blocked, the manager still answers.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("manager wedged behind blocked bridge")
		default:
		}
		if got := m.State("r"); got == StateRunning {
			break // still running: signal in flight, lock free
		}
		time.Sleep(time.Millisecond)
	}
	if err := m.Gate("r"); err != nil {
		t.Fatalf("Gate during in-flight signal: %v", err)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RequestInterrupt: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RequestInterrupt did not finish after bridge release")
	}
	if got := m.State("r"); got != StatePauseRequested {
		t.Fatalf("state = %q, want pause_requested", got)
	}
}

// Concurrent interrupt attempts serialize: exactly one wins, no panic, no
// double-close of Done.
func TestConcurrentInterruptSingleWinner(t *testing.T) {
	m := NewInterruptManager()
	if err := m.Register("r", nil); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wins := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			steerer := "alice"
			if i%2 == 1 {
				steerer = "bob"
			}
			if err := m.RequestInterrupt("r", steerer, ""); err == nil {
				wins <- steerer
			}
		}(i)
	}
	wg.Wait()
	close(wins)
	n := 0
	for range wins {
		n++
	}
	if n != 1 {
		t.Fatalf("winners = %d, want exactly 1", n)
	}
}

func TestRegisterEvictsFinishedAtCap(t *testing.T) {
	m := NewInterruptManager()
	// Fill to cap with finished runs plus one live run.
	for i := 0; i < MaxRuns; i++ {
		id := "run-" + string(rune('a'+i%26)) + "-" + string(rune('0'+i/26%10)) + "-" + itoa(i)
		if err := m.Register(id, nil); err != nil {
			t.Fatalf("register %d: %v", i, err)
		}
		if i > 0 {
			_ = m.Unregister(id) // all but run-0 finish
		}
	}
	// Cap reached with only finished runs evictable: new register succeeds
	// by evicting finished entries, keeping the live one.
	if err := m.Register("fresh", nil); err != nil {
		t.Fatalf("register at cap with evictable finished runs: %v", err)
	}
	if got := m.State("run-a-0-0"); got == "" {
		t.Fatal("live run must survive eviction")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}
