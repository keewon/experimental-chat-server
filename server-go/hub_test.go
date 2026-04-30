package main

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// newTestClient builds a Client suitable for tests: just enough fields to
// be inserted into a hub. The conn is nil because run() never touches it
// (it only writes to client.send). Anything that calls readPump/writePump
// in tests would crash; we don't.
func newTestClient(userID string) *Client {
	return &Client{
		userID:      userID,
		send:        make(chan []byte, 16),
		ip:          "127.0.0.1",
		connectedAt: time.Now(),
	}
}

// hubExists reports whether the manager currently has a hub for roomID.
func hubExists(m *HubManager, roomID string) bool {
	_, ok := m.hubs.Load(roomID)
	return ok
}

func TestAttachClient_CreatesHub(t *testing.T) {
	setupManager(t, time.Hour)

	c := newTestClient("u1")
	h := manager.attachClient("room-1", c)
	defer drainAndShutdown(t, h, c)

	if h == nil {
		t.Fatal("attachClient returned nil hub")
	}
	if !hubExists(manager, "room-1") {
		t.Fatal("hub not registered in manager")
	}
}

func TestAttachClient_ReusesExistingHub(t *testing.T) {
	setupManager(t, time.Hour)

	c1 := newTestClient("u1")
	c2 := newTestClient("u2")

	h1 := manager.attachClient("room-1", c1)
	h2 := manager.attachClient("room-1", c2)
	defer drainAndShutdown(t, h1, c1, c2)

	if h1 != h2 {
		t.Fatal("attachClient created two hubs for the same room")
	}
}

func TestRoomHub_BroadcastReachesAllClients(t *testing.T) {
	setupManager(t, time.Hour)

	c1 := newTestClient("u1")
	c2 := newTestClient("u2")
	h := manager.attachClient("room-1", c1)
	manager.attachClient("room-1", c2)
	defer drainAndShutdown(t, h, c1, c2)

	// Drain the join broadcasts each client received before our test message.
	drainSends(c1.send, 100*time.Millisecond)
	drainSends(c2.send, 100*time.Millisecond)

	h.broadcast <- []byte("hello")

	for _, c := range []*Client{c1, c2} {
		select {
		case got := <-c.send:
			if string(got) != "hello" {
				t.Fatalf("got %q, want %q", got, "hello")
			}
		case <-time.After(time.Second):
			t.Fatalf("client %s did not receive broadcast", c.userID)
		}
	}
}

func TestRoomHub_IdleTeardownRemovesHub(t *testing.T) {
	setupManager(t, 50*time.Millisecond)

	c := newTestClient("u1")
	h := manager.attachClient("room-1", c)

	// Unregister so client count drops to 0 → idle timer arms.
	h.unregister <- c

	if !waitFor(time.Second, func() bool {
		select {
		case <-h.done:
			return true
		default:
			return false
		}
	}) {
		t.Fatal("hub did not close done within 1s after idle")
	}

	if hubExists(manager, "room-1") {
		t.Fatal("hub still in manager map after teardown")
	}
}

// TestAttachClient_ConcurrentWithIdleTeardown — the core race-fix test.
//
// We wedge the manager into a state where idle teardowns happen
// continuously (idleTime = 1ms) and then hammer attachClient. A correct
// implementation must always return a *live* hub: the client has been
// registered and broadcasts to that hub will reach the client. A buggy
// implementation could:
//   - return a hub mid-teardown (caller's register send hangs forever
//     → goroutine leak / test timeout), or
//   - silently drop the registration without re-registering.
//
// We assert that every client receives its own join broadcast after attach.
func TestAttachClient_ConcurrentWithIdleTeardown(t *testing.T) {
	setupManager(t, time.Millisecond)

	const goroutines = 50
	const perGoroutine = 20

	var wg sync.WaitGroup
	errs := make(chan error, goroutines*perGoroutine)

	start := make(chan struct{})
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			<-start
			for i := 0; i < perGoroutine; i++ {
				c := newTestClient("u")
				h := manager.attachClient("room-x", c)

				// The hub must broadcast the join we just triggered. If we
				// landed in a dying hub, this would never come.
				select {
				case <-c.send:
					// Got join (or some broadcast) — alive.
				case <-time.After(2 * time.Second):
					errs <- fmtErr("attempt %d.%d: no broadcast from attached hub", g, i)
					return
				}

				// Detach so the room can idle out and trigger teardowns.
				select {
				case h.unregister <- c:
				case <-h.done:
					// Hub already gone; that's fine.
				}
				// Drain anything else we got.
				drainSends(c.send, 5*time.Millisecond)
			}
		}(g)
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}
}

func TestTryRegister_ReturnsFalseAfterShutdown(t *testing.T) {
	setupManager(t, 20*time.Millisecond)

	c := newTestClient("u1")
	h := manager.attachClient("room-1", c)
	h.unregister <- c

	// Wait for the hub's run loop to actually exit — only then is
	// tryRegister guaranteed to return false (no goroutine left to
	// receive on h.register).
	if !waitFor(time.Second, func() bool {
		select {
		case <-h.done:
			return true
		default:
			return false
		}
	}) {
		t.Fatal("hub did not shut down")
	}

	stranger := newTestClient("u2")
	if h.tryRegister(stranger) {
		t.Fatal("tryRegister returned true on a shut-down hub")
	}
}

// ─── helpers ────────────────────────────────────────────────────

// drainAndShutdown unregisters the given clients (best-effort) and waits
// for the hub to teardown so subsequent tests start clean. With idleTime
// set to time.Hour we cannot wait for the natural teardown — caller is
// expected to set a short idle if they want hub.done closed.
func drainAndShutdown(t *testing.T, h *RoomHub, clients ...*Client) {
	t.Helper()
	for _, c := range clients {
		select {
		case h.unregister <- c:
		case <-h.done:
		case <-time.After(time.Second):
			t.Errorf("unregister of %s timed out", c.userID)
		}
	}
}

func drainSends(ch <-chan []byte, d time.Duration) {
	deadline := time.After(d)
	for {
		select {
		case <-ch:
		case <-deadline:
			return
		}
	}
}

func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

func fmtErr(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}
