package auth

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const (
	DefaultSchedulerMaxWaiters       = 4096
	DefaultSchedulerMaxWaitersPerKey = 256
	availabilityMaxConcurrentWakes   = 8
	availabilityRecheckInterval      = time.Second
)

var (
	ErrSchedulerQueueFull    = errors.New("scheduler wait queue is full")
	ErrSchedulerKeyQueueFull = fmt.Errorf("%w: API key wait limit reached", ErrSchedulerQueueFull)
)

// SchedulerWaitHeartbeat runs on the waiting request goroutine, outside queue
// locks. It emits a heartbeat when due and returns the next deadline delay.
// Keeping it inside one wait preserves queue position across SSE/WS heartbeats.
type SchedulerWaitHeartbeat func() (time.Duration, error)

// availabilityHub bounds waiting requests and hands capacity notifications to
// individual waiters. Ready API-key lanes rotate; each lane selects its oldest
// eligible waiter. Arbitrary account filters and all admission/CAS operations
// run in the request goroutine, never under this lock.
type availabilityHub struct {
	mu               sync.Mutex
	lanes            map[int64]*availabilityLane
	ready            list.List
	count            int
	waiters          atomic.Int64 // Keeps the unsaturated Release path lock-free.
	maxWaiters       int
	maxWaitersPerKey int
	sequence         uint64
	generation       uint64
	activeWaves      int
	pending          bool
	timer            *time.Timer
	timerSequence    uint64
	done             chan struct{}
	closed           bool
}

type availabilityLane struct {
	keyID   int64
	count   int // Includes waiters doing their initial check or a notified retry.
	ready   list.List
	element *list.Element
}

type availabilityWaiter struct {
	lane       *availabilityLane
	element    *list.Element
	sequence   uint64
	generation uint64
	boundID    int64
	exclude    map[int64]bool
	ready      chan struct{}
	wave       *availabilityWave
	left       bool
}

// A release consumes its wave on the first grant. A state change drains newly
// available capacity. Failed admission passes the wave on; a finite visited
// set prevents incompatible filters or continuations from spinning forever.
type availabilityWave struct {
	accountID     int64
	maxSequence   uint64
	drain         bool
	firstSequence uint64
	visited       map[uint64]struct{}
}

func newAvailabilityHub() *availabilityHub {
	return &availabilityHub{
		lanes: make(map[int64]*availabilityLane), done: make(chan struct{}),
		maxWaiters: DefaultSchedulerMaxWaiters, maxWaitersPerKey: DefaultSchedulerMaxWaitersPerKey,
	}
}

func (h *availabilityHub) join(keyID, boundID int64, exclude map[int64]bool) (*availabilityWaiter, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, context.Canceled
	}
	lane := h.lanes[keyID]
	if lane != nil && lane.count >= h.maxWaitersPerKey {
		return nil, ErrSchedulerKeyQueueFull
	}
	if h.count >= h.maxWaiters {
		return nil, ErrSchedulerQueueFull
	}
	if lane == nil {
		lane = &availabilityLane{keyID: keyID}
		h.lanes[keyID] = lane
	}
	lane.count++
	h.count++
	h.waiters.Add(1)
	h.sequence++
	w := &availabilityWaiter{lane: lane, sequence: h.sequence, generation: h.generation, boundID: boundID, exclude: exclude, ready: make(chan struct{}, 1)}
	if h.count == 1 {
		h.armRecheckLocked()
	}
	return w, nil
}

func (h *availabilityHub) enqueueLocked(w *availabilityWaiter) {
	lane := w.lane
	w.element = lane.ready.PushBack(w)
	if lane.element == nil {
		lane.element = h.ready.PushBack(lane)
	}
}

func (h *availabilityHub) dequeueLocked(w *availabilityWaiter) {
	if w.element == nil {
		return
	}
	lane := w.lane
	lane.ready.Remove(w.element)
	w.element = nil
	if lane.ready.Len() == 0 {
		h.ready.Remove(lane.element)
		lane.element = nil
	} else {
		h.ready.MoveToBack(lane.element)
	}
}

func (h *availabilityHub) dispatchLocked(wave *availabilityWave) bool {
	if h.closed {
		return false
	}
	for le := h.ready.Front(); le != nil; le = le.Next() {
		lane := le.Value.(*availabilityLane)
		for e := lane.ready.Front(); e != nil; e = e.Next() {
			w := e.Value.(*availabilityWaiter)
			if w.sequence > wave.maxSequence {
				continue
			}
			if _, seen := wave.visited[w.sequence]; seen || w.sequence == wave.firstSequence {
				continue
			}
			if id := wave.accountID; id != 0 && (w.exclude[id] || (w.boundID != 0 && w.boundID != id)) {
				continue
			}
			h.dequeueLocked(w)
			// A normal release succeeds on its first recipient. Only failed
			// filtering or capacity draining needs an allocated visited set.
			if wave.firstSequence == 0 {
				wave.firstSequence = w.sequence
			} else {
				if wave.visited == nil {
					wave.visited = make(map[uint64]struct{})
				}
				wave.visited[w.sequence] = struct{}{}
			}
			w.wave = wave
			w.generation = h.generation
			w.ready <- struct{}{}
			return true
		}
	}
	return false
}

func (h *availabilityHub) startWaveLocked(accountID int64, drain bool) {
	if h.closed || h.count == 0 {
		return
	}
	if h.activeWaves >= availabilityMaxConcurrentWakes {
		// A burst is coalesced into one capacity-draining pass after an active
		// wave finishes, rather than spawning one selector per notification.
		h.pending = true
		return
	}
	wave := &availabilityWave{accountID: accountID, maxSequence: h.sequence, drain: drain}
	if h.dispatchLocked(wave) {
		h.activeWaves++
	}
}

func (h *availabilityHub) notify() { h.notifyAccount(0, true) }

func (h *availabilityHub) notifyAccount(accountID int64, drain bool) {
	if h.waiters.Load() == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.generation++
	h.startWaveLocked(accountID, drain)
}

// finish parks a failed attempt or removes a completed/canceled waiter. A
// canceled recipient hands its notification on even if it never read ready.
func (h *availabilityHub) finish(w *availabilityWaiter, acquired, leave bool, boundID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if w.left {
		return
	}
	if leave {
		h.dequeueLocked(w)
		w.left = true
		h.count--
		h.waiters.Add(-1)
		w.lane.count--
		if w.lane.count == 0 {
			delete(h.lanes, w.lane.keyID)
		}
	} else {
		w.boundID = boundID
		h.enqueueLocked(w)
		// Registration/dispatch precedes selection. A concurrent release
		// between the failed CAS and parking must cause a fresh pass.
		if w.generation != h.generation {
			h.pending = true
		}
	}
	wave := w.wave
	w.wave = nil
	if wave != nil && (acquired && !wave.drain || !h.dispatchLocked(wave)) {
		h.activeWaves--
	}
	if h.pending && h.activeWaves < availabilityMaxConcurrentWakes {
		h.pending = false
		h.startWaveLocked(0, true)
	}
	if h.count == 0 {
		h.pending = false
		h.timerSequence++
		if h.timer != nil {
			h.timer.Stop()
			h.timer = nil
		}
	}
}

func (h *availabilityHub) armRecheckLocked() {
	h.timerSequence++
	sequence := h.timerSequence
	h.timer = time.AfterFunc(availabilityRecheckInterval, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.closed || h.count == 0 || h.timerSequence != sequence {
			return
		}
		h.generation++
		h.startWaveLocked(0, true)
		h.armRecheckLocked()
	})
}

func (h *availabilityHub) stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.closed {
		h.closed = true
		close(h.done)
		if h.timer != nil {
			h.timer.Stop()
		}
	}
}

// SetSchedulerWaitLimits configures process-local overload protection. Lowering
// a limit preserves existing waiters and rejects new ones until they drain.
func (s *Store) SetSchedulerWaitLimits(total, perKey int) {
	if s == nil {
		return
	}
	if total <= 0 {
		total = DefaultSchedulerMaxWaiters
	}
	if perKey <= 0 {
		perKey = DefaultSchedulerMaxWaitersPerKey
	}
	h := s.schedulerAvailabilityHub()
	h.mu.Lock()
	h.maxWaiters, h.maxWaitersPerKey = total, perKey
	h.mu.Unlock()
}

func (s *Store) notifySchedulerAccountAvailability(acc *Account, drain bool) {
	if s == nil || acc == nil {
		return
	}
	if s.schedulerMetrics != nil {
		s.schedulerMetrics.availabilitySignals.Add(1)
	}
	s.schedulerAvailabilityHub().notifyAccount(acc.ID(), drain)
}
