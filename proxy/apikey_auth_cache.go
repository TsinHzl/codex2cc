package proxy

import (
	"container/list"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/codex2cc/cache"
	"github.com/codex2cc/database"
	"golang.org/x/sync/singleflight"
)

const (
	// Bump when the cached authentication schema or its semantics change.
	apiKeyAuthSnapshotVersion = 1
	apiKeyAuthNamespace       = "api-key-auth-v1"
	apiKeyAuthL1TTL           = 15 * time.Second
	apiKeyAuthL2TTL           = 5 * time.Minute
	apiKeyAuthRevisionTTL     = 250 * time.Millisecond
	apiKeyAuthNegativeTTL     = 2 * time.Second
	apiKeyAuthMaxEntries      = 4096
	apiKeyAuthMaxBytes        = 16 << 20
	apiKeyAuthMaxEntryBytes   = 64 << 10
)

var errAPIKeyAuthRetry = errors.New("authentication configuration changed during lookup")

func waitAPIKeyAuthLoad(ctx context.Context, group *singleflight.Group, key string, load func() (any, error)) (any, error) {
	result := group.DoChan(key, load)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-result:
		return result.Val, result.Err
	}
}

type apiKeyAuthStore interface {
	GetAPIKeyAuthRevision(context.Context) (database.APIKeyAuthRevision, error)
	GetAPIKeyByValue(context.Context, string) (*database.APIKeyRow, error)
	GetAPIKeyAuthQuota(context.Context, int64) (float64, database.APIKeyAuthRevision, error)
}

type apiKeyAuthRecord struct {
	KeyDigest  string              `json:"key_digest"`
	Scope      string              `json:"scope"`
	Version    int                 `json:"version"`
	Generation int64               `json:"generation"`
	ExpiresAt  time.Time           `json:"expires_at"`
	Row        *database.APIKeyRow `json:"row"`
}

type apiKeyAuthEntry struct {
	key       string
	record    apiKeyAuthRecord
	expiresAt time.Time
	bytes     int
}

type apiKeyAuthCache struct {
	db                   apiKeyAuthStore
	backend              cache.TokenCache
	mu                   sync.Mutex
	state                database.APIKeyAuthRevision
	checkedAt            time.Time
	epoch                uint64
	entries              map[string]*list.Element
	lru                  *list.List
	bytes                int
	loads, revisionLoads singleflight.Group
	ctx                  context.Context
	workMu               sync.Mutex
	closing              bool
	cancel               context.CancelFunc
	wg                   sync.WaitGroup
	localHits            atomic.Uint64
	remoteHits           atomic.Uint64
	misses               atomic.Uint64
	negativeHits         atomic.Uint64
	dbLoads              atomic.Uint64
	quotaReads           atomic.Uint64
	revisionChecks       atomic.Uint64
	invalidations        atomic.Uint64
	errors               atomic.Uint64
	evictions            atomic.Uint64
	bypasses             atomic.Uint64
}

type APIKeyAuthCacheStats struct {
	Enabled          bool   `json:"enabled"`
	Shared           bool   `json:"shared"`
	Entries          int    `json:"entries"`
	Bytes            int    `json:"bytes"`
	MaxEntries       int    `json:"max_entries"`
	MaxBytes         int    `json:"max_bytes"`
	Generation       int64  `json:"generation"`
	RevisionMaxAgeMs int64  `json:"revision_max_age_ms"`
	LocalHits        uint64 `json:"local_hits"`
	RemoteHits       uint64 `json:"remote_hits"`
	Misses           uint64 `json:"misses"`
	NegativeHits     uint64 `json:"negative_hits"`
	DBLoads          uint64 `json:"db_loads"`
	QuotaReads       uint64 `json:"quota_reads"`
	RevisionChecks   uint64 `json:"revision_checks"`
	Invalidations    uint64 `json:"invalidations"`
	Errors           uint64 `json:"errors"`
	Evictions        uint64 `json:"evictions"`
	OversizeBypasses uint64 `json:"oversize_bypasses"`
}

func newAPIKeyAuthCache(db apiKeyAuthStore, backend cache.TokenCache) *apiKeyAuthCache {
	ctx, cancel := context.WithCancel(context.Background())
	a := &apiKeyAuthCache{db: db, backend: backend, entries: make(map[string]*list.Element), lru: list.New(), ctx: ctx, cancel: cancel}
	if bus, ok := backend.(cache.AuthInvalidationBus); ok && backend.SharedAcrossInstances() {
		a.wg.Add(1)
		go func() { defer a.wg.Done(); a.subscribe(ctx, bus) }()
	}
	return a
}

func (a *apiKeyAuthCache) close() {
	a.workMu.Lock()
	a.closing = true
	a.workMu.Unlock()
	a.cancel()
	a.wg.Wait()
}

func (a *apiKeyAuthCache) operationContext(parent context.Context, timeout time.Duration) (context.Context, func(), error) {
	a.workMu.Lock()
	if a.closing {
		a.workMu.Unlock()
		return nil, nil, context.Canceled
	}
	a.wg.Add(1)
	a.workMu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
	stop := context.AfterFunc(a.ctx, cancel)
	return ctx, func() { stop(); cancel(); a.wg.Done() }, nil
}

func (a *apiKeyAuthCache) subscribe(ctx context.Context, bus cache.AuthInvalidationBus) {
	for ctx.Err() == nil {
		state, _, err := a.revision(ctx)
		if err == nil {
			err = bus.SubscribeAuthInvalidations(ctx, state.Namespace, func() { a.invalidate() })
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			a.errors.Add(1)
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (a *apiKeyAuthCache) clearLocked() {
	a.entries = make(map[string]*list.Element)
	a.lru.Init()
	a.bytes = 0
}

func (a *apiKeyAuthCache) invalidate() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.epoch++
	a.checkedAt = time.Time{}
	a.clearLocked()
	a.invalidations.Add(1)
	return a.state.Namespace
}

type apiKeyAuthRevisionResult struct {
	state database.APIKeyAuthRevision
	epoch uint64
}

func (a *apiKeyAuthCache) readRevision(ctx context.Context) (database.APIKeyAuthRevision, error) {
	a.revisionChecks.Add(1)
	state, err := a.db.GetAPIKeyAuthRevision(ctx)
	if err == nil && (state.Namespace == "" || state.Generation <= 0 || state.KeyCount < 0) {
		err = errors.New("invalid authentication cache revision")
	}
	return state, err
}

func (a *apiKeyAuthCache) revision(ctx context.Context) (database.APIKeyAuthRevision, uint64, error) {
	a.mu.Lock()
	state, epoch, checked := a.state, a.epoch, a.checkedAt
	a.mu.Unlock()
	if !checked.IsZero() && time.Since(checked) < apiKeyAuthRevisionTTL {
		return state, epoch, nil
	}
	value, err := waitAPIKeyAuthLoad(ctx, &a.revisionLoads, fmt.Sprint(epoch), func() (any, error) {
		a.mu.Lock()
		if a.epoch != epoch {
			a.mu.Unlock()
			return nil, errAPIKeyAuthRetry
		}
		if !a.checkedAt.IsZero() && time.Since(a.checkedAt) < apiKeyAuthRevisionTTL {
			result := apiKeyAuthRevisionResult{a.state, a.epoch}
			a.mu.Unlock()
			return result, nil
		}
		a.mu.Unlock()
		readCtx, cancel, err := a.operationContext(ctx, 2*time.Second)
		if err != nil {
			return nil, err
		}
		defer cancel()
		started := time.Now()
		fresh, err := a.readRevision(readCtx)
		if err != nil {
			a.errors.Add(1)
			return nil, err
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.epoch != epoch {
			return nil, errAPIKeyAuthRetry
		}
		if fresh != a.state {
			a.clearLocked()
			a.epoch++
			a.invalidations.Add(1)
		}
		a.state = fresh
		a.checkedAt = started
		return apiKeyAuthRevisionResult{fresh, a.epoch}, nil
	})
	if err != nil {
		return database.APIKeyAuthRevision{}, 0, err
	}
	result := value.(apiKeyAuthRevisionResult)
	return result.state, result.epoch, nil
}

func (a *apiKeyAuthCache) local(key string, state database.APIKeyAuthRevision, epoch uint64) (apiKeyAuthRecord, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.epoch != epoch || a.state != state {
		return apiKeyAuthRecord{}, false
	}
	element := a.entries[key]
	if element == nil {
		return apiKeyAuthRecord{}, false
	}
	entry := element.Value.(*apiKeyAuthEntry)
	if !time.Now().Before(entry.expiresAt) {
		a.removeLocked(element)
		return apiKeyAuthRecord{}, false
	}
	a.lru.MoveToFront(element)
	if entry.record.Row == nil {
		a.negativeHits.Add(1)
	} else {
		a.localHits.Add(1)
	}
	return entry.record, true
}

func (a *apiKeyAuthCache) removeLocked(element *list.Element) {
	entry := element.Value.(*apiKeyAuthEntry)
	delete(a.entries, entry.key)
	a.bytes -= entry.bytes
	a.lru.Remove(element)
}

func (a *apiKeyAuthCache) admit(key string, record apiKeyAuthRecord, size int, state database.APIKeyAuthRevision, epoch uint64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.epoch != epoch || a.state != state {
		return false
	}
	if size > apiKeyAuthMaxEntryBytes {
		a.bypasses.Add(1)
		return true
	}
	if old := a.entries[key]; old != nil {
		a.removeLocked(old)
	}
	for len(a.entries) >= apiKeyAuthMaxEntries || a.bytes+size > apiKeyAuthMaxBytes {
		a.removeLocked(a.lru.Back())
		a.evictions.Add(1)
	}
	ttl := apiKeyAuthL1TTL
	if record.Row == nil {
		ttl = apiKeyAuthNegativeTTL
	}
	expires := time.Now().Add(ttl)
	if record.ExpiresAt.Before(expires) {
		expires = record.ExpiresAt
	}
	a.entries[key] = a.lru.PushFront(&apiKeyAuthEntry{key: key, record: record, expiresAt: expires, bytes: size})
	a.bytes += size
	return true
}

func authSnapshotKey(state database.APIKeyAuthRevision, key string) string {
	return fmt.Sprintf("%s:%d:%s", state.Namespace, state.Generation, key)
}

func (a *apiKeyAuthCache) load(ctx context.Context, key, digest string, state database.APIKeyAuthRevision, epoch uint64) (apiKeyAuthRecord, error) {
	if record, ok := a.local(digest, state, epoch); ok {
		return record, nil
	}
	storeKey := authSnapshotKey(state, digest)
	if a.backend != nil && a.backend.SharedAcrossInstances() {
		readCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		var raw []byte
		var found bool
		var err error
		if bounded, ok := a.backend.(cache.BoundedRuntimeReader); ok {
			raw, found, err = bounded.GetRuntimeBounded(readCtx, apiKeyAuthNamespace, storeKey, apiKeyAuthMaxEntryBytes)
		} else {
			raw, found, err = a.backend.GetRuntime(readCtx, apiKeyAuthNamespace, storeKey)
		}
		cancel()
		if err != nil {
			a.errors.Add(1)
		}
		var record apiKeyAuthRecord
		if err == nil && found && len(raw) <= apiKeyAuthMaxEntryBytes && json.Unmarshal(raw, &record) == nil && record.Version == apiKeyAuthSnapshotVersion && record.KeyDigest == digest && record.Scope == state.Namespace && record.Generation == state.Generation && record.Row != nil && record.Row.ID > 0 && record.Row.Key == "" && time.Now().Before(record.ExpiresAt) && record.ExpiresAt.Before(time.Now().Add(apiKeyAuthL2TTL+time.Second)) {
			if !a.admit(digest, record, len(raw), state, epoch) {
				return apiKeyAuthRecord{}, errAPIKeyAuthRetry
			}
			a.remoteHits.Add(1)
			return record, nil
		}
	}
	a.dbLoads.Add(1)
	row, err := a.db.GetAPIKeyByValue(ctx, key)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return apiKeyAuthRecord{}, err
	}
	// Confirm the namespace after the SQL read. A delayed pre-update reader
	// can only write to its old revision, never repopulate the current one.
	checked := time.Now()
	fresh, err := a.readRevision(ctx)
	if err != nil {
		return apiKeyAuthRecord{}, err
	}
	if fresh != state {
		a.invalidate()
		return apiKeyAuthRecord{}, errAPIKeyAuthRetry
	}
	a.mu.Lock()
	current := a.epoch == epoch && a.state == state
	if current && checked.After(a.checkedAt) {
		a.checkedAt = checked
	}
	a.mu.Unlock()
	if !current {
		return apiKeyAuthRecord{}, errAPIKeyAuthRetry
	}
	if row != nil {
		row = cloneAPIKeyLookupRow(row)
		row.Key = ""
		row.QuotaUsed = 0
		row.TotalUsed = 0
		row.ResetCount = 0
		row.LastResetAt = sql.NullTime{}
	}
	record := apiKeyAuthRecord{Version: apiKeyAuthSnapshotVersion, KeyDigest: digest, Scope: state.Namespace, Generation: state.Generation, ExpiresAt: time.Now().Add(apiKeyAuthL2TTL), Row: row}
	raw, err := json.Marshal(record)
	if err != nil {
		return apiKeyAuthRecord{}, err
	}
	if !a.admit(digest, record, len(raw), state, epoch) {
		return apiKeyAuthRecord{}, errAPIKeyAuthRetry
	}
	// Invalid credentials are attacker-controlled: negative entries stay in
	// bounded L1, and never create shared Redis keys.
	if row != nil && len(raw) <= apiKeyAuthMaxEntryBytes && a.backend != nil && a.backend.SharedAcrossInstances() {
		writeCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		var writeErr error
		if writer, ok := a.backend.(cache.AuthSnapshotWriter); ok {
			writeErr = writer.SetAuthSnapshot(writeCtx, apiKeyAuthNamespace, storeKey, raw, apiKeyAuthL2TTL)
		} else {
			writeErr = a.backend.SetRuntime(writeCtx, apiKeyAuthNamespace, storeKey, raw, apiKeyAuthL2TTL)
		}
		if writeErr != nil {
			a.errors.Add(1)
		}
		cancel()
	}
	return record, nil
}

func (a *apiKeyAuthCache) resolve(ctx context.Context, key string) (*database.APIKeyRow, bool, error) {
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
	for attempt := 0; attempt < 3; attempt++ {
		if err := a.ctx.Err(); err != nil {
			return nil, false, err
		}
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		state, epoch, err := a.revision(ctx)
		if errors.Is(err, errAPIKeyAuthRetry) {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		record, ok := a.local(digest, state, epoch)
		if !ok {
			a.misses.Add(1)
			value, loadErr := waitAPIKeyAuthLoad(ctx, &a.loads, fmt.Sprintf("%d:%s", epoch, digest), func() (any, error) {
				loadCtx, cancel, err := a.operationContext(ctx, 3*time.Second)
				if err != nil {
					return nil, err
				}
				defer cancel()
				return a.load(loadCtx, key, digest, state, epoch)
			})
			if errors.Is(loadErr, errAPIKeyAuthRetry) {
				continue
			}
			if loadErr != nil {
				a.errors.Add(1)
				return nil, false, loadErr
			}
			record = value.(apiKeyAuthRecord)
		}
		var row *database.APIKeyRow
		if record.Row != nil {
			row = cloneAPIKeyLookupRow(record.Row)
			row.Key = key
			if row.QuotaLimit > 0 && row.Enabled && !row.IsExpired(time.Now()) {
				a.quotaReads.Add(1)
				used, current, quotaErr := a.db.GetAPIKeyAuthQuota(ctx, row.ID)
				if errors.Is(quotaErr, sql.ErrNoRows) {
					a.invalidate()
					continue
				}
				if quotaErr != nil {
					a.errors.Add(1)
					return nil, false, quotaErr
				}
				if current != state {
					a.invalidate()
					continue
				}
				row.QuotaUsed = used
			}
		}
		a.mu.Lock()
		current := a.epoch == epoch && a.state == state
		a.mu.Unlock()
		if current {
			return row, row != nil, nil
		}
	}
	return nil, false, errAPIKeyAuthRetry
}

func (h *Handler) InvalidateAPIKeyAuthCache(ctx context.Context) {
	if h == nil || h.authCache == nil {
		return
	}
	a := h.authCache
	scope := a.invalidate()
	if bus, ok := a.backend.(cache.AuthInvalidationBus); ok && scope != "" {
		notifyCtx, cancel := context.WithTimeout(ctx, cache.AuthInvalidationTimeout)
		defer cancel()
		if err := bus.PublishAuthInvalidation(notifyCtx, scope); err != nil {
			a.errors.Add(1)
		}
	}
}

func (h *Handler) CloseAPIKeyAuthCache() {
	if h != nil && h.authCache != nil {
		h.authCache.close()
	}
}

func (h *Handler) APIKeyAuthCacheStats() APIKeyAuthCacheStats {
	if h == nil || h.authCache == nil {
		return APIKeyAuthCacheStats{}
	}
	a := h.authCache
	a.mu.Lock()
	entries, bytes, generation := len(a.entries), a.bytes, a.state.Generation
	a.mu.Unlock()
	return APIKeyAuthCacheStats{Enabled: true, Shared: a.backend != nil && a.backend.SharedAcrossInstances(), Entries: entries, Bytes: bytes, MaxEntries: apiKeyAuthMaxEntries, MaxBytes: apiKeyAuthMaxBytes, Generation: generation, RevisionMaxAgeMs: apiKeyAuthRevisionTTL.Milliseconds(), LocalHits: a.localHits.Load(), RemoteHits: a.remoteHits.Load(), Misses: a.misses.Load(), NegativeHits: a.negativeHits.Load(), DBLoads: a.dbLoads.Load(), QuotaReads: a.quotaReads.Load(), RevisionChecks: a.revisionChecks.Load(), Invalidations: a.invalidations.Load(), Errors: a.errors.Load(), Evictions: a.evictions.Load(), OversizeBypasses: a.bypasses.Load()}
}
