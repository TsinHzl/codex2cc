package auth

import (
	"strings"
	"sync/atomic"
	"time"
)

// accountListSnapshot is an immutable view of the Store account set. Account
// objects remain mutable, but the pointer slice is never changed after publish.
// Request and maintenance paths can therefore iterate without allocating a
// defensive copy or holding Store.mu for the whole scan.
type accountListSnapshot struct {
	accounts []*Account
}

type schedulerRuntimeMetrics struct {
	selectionTotal            atomic.Uint64
	selectionFastHit          atomic.Uint64
	selectionSlowHit          atomic.Uint64
	selectionMiss             atomic.Uint64
	selectionDurationNS       atomic.Uint64
	selectionDurationBuckets  [7]atomic.Uint64
	slowScannedAccounts       atomic.Uint64
	fastScannedAccounts       atomic.Uint64
	fastFilterChecks          atomic.Uint64
	fastAcquireFailures       atomic.Uint64
	fastLockWaitNS            atomic.Uint64
	modelCooldownCacheReads   atomic.Uint64
	waitStarted               atomic.Uint64
	waitWakeups               atomic.Uint64
	waitTimeouts              atomic.Uint64
	waitCanceled              atomic.Uint64
	waitRejected              atomic.Uint64
	waitRejectedPerKey        atomic.Uint64
	waitGranted               atomic.Uint64
	waitDurationNS            atomic.Uint64
	waitDurationBuckets       [6]atomic.Uint64
	waiters                   atomic.Int64
	availabilitySignals       atomic.Uint64
	snapshotGeneration        atomic.Uint64
	snapshotAccountCount      atomic.Int64
	lastSnapshotAtNS          atomic.Int64
	outboxWatermark           atomic.Int64
	outboxHighWatermark       atomic.Int64
	outboxEvents              atomic.Uint64
	outboxBatches             atomic.Uint64
	outboxErrors              atomic.Uint64
	outboxLagNS               atomic.Int64
	outboxLastAppliedNS       atomic.Int64
	routingCacheHits          atomic.Uint64
	routingCacheMisses        atomic.Uint64
	routingCacheBuilds        atomic.Uint64
	routingCacheFallbacks     atomic.Uint64
	routingCacheInvalidations atomic.Uint64
	routingCacheEvictions     atomic.Uint64
	routingCacheEntries       atomic.Int64
	routingCacheAccounts      atomic.Int64
	shadowSampleCounter       atomic.Uint64
	shadowChecks              atomic.Uint64
	shadowAgreements          atomic.Uint64
	shadowMismatches          atomic.Uint64
}

// SchedulerMetricsSnapshot is safe to expose through the admin operations API.
// All values are process-local and monotonic except Waiters and account count.
type SchedulerMetricsSnapshot struct {
	Engine                    string            `json:"engine"`
	SelectionTotal            uint64            `json:"selection_total"`
	SelectionFastHit          uint64            `json:"selection_fast_hit"`
	SelectionSlowHit          uint64            `json:"selection_slow_hit"`
	SelectionMiss             uint64            `json:"selection_miss"`
	SelectionDurationNS       uint64            `json:"selection_duration_ns"`
	SelectionDurationBuckets  map[string]uint64 `json:"selection_duration_buckets"`
	SlowScannedAccounts       uint64            `json:"slow_scanned_accounts"`
	FastScannedAccounts       uint64            `json:"fast_scanned_accounts"`
	FastFilterChecks          uint64            `json:"fast_filter_checks"`
	FastAcquireFailures       uint64            `json:"fast_acquire_failures"`
	FastLockWaitNS            uint64            `json:"fast_lock_wait_ns"`
	ModelCooldownCacheReads   uint64            `json:"model_cooldown_cache_reads"`
	WaitStarted               uint64            `json:"wait_started"`
	WaitWakeups               uint64            `json:"wait_wakeups"`
	WaitTimeouts              uint64            `json:"wait_timeouts"`
	WaitCanceled              uint64            `json:"wait_canceled"`
	WaitRejected              uint64            `json:"wait_rejected"`
	WaitRejectedPerKey        uint64            `json:"wait_rejected_per_key"`
	WaitGranted               uint64            `json:"wait_granted"`
	WaitDurationNS            uint64            `json:"wait_duration_ns"`
	WaitDurationBuckets       map[string]uint64 `json:"wait_duration_buckets"`
	MaxWaiters                int               `json:"max_waiters"`
	MaxWaitersPerKey          int               `json:"max_waiters_per_key"`
	Waiters                   int64             `json:"waiters"`
	AvailabilitySignals       uint64            `json:"availability_signals"`
	SnapshotGeneration        uint64            `json:"snapshot_generation"`
	SnapshotAccountCount      int64             `json:"snapshot_account_count"`
	LastSnapshotAt            string            `json:"last_snapshot_at"`
	OutboxWatermark           int64             `json:"outbox_watermark"`
	OutboxHighWatermark       int64             `json:"outbox_high_watermark"`
	OutboxBacklog             int64             `json:"outbox_backlog"`
	OutboxEvents              uint64            `json:"outbox_events"`
	OutboxBatches             uint64            `json:"outbox_batches"`
	OutboxErrors              uint64            `json:"outbox_errors"`
	OutboxLagMS               int64             `json:"outbox_lag_ms"`
	OutboxLastAppliedAt       string            `json:"outbox_last_applied_at"`
	RoutingCacheHits          uint64            `json:"routing_cache_hits"`
	RoutingCacheMisses        uint64            `json:"routing_cache_misses"`
	RoutingCacheBuilds        uint64            `json:"routing_cache_builds"`
	RoutingCacheFallbacks     uint64            `json:"routing_cache_fallbacks"`
	RoutingCacheInvalidations uint64            `json:"routing_cache_invalidations"`
	RoutingCacheEvictions     uint64            `json:"routing_cache_evictions"`
	RoutingCacheEntries       int64             `json:"routing_cache_entries"`
	RoutingCacheAccounts      int64             `json:"routing_cache_accounts"`
	ShadowChecks              uint64            `json:"shadow_checks"`
	ShadowAgreements          uint64            `json:"shadow_agreements"`
	ShadowMismatches          uint64            `json:"shadow_mismatches"`
}

func newSchedulerRuntimeMetrics() *schedulerRuntimeMetrics {
	return &schedulerRuntimeMetrics{}
}

func (m *schedulerRuntimeMetrics) snapshot(engine string) SchedulerMetricsSnapshot {
	if m == nil {
		return SchedulerMetricsSnapshot{Engine: engine}
	}
	lastSnapshotAt := ""
	if value := m.lastSnapshotAtNS.Load(); value > 0 {
		lastSnapshotAt = time.Unix(0, value).UTC().Format(time.RFC3339Nano)
	}
	outboxLastAppliedAt := ""
	if value := m.outboxLastAppliedNS.Load(); value > 0 {
		outboxLastAppliedAt = time.Unix(0, value).UTC().Format(time.RFC3339Nano)
	}
	watermark := m.outboxWatermark.Load()
	highWatermark := m.outboxHighWatermark.Load()
	backlog := highWatermark - watermark
	if backlog < 0 {
		backlog = 0
	}
	return SchedulerMetricsSnapshot{
		Engine:                    engine,
		SelectionTotal:            m.selectionTotal.Load(),
		SelectionFastHit:          m.selectionFastHit.Load(),
		SelectionSlowHit:          m.selectionSlowHit.Load(),
		SelectionMiss:             m.selectionMiss.Load(),
		SelectionDurationNS:       m.selectionDurationNS.Load(),
		SelectionDurationBuckets:  m.durationBucketsSnapshot(),
		SlowScannedAccounts:       m.slowScannedAccounts.Load(),
		FastScannedAccounts:       m.fastScannedAccounts.Load(),
		FastFilterChecks:          m.fastFilterChecks.Load(),
		FastAcquireFailures:       m.fastAcquireFailures.Load(),
		FastLockWaitNS:            m.fastLockWaitNS.Load(),
		ModelCooldownCacheReads:   m.modelCooldownCacheReads.Load(),
		WaitStarted:               m.waitStarted.Load(),
		WaitWakeups:               m.waitWakeups.Load(),
		WaitTimeouts:              m.waitTimeouts.Load(),
		WaitCanceled:              m.waitCanceled.Load(),
		WaitRejected:              m.waitRejected.Load(),
		WaitRejectedPerKey:        m.waitRejectedPerKey.Load(),
		WaitGranted:               m.waitGranted.Load(),
		WaitDurationNS:            m.waitDurationNS.Load(),
		WaitDurationBuckets:       m.waitDurationBucketsSnapshot(),
		Waiters:                   m.waiters.Load(),
		AvailabilitySignals:       m.availabilitySignals.Load(),
		SnapshotGeneration:        m.snapshotGeneration.Load(),
		SnapshotAccountCount:      m.snapshotAccountCount.Load(),
		LastSnapshotAt:            lastSnapshotAt,
		OutboxWatermark:           watermark,
		OutboxHighWatermark:       highWatermark,
		OutboxBacklog:             backlog,
		OutboxEvents:              m.outboxEvents.Load(),
		OutboxBatches:             m.outboxBatches.Load(),
		OutboxErrors:              m.outboxErrors.Load(),
		OutboxLagMS:               m.outboxLagNS.Load() / int64(time.Millisecond),
		OutboxLastAppliedAt:       outboxLastAppliedAt,
		RoutingCacheHits:          m.routingCacheHits.Load(),
		RoutingCacheMisses:        m.routingCacheMisses.Load(),
		RoutingCacheBuilds:        m.routingCacheBuilds.Load(),
		RoutingCacheFallbacks:     m.routingCacheFallbacks.Load(),
		RoutingCacheInvalidations: m.routingCacheInvalidations.Load(),
		RoutingCacheEvictions:     m.routingCacheEvictions.Load(),
		RoutingCacheEntries:       m.routingCacheEntries.Load(),
		RoutingCacheAccounts:      m.routingCacheAccounts.Load(),
		ShadowChecks:              m.shadowChecks.Load(),
		ShadowAgreements:          m.shadowAgreements.Load(),
		ShadowMismatches:          m.shadowMismatches.Load(),
	}
}

func (s *Store) publishAccountSnapshot(accounts []*Account) {
	if s == nil {
		return
	}
	view := append([]*Account(nil), accounts...)
	s.accountSnapshot.Store(&accountListSnapshot{accounts: view})
	if s.schedulerMetrics != nil {
		s.schedulerMetrics.snapshotGeneration.Add(1)
		s.schedulerMetrics.snapshotAccountCount.Store(int64(len(view)))
		s.schedulerMetrics.lastSnapshotAtNS.Store(time.Now().UnixNano())
	}
}

// accountSnapshotAccounts returns an immutable pointer slice. Callers must not
// append to, reorder, or otherwise mutate the returned slice.
func (s *Store) accountSnapshotAccounts() []*Account {
	if s == nil {
		return nil
	}
	if snapshot := s.accountSnapshot.Load(); snapshot != nil {
		return snapshot.accounts
	}

	// Stores assembled directly by tests may predate NewStore initialization.
	// Publish one defensive snapshot so later calls remain allocation-free.
	s.mu.RLock()
	accounts := append([]*Account(nil), s.accounts...)
	s.mu.RUnlock()
	s.publishAccountSnapshot(accounts)
	return s.accountSnapshot.Load().accounts
}

func (s *Store) notifySchedulerAvailability() {
	if s == nil {
		return
	}
	hub := s.schedulerAvailabilityHub()
	if s.schedulerMetrics != nil {
		s.schedulerMetrics.availabilitySignals.Add(1)
	}
	hub.notify()
}

func (s *Store) schedulerAvailabilityHub() *availabilityHub {
	if s == nil {
		return nil
	}
	if hub := s.availability.Load(); hub != nil {
		return hub
	}
	hub := newAvailabilityHub()
	if s.availability.CompareAndSwap(nil, hub) {
		return hub
	}
	return s.availability.Load()
}

func (s *Store) SchedulerEngine() string {
	if s == nil {
		return "legacy"
	}
	if raw := s.schedulerEngine.Load(); raw != nil {
		if value, ok := raw.(string); ok {
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "legacy", "shadow", "indexed":
				return strings.ToLower(strings.TrimSpace(value))
			}
		}
	}
	if s.FastSchedulerEnabled() {
		return "indexed"
	}
	return "legacy"
}

func normalizeSchedulerEngine(raw string, fastEnabled bool) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "legacy", "shadow", "indexed":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		if fastEnabled {
			return "indexed"
		}
		return "legacy"
	}
}

// SetSchedulerEngine hot-switches the dispatch engine. Shadow keeps legacy
// dispatch authoritative while maintaining the indexed scheduler for parity
// checks and operational inspection.
func (s *Store) SetSchedulerEngine(engine string) {
	if s == nil {
		return
	}
	engine = normalizeSchedulerEngine(engine, s.FastSchedulerEnabled())
	if s.SchedulerEngine() == engine {
		if engine == "legacy" || s.getFastScheduler() != nil {
			return
		}
	}
	s.schedulerEngine.Store(engine)
	enabled := engine != "legacy"
	s.fastSchedulerEnabled.Store(enabled)
	if enabled {
		s.recomputeAllAccountSchedulerState()
		s.rebuildFastScheduler()
	} else {
		s.fastScheduler.Store(nil)
		s.invalidateRoutingSchedulers()
	}
	s.notifySchedulerAvailability()
}

func (s *Store) GetSchedulerMetrics() SchedulerMetricsSnapshot {
	if s == nil {
		return SchedulerMetricsSnapshot{Engine: "legacy"}
	}
	snapshot := s.schedulerMetrics.snapshot(s.SchedulerEngine())
	hub := s.schedulerAvailabilityHub()
	hub.mu.Lock()
	snapshot.MaxWaiters, snapshot.MaxWaitersPerKey = hub.maxWaiters, hub.maxWaitersPerKey
	hub.mu.Unlock()
	return snapshot
}

var schedulerWaitDurationBounds = [...]time.Duration{10 * time.Millisecond, 100 * time.Millisecond, time.Second, 10 * time.Second, 30 * time.Second}
var schedulerWaitDurationLabels = [...]string{"le_10ms", "le_100ms", "le_1s", "le_10s", "le_30s", "inf"}

func (m *schedulerRuntimeMetrics) recordWaitDuration(elapsed time.Duration) {
	m.waitDurationNS.Add(uint64(elapsed))
	for i, bound := range schedulerWaitDurationBounds {
		if elapsed <= bound {
			m.waitDurationBuckets[i].Add(1)
			return
		}
	}
	m.waitDurationBuckets[len(schedulerWaitDurationBounds)].Add(1)
}

func (m *schedulerRuntimeMetrics) waitDurationBucketsSnapshot() map[string]uint64 {
	result := make(map[string]uint64, len(schedulerWaitDurationLabels))
	var cumulative uint64
	for i, label := range schedulerWaitDurationLabels {
		cumulative += m.waitDurationBuckets[i].Load()
		result[label] = cumulative
	}
	return result
}

func (s *Store) RuntimeRequestCounts() (active, total int64) {
	for _, acc := range s.accountSnapshotAccounts() {
		if acc == nil {
			continue
		}
		active += acc.GetActiveRequests()
		total += acc.GetTotalRequests()
	}
	return active, total
}

func (s *Store) recordSchedulerSelection(started time.Time, fast, slow, hit bool, scanned int) {
	if s == nil || s.schedulerMetrics == nil {
		return
	}
	m := s.schedulerMetrics
	m.selectionTotal.Add(1)
	elapsed := time.Since(started)
	m.selectionDurationNS.Add(uint64(elapsed))
	bucket := len(schedulerDurationBounds)
	for i, bound := range schedulerDurationBounds {
		if elapsed <= bound {
			bucket = i
			break
		}
	}
	m.selectionDurationBuckets[bucket].Add(1)
	if fast && hit {
		m.selectionFastHit.Add(1)
	} else if slow && hit {
		m.selectionSlowHit.Add(1)
	} else if !hit {
		m.selectionMiss.Add(1)
	}
	if scanned > 0 {
		m.slowScannedAccounts.Add(uint64(scanned))
	}
}

var schedulerDurationBounds = [...]time.Duration{
	10 * time.Microsecond, 100 * time.Microsecond, time.Millisecond,
	10 * time.Millisecond, 100 * time.Millisecond, time.Second,
}

// Cumulative buckets describe the same selections as selection_total; bound
// session hits do not pass through this metric. Units are part of each key.
func (m *schedulerRuntimeMetrics) durationBucketsSnapshot() map[string]uint64 {
	labels := [...]string{"10us", "100us", "1ms", "10ms", "100ms", "1s", "+Inf"}
	result := make(map[string]uint64, len(labels))
	var sum uint64
	for i, label := range labels {
		sum += m.selectionDurationBuckets[i].Load()
		result[label] = sum
	}
	return result
}

const schedulerShadowSampleEvery = 64

func (s *Store) shouldSampleSchedulerShadow() bool {
	if s == nil || s.schedulerMetrics == nil {
		return false
	}
	return (s.schedulerMetrics.shadowSampleCounter.Add(1)-1)%schedulerShadowSampleEvery == 0
}

func (s *Store) recordSchedulerShadow(indexedHit, legacyHit bool) {
	if s == nil || s.schedulerMetrics == nil {
		return
	}
	s.schedulerMetrics.shadowChecks.Add(1)
	if indexedHit == legacyHit {
		s.schedulerMetrics.shadowAgreements.Add(1)
		return
	}
	s.schedulerMetrics.shadowMismatches.Add(1)
}
