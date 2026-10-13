package wsrelay

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex2cc/auth"
	"github.com/gorilla/websocket"
)

func responseBindingCount(m *Manager) int {
	m.respConnMu.Lock()
	defer m.respConnMu.Unlock()
	return len(m.respConnBindings)
}

func requireMemoryLifecycleCondition(t *testing.T, label string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", label)
}

func TestResponseBindingsReleasedAfterRealDownstreamCancellation(t *testing.T) {
	manager := NewManager()
	defer manager.Stop()
	sent := make(chan error, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			sent <- err
			return
		}
		defer conn.Close()
		if _, _, err = conn.ReadMessage(); err != nil {
			sent <- err
			return
		}
		if err = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp-first-success"}}`)); err != nil {
			sent <- err
			return
		}
		if _, _, err = conn.ReadMessage(); err != nil {
			sent <- err
			return
		}
		payload := []byte(`{"type":"response.output_text.delta","delta":"` + strings.Repeat("x", 1<<20) + `"}`)
		for range 9 {
			if err = conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				sent <- err
				return
			}
		}
		sent <- nil
		_, _, _ = conn.ReadMessage() // Wait for actual downstream cancellation to close the socket.
	}))
	defer upstream.Close()
	account := &auth.Account{DBID: 678, DynamicConcurrencyLimit: 2}
	wsURL := "ws" + strings.TrimPrefix(upstream.URL, "http")
	wc, pending, err := manager.AcquireConnection(context.Background(), account, wsURL, "session-memory", http.Header{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := wc.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create"}`)); err != nil {
		t.Fatal(err)
	}
	first := &WsResponse{conn: wc, pendingReq: pending, sessionID: "session-memory", manager: manager, apiKey: "test-key"}
	firstHTTP := websocketResponseToHTTP(context.Background(), first, 200, nil)
	body, err := io.ReadAll(firstHTTP.Body)
	_ = firstHTTP.Body.Close()
	if err != nil || !strings.Contains(string(body), "resp-first-success") {
		t.Fatalf("first response = %q, err = %v", body, err)
	}
	requireMemoryLifecycleCondition(t, "first pending released", func() bool { return wc.session.PendingCount() == 0 })
	if bound, _ := manager.lookupResponseConn("resp-first-success", account.ID(), "test-key"); bound != wc {
		t.Fatal("successful first response must bind its actual connection")
	}
	secondConn, secondPending, err := manager.AcquireConnection(context.Background(), account, wsURL, "session-memory", http.Header{}, "")
	if err != nil || secondConn != wc {
		t.Fatalf("second acquire did not reuse the first socket: %v", err)
	}
	if err := secondConn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create"}`)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	second := &WsResponse{conn: secondConn, pendingReq: secondPending, sessionID: "session-memory", manager: manager, apiKey: "test-key"}
	secondHTTP := websocketResponseToHTTP(ctx, second, 200, nil)
	defer secondHTTP.Body.Close() // An unread Body models a stalled downstream consumer.
	select {
	case err := <-sent:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not finish sending")
	}
	state := wc.ensureReadState()
	queuedBytes := func() int {
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.queuedPayload
	}
	requireMemoryLifecycleCondition(t, "8 MiB of pending payload", func() bool { return queuedBytes() >= 8<<20 })
	cancel()
	requireMemoryLifecycleCondition(t, "consumer stopped and socket removed", func() bool {
		second.mu.Lock()
		finished := second.closed && second.connBroken
		second.mu.Unlock()
		return finished && manager.ConnectionCount() == 0 && wc.session.PendingCount() == 0
	})
	select {
	case <-state.readerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("raw reader did not stop")
	}
	if n := queuedBytes(); n < 8<<20 {
		t.Fatalf("Close unexpectedly discarded unread frames: %d bytes", n)
	}
	if n := responseBindingCount(manager); n != 0 {
		t.Fatalf("%d bindings still retain the closed socket and its queued payload", n)
	}
	// A completion callback that was already in flight cannot resurrect the
	// strong reference after cancellation removed this connection.
	manager.BindResponseConn("late-completion", wc, "session-memory", account.ID(), "test-key")
	if n := responseBindingCount(manager); n != 0 {
		t.Fatalf("late completion recreated %d stale bindings", n)
	}
	requireNoAcquisitionLocks(t, manager)
}

func TestResponseBindingCleanupPreservesLiveConnection(t *testing.T) {
	manager := NewManager()
	defer manager.Stop()
	wc := newBoundTestConn(t, manager, 7, "session")
	manager.BindResponseConn("expired", wc, "session", 7, "key")
	manager.BindResponseConn("live", wc, "session", 7, "key")
	manager.respConnMu.Lock()
	binding := manager.respConnBindings["expired"]
	binding.expiresAt = time.Now().Add(-time.Second)
	manager.respConnBindings["expired"] = binding
	manager.respConnMu.Unlock()
	manager.evictExpired()
	if n := responseBindingCount(manager); n != 1 {
		t.Fatalf("bindings after periodic cleanup = %d, want 1", n)
	}
	if got, _ := manager.lookupResponseConn("live", 7, "key"); got != wc {
		t.Fatal("expired binding cleanup must preserve a live connection and its other bindings")
	}
	if !wc.IsConnected() || manager.ConnectionCount() != 1 {
		t.Fatal("binding expiry closed its still usable connection")
	}
}

func TestResponseBindingCleanupRemovalPaths(t *testing.T) {
	for _, mode := range []string{"discard", "remove", "detach", "idle", "stop"} {
		t.Run(mode, func(t *testing.T) {
			manager := NewManager()
			defer manager.Stop()
			wc := newBoundTestConn(t, manager, 7, "session")
			manager.BindResponseConn("response", wc, "session", 7, "key")
			switch mode {
			case "discard":
				manager.DiscardConnection(wc)
			case "remove":
				manager.RemoveConnection(7, wc.URL, "session", "")
			case "detach":
				manager.removeConnectionFromPool(wc)
				if !wc.IsConnected() {
					t.Fatal("detaching must not close an active socket")
				}
			case "idle":
				wc.lastUsed.Store(time.Now().Add(-2 * IdleTimeout).UnixNano())
				manager.evictExpired()
			case "stop":
				manager.Stop()
			}
			manager.BindResponseConn("late", wc, "session", 7, "key")
			if n := responseBindingCount(manager); n != 0 {
				t.Fatalf("removal retained/recreated %d bindings", n)
			}
		})
	}
}

func TestResponseBindingCleanupDoesNotRemoveReplacement(t *testing.T) {
	manager := NewManager()
	defer manager.Stop()
	old := newBoundTestConn(t, manager, 7, "same-key")
	manager.BindResponseConn("same-response", old, "same-key", 7, "key")
	replacement := newBoundTestConn(t, manager, 7, "same-key")
	manager.BindResponseConn("same-response", replacement, "same-key", 7, "key")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 100 {
			manager.BindResponseConn("same-response", old, "same-key", 7, "key")
		}
	}()
	go func() {
		defer wg.Done()
		manager.DiscardConnection(old)
	}()
	wg.Wait()
	if got, _ := manager.lookupResponseConn("same-response", 7, "key"); got != replacement {
		t.Fatal("old cleanup/completion removed or overwrote the replacement's binding")
	}
	if !replacement.IsConnected() || manager.ConnectionCount() != 1 || manager.SessionCount() != 1 {
		t.Fatal("old cleanup affected the replacement connection/session")
	}
}

func TestResponseBindingCompletionRacesDiscard(t *testing.T) {
	manager := NewManager()
	defer manager.Stop()
	for i := range 100 {
		wc := newBoundTestConn(t, manager, 7, fmt.Sprintf("race-%d", i))
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			manager.BindResponseConn("race-response", wc, wc.session.ID, 7, "key")
		}()
		go func() {
			defer wg.Done()
			<-start
			manager.DiscardConnection(wc)
		}()
		close(start)
		wg.Wait()
		if n := responseBindingCount(manager); n != 0 {
			t.Fatalf("iteration %d left %d stale bindings", i, n)
		}
	}
}

func requireNoAcquisitionLocks(t *testing.T, m *Manager) {
	t.Helper()
	m.keyLocks.mu.Lock()
	keys := len(m.keyLocks.locks)
	m.keyLocks.mu.Unlock()
	m.accountLocks.mu.Lock()
	accounts := len(m.accountLocks.locks)
	m.accountLocks.mu.Unlock()
	if keys != 0 || accounts != 0 {
		t.Fatalf("acquisition locks retained after return: keys=%d accounts=%d", keys, accounts)
	}
}

func TestFailedAcquiresReleaseLockReferences(t *testing.T) {
	manager := NewManager()
	defer manager.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := range 1000 {
		account := &auth.Account{DBID: int64(i + 1), DynamicConcurrencyLimit: 2}
		_, _, err := manager.AcquireConnection(ctx, account, "ws://127.0.0.1:1/responses", fmt.Sprintf("stateless-%d", i), nil, "")
		if err == nil {
			t.Fatal("expected cancelled dial")
		}
	}
	requireNoAcquisitionLocks(t, manager)
	if manager.ConnectionCount() != 0 || manager.SessionCount() != 0 {
		t.Fatal("failed acquires retained a connection or session")
	}
}

func TestLockRegistryKeepsMutexForWaiters(t *testing.T) {
	var registry refCountedLockRegistry[string]
	first, releaseFirst := registry.retain("shared")
	first.Lock()
	registered := make(chan *sync.Mutex, 1)
	continueWaiter := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		waiter, releaseWaiter := registry.retain("shared")
		defer releaseWaiter()
		registered <- waiter
		waiter.Lock()
		defer waiter.Unlock()
		<-continueWaiter
	}()
	waiter := <-registered
	first.Unlock()
	releaseFirst()
	later, releaseLater := registry.retain("shared")
	if waiter != first || later != waiter {
		t.Error("registry replaced a mutex with an existing waiter/holder")
	}
	releaseLater()
	close(continueWaiter)
	<-finished
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if len(registry.locks) != 0 {
		t.Fatal("last waiter did not release its registry entry")
	}
}

func TestLockRegistrySerializesConcurrentReferences(t *testing.T) {
	var registry refCountedLockRegistry[string]
	var active atomic.Int32
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				lock, release := registry.retain("shared")
				lock.Lock()
				if active.Add(1) != 1 {
					t.Error("same key entered concurrently through different mutexes")
				}
				active.Add(-1)
				lock.Unlock()
				release()
			}
		}()
	}
	wg.Wait()
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if len(registry.locks) != 0 {
		t.Fatal("concurrent callers retained their mutex entry")
	}
}
